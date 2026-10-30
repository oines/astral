package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

func TestTelegramOrderedPartsUploadRichMediaAndNativeMention(t *testing.T) {
	png := []byte("immutable image bytes")
	var got map[string]any
	var uploaded []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendRichMessage") {
			t.Errorf("wrong method %s", r.URL.Path)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		got = map[string]any{}
		for {
			part, e := reader.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				t.Error(e)
				return
			}
			data, _ := io.ReadAll(part)
			if part.FileName() != "" {
				uploaded = data
				got["file_field"] = part.FormName()
			} else if part.FormName() == "rich_message" {
				var rich map[string]any
				_ = json.Unmarshal(data, &rich)
				got["rich"] = rich
			} else {
				got[part.FormName()] = string(data)
			}
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":42}}`)
	}))
	defer server.Close()
	adapter, err := NewTelegram(domain.Connection{}, "token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	tg := adapter.(*telegram)
	tg.baseURL = server.URL
	receipt, err := tg.Send(context.Background(), Send{MessageOptions: domain.MessageOptions{Format: "markdown", DisableNotification: true}, Target: domain.Conversation{ExternalID: "-100", TopicID: "8"}, ReplyTo: "7", Parts: []SendPart{
		{Type: "mention", UserID: "123", Name: "A [name]"}, {Type: "text", Text: "\n# Result\n\n**done**"},
		{Type: "image", FileName: "图.png", MediaType: "image/png", Size: int64(len(png)), OpenFile: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(png)), nil }},
		{Type: "text", Text: "after image"},
	}})
	if err != nil || len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "42" {
		t.Fatalf("receipt %#v %v", receipt, err)
	}
	rich := got["rich"].(map[string]any)
	text := rich["markdown"].(string)
	if !strings.HasPrefix(text, `[@A \[name\]](tg://user?id=123)`+"\n# Result") || !strings.HasSuffix(text, "after image") {
		t.Fatalf("mention/whitespace/order lost: %q", text)
	}
	media := rich["media"].([]any)
	m := media[0].(map[string]any)
	if m["media"].(map[string]any)["media"] != "attach://"+got["file_field"].(string) || !bytes.Equal(png, uploaded) {
		t.Fatalf("media was not streamed: %#v %q", rich, uploaded)
	}
	if got["message_thread_id"] != "8" || got["disable_notification"] != "true" || got["reply_parameters"] != `{"message_id":7}` {
		t.Fatalf("options lost: %#v", got)
	}
}

func TestOrderedPartsPreparationFreezesAllSourcesAndRejectsUnsupportedMentions(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	account := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, account, "-100", "")
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	file, err := p.Harness.Files.Upload(p.Context, source, "report.txt", strings.NewReader("report"))
	if err != nil {
		t.Fatal(err)
	}
	request := SendRequest{IntentID: "parts", Parts: []domain.OutboundPart{{Type: "mention", UserID: "123"}, {Type: "text", Text: "\nresult "}, {Type: "file", FileURI: filestream.URL(file)}, {Type: "text", Text: " after"}}}
	queued, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, request)
	if err != nil || len(queued) != 1 {
		t.Fatalf("enqueue %#v %v", queued, err)
	}
	if len(queued[0].Parts) != 4 || queued[0].Parts[2].FileRefID != file.ID || queued[0].Parts[1].Text != "\nresult " {
		t.Fatalf("frozen parts %#v", queued[0].Parts)
	}
	if err = s.dispatch(p.Context, queued[0].ID); err != nil {
		t.Fatal(err)
	}
	repeated, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, request)
	if err != nil || repeated[0].ID != queued[0].ID || repeated[0].Status != "accepted" || bot.sends.Load() != 1 {
		t.Fatalf("duplicate send: %#v %v", repeated, err)
	}
	message, err := p.Store.Message(p.Context, binding.ConversationID, repeated[0].PlatformMessageIDs[0])
	if err != nil || len(message.Parts) != 4 || message.Text != "@123\nresult  after" || len(message.Attachments) != 1 {
		t.Fatalf("archive %#v %v", message, err)
	}
	for _, a := range []Adapter{&qqbot{}, &weixinAdapter{}} {
		if _, err = s.freezeOutboundParts(p.Context, p.Agent.ID, p.Session.ID, request, a.Capabilities()); err == nil {
			t.Fatalf("%s accepted fake mention", a.Capabilities().Adapter)
		}
	}
	for _, id := range []string{"+123", "0123", "0", "123abc"} {
		invalid := SendRequest{Parts: []domain.OutboundPart{{Type: "mention", UserID: id}}}
		if _, err := s.freezeOutboundParts(p.Context, p.Agent.ID, p.Session.ID, invalid, (&napcat{}).Capabilities()); err == nil {
			t.Fatalf("invalid user ID %q passed whole-message validation", id)
		}
	}
	bad := request
	bad.IntentID = "ambiguous"
	bad.Text = "also text"
	if _, err = s.Enqueue(p.Context, p.Agent.ID, p.Session.ID, bad); err == nil {
		t.Fatal("ambiguous input accepted")
	}
}

func TestAttachmentConvenienceKeepsAutomaticPhotosAndExplicitFilesStayDocuments(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	service := New(p.Store, p.Harness)
	account := bot.connection(t, service, p.Agent.ID)
	binding := createBinding(t, p, service, account, "-100", "")
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	image, err := p.Harness.Files.Upload(p.Context, source, "photo.png", bytes.NewReader(bot.png))
	if err != nil {
		t.Fatal(err)
	}
	startChannels(t, service, p.Context)
	for _, asDocument := range []bool{false, true} {
		intent := "photo"
		want := "photo"
		if asDocument {
			intent = "photo-as-document"
			want = "document"
		}
		receipts, err := service.SendMessage(p.Context, p.Agent.ID, binding.SessionID, intent, harness.MessageTarget{BindingID: binding.ID, ConversationID: binding.ConversationID}, harness.MessageArgs{MessageOptions: domain.MessageOptions{AsDocument: asDocument}, Text: "caption", Attachments: []string{filestream.URL(image)}})
		if err != nil || len(receipts) != 1 || len(receipts[0].Parts) != 2 || receipts[0].Parts[1].Type != "image" {
			t.Fatalf("convenience lost image type: %#v %v", receipts, err)
		}
		bot.mu.Lock()
		rich := bot.sendParameters[len(bot.sendParameters)-1]["rich_message"].(map[string]any)
		media := rich["media"].([]any)
		kind := media[0].(map[string]any)["media"].(map[string]any)["type"]
		bot.mu.Unlock()
		if kind != want {
			t.Fatalf("as_document=%v produced %v, want %s", asDocument, kind, want)
		}
	}
	explicit := SendRequest{IntentID: "explicit-document", Parts: []domain.OutboundPart{{Type: "file", FileURI: filestream.URL(image)}}}
	receipts, err := service.Send(p.Context, p.Agent.ID, binding.SessionID, explicit)
	if err != nil || len(receipts) != 1 || receipts[0].Parts[0].Type != "file" {
		t.Fatalf("explicit file converted to image: %#v %v", receipts, err)
	}
	// Both convenience entry points share the same inference before platform
	// adaptation, so NapCat also retains its prior automatic image segments.
	inferred, err := service.freezeOutboundParts(p.Context, p.Agent.ID, binding.SessionID, SendRequest{FileURI: filestream.URL(image)}, (&napcat{}).Capabilities())
	if err != nil || len(inferred) != 1 || inferred[0].Type != "image" {
		t.Fatalf("NapCat convenience lost image presentation: %#v %v", inferred, err)
	}
}
