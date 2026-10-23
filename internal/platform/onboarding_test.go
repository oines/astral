package platform

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

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/httpapi"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
	testshell "github.com/oines/astral/internal/testkit/shell"
)

func TestDefaultBotIsCreatedOnceAndRespectsRenamingAndDeletion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := Config{Listen: "127.0.0.1:0", NoLocal: true, DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers")}
	open := func() *Server {
		t.Helper()
		s, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close(ctx) })
		return s
	}
	s := open()
	state, err := s.Harness.Onboarding(ctx)
	if err != nil || state.Completed || state.AgentID == "" {
		t.Fatalf("fresh installation did not begin setup: %+v %v", state, err)
	}
	var bot domain.Agent
	if err = s.Store.Get(ctx, "agents", state.AgentID, &bot); err != nil {
		t.Fatal(err)
	}
	if bot.Name != "Astral" || len(bot.AllowedComputers) != 0 || bot.DefaultModelID != "" || bot.DefaultComputerID != "" {
		t.Fatalf("default Bot should await explicit resource selection: %+v", bot)
	}
	bot.Name = "小星"
	if err = s.Harness.SaveAgent(ctx, &bot); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s = open()
	var renamed domain.Agent
	if err = s.Store.Get(ctx, "agents", state.AgentID, &renamed); err != nil || renamed.Name != "小星" || renamed.Revision != bot.Revision {
		t.Fatal("restart overwrote the existing Bot", renamed, err)
	}
	items, _ := s.Store.List(ctx, "agents")
	if len(items) != 1 {
		t.Fatal("restart created a duplicate Bot")
	}
	if err = s.Harness.DeleteAgent(ctx, renamed.ID, renamed.Revision); err != nil {
		t.Fatal(err)
	}
	_ = s.Close(ctx)
	s = open()
	items, _ = s.Store.List(ctx, "agents")
	state, err = s.Harness.Onboarding(ctx)
	if len(items) != 0 || err != nil || !state.Completed {
		t.Fatal("a deliberately deleted Bot was recreated or setup was left blocking")
	}
}

func TestExistingInstallationKeepsItsBotsAndResourceChoices(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := Config{Listen: "127.0.0.1:0", NoLocal: true, DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers")}
	store, err := persistence.Open(config.DataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	bot := domain.Agent{Meta: domain.Meta{ID: domain.ID("agent")}, Name: "Existing Bot", DefaultModelID: "existing-model", DefaultComputerID: "existing-computer", AllowedComputers: []string{"existing-computer"}}
	if err = store.Save(ctx, "agents", &bot.Meta, &bot); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	s, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	state, err := s.Harness.Onboarding(ctx)
	var existing domain.Agent
	if err != nil || !state.Completed || state.AgentID != "" {
		t.Fatal("existing installation was forced through setup")
	}
	if err = s.Store.Get(ctx, "agents", bot.ID, &existing); err != nil || string(testkit.JSON(existing)) != string(testkit.JSON(bot)) {
		t.Fatal("initialization modified an existing Bot", err)
	}
	items, _ := s.Store.List(ctx, "agents")
	if len(items) != 1 {
		t.Fatal("initialization added an unexpected Bot")
	}
}

func TestOnboardingRequiresBothResourcesAndStartsAWorkingSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	s, err := New(Config{Listen: "127.0.0.1:0", DataDirectory: filepath.Join(root, "server"), ComputerDirectory: filepath.Join(root, "computers")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	local, err := s.UseLocal(ctx)
	if err != nil || !local.Online {
		t.Fatal("local Computer did not connect", err)
	}
	var bootstrap httpapi.Bootstrap
	if err = HTTPJSON(ctx, "GET", s.URL+"/api/v1/bootstrap", s.token, nil, &bootstrap); err != nil {
		t.Fatal(err)
	}
	if len(bootstrap.Agents) != 1 || bootstrap.Onboarding.Completed || len(bootstrap.Agents[0].AllowedComputers) != 0 {
		t.Fatal("local registration should not complete setup or grant access")
	}
	var commandWorked atomic.Bool
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools    []any `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if decodeErr := json.NewDecoder(r.Body).Decode(&body); decodeErr != nil {
			t.Error(decodeErr)
			w.WriteHeader(400)
			return
		}
		response := testkit.Text("Ready")
		if len(body.Tools) > 0 {
			response = testkit.Tool("exec_command", map[string]any{"cmd": testshell.Print("onboarding-command-ok"), "login": false, "yield_time_ms": 3000})
			for _, message := range body.Messages {
				if message.Role == "tool" {
					commandWorked.Store(strings.Contains(string(testkit.JSON(message.Content)), "onboarding-command-ok"))
					response = testkit.Text("The Computer is ready.")
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer model.Close()
	provider := domain.Provider{Name: "Setup model service", Protocol: "openai-completions", Dialect: "openai", BaseURL: model.URL}
	if err = s.Harness.Models.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	profile := domain.ModelProfile{Name: "Setup model", ProviderID: provider.ID, ModelID: "setup-model", Recommendations: map[string]json.RawMessage{"context_window": testkit.JSON(128000), "max_output_tokens": testkit.JSON(4096), "streaming": testkit.JSON(false)}}
	if err = s.Harness.Models.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	offline := domain.Computer{Meta: domain.Meta{ID: domain.ID("computer")}, Name: "Offline Computer"}
	if err = s.Store.Save(ctx, "computers", &offline.Meta, &offline); err != nil {
		t.Fatal(err)
	}
	complete := func(input harness.OnboardingInput) (domain.Agent, error) {
		var bot domain.Agent
		err := HTTPJSON(ctx, "POST", s.URL+"/api/v1/onboarding/complete", s.token, input, &bot)
		return bot, err
	}
	for _, input := range []harness.OnboardingInput{{ModelID: profile.ID}, {ComputerID: local.ID}, {ModelID: profile.ID, ComputerID: offline.ID}, {ModelID: "missing", ComputerID: local.ID}} {
		if _, err = complete(input); err == nil {
			t.Fatal("incomplete or unavailable resources were accepted", input)
		}
	}
	no := false
	profile.Enabled = &no
	if err = s.Harness.Models.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	input := harness.OnboardingInput{ModelID: profile.ID, ComputerID: local.ID}
	if _, err = complete(input); err == nil {
		t.Fatal("a disabled model was accepted")
	}
	profile.Enabled = nil
	if err = s.Harness.Models.SaveProfile(ctx, &profile); err != nil {
		t.Fatal(err)
	}
	provider.Enabled = &no
	if err = s.Harness.Models.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = complete(input); err == nil {
		t.Fatal("a disabled provider was accepted")
	}
	provider.Enabled = nil
	if err = s.Harness.Models.SaveProvider(ctx, &provider, nil, nil); err != nil {
		t.Fatal(err)
	}
	var untouched domain.Agent
	if err = s.Store.Get(ctx, "agents", bootstrap.Onboarding.AgentID, &untouched); err != nil || untouched.DefaultModelID != "" || len(untouched.AllowedComputers) != 0 {
		t.Fatal("failed setup partially assigned resources")
	}
	bot, err := complete(input)
	if err != nil || bot.DefaultModelID != profile.ID || bot.DefaultComputerID != local.ID || len(bot.AllowedComputers) != 1 || bot.AllowedComputers[0] != local.ID {
		t.Fatal("setup did not atomically select and authorize both resources", bot, err)
	}
	repeated, err := complete(input)
	if err != nil || repeated.Revision != bot.Revision {
		t.Fatal("retry changed an already completed setup", err)
	}
	if _, err = complete(harness.OnboardingInput{ModelID: profile.ID, ComputerID: offline.ID}); err == nil {
		t.Fatal("a stale setup request overwrote the completed configuration")
	}
	if err = HTTPJSON(ctx, "GET", s.URL+"/api/v1/bootstrap", s.token, nil, &bootstrap); err != nil || !bootstrap.Onboarding.Completed {
		t.Fatal("bootstrap did not report completed setup", err)
	}
	session, err := s.Harness.CreateSession(ctx, bot.ID, "")
	if err != nil || session.ProfileID != profile.ID || session.ComputerID != local.ID {
		t.Fatal("new conversation did not inherit setup", session, err)
	}
	if _, err = s.Harness.Submit(ctx, session.ID, harness.InputRequest{ID: "setup-first-message", Text: "Check that the Computer works."}); err != nil {
		t.Fatal(err)
	}
	await(t, ctx, func() bool {
		view, viewErr := s.Harness.View(ctx, session.ID)
		if viewErr != nil {
			t.Fatal(viewErr)
		}
		if view.Status == "needs_attention" {
			t.Fatal("first conversation could not run", view.Error)
		}
		return view.Status == "idle" && commandWorked.Load()
	})
}
