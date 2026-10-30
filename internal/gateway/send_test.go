package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/testkit"
)

type sendAdapter struct {
	channels.Adapter
	calls atomic.Int64
}

func (*sendAdapter) Capabilities() channels.Capabilities {
	return channels.Capabilities{Adapter: "fixture", Platform: "fixture", Name: "fixture", FileSending: true, TextLimit: 4096, CaptionLimit: 1024, MessageFormats: []string{"text"}}
}
func (*sendAdapter) Validate(context.Context) (channels.Identity, error) {
	return channels.Identity{ID: "fixture-account"}, nil
}
func (*sendAdapter) Poll(ctx context.Context, _ string) (channels.Batch, error) {
	<-ctx.Done()
	return channels.Batch{}, ctx.Err()
}
func (*sendAdapter) SplitText(text string) ([]string, error) { return []string{text}, nil }
func (a *sendAdapter) Send(_ context.Context, send channels.Send) (channels.Receipt, error) {
	a.calls.Add(1)
	for _, part := range send.Parts {
		if part.Type == "text" {
			send.Text += part.Text
		} else if part.OpenFile != nil {
			file, err := part.OpenFile(context.Background())
			if err != nil {
				return channels.Receipt{}, err
			}
			defer file.Close()
			send.File = file
		}
	}
	if send.File != nil {
		if _, err := io.Copy(io.Discard, send.File); err != nil {
			return channels.Receipt{}, err
		}
		return channels.Receipt{}, &channels.SendError{Message: "platform rejected document"}
	}
	if send.Text == "reject" {
		return channels.Receipt{}, &channels.SendError{Message: "platform rejected text"}
	}
	return channels.Receipt{MessageIDs: []string{"accepted-1"}}, nil
}

func TestSendAPIsReturnPlatformFailureAndIndependentSuccess(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	channel := channels.New(p.Store, p.Harness)
	adapter := &sendAdapter{}
	channel.Registry["fixture"] = func(domain.Connection, string, *http.Client) (channels.Adapter, error) { return adapter, nil }
	account := domain.Connection{Name: "send fixture", AgentID: p.Agent.ID, Adapter: "fixture", AccessMode: "all", Enabled: true}
	token := "fixture-token"
	if err := channel.SaveConnection(p.Context, &account, &token); err != nil {
		t.Fatal(err)
	}
	chat := domain.Conversation{Meta: domain.Meta{ID: domain.ID("conversation")}, ConnectionID: account.ID, ExternalID: "42", Kind: "private"}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	if err := channel.Start(p.Context); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := channel.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	api := New(p.Harness, channel)
	p.Hub.Gateway = api.Handle
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	file, err := p.Harness.Files.Upload(p.Context, source, "报告.md", strings.NewReader("report"))
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, intent, body string) computers.GatewayResponse {
		t.Helper()
		reply, err := api.Handle(p.Context, computers.GatewayRequest{Binding: source, Method: "POST", Path: path, Body: json.RawMessage(body), Intent: intent})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	}
	textBody, _ := json.Marshal(SendText{ConversationID: chat.ID, Text: "reject"})
	fileBody, _ := json.Marshal(SendFile{ConversationID: chat.ID, FileURI: filestream.URL(file)})
	for _, test := range []struct{ path, intent, body, message string }{
		{"/v1/im/messages", "text", string(textBody), "platform rejected text"},
		{"/v1/im/files", "file", string(fileBody), "platform rejected document"},
	} {
		failed := call(test.path, test.intent, test.body)
		if failed.Status != http.StatusBadRequest || !strings.Contains(string(failed.Body), test.message) || !strings.Contains(string(failed.Body), "outbox_") {
			t.Fatalf("failure was not returned to the caller: %+v", failed)
		}
		before := adapter.calls.Load()
		_ = call(test.path, test.intent, test.body)
		if adapter.calls.Load() != before {
			t.Fatal("replaying a failed intent automatically sent it again")
		}
	}
	successBody, _ := json.Marshal(SendText{ConversationID: chat.ID, Text: "independent"})
	accepted := call("/v1/im/messages", "independent", string(successBody))
	var receipts []domain.Outbox
	if accepted.Status != http.StatusOK || json.Unmarshal(accepted.Body, &receipts) != nil || len(receipts) != 1 || receipts[0].Status != "accepted" {
		t.Fatalf("independent send blocked or returned pending: %+v", accepted)
	}
	partsBody, _ := json.Marshal(SendText{ConversationID: chat.ID, Parts: []domain.OutboundPart{{Type: "text", Text: "ordered\n"}, {Type: "file", Path: file.Path}, {Type: "text", Text: "must not follow a failed file"}}})
	partsReply := call("/v1/im/messages", "ordered-parts", string(partsBody))
	if partsReply.Status != http.StatusBadRequest || !strings.Contains(string(partsReply.Body), "platform rejected document") {
		t.Fatalf("parts did not use the authenticated Computer source and shared Outbox: %+v", partsReply)
	}
	before := adapter.calls.Load()
	_ = call("/v1/im/messages", "ordered-parts", string(partsBody))
	if adapter.calls.Load() != before {
		t.Fatal("replaying ordered parts duplicated platform sends")
	}
	unsupported, _ := json.Marshal(SendText{ConversationID: chat.ID, Parts: []domain.OutboundPart{{Type: "text", Text: "must never send"}, {Type: "mention", UserID: "123"}}})
	refused := call("/v1/im/messages", "unsupported-native-mention", string(unsupported))
	if refused.Status != http.StatusBadRequest || adapter.calls.Load() != before {
		t.Fatalf("unsupported mention partially sent: %+v", refused)
	}
}
