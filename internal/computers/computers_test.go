package computers_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	coresession "github.com/oines/astral-core/session"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

type setup struct {
	store   *persistence.Store
	hub     *computers.Hub
	server  *httptest.Server
	daemons []*computers.Daemon
	agent   domain.Agent
	session domain.Session
	ctx     context.Context
}

func fixture(t *testing.T, count int) *setup {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := computers.NewHub(store)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/computers/{id}/connect", hub.Connect)
	server := httptest.NewServer(mux)
	s := &setup{store: store, hub: hub, server: server, ctx: ctx}
	t.Cleanup(func() {
		cancel()
		for _, d := range s.daemons {
			_ = d.Close(context.Background())
		}
		_ = hub.Close()
		server.Close()
		_ = store.Close()
	})
	for range count {
		d, err := computers.NewDaemon(t.TempDir(), computers.Credentials{ServerURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		enrollment, err := hub.Enroll(ctx, server.URL, false, false)
		if err != nil {
			t.Fatal(err)
		}
		credentials, err := hub.Register(ctx, enrollment.Enrollment.ID, computers.EnrollmentRequest{Token: enrollment.Token, Hello: d.Hello})
		if err != nil {
			t.Fatal(err)
		}
		credentials.ServerURL = server.URL
		d.Credentials = credentials
		s.daemons = append(s.daemons, d)
		go func() { _ = d.Connect(ctx) }()
		waitOnline(t, s, d.Credentials.ComputerID, d.Hello.BootID)
		s.agent.AllowedComputers = append(s.agent.AllowedComputers, credentials.ComputerID)
	}
	s.agent.ID, s.agent.Name = domain.ID("agent"), "Test Agent"
	if err = store.Save(ctx, "agents", &s.agent.Meta, &s.agent); err != nil {
		t.Fatal(err)
	}
	s.session = domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: s.agent.ID, Source: "web", ComputerID: s.daemons[0].Credentials.ComputerID}
	if err = store.Save(ctx, "sessions", &s.session.Meta, &s.session); err != nil {
		t.Fatal(err)
	}
	return s
}
func waitOnline(t *testing.T, s *setup, id, boot string) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var c domain.Computer
		if err := s.store.Get(s.ctx, "computers", id, &c); err != nil {
			t.Fatal(err)
		}
		if status := s.hub.Status(c); status.Online && status.BootID == boot {
			return
		}
		select {
		case <-s.ctx.Done():
			t.Fatal(s.ctx.Err())
		case <-ticker.C:
		}
	}
}
func (s *setup) bind(t *testing.T, index int) domain.ExecutionBinding {
	t.Helper()
	b, err := s.hub.Bind(s.ctx, s.agent.ID, s.session.ID, s.daemons[index].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func invocation(b domain.ExecutionBinding) core.Invocation {
	return core.Invocation{SessionID: b.SessionID, ExecutionID: domain.ID("execution"), Binding: computers.BindingJSON(b)}
}

func TestTwoComputerFilesystemAndGatewayBindings(t *testing.T) {
	s := fixture(t, 2)
	a, b := s.bind(t, 0), s.bind(t, 1)
	if a.AgentHome == b.AgentHome {
		t.Fatal("independent Computers share a home")
	}
	reused, err := s.hub.Bind(s.ctx, a.AgentID, a.SessionID, a.ComputerID, "")
	if err != nil || reused != a {
		t.Fatalf("binding did not reuse its gateway: %v", err)
	}
	s.hub.Gateway = func(_ context.Context, r computers.GatewayRequest) (computers.GatewayResponse, error) {
		raw, _ := json.Marshal(r.Binding)
		return computers.GatewayResponse{Status: http.StatusOK, Body: raw}, nil
	}
	backend := &computers.Backend{Hub: s.hub}
	fsA, err := backend.FileSystem(s.ctx, invocation(a))
	if err != nil {
		t.Fatal(err)
	}
	fsB, err := backend.FileSystem(s.ctx, invocation(b))
	if err != nil {
		t.Fatal(err)
	}
	pathA, _ := fsA.ResolvePath(s.ctx, "same.txt", false)
	pathB, _ := fsB.ResolvePath(s.ctx, "same.txt", false)
	if pathA.Key == pathB.Key {
		t.Fatal("file mutation keys cross Computers")
	}
	for _, test := range []struct {
		fs   standardtools.FileSystem
		path string
		text string
	}{{fsA, pathA.Path, "from A"}, {fsB, pathB.Path, "from B"}} {
		if err = test.fs.WriteFile(s.ctx, test.path, []byte(test.text)); err != nil {
			t.Fatal(err)
		}
		data, err := test.fs.ReadFile(s.ctx, test.path)
		if err != nil || string(data) != test.text {
			t.Fatalf("read %q: %v", data, err)
		}
	}
	// A process uses the listener attached to its original binding even after a
	// separate binding has prepared the Session's other Computer.
	process, err := backend.StartProcess(s.ctx, invocation(a), standardtools.ProcessRequest{
		Command: `curl --fail --silent -X POST -H 'Content-Type: application/json' -d '{"agent_id":"forged"}' "$ASTRAL_API_URL/v1/context"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(process.Output())
	if err != nil {
		t.Fatal(err)
	}
	_ = process.Output().Close()
	if code, err := process.Wait(); err != nil || code != 0 {
		t.Fatalf("process code=%d err=%v output=%s", code, err, output)
	}
	var actual domain.ExecutionBinding
	if err = json.Unmarshal(output, &actual); err != nil || actual != a {
		t.Fatalf("gateway binding was not pinned: %s, %v", output, err)
	}
	forged := a
	forged.AgentID = "forged"
	if _, err = backend.FileSystem(s.ctx, invocation(forged)); err == nil {
		t.Fatal("accepted a forged agent binding")
	}
}

func TestFileReferencesSnapshotRangeAndSourceFailure(t *testing.T) {
	s := fixture(t, 2)
	a, _ := s.bind(t, 0), s.bind(t, 1)
	files := &filestream.Service{Store: s.store, Hub: s.hub}
	content := bytes.Repeat([]byte("original-byte-sequence\n"), 60000)
	ref, err := files.Upload(s.ctx, a, "large.txt", bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if ref.ComputerID != a.ComputerID || ref.Version == "" {
		t.Fatalf("source/version missing: %+v", ref)
	}
	length := int64(130)
	_, info, stream, err := files.Read(s.ctx, a.AgentID, a.SessionID, filestream.URL(ref), 37, &length)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(stream)
	_ = stream.Close()
	if err != nil || !bytes.Equal(data, content[37:167]) || info.Size != int64(len(content)) {
		t.Fatalf("range mismatch (%d bytes): %v", len(data), err)
	}
	entries, _ := os.ReadDir(s.store.Directory)
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "large") || entry.IsDir() {
			t.Fatalf("file bytes were saved to Server storage: %s", entry.Name())
		}
	}
	if err = os.WriteFile(ref.Path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, stream, err = files.Read(s.ctx, a.AgentID, a.SessionID, filestream.URL(ref), 0, nil); err == nil {
		stream.Close()
		t.Fatal("changed snapshot was accepted")
	}
	_ = s.daemons[0].Close(s.ctx)
	if _, _, stream, err = files.Read(s.ctx, a.AgentID, a.SessionID, filestream.URL(ref), 0, nil); err == nil {
		stream.Close()
		t.Fatal("source failure fell back to another Computer")
	}
}

func TestProcessOriginalComputerAndBootCannotBeRebound(t *testing.T) {
	s := fixture(t, 2)
	a := s.bind(t, 0)
	backend := &computers.Backend{Hub: s.hub}
	process, err := backend.StartProcess(s.ctx, invocation(a), standardtools.ProcessRequest{Command: `read line; printf 'original:%s' "$line"`, TTY: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.bind(t, 1)
	if err = process.Write(s.ctx, []byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(process.Output())
	_ = process.Output().Close()
	if err != nil || !strings.Contains(string(output), "original:hello") {
		t.Fatalf("interactive output %q, %v", output, err)
	}
	if code, err := process.Wait(); err != nil || code != 0 {
		t.Fatalf("exit %d %v", code, err)
	}
	replacement, err := computers.NewDaemon(filepath.Join(t.TempDir(), "replacement"), s.daemons[0].Credentials)
	if err != nil {
		t.Fatal(err)
	}
	s.daemons = append(s.daemons, replacement)
	go func() { _ = replacement.Connect(s.ctx) }()
	waitOnline(t, s, replacement.Credentials.ComputerID, replacement.Hello.BootID)
	err = process.Write(s.ctx, []byte("must not reach a new process\n"))
	var tool *core.ToolError
	if !errors.As(err, &tool) || tool.Outcome != core.OutcomeNotExecuted {
		t.Fatalf("stale process was not rejected before dispatch: %v", err)
	}
	if _, err = backend.FileSystem(s.ctx, invocation(a)); err == nil {
		t.Fatal("accepted stale boot binding")
	}
}

func TestEnrollmentRetryAndAgentAllowlistRemainExplicit(t *testing.T) {
	s := fixture(t, 1)
	e, err := s.hub.Enroll(s.ctx, s.server.URL, true, false)
	if err != nil {
		t.Fatal(err)
	}
	request := computers.EnrollmentRequest{Token: e.Token, Hello: s.daemons[0].Hello}
	first, err := s.hub.Register(s.ctx, e.Enrollment.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.hub.Register(s.ctx, e.Enrollment.ID, request)
	if err != nil || first != second {
		t.Fatalf("enrollment retry duplicated identity: %v", err)
	}
	request.Token = "invalid"
	if _, err = s.hub.Register(s.ctx, e.Enrollment.ID, request); err == nil {
		t.Fatal("invalid enrollment token was accepted")
	}
	if _, err = s.hub.Bind(s.ctx, s.agent.ID, s.session.ID, first.ComputerID, ""); err == nil {
		t.Fatal("new Computer silently expanded the Agent allowlist")
	}
	unauthorized := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/internal/computers/"+first.ComputerID+"/connect", nil)
	r.SetPathValue("id", first.ComputerID)
	s.hub.Connect(unauthorized, r)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized daemon status=%d", unauthorized.Code)
	}
}

func TestRolloutImagesSurviveSourceDeletionReopenAndFork(t *testing.T) {
	s := fixture(t, 1)
	binding := s.bind(t, 0)
	files := &filestream.Service{Store: s.store, Hub: s.hub}
	const pixels = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	image, err := base64.StdEncoding.DecodeString(pixels)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := files.Upload(s.ctx, binding, "original.png", bytes.NewReader(image))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Path, filepath.Join(binding.AgentHome, ".astral", "attachments")+string(filepath.Separator)) {
		t.Fatalf("original image was not saved in the dedicated attachment directory: %s", ref.Path)
	}
	message := messages.Web("image_input", "Inspect this image", "Owner", time.Now(), []domain.FileRef{ref})
	if err = files.FreezeImages(s.ctx, binding.AgentID, binding.SessionID, &message); err != nil {
		t.Fatal(err)
	}
	var frozen string
	if json.Unmarshal(message.Content[1].Data, &frozen) != nil || frozen != pixels || !strings.Contains(message.Content[0].Text, filestream.URL(ref)) {
		t.Fatal("the input did not include both attachment URI and frozen native pixels")
	}
	if err = os.Remove(ref.Path); err != nil {
		t.Fatal(err)
	}
	_ = s.daemons[0].Close(s.ctx)
	var mu sync.Mutex
	requests := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Error(readErr)
		}
		// This is the actual HTTP request body produced by astral-models.
		if !bytes.Contains(body, []byte("data:image/png;base64,"+pixels)) || !bytes.Contains(body, []byte(filestream.URL(ref))) {
			t.Error("provider request lost historical pixels or the attachment URI")
		}
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"image-response","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Image received."}}]}`))
	}))
	defer providerServer.Close()
	bridge := &modelbridge.Bridge{Store: s.store}
	provider := domain.Provider{Name: "test", Protocol: "openai-completions", Dialect: "openai", BaseURL: providerServer.URL}
	if err = bridge.SaveProvider(s.ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw := func(value any) json.RawMessage { b, _ := json.Marshal(value); return b }
	profile := domain.ModelProfile{Name: "vision", ModelID: "vision", ProviderID: provider.ID, Recommendations: map[string]json.RawMessage{
		"context_window": raw(128000), "max_output_tokens": raw(4096), "streaming": raw(false), "images": raw("supported"), "image_token_budget": raw(4096),
	}}
	if err = bridge.SaveProfile(s.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	model, err := bridge.Resolve(s.ctx, modelbridge.Reference(profile, provider), nil)
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(s.store.Directory, "core.db")
	coreStore, err := sqlite.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	config := coresession.Config{Store: coreStore, Model: model, EstimateTokens: modelbridge.EstimateTokens}
	manager, err := coresession.New(config)
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.NewSession(s.ctx, coresession.SessionOptions{ID: s.session.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, err := session.Start(s.ctx, message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = run.Wait(s.ctx); err != nil {
		t.Fatal(err)
	}
	forkAt := session.Head()
	if err = manager.Close(s.ctx); err != nil {
		t.Fatal(err)
	}
	if err = coreStore.Close(); err != nil {
		t.Fatal(err)
	}
	coreStore, err = sqlite.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer coreStore.Close()
	config.Store = coreStore
	manager, err = coresession.New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close(context.Background())
	session, err = manager.OpenSession(s.ctx, s.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err = session.Start(s.ctx, core.User("Inspect the same historical image again."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = run.Wait(s.ctx); err != nil {
		t.Fatal(err)
	}
	fork, err := session.Fork(s.ctx, forkAt, coresession.ForkOptions{ID: "image_fork"})
	if err != nil {
		t.Fatal(err)
	}
	run, err = fork.Start(s.ctx, core.User("Describe the image in this branch."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = run.Wait(s.ctx); err != nil {
		t.Fatal(err)
	}
	page, err := fork.History().Path(s.ctx, fork.Head(), coresession.Query{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range page.Entries {
		if entry.Message == nil {
			continue
		}
		for _, part := range entry.Message.Content {
			if part.Type == "image" {
				if json.Unmarshal(part.Data, &frozen) != nil || frozen != pixels {
					t.Fatal("rollout did not retain immutable image pixels")
				}
				found = true
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !found || requests != 3 {
		t.Fatalf("expected original/reopened/forked image requests; found=%v requests=%d", found, requests)
	}
}
