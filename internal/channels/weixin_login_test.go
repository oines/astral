package channels

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/testkit"
)

type weixinLoginFixture struct {
	client       *http.Client
	botID        atomic.Value
	statusPolls  atomic.Int64
	configCalls  atomic.Int64
	validateFail atomic.Bool
	updates      chan []weixinMessage
	mu           sync.Mutex
	sent         []weixinMessage
	uploadKey    []byte
	uploaded     []byte
}

func newWeixinLoginFixture(t *testing.T) *weixinLoginFixture {
	t.Helper()
	f := &weixinLoginFixture{updates: make(chan []weixinMessage, 10)}
	f.botID.Store("qr-authorized-bot")
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_, _ = io.WriteString(rw, `{"qrcode":"fixture-qr","qrcode_img_content":"https://weixin.qq.com/login?fixture=1"}`)
		case "/ilink/bot/get_qrcode_status":
			f.statusPolls.Add(1)
			_ = json.NewEncoder(rw).Encode(map[string]any{"status": "confirmed", "bot_token": "qr-fixture-secret-token", "ilink_bot_id": f.botID.Load(), "ilink_user_id": "scanning-user", "baseurl": weixinDefaultURL})
		case "/ilink/bot/getconfig":
			f.configCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer qr-fixture-secret-token" {
				t.Error("QR grant not used for validation")
			}
			if f.validateFail.Load() {
				rw.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = io.WriteString(rw, `{"ret":0}`)
		case "/ilink/bot/getupdates":
			// Read the request before long polling, as the real backend does.
			// Otherwise net/http cannot observe client cancellation while unread
			// request bytes are still queued on this fixture connection.
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case updates := <-f.updates:
				_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0, "msgs": updates, "get_updates_buf": "service-committed-cursor"})
			case <-r.Context().Done():
			}
		case "/ilink/bot/sendmessage":
			var body struct {
				Message weixinMessage `json:"msg"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.sent = append(f.sent, body.Message)
			id := len(f.sent)
			f.mu.Unlock()
			_ = json.NewEncoder(rw).Encode(map[string]any{"ret": 0, "message_id": id})
		case "/ilink/bot/getuploadurl":
			var body struct {
				Key string `json:"aeskey"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			key, err := hex.DecodeString(body.Key)
			if err != nil {
				t.Error(err)
			}
			f.mu.Lock()
			f.uploadKey = key
			f.mu.Unlock()
			_, _ = io.WriteString(rw, `{"upload_full_url":"https://novac2c.cdn.weixin.qq.com/c2c/upload?opaque=1"}`)
		case "/c2c/upload":
			data, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			plain, err := weixinDecrypt(data, f.uploadKey)
			f.uploaded = plain
			f.mu.Unlock()
			if err != nil {
				t.Error(err)
			}
			rw.Header().Set("x-encrypted-param", "fixture-download")
		default:
			t.Errorf("unexpected iLink request: %s", r.URL.Path)
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	f.client = &http.Client{Transport: weixinRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "ilinkai.weixin.qq.com" && r.URL.Host != "novac2c.cdn.weixin.qq.com" {
			t.Errorf("unexpected service request host: %s", r.URL.Host)
		}
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(copy)
	})}
	return f
}

func (f *weixinLoginFixture) login(t *testing.T, s *Service, agentID, connectionID string) WeixinLoginView {
	t.Helper()
	started, err := s.StartWeixinLogin(t.Context(), agentID, WeixinLoginInput{Name: "Weixin account", ConnectionID: connectionID})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := s.PollWeixinLogin(t.Context(), started.ID, WeixinLoginPoll{})
	if err != nil || confirmed.Status != "confirmed" || confirmed.Connection == nil {
		t.Fatalf("login did not create an account: %#v %v", confirmed, err)
	}
	return confirmed
}

func TestWeixinOwnerLoginKeepsGrantPrivateAndReconnectPreservesEdits(t *testing.T) {
	f := newWeixinLoginFixture(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	s.Client = f.client
	confirmed := f.login(t, s, p.Agent.ID, "")
	c := *confirmed.Connection
	if c.AccountID != "qr-authorized-bot" || c.AccessMode != "specified" || !c.Enabled || c.Platform != "weixin" || c.CredentialID == "" {
		t.Fatalf("invalid connected account: %#v", c)
	}
	if strings.Contains(string(encoded(confirmed)), "qr-fixture-secret-token") || strings.Contains(string(c.Config), "bot_token") || strings.Contains(string(c.Config), "bot_id") {
		t.Fatal("public account or login response leaked login grant")
	}
	secret, err := p.Store.Secret(p.Context, c.CredentialID)
	var grant WeixinCredential
	if err != nil || json.Unmarshal([]byte(secret), &grant) != nil || grant.BotToken != "qr-fixture-secret-token" || grant.BotID != c.AccountID {
		t.Fatalf("QR grant not stored as encrypted credential: %v", err)
	}
	pollsBefore := f.statusPolls.Load()
	repeated, err := s.PollWeixinLogin(p.Context, confirmed.ID, WeixinLoginPoll{})
	accounts, listErr := list[domain.Connection](p.Context, p.Store, "connections")
	if err != nil || listErr != nil || repeated.Connection == nil || repeated.Connection.ID != c.ID || len(accounts) != 1 || f.statusPolls.Load() != pollsBefore {
		t.Fatal("login polling replay duplicated authorization or account creation")
	}
	forged := domain.Connection{AgentID: p.Agent.ID, Name: "forged", Adapter: "weixin.ilink", Enabled: true}
	if err := s.SaveConnection(p.Context, &forged, &secret); err == nil {
		t.Fatal("public SaveConnection bypassed QR authorization")
	}
	if err := s.SaveConnection(p.Context, &c, &secret); err == nil {
		t.Fatal("public SaveConnection replaced QR credentials directly")
	}
	started, err := s.StartWeixinLogin(p.Context, p.Agent.ID, WeixinLoginInput{Name: "old draft name", ConnectionID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	c.Name, c.AccessMode, c.Enabled = "edited while scanning", "all", false
	if err := s.SaveConnection(p.Context, &c, nil); err != nil {
		t.Fatal(err)
	}
	reconnected, err := s.PollWeixinLogin(p.Context, started.ID, WeixinLoginPoll{})
	if err != nil || reconnected.Connection == nil {
		t.Fatalf("reconnect failed: %#v %v", reconnected, err)
	}
	got := reconnected.Connection
	if got.ID != c.ID || got.Name != c.Name || got.AccessMode != "all" || got.Enabled || got.AccountID != c.AccountID {
		t.Fatalf("reconnect overwrote concurrent account edits: %#v", got)
	}
	f.botID.Store("different-QR-bot")
	started, err = s.StartWeixinLogin(p.Context, p.Agent.ID, WeixinLoginInput{Name: c.Name, ConnectionID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PollWeixinLogin(p.Context, started.ID, WeixinLoginPoll{}); err == nil {
		t.Fatal("reconnect replaced the platform account identity")
	}
	var unchanged domain.Connection
	if err = p.Store.Get(p.Context, "connections", c.ID, &unchanged); err != nil || unchanged.AccountID != c.AccountID || unchanged.Name != c.Name {
		t.Fatal("failed reconnect modified the existing account")
	}
}

func TestWeixinOwnerLoginDoesNotRequireTypingConfiguration(t *testing.T) {
	f := newWeixinLoginFixture(t)
	f.validateFail.Store(true)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	s.Client = f.client
	confirmed := f.login(t, s, p.Agent.ID, "")
	if confirmed.Connection.AccountID != "qr-authorized-bot" || f.configCalls.Load() != 0 {
		t.Fatal("confirmed QR authorization depended on the optional typing configuration API")
	}
	var stored domain.Connection
	if err := p.Store.Get(p.Context, "connections", confirmed.Connection.ID, &stored); err != nil || stored.CredentialID == "" || !stored.Enabled {
		t.Fatalf("confirmed authorization was not persisted: %#v %v", stored, err)
	}
}

func TestWeixinOwnerLoginRetainsConfirmedGrantAfterAccountConflict(t *testing.T) {
	f := newWeixinLoginFixture(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	s.Client = f.client
	prior := *f.login(t, s, p.Agent.ID, "").Connection
	started, err := s.StartWeixinLogin(p.Context, p.Agent.ID, WeixinLoginInput{Name: "recoverable login"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PollWeixinLogin(p.Context, started.ID, WeixinLoginPoll{}); err == nil {
		t.Fatal("the same platform account was enabled twice")
	}
	prior.Enabled = false
	if err := s.SaveConnection(p.Context, &prior, nil); err != nil {
		t.Fatal(err)
	}
	f.botID.Store("must-not-use-another-grant")
	confirmed, err := s.PollWeixinLogin(p.Context, started.ID, WeixinLoginPoll{})
	if err != nil || confirmed.Connection == nil || confirmed.Connection.AccountID != "qr-authorized-bot" || f.statusPolls.Load() != 2 {
		t.Fatalf("confirmed grant was discarded or fetched twice: %#v %v", confirmed, err)
	}
}

func TestWeixinServicePollingScopedResolutionAndDurableFileReceipts(t *testing.T) {
	f := newWeixinLoginFixture(t)
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("web-only assistant output") })
	s := New(p.Store, p.Harness)
	s.Client = f.client
	view := f.login(t, s, p.Agent.ID, "")
	c := *view.Connection
	f.updates <- []weixinMessage{
		{ID: "1", From: "authorized-peer", Type: 1, State: 2, Created: time.Now().UnixMilli(), ContextToken: "context-for-authorized", Items: []weixinItem{{Type: 1, Text: &weixinText{Text: "hello"}}}},
		{ID: "2", From: "other-peer", Type: 1, State: 2, Created: time.Now().UnixMilli(), ContextToken: "context-for-other", Items: []weixinItem{{Type: 1, Text: &weixinText{Text: "other chat"}}}},
	}
	ctx, cancel := context.WithCancel(p.Context)
	done := make(chan struct{})
	go func() { defer close(done); s.poll(ctx, c) }()
	testkit.Await(t, p.Context, func() bool {
		var pos cursor
		return p.Store.Get(p.Context, "channel_cursors", c.ID, &pos) == nil && pos.Value == "service-committed-cursor"
	})
	cancel()
	<-done
	if p.Requests.Load() != 0 {
		t.Fatal("specified-scope unknown chats activated the model")
	}
	sessionsBefore, err := p.Store.List(p.Context, "sessions")
	if err != nil || len(sessionsBefore) != 1 {
		t.Fatal("incoming unbound chats created Sessions in specified scope")
	}
	chat, err := s.ResolveConversation(p.Context, c.ID, "authorized-peer", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.ResolveConversation(p.Context, c.ID, "other-peer", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AuthorizeConversation(p.Context, p.Agent.ID, chat.ID); err == nil {
		t.Fatal("API resolution granted specified-scope chat access")
	}
	if _, err = s.Bind(p.Context, chat.ID, p.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Enqueue(p.Context, p.Agent.ID, p.Session.ID, SendRequest{IntentID: "denied-other-chat", ConversationID: other.ID, Text: "no access"}); err == nil {
		t.Fatal("send escaped specified chat scope")
	}
	computerBinding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("real Computer file streamed into encrypted iLink CDN upload")
	file, err := p.Harness.Files.Upload(p.Context, computerBinding, "report.txt", bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	request := SendRequest{IntentID: "caption-and-file", FileURI: filestream.URL(file), Text: "report caption"}
	queued, err := s.Enqueue(p.Context, p.Agent.ID, p.Session.ID, request)
	if err != nil || len(queued) != 2 || queued[0].FileRefID != "" || queued[0].Text != "report caption" || queued[1].FileRefID == "" || queued[1].Text != "" {
		t.Fatalf("caption/file effects did not get independent receipts: %#v %v", queued, err)
	}
	for _, out := range queued {
		if err = s.dispatch(p.Context, out.ID); err != nil {
			t.Fatal(err)
		}
	}
	replayed, err := s.Enqueue(p.Context, p.Agent.ID, p.Session.ID, request)
	if err != nil || len(replayed) != 2 || replayed[0].Status != "accepted" || replayed[1].Status != "accepted" {
		t.Fatalf("durable receipts lost acceptance: %#v %v", replayed, err)
	}
	f.mu.Lock()
	sent := append([]weixinMessage(nil), f.sent...)
	uploaded := append([]byte(nil), f.uploaded...)
	f.mu.Unlock()
	if len(sent) != 2 || sent[0].Items[0].Type != 1 || sent[1].Items[0].Type != 4 || sent[0].To != "authorized-peer" || sent[1].To != "authorized-peer" || sent[1].ContextToken != "context-for-authorized" || !bytes.Equal(uploaded, plain) {
		t.Fatalf("actual queued transport failed: %#v / uploaded %q", sent, uploaded)
	}
	// Expanding API access permits an explicit API target without activating a
	// Session for that target. The current Web Session remains bound to chat.
	c.AccessMode = "all"
	if err = s.SaveConnection(p.Context, &c, nil); err != nil {
		t.Fatal(err)
	}
	otherSend, err := s.Enqueue(p.Context, p.Agent.ID, p.Session.ID, SendRequest{IntentID: "explicit-other-chat", ConversationID: other.ID, Text: "explicit API destination"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.dispatch(p.Context, otherSend[0].ID); err != nil {
		t.Fatal(err)
	}
	sessionsAfter, err := p.Store.List(p.Context, "sessions")
	if err != nil || len(sessionsAfter) != len(sessionsBefore) {
		t.Fatal("API resolution or explicit API sending created a Session")
	}
	if p.Requests.Load() != 0 {
		t.Fatal("transport or API access invoked the model")
	}
}
