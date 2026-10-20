// Package testkit supplies real local platform components with fake network
// providers for end-to-end boundary tests. No external credentials are used.
package testkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

type Platform struct {
	Context   context.Context
	Store     *persistence.Store
	CoreStore *sqlite.Store
	Harness   *harness.Service
	Hub       *computers.Hub
	Daemons   []*computers.Daemon
	Agent     domain.Agent
	Profile   domain.ModelProfile
	Session   domain.Session
	Directory string
	Requests  atomic.Int64
}

func JSON(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
func Text(text string) any {
	return map[string]any{"id": domain.ID("reply"), "choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5}}
}
func Tool(name string, args any) map[string]any {
	return map[string]any{"id": domain.ID("reply"), "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": domain.ID("call"), "type": "function", "function": map[string]any{"name": name, "arguments": string(JSON(args))}}}}}}}
}
func Await(t *testing.T, ctx context.Context, check func() bool) {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for !check() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}
func New(t *testing.T, computerCount int, generate func(context.Context, int, map[string]any) any) *Platform {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	p := &Platform{Context: ctx, Directory: t.TempDir()}
	var err error
	p.Store, err = persistence.Open(filepath.Join(p.Directory, "data"))
	if err != nil {
		t.Fatal(err)
	}
	p.CoreStore, err = sqlite.Open(filepath.Join(p.Directory, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	p.Hub = computers.NewHub(p.Store)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/computers/{id}/connect", p.Hub.Connect)
	server := httptest.NewServer(mux)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		tools, _ := request["tools"].([]any)
		var response any
		if len(tools) == 1 && strings.Contains(string(JSON(tools)), "astral_verify") {
			if request["tool_choice"] == "none" {
				response = Text("verified")
			} else {
				response = Tool("astral_verify", map[string]string{"value": "ping"})
			}
		} else {
			response = generate(r.Context(), int(p.Requests.Add(1)), request)
		}
		if response != nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
		}
	}))
	bridge := &modelbridge.Bridge{Store: p.Store, HTTPClient: provider.Client()}
	modelProvider := domain.Provider{Name: "test Provider", Dialect: "openai", Protocol: "openai-completions", BaseURL: provider.URL}
	key := "test-only-provider-key"
	if err = bridge.SaveProvider(ctx, &modelProvider, &key, nil); err != nil {
		t.Fatal(err)
	}
	p.Profile = domain.ModelProfile{Name: "test model", ProviderID: modelProvider.ID, ModelID: "fixture", Recommendations: map[string]json.RawMessage{
		"context_window": JSON(128000), "max_output_tokens": JSON(4096), "streaming": JSON(false), "images": JSON("supported"), "image_token_budget": JSON(2048),
	}}
	if err = bridge.SaveProfile(ctx, &p.Profile); err != nil {
		t.Fatal(err)
	}
	if _, err = bridge.Verify(ctx, p.Profile.ID); err != nil {
		t.Fatal(err)
	}
	p.Harness = harness.New(p.Store, p.CoreStore, bridge, p.Hub)
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = p.Harness.Close(cleanup)
		cancel()
		for _, d := range p.Daemons {
			_ = d.Close(cleanup)
		}
		_ = p.Hub.Close()
		server.Close()
		provider.Close()
		_ = p.CoreStore.Close()
		_ = p.Store.Close()
	})
	for range computerCount {
		d, createErr := computers.NewDaemon(t.TempDir(), computers.Credentials{ServerURL: server.URL})
		if createErr != nil {
			t.Fatal(createErr)
		}
		enrollment, enrollErr := p.Hub.Enroll(ctx, server.URL, false, false)
		if enrollErr != nil {
			t.Fatal(enrollErr)
		}
		credentials, registerErr := p.Hub.Register(ctx, enrollment.Enrollment.ID, computers.EnrollmentRequest{Token: enrollment.Token, Hello: d.Hello})
		if registerErr != nil {
			t.Fatal(registerErr)
		}
		credentials.ServerURL = server.URL
		d.Credentials = credentials
		p.Daemons = append(p.Daemons, d)
		go func() { _ = d.Connect(ctx) }()
		Await(t, ctx, func() bool {
			var c domain.Computer
			return p.Store.Get(ctx, "computers", credentials.ComputerID, &c) == nil && p.Hub.Status(c).Online
		})
		p.Agent.AllowedComputers = append(p.Agent.AllowedComputers, credentials.ComputerID)
	}
	p.Agent.Name, p.Agent.DefaultModelID = "test Agent", p.Profile.ID
	if computerCount > 0 {
		p.Agent.DefaultComputerID = p.Daemons[0].Credentials.ComputerID
	}
	if err = p.Harness.SaveAgent(ctx, &p.Agent); err != nil {
		t.Fatal(err)
	}
	p.Session, err = p.Harness.CreateSession(ctx, p.Agent.ID, "test Session")
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (p *Platform) Idle(t *testing.T, id string) harness.View {
	t.Helper()
	var view harness.View
	Await(t, p.Context, func() bool {
		var err error
		view, err = p.Harness.View(p.Context, id)
		if err != nil {
			t.Error(err)
		}
		return err == nil && view.Status != "running" && view.Status != "stopping" && view.Status != "compacting"
	})
	if view.Status == "needs_attention" {
		t.Fatalf("Session failed: %+v", view)
	}
	return view
}
