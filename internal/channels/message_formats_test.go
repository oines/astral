package channels

import (
	"context"
	"encoding/json"
	"fmt"
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

type formatAdapter struct{ Adapter }

func (formatAdapter) Capabilities() Capabilities { return (&napcat{}).Capabilities() }

func TestMessageToolBindingChangePreservesHistoryAndReplacesSchema(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("keep this history") })
	s := New(p.Store, p.Harness)
	account := bot.connection(t, s, p.Agent.ID)
	if _, err := p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: "before-binding", Text: "remember me"}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, p.Session.ID)
	before, err := p.Harness.Core(p.Context, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	head := before.Head()
	chat, err := s.ResolveConversation(p.Context, account.ID, "-100", "")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.Bind(p.Context, chat.ID, p.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSchema := func(want, absent string) {
		t.Helper()
		current, err := p.Harness.Core(p.Context, p.Session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := current.History().Get(p.Context, head); err != nil {
			t.Fatal("binding change lost committed history:", err)
		}
		for _, definition := range current.Configuration().Tools {
			if definition.Name == "send_message" {
				if definition.Version != want || absent != "" && strings.Contains(string(definition.Parameters), absent) {
					t.Fatalf("stale declaration after binding change: %+v", definition)
				}
				return
			}
		}
		t.Fatal("bound Session has no send_message")
	}
	assertSchema("astral-2/telegram.bot", "markdown_template")
	if err := s.Unbind(p.Context, bound.ID, bound.Revision); err != nil {
		t.Fatal(err)
	}
	s.Registry["napcat.fixture"] = func(c domain.Connection, token string, client *http.Client) (Adapter, error) {
		a, err := NewTelegram(c, token, client)
		return formatAdapter{a}, err
	}
	account.Meta, account.Adapter = domain.Meta{}, "napcat.fixture"
	token := "test-token"
	if err := s.SaveConnection(p.Context, &account, &token); err != nil {
		t.Fatal(err)
	}
	chat, err = s.ResolveConversation(p.Context, account.ID, "-200", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Bind(p.Context, chat.ID, p.Session.ID); err != nil {
		t.Fatal(err)
	}
	assertSchema("astral-2/qq.napcat", `"format"`)
	if _, err = p.Harness.Submit(p.Context, p.Session.ID, harness.InputRequest{ID: "after-binding", Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	p.Idle(t, p.Session.ID)
	current, err := p.Harness.View(p.Context, p.Session.ID)
	if err != nil || current.LastRun.Status != "completed" {
		t.Fatal("rebound Session could not continue")
	}
}

func TestMessageToolExposesOnlyCurrentPlatformParameters(t *testing.T) {
	for _, a := range []Adapter{&telegram{}, &feishu{}, &qqbot{}, &napcat{}, &weixinAdapter{}} {
		t.Run(a.Capabilities().Adapter, func(t *testing.T) {
			def := messageTool(a.Capabilities())
			var schema struct {
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(def.Parameters, &schema); err != nil {
				t.Fatal(err)
			}
			_, reply := schema.Properties["reply_to"]
			_, format := schema.Properties["format"]
			if reply != a.Capabilities().ReplyReferences || format != (a.Capabilities().Adapter != "qq.napcat") {
				t.Fatalf("wrong tool capabilities: %s", def.Parameters)
			}
			if a.Capabilities().Adapter == "qq.napcat" {
				if len(schema.Properties) != 3 || validateMessageOptions(a.Capabilities(), domain.MessageOptions{Format: "markdown"}) == nil {
					t.Fatal("NapCat exposed rich-message parameters")
				}
			}
		})
	}
}

func TestTelegramRichMessageAndFileFreezeSeparateReceipts(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, c, "-100", "22")
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, b.SessionID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := p.Harness.Files.Upload(p.Context, source, "报告.md", strings.NewReader("report"))
	if err != nil {
		t.Fatal(err)
	}
	request := SendRequest{MessageOptions: domain.MessageOptions{Format: "markdown", DisableNotification: true}, IntentID: "rich-file", Text: "# Result\n\n**done**", FileURI: filestream.URL(ref)}
	receipts, err := s.Enqueue(p.Context, p.Agent.ID, b.SessionID, request)
	if err != nil || len(receipts) != 2 {
		t.Fatalf("receipts: %#v %v", receipts, err)
	}
	if receipts[0].Format != "markdown" || receipts[0].FileRefID != "" || receipts[1].Format != "" || receipts[1].FileRefID != ref.ID || !receipts[1].DisableNotification {
		t.Fatalf("presentation or file intent lost: %#v", receipts)
	}
	for _, receipt := range receipts {
		if err = s.dispatch(p.Context, receipt.ID); err != nil {
			t.Fatal(err)
		}
	}
	bot.mu.Lock()
	if len(bot.sendParameters) != 1 || bot.sendParameters[0]["rich_message"] == nil || bot.sendParameters[0]["message_thread_id"] != float64(22) {
		t.Errorf("not sent as a rich message to the selected Topic: %#v", bot.sendParameters)
	}
	bot.mu.Unlock()
	var sent domain.Outbox
	_ = p.Store.Get(p.Context, "outbox", receipts[0].ID, &sent)
	stored, err := p.Store.Message(p.Context, b.ConversationID, sent.PlatformMessageIDs[0])
	if err != nil || stored.Text != request.Text || stored.Format != "markdown" {
		t.Fatalf("archive: %#v %v", stored, err)
	}
	repeated, err := s.Enqueue(p.Context, p.Agent.ID, b.SessionID, request)
	if err != nil || repeated[0].ID != receipts[0].ID || repeated[0].Status != "accepted" {
		t.Fatal("replay changed the frozen send")
	}
	request.Format = "html"
	if _, err = s.Enqueue(p.Context, p.Agent.ID, b.SessionID, request); err == nil {
		t.Fatal("same intent changed format")
	}
	view, err := p.Harness.Core(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range view.Configuration().Tools {
		if tool.Name == "send_message" {
			found = strings.Contains(string(tool.Parameters), "as_document")
		}
	}
	if !found {
		t.Fatal("Core did not register the platform-specific tool")
	}
}

func TestTelegramUnicodeUploadsUseLiteralFilenameAndPhotoRouting(t *testing.T) {
	for _, test := range []struct {
		name, media, method, field string
		document                   bool
	}{
		{"报告.md", "text/markdown", "sendDocument", "document", false},
		{"截图🙂.png", "image/png", "sendPhoto", "photo", false},
		{"原图.png", "image/png", "sendDocument", "document", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/"+test.method) {
					t.Errorf("wrong method: %s", r.URL.Path)
				}
				reader, err := r.MultipartReader()
				if err != nil {
					t.Error(err)
					return
				}
				found := false
				for {
					part, nextErr := reader.NextPart()
					if nextErr == io.EOF {
						break
					}
					if nextErr != nil {
						t.Error(nextErr)
						return
					}
					data, _ := io.ReadAll(part)
					if part.FormName() == test.field {
						found = true
						if !strings.Contains(part.Header.Get("Content-Disposition"), `filename="`+test.name+`"`) || strings.Contains(part.Header.Get("Content-Disposition"), "filename*=") || string(data) != "payload" {
							t.Errorf("Telegram cannot recognize the upload: %s", part.Header.Get("Content-Disposition"))
						}
					}
				}
				if !found {
					t.Error("missing file field")
				}
				fmt.Fprint(w, `{"ok":true,"result":{"message_id":9}}`)
			}))
			defer server.Close()
			a, _ := NewTelegram(domain.Connection{Config: encoded(TelegramConfig{BaseURL: server.URL})}, "token", server.Client())
			receipt, err := a.Send(t.Context(), Send{MessageOptions: domain.MessageOptions{AsDocument: test.document}, Target: domain.Conversation{ExternalID: "1"}, File: io.NopCloser(strings.NewReader("payload")), FileName: test.name, MediaType: test.media, Size: 1 << 40})
			if err != nil || len(receipt.MessageIDs) != 1 {
				t.Fatalf("upload rejected locally: %#v %v", receipt, err)
			}
		})
	}
}

func TestFailedGroupDoesNotBlockIndependentMessages(t *testing.T) {
	for _, status := range []string{"failed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			bot := newBot(t)
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
			s := New(p.Store, p.Harness)
			c := bot.connection(t, s, p.Agent.ID)
			b := createBinding(t, p, s, c, "-100", "")
			group, err := s.Enqueue(p.Context, p.Agent.ID, b.SessionID, SendRequest{IntentID: "first", Text: strings.Repeat("a", 5000)})
			if err != nil {
				t.Fatal(err)
			}
			group[0].Status = status
			if err = p.Store.Save(p.Context, "outbox", &group[0].Meta, &group[0]); err != nil {
				t.Fatal(err)
			}
			later, err := s.Enqueue(p.Context, p.Agent.ID, b.SessionID, SendRequest{IntentID: "later", Text: "independent message"})
			if err != nil {
				t.Fatal(err)
			}
			s.ctx = p.Context
			s.dispatchPending()
			s.workers.Wait()
			var got domain.Outbox
			_ = p.Store.Get(p.Context, "outbox", later[0].ID, &got)
			if got.Status != "accepted" {
				t.Fatalf("queue result: %#v", got)
			}
			_ = p.Store.Get(p.Context, "outbox", group[1].ID, &got)
			if got.Status != "pending" {
				t.Fatal("dependent fragment overtook the failed/unknown fragment")
			}
		})
	}
}

type rejectedSend struct{ Adapter }

func (a rejectedSend) Send(context.Context, Send) (Receipt, error) {
	return Receipt{}, &SendError{Message: "platform rejected this file", Retryable: true}
}

func TestSendMessageFailureReturnsToModel(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(_ context.Context, n int, request map[string]any) any {
		if n == 1 {
			return testkit.Tool("send_message", map[string]any{"text": "send"})
		}
		if !strings.Contains(string(encoded(request["messages"])), "platform rejected this file") {
			t.Error("platform failure did not reach the model's tool result")
		}
		return testkit.Text("I can handle the failure.")
	})
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, c, "-100", "")
	s.Registry[c.Adapter] = func(c domain.Connection, token string, client *http.Client) (Adapter, error) {
		a, err := NewTelegram(c, token, client)
		return rejectedSend{a}, err
	}
	startChannels(t, s, p.Context)
	trigger := update(1, "@astral_bot send")
	trigger.Message.Entities = []telegramEntity{{Type: "mention", Length: 11}}
	bot.updates <- []telegramUpdate{trigger}
	testkit.Await(t, p.Context, func() bool { return p.Requests.Load() >= 2 })
	view := p.Idle(t, b.SessionID)
	if view.LastRun.Status != "completed" {
		t.Fatal("failure did not let the model continue")
	}
}

func TestRichMarkupSplittingAndWeixinCodeContent(t *testing.T) {
	code := "```go\n" + strings.Repeat("fmt.Println(\"你好🙂\")\n", 80) + "```\n"
	parts, err := splitMarkdown(code, 160)
	if err != nil || len(parts) < 2 {
		t.Fatalf("code split: %d %v", len(parts), err)
	}
	for _, part := range parts {
		if utf16Length(part) > 160 || strings.Count(part, "```") != 2 {
			t.Fatalf("broken code fence: %q", part)
		}
	}
	htmlParts, err := splitHTML(strings.Repeat("<p><b>hello &amp; 世界</b></p>\n", 12), 100)
	if err != nil || len(htmlParts) < 2 {
		t.Fatal("HTML not split between elements")
	}
	for _, part := range htmlParts {
		if strings.Count(part, "<p>") != strings.Count(part, "</p>") {
			t.Fatal("HTML tag was cut")
		}
	}
	filtered := weixinMarkdown("##### 中文标题\n\n*中文* **保留粗体**\n\n```txt\na_b **literal** &amp;\n```\n\n![图](https://example.com/a.png)")
	if strings.Contains(filtered, "#####") || strings.Contains(filtered, "*中文*") || !strings.Contains(filtered, "**保留粗体**") || !strings.Contains(filtered, "a_b **literal** &amp;") || !strings.Contains(filtered, "https://example.com/a.png") {
		t.Fatalf("filter lost text or changed code: %s", filtered)
	}
	var message telegramMessage
	_ = json.Unmarshal([]byte(`{"message_id":1,"rich_message":{"blocks":[{"type":"paragraph","text":["hello ",{"type":"bold","text":"世界"}]},{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":"item"}]}]}]}}`), &message)
	event := (&telegram{}).normalize(1, message, false)
	if event.Observation.Text != "hello 世界\n1. item" || event.Observation.Format != "rich" {
		t.Fatalf("rich body lost: %#v", event.Observation)
	}
}

func TestFeishuAndQQUseNativeMarkdownRequests(t *testing.T) {
	t.Run("feishu", func(t *testing.T) {
		f := newFeishuFixture(t)
		_, err := f.adapter.Send(t.Context(), Send{MessageOptions: domain.MessageOptions{Format: "markdown", Title: "报告"}, Target: domain.Conversation{ExternalID: "oc_chat"}, Text: "**正文**"})
		if err != nil {
			t.Fatal(err)
		}
		body := <-f.sends
		var post struct {
			Chinese struct {
				Title   string                `json:"title"`
				Content [][]map[string]string `json:"content"`
			} `json:"zh_cn"`
		}
		_ = json.Unmarshal([]byte(body["content"].(string)), &post)
		if body["msg_type"] != "post" || post.Chinese.Title != "报告" || post.Chinese.Content[0][0]["tag"] != "md" || post.Chinese.Content[0][0]["text"] != "**正文**" {
			t.Fatalf("not a Markdown post: %#v", body)
		}
	})
	for _, format := range []string{"markdown", "markdown_template"} {
		t.Run("qq_"+format, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					fmt.Fprint(w, `{"access_token":"access","expires_in":7200}`)
					return
				}
				var body map[string]json.RawMessage
				_ = json.NewDecoder(r.Body).Decode(&body)
				if string(body["msg_type"]) != "2" || body["content"] != nil || body["markdown"] == nil {
					t.Errorf("not a QQ Markdown request: %#v", body)
				}
				if format == "markdown_template" && !strings.Contains(string(body["markdown"]), "approved") {
					t.Error("template identity lost")
				}
				fmt.Fprint(w, `{"id":"outgoing"}`)
			}))
			defer server.Close()
			send := Send{MessageOptions: domain.MessageOptions{Format: format}, Target: domain.Conversation{ExternalID: "private:u-1"}, Text: "# QQ"}
			if format == "markdown_template" {
				send.Text = ""
				send.MarkdownTemplate = &domain.MarkdownTemplate{ID: "approved", Params: []domain.MarkdownTemplateParam{{Key: "body", Values: []string{"正文"}}}}
			}
			if _, err := qqTestAdapter(t, server).Send(t.Context(), send); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNapCatSendsMarkdownCharactersAsLiteralText(t *testing.T) {
	const text = "# 标题\n\n**bold** `a_b` [link](https://example.com) [CQ:at,qq=all]"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Message []napcatSegment `json:"message"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Message) != 1 || body.Message[0].Type != "text" || body.Message[0].Data["text"] != text {
			t.Errorf("NapCat text was interpreted or rewritten: %#v", body)
		}
		fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"message_id":12}}`)
	}))
	defer server.Close()
	if _, err := napcatTestAdapter(t, server).Send(t.Context(), Send{Target: domain.Conversation{ExternalID: "private:123"}, Text: text}); err != nil {
		t.Fatal(err)
	}
}
