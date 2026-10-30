package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func forwardImagePart(ref string) domain.MessagePart {
	return domain.MessagePart{Ref: ref, Type: "attachment", Attachment: &domain.Attachment{Ref: ref, ExternalID: ref, Kind: "image", MediaType: "image/png", Name: "scene.png"}}
}

func TestForwardImagesReuseNativePreviewWithoutSavingOriginals(t *testing.T) {
	requests := make(chan map[string]any, 1)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		return testkit.Text("received forwarded text and pictures")
	})
	s := New(p.Store, p.Harness)
	image := observationPNG(t, 1200, 800)
	source := &inboxImageAdapter{download: func(_ context.Context, attachment domain.Attachment) (Download, error) {
		if attachment.Kind != "image" {
			t.Errorf("reception fetched an original non-image attachment: %+v", attachment)
		}
		return Download{Body: io.NopCloser(bytes.NewReader(image))}, nil
	}}
	account, binding := imageFailureChat(t, p, s, source)
	file := domain.MessagePart{Ref: "p0.m0.p1", Type: "attachment", Attachment: &domain.Attachment{Ref: "p0.m0.p1", Kind: "file", Name: "notes.pdf", MediaType: "application/pdf"}}
	video := domain.MessagePart{Ref: "p0.m0.p2", Type: "attachment", Attachment: &domain.Attachment{Ref: "p0.m0.p2", Kind: "video", Name: "scene.mp4", MediaType: "video/mp4"}}
	forward := domain.MessagePart{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{Status: "inline", Total: 1, Messages: []domain.ForwardedMessage{{Sender: domain.Sender{ID: "7", Name: "Alice"}, Parts: []domain.MessagePart{
		forwardImagePart("p0.m0.p0"), file, video,
		{Ref: "p0.m0.p3", Type: "forward", Forward: &domain.MessageForward{Status: "inline", Total: 1, Messages: []domain.ForwardedMessage{{Sender: domain.Sender{ID: "8", Name: "Bob"}, Parts: []domain.MessagePart{forwardImagePart("p0.m0.p3.m0.p0")}}}}},
	}}}}}
	replyForward := domain.MessagePart{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{Status: "inline", Total: 1, Messages: []domain.ForwardedMessage{{Sender: domain.Sender{ID: "9", Name: "Carol"}, Parts: []domain.MessagePart{forwardImagePart("p0.m0.p0")}}}}}
	rootImage := domain.Attachment{Ref: "10:0", ExternalID: "root", Kind: "image", MediaType: "image/png", Name: "root.png"}
	event := domain.Observation{SchemaVersion: 1, ID: "forward-images", Kind: "message.created", MessageID: "10", ConnectionID: account.ID, ConversationID: binding.ConversationID, Sender: domain.Sender{ID: "7", Name: "Ada"}, Text: "inspect the forward", Trigger: "mention", SentAt: time.Now(), ReceivedAt: time.Now(),
		Parts: []domain.MessagePart{forward, {Ref: "p1", Type: "attachment", Attachment: &rootImage}}, Attachments: []domain.Attachment{rootImage}, ReplyID: "9", ReplySender: "Carol", ReplyParts: []domain.MessagePart{replyForward}}
	before := string(encoded(event))
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	if err := s.deliver(p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, binding.SessionID)
	request := <-requests
	text := activeDeliveryText(request)
	for _, fragment := range []string{"Alice", "Bob", "Carol", "notes.pdf", "scene.mp4", "/messages/10/parts/p0.m0.p0/content", "/messages/10/parts/p0.m0.p3.m0.p0/content", "/messages/9/parts/p0.m0.p0/content", "75,000 pixels"} {
		if !strings.Contains(text, fragment) {
			t.Errorf("model input lost %q: %s", fragment, text)
		}
	}
	// Four native images: one ordinary root image, two in the current forward,
	// and one in the quoted forward. A root part must not duplicate its image.
	if got := strings.Count(string(encoded(request)), "data:image/"); got != 4 || source.downloads.Load() != 4 {
		t.Fatalf("native previews duplicated or omitted: images=%d downloads=%d", got, source.downloads.Load())
	}
	files, err := list[domain.FileRef](p.Context, p.Store, "files")
	if err != nil || len(files) != 0 || string(encoded(event)) != before {
		t.Fatal("forward preview saved original files or rewrote the archived source", err)
	}
	if err := s.deliver(p.Context, binding, event); err != nil || source.downloads.Load() != 4 || p.Requests.Load() != 1 {
		t.Fatal("receipt replay fetched pictures or woke the Agent again", err)
	}
	view, err := p.Harness.InspectContext(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range view.Request.Messages {
		if message.ContextKind != "im.observation" {
			continue
		}
		var meta struct {
			Files []domain.FileRef `json:"files"`
		}
		if json.Unmarshal(message.Metadata, &meta) != nil || len(meta.Files) != 0 {
			t.Fatal("forward preview registered a saved attachment")
		}
	}
}

func TestForwardImageFailureStillAdmitsMessageAndLaterTrigger(t *testing.T) {
	requests := make(chan map[string]any, 1)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		return testkit.Text("received later mention")
	})
	s := New(p.Store, p.Harness)
	source := &inboxImageAdapter{download: func(context.Context, domain.Attachment) (Download, error) {
		return Download{}, errors.New("https://private.example/image?token=secret")
	}}
	account, binding := imageFailureChat(t, p, s, source)
	event := domain.Observation{SchemaVersion: 1, ID: "broken-forward-picture", Kind: "message.created", MessageID: "10", ConnectionID: account.ID, ConversationID: binding.ConversationID, Text: "forward before the mention", SentAt: time.Now(), ReceivedAt: time.Now(),
		Parts: []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{Status: "inline", Total: 1, Messages: []domain.ForwardedMessage{{Sender: domain.Sender{Name: "Alice"}, Parts: []domain.MessagePart{{Ref: "p0.m0.p0", Type: "text", Text: "keep this quotation"}, forwardImagePart("p0.m0.p1")}}}}}}}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	later, _ := activeDeliveryArchive(t, p, binding, "forward-later-mention", "mention")
	startChannels(t, s, p.Context)
	activeDeliveryRouted(t, p, later.ID)
	p.Idle(t, binding.SessionID)
	text := activeDeliveryText(<-requests)
	for _, want := range []string{"keep this quotation", "Forwarded image preview unavailable", "download-url", later.Text} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in admitted context: %s", want, text)
		}
	}
	if strings.Contains(text, "private.example") || strings.Contains(text, "token=secret") || p.Requests.Load() != 1 {
		t.Fatal("failed forwarded image leaked provider credentials or duplicated activation")
	}
}

func TestForwardImagesShareBudgetAndRespectParentCancellation(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	source := &inboxImageAdapter{download: func(context.Context, domain.Attachment) (Download, error) {
		t.Fatal("expired preview budget fetched a new image")
		return Download{}, nil
	}}
	account, binding := imageFailureChat(t, p, s, source)
	var conversation domain.Conversation
	if err := p.Store.Get(p.Context, "conversations", binding.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{MessageID: "10", Parts: []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{Status: "inline", Total: 1, Messages: []domain.ForwardedMessage{{Parts: []domain.MessagePart{forwardImagePart("p0.m0.p0")}}}}}}}
	expired, cancel := context.WithCancel(p.Context)
	cancel()
	content, err := s.forwardObservationImages(p.Context, expired, binding, conversation, event, account)
	if err != nil || len(content) != 1 || !strings.Contains(content[0].Text, "budget") || source.downloads.Load() != 0 {
		t.Fatal("optional budget expiry blocked text or acquired another image", err)
	}
	if _, err := s.forwardObservationImages(expired, expired, binding, conversation, event, account); !errors.Is(err, context.Canceled) {
		t.Fatal("parent cancellation was swallowed", err)
	}
}

func TestOnlyIDForwardIsExpandedBeforeDeliveryWithoutRewritingArchive(t *testing.T) {
	f := newPartResourceFixture(t, 0)
	f.parent = json.RawMessage(`[{"type":"forward","data":{"id":"current-handle"}}]`)
	f.forward = json.RawMessage(`{"messages":[{"sender":{"user_id":7,"nickname":"Alice"},"message":[{"type":"text","data":{"text":"This text came from the forward resource."}},{"type":"file","data":{"file":"opaque-file","name":"notes.pdf"}},{"type":"at","data":{"qq":"999"}}]}]}`)
	binding, err := f.s.Bind(f.p.Context, f.chat.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	event := f.archive(t, []domain.MessagePart{{Ref: "p0", Type: "forward", Forward: &domain.MessageForward{ExternalID: "current-handle", Status: "unexpanded"}}})
	if err := f.s.deliver(f.p.Context, binding, event); err != nil {
		t.Fatal(err)
	}
	view, err := f.p.Harness.InspectContext(f.p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded(view.Request.Messages))
	for _, want := range []string{"This text came from the forward resource.", "notes.pdf", "/messages/42/parts/p0.m0.p1/content"} {
		if !strings.Contains(text, want) {
			t.Errorf("only-ID forward missing %q in context: %s", want, text)
		}
	}
	stored, err := f.p.Store.Observation(f.p.Context, event.ID)
	if err != nil || len(stored.Parts[0].Forward.Messages) != 0 || stored.Parts[0].Forward.Status != "unexpanded" {
		t.Fatal("delivery expansion rewrote the archived observation", err)
	}
	if f.p.Requests.Load() != 0 || f.getMedia.Load() != 0 || f.getMessage.Load() != 1 || f.getForward.Load() != 1 {
		t.Fatal("quoted mention triggered the Bot, a file was downloaded, or forward resolution repeated")
	}
}
