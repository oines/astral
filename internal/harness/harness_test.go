package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sessions "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
	testshell "github.com/oines/astral/internal/testkit/shell"
)

type fixture struct {
	ctx       context.Context
	service   *Service
	store     *persistence.Store
	coreStore *sqlite.Store
	hub       *computers.Hub
	daemons   []*computers.Daemon
	agent     domain.Agent
	session   domain.Session
	directory string
	mu        sync.Mutex
	requests  []json.RawMessage
	generate  func(context.Context, int, map[string]any) any
}

func newFixture(t *testing.T, computerCount int, generate func(context.Context, int, map[string]any) any) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	f := &fixture{ctx: ctx, directory: t.TempDir(), generate: generate}
	var err error
	f.store, err = persistence.Open(filepath.Join(f.directory, "data"))
	if err != nil {
		t.Fatal(err)
	}
	f.coreStore, err = sqlite.Open(filepath.Join(f.directory, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.hub = computers.NewHub(f.store)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/computers/{id}/connect", f.hub.Connect)
	server := httptest.NewServer(mux)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid", 400)
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, encode(request))
		n := len(f.requests)
		f.mu.Unlock()
		response := f.generate(r.Context(), n, request)
		if response != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		}
	}))
	bridge := &modelbridge.Bridge{Store: f.store, HTTPClient: provider.Client()}
	p := domain.Provider{Name: "test", Dialect: "openai", Protocol: "openai-completions", BaseURL: provider.URL}
	key := "fixture-key"
	if err = bridge.SaveProvider(ctx, &p, &key, nil); err != nil {
		t.Fatal(err)
	}
	profile := domain.ModelProfile{Name: "test", ProviderID: p.ID, ModelID: "fixture", Recommendations: map[string]json.RawMessage{
		"context_window": encode(128000), "max_output_tokens": encode(4096), "streaming": encode(false), "images": encode("supported"), "image_token_budget": encode(2048),
	}}
	if err = bridge.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	f.service = New(f.store, f.coreStore, bridge, f.hub)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := f.service.Close(cleanup); err != nil {
			t.Errorf("close fixture Harness workers: %v", err)
		}
		cancel()
		for _, d := range f.daemons {
			if err := d.Close(cleanup); err != nil {
				t.Errorf("close fixture Daemon: %v", err)
			}
		}
		if err := f.hub.Close(); err != nil {
			t.Errorf("close fixture Computer hub: %v", err)
		}
		server.Close()
		provider.Close()
		if err := f.coreStore.Close(); err != nil {
			t.Errorf("close fixture Core database: %v", err)
		}
		if err := f.store.Close(); err != nil {
			t.Errorf("close fixture Astral database: %v", err)
		}
	})
	for range computerCount {
		d, err := computers.NewDaemon(t.TempDir(), computers.Credentials{ServerURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		d.Hello.Home = t.TempDir()
		enrollment, err := f.hub.Enroll(ctx, server.URL, false, false)
		if err != nil {
			t.Fatal(err)
		}
		credentials, err := f.hub.Register(ctx, enrollment.Enrollment.ID, computers.EnrollmentRequest{Token: enrollment.Token, Hello: d.Hello})
		if err != nil {
			t.Fatal(err)
		}
		credentials.ServerURL = server.URL
		d.Credentials = credentials
		f.daemons = append(f.daemons, d)
		go func() { _ = d.Connect(ctx) }()
		eventually(t, ctx, func() bool {
			var c domain.Computer
			return f.store.Get(ctx, "computers", credentials.ComputerID, &c) == nil && f.hub.Status(c).Online
		})
		f.agent.AllowedComputers = append(f.agent.AllowedComputers, credentials.ComputerID)
	}
	f.agent.Name, f.agent.DefaultModelID = "test Agent", profile.ID
	if computerCount > 0 {
		f.agent.DefaultComputerID = f.daemons[0].Credentials.ComputerID
	}
	if err = f.service.SaveAgent(ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	f.session, err = f.service.CreateSession(ctx, f.agent.ID, "test Session")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func textReply(text string) any {
	return map[string]any{"id": domain.ID("reply"), "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5}}
}
func toolReply(id, name string, args any) any {
	return map[string]any{"id": domain.ID("reply"), "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(encode(args))}}}}}}}
}
func eventually(t *testing.T, ctx context.Context, check func() bool) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !check() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
}
func (f *fixture) idle(t *testing.T, id string) View {
	t.Helper()
	var view View
	eventually(t, f.ctx, func() bool {
		var err error
		view, err = f.service.View(f.ctx, id)
		if err != nil {
			t.Error(err)
			return false
		}
		return view.Status == "idle" || view.Status == "needs_attention"
	})
	if view.Status == "needs_attention" {
		t.Fatalf("Session failed: %s %+v", view.Error, view.LastRun)
	}
	return view
}
func (f *fixture) restart(t *testing.T) {
	t.Helper()
	if err := f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.coreStore.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.coreStore, err = sqlite.Open(filepath.Join(f.directory, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	f.service = New(f.store, f.coreStore, f.service.Models, f.hub)
}
func (f *fixture) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.requests) }

func TestSessionImagesRemainSelfContainedAcrossDeletionReopenAndFork(t *testing.T) {
	f := newFixture(t, 1, func(_ context.Context, _ int, request map[string]any) any {
		body := string(encode(request))
		if !strings.Contains(body, "data:image/png;base64,") || !strings.Contains(body, "astral-file://") {
			t.Error("native pixels and file URI must coexist")
		}
		if !strings.Contains(body, "Current Astral environment snapshot:") {
			t.Error("actual request needs a persisted environment snapshot")
		}
		for _, value := range request["messages"].([]any) {
			message := value.(map[string]any)
			if message["role"] == "system" && strings.Contains(string(encode(message)), "Current Astral environment snapshot:") {
				t.Error("environment snapshot was moved into the system prompt")
			}
		}
		return textReply("I can see the image")
	})
	b, err := f.hub.Bind(f.ctx, f.agent.ID, f.session.ID, f.agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aK7kAAAAASUVORK5CYII=")
	ref, err := f.service.Files.Upload(f.ctx, b, "image.png", bytes.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	input := InputRequest{ID: "image-input", Text: "Describe this", Files: []string{filestream.URL(ref)}}
	receipt, err := f.service.Submit(f.ctx, f.session.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.InputID != input.ID || receipt.State != "pending" {
		t.Fatalf("first input not admitted through the queue: %+v", receipt)
	}
	view := f.idle(t, f.session.ID)
	if err = os.Remove(ref.Path); err != nil {
		t.Fatal(err)
	}
	_ = f.daemons[0].Close(f.ctx)
	if _, err = f.service.Submit(f.ctx, f.session.ID, input); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatal("same input ID was submitted twice")
	}
	f.restart(t)
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "continue", Text: "Describe it again"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	child, err := f.service.Fork(f.ctx, f.session.ID, view.HeadID, "fork", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	if child.BindingID != "" || child.ConversationID != "" {
		t.Fatal("Fork copied an inbound IM binding")
	}
	if _, err = f.service.Submit(f.ctx, child.ID, InputRequest{ID: "fork-input", Text: "Describe the earlier image"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, child.ID)
	if f.count() != 3 {
		t.Fatalf("expected three actual generations, got %d", f.count())
	}
	coreSession, _ := f.service.Core(f.ctx, child.ID)
	page, err := coreSession.History().Scan(f.ctx, sessions.Query{Kind: sessions.EntryModelRequest, MaxBytes: 128 << 20})
	if err != nil {
		t.Fatal(err)
	}
	frozen := 0
	for _, entry := range page.Entries {
		for _, message := range entry.ModelRequest.Request.Messages {
			for _, part := range message.Content {
				if part.Type == "image" && string(part.Data) == string(encode(base64.StdEncoding.EncodeToString(png))) {
					frozen++
				}
			}
		}
	}
	// A detached copy imports conversation history, including its pixels.
	// Its operational trace contains only the child's own request.
	if frozen != 1 {
		t.Fatalf("child request did not retain the copied pixels: %d", frozen)
	}
}

var handlePattern = regexp.MustCompile(`Process running with session ID ([0-9]+)`)

func lastHandle(request map[string]any) int {
	matches := handlePattern.FindAllStringSubmatch(string(encode(request)), -1)
	if len(matches) == 0 {
		return 0
	}
	value, _ := strconv.Atoi(matches[len(matches)-1][1])
	return value
}

func TestSessionKeepsProcessOnOriginalComputerAndRetiresHandlesAcrossRestart(t *testing.T) {
	var f *fixture
	var first, old, newHandle atomic.Int64
	f = newFixture(t, 2, func(_ context.Context, n int, request map[string]any) any {
		switch n {
		case 1:
			return toolReply("interactive", "exec_command", map[string]any{"cmd": testshell.ReadLine("original-"), "tty": true, "login": false, "yield_time_ms": 1000})
		case 2:
			first.Store(int64(lastHandle(request)))
			return toolReply("switch", "switch_computer", map[string]any{"computer_id": f.daemons[1].Credentials.ComputerID})
		case 3:
			return toolReply("stdin", "write_stdin", map[string]any{"session_id": first.Load(), "chars": "hello\n", "yield_time_ms": 1000})
		case 4:
			if !strings.Contains(string(encode(request)), "original-hello") {
				t.Error("process output did not come from original Computer")
			}
			return toolReply("long", "exec_command", map[string]any{"cmd": testshell.Sleep(30 * time.Second), "login": false, "yield_time_ms": 250})
		case 5:
			old.Store(int64(lastHandle(request)))
			return textReply("Finished.")
		case 6:
			return toolReply("stale", "write_stdin", map[string]any{"session_id": old.Load(), "chars": "\u0003"})
		case 7:
			if !strings.Contains(string(encode(request)), "earlier Server runtime") {
				t.Error("old process handle was not rejected")
			}
			return toolReply("new", "exec_command", map[string]any{"cmd": testshell.Sleep(30 * time.Second), "login": false, "yield_time_ms": 250})
		case 8:
			newHandle.Store(int64(lastHandle(request)))
			return textReply("Finished.")
		default:
			t.Errorf("unexpected generation %d", n)
			return textReply("unexpected")
		}
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "first", Text: "run"}); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	if view.ComputerID != f.daemons[1].Credentials.ComputerID || view.LastRun.Status != "completed" {
		t.Fatalf("switch/completion failed: %+v", view)
	}
	f.restart(t)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "second", Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if first.Load() < 1 || old.Load() <= first.Load() || newHandle.Load() <= old.Load() {
		t.Fatalf("public handles were reused across restart: %d %d %d", first.Load(), old.Load(), newHandle.Load())
	}
}

func TestPassiveObservationAndAtomicTriggerDoNotDuplicateOrInventActivation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, n int, request map[string]any) any {
		if n == 1 {
			body := string(encode(request))
			if strings.Count(body, "trigger-body") != 1 || !strings.Contains(body, "im_trigger") {
				t.Error("trigger duplicated or omitted observation")
			}
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
			return textReply("Finished.")
		}
		return textReply("queued independent input")
	})
	connection := domain.Connection{Meta: domain.Meta{ID: "connection"}, Platform: "telegram"}
	conversation := domain.Conversation{Meta: domain.Meta{ID: "conversation"}, ExternalID: "group", Kind: "group"}
	event := domain.Observation{ID: "passive", MessageID: "1", Text: "background only", SentAt: time.Now(), Sender: domain.Sender{ID: "member", Name: "member"}}
	if _, err := f.service.Deliver(f.ctx, f.session.ID, messages.Observation(event, connection, conversation, nil, ""), nil); err != nil {
		t.Fatal(err)
	}
	if f.count() != 0 {
		t.Fatal("passive observation called the model")
	}
	event.ID, event.MessageID, event.Text, event.Trigger = "trigger", "2", "trigger-body", "mention"
	observation, trigger := messages.Observation(event, connection, conversation, nil, ""), messages.Trigger(event)
	if _, err := f.service.Deliver(f.ctx, f.session.ID, observation, &trigger); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if err := f.service.SelectComputer(f.ctx, f.session.ID, "different", ""); err == nil {
		t.Fatal("Owner switched Computer while active")
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "queued", Text: "next activation"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	f.idle(t, f.session.ID)
	if f.count() != 2 {
		t.Fatalf("queued input did not become one independent activation: %d", f.count())
	}
	if _, err := f.service.Deliver(f.ctx, f.session.ID, observation, &trigger); err != nil {
		t.Fatal(err)
	}
	if f.count() != 2 {
		t.Fatal("same trigger activated twice")
	}
}

func TestStopPreservesQueuedInputUntilExplicitContinue(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return textReply("continued")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "first", Text: "start"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "next", Text: "retained"}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	if len(view.Pending) != 1 || !view.CanContinue || view.LastRun.Status != "cancelled" || f.count() != 1 {
		t.Fatalf("Stop consumed or restarted pending input: %+v", view)
	}
	if err := f.service.Continue(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if f.count() != 2 {
		t.Fatal("explicit Continue did not consume the retained input")
	}
}

func TestInterruptedWorkWithoutQueueCanContinueAfterRestart(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		return textReply("continued")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "work", Text: "work in progress"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	if err := f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	if len(view.Pending) != 0 || !view.CanContinue {
		t.Fatalf("unfinished work lost: %+v", view)
	}
	f.restart(t)
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || !view.CanContinue || len(view.Pending) != 0 || f.count() != 1 {
		t.Fatalf("reopen did not restore an idle resumable execution: %+v %v", view, err)
	}
	if err = f.service.Continue(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	view = f.idle(t, f.session.ID)
	if view.CanContinue || f.count() != 2 {
		t.Fatalf("completed work still offers Continue: %+v", view)
	}
}

func TestEditedQueueRetainsFrozenImageAcrossRestart(t *testing.T) {
	entered := make(chan struct{})
	f := newFixture(t, 1, func(ctx context.Context, n int, request map[string]any) any {
		if n == 1 {
			close(entered)
			<-ctx.Done()
			return nil
		}
		if n == 2 {
			body := string(encode(request))
			if !strings.Contains(body, "edited image message") || !strings.Contains(body, "data:image/png;base64,") {
				t.Error("edited message lost its frozen image")
			}
		}
		return textReply("done")
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "first", Text: "start"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	binding, err := f.hub.Bind(f.ctx, f.agent.ID, f.session.ID, f.session.ComputerID, f.session.CWD)
	if err != nil {
		t.Fatal(err)
	}
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aK7kAAAAASUVORK5CYII=")
	file, err := f.service.Files.Upload(f.ctx, binding, "queued.png", bytes.NewReader(png))
	if err != nil {
		t.Fatal(err)
	}
	imageInput := InputRequest{ID: "b", Text: "original image message", Files: []string{filestream.URL(file)}}
	for _, input := range []InputRequest{{ID: "a", Text: "later"}, imageInput, {ID: "c", Text: "remove me"}} {
		if _, err = f.service.Submit(f.ctx, f.session.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.service.Stop(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	coreSession, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := coreSession.LookupInput(f.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(file.Path); err != nil {
		t.Fatal(err)
	}
	edited := "edited image message"
	if err = f.service.UpdatePending(f.ctx, f.session.ID, "b", PendingEdit{Text: &edited}); err != nil {
		t.Fatal(err)
	}
	if err = f.service.MovePending(f.ctx, f.session.ID, "b", "a"); err != nil {
		t.Fatal(err)
	}
	if err = f.service.RemovePending(f.ctx, f.session.ID, "c"); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || len(view.Pending) != 2 || view.Pending[0].ID != "b" || view.Pending[0].Metadata.DisplayText != edited || view.Pending[1].ID != "a" {
		t.Fatalf("queue not restored: %+v %v", view.Pending, err)
	}
	coreSession, err = f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := coreSession.LookupInput(f.ctx, "b")
	if err != nil || before.Digest != after.Digest || !before.AcceptedAt.Equal(after.AcceptedAt) || !bytes.Equal(encode(before.Message.Content[1:]), encode(after.Message.Content[1:])) {
		t.Fatalf("edit changed frozen attachment or identity: %v", err)
	}
	if err = f.service.UpdatePending(f.ctx, f.session.ID, "b", PendingEdit{Mode: "steer"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if f.count() != 3 {
		t.Fatalf("wrong consumption count: %d", f.count())
	}
	if _, err = f.service.Submit(f.ctx, f.session.ID, imageInput); err != nil {
		t.Fatal(err)
	}
	if f.count() != 3 {
		t.Fatal("retry resubmitted edited input")
	}
}
