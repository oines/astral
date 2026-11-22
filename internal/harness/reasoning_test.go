package harness

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func TestAgentReasoningDefaultsAreCopiedIntoNewSessions(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"low", "high"}})
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	f.agent.DefaultReasoning = &core.ReasoningOptions{Effort: "high"}
	if err := f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	first, err := f.service.CreateSession(f.ctx, f.agent.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	if choice := sessionSelection(t, f, first.ID); choice.Reasoning == nil || choice.Reasoning.Effort != "high" {
		t.Fatalf("new Session did not inherit reasoning: %+v", choice)
	}
	selected, err := f.service.Core(f.ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Configuration().Selection.Reasoning.Effort != "high" {
		t.Fatal("Core did not use inherited reasoning")
	}
	f.agent.DefaultReasoning = &core.ReasoningOptions{Effort: "low"}
	if err = f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	second, err := f.service.CreateSession(f.ctx, f.agent.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if choice := sessionSelection(t, f, second.ID); choice.Reasoning == nil || choice.Reasoning.Effort != "low" {
		t.Fatal("later Session did not use updated default")
	}
	if err = f.store.Get(f.ctx, "sessions", first.ID, &first); err != nil {
		t.Fatal(err)
	}
	if choice := sessionSelection(t, f, first.ID); choice.Reasoning == nil || choice.Reasoning.Effort != "high" {
		t.Fatal("changing Bot defaults rewrote an existing Session")
	}
	if err = f.service.SelectModel(f.ctx, first.ID, profile.ID, nil); err != nil {
		t.Fatal(err)
	}
	first = domain.Session{Meta: domain.Meta{ID: first.ID}}
	if err = f.store.Get(f.ctx, "sessions", first.ID, &first); err != nil {
		t.Fatal(err)
	}
	if choice := sessionSelection(t, f, first.ID); choice.Reasoning != nil || choice.Options.Reasoning.Effort != "" {
		t.Fatal("returning to model defaults retained the Bot override")
	}
	var agent domain.Agent
	if err = f.store.Get(f.ctx, "agents", f.agent.ID, &agent); err != nil {
		t.Fatal(err)
	}
	if agent.DefaultReasoning.Effort != "low" {
		t.Fatal("Session selection wrote back to Bot defaults")
	}
	f.restart(t)
	selected, err = f.service.Core(f.ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Configuration().Selection.Reasoning.Effort != "low" {
		t.Fatal("inherited reasoning was lost after restart")
	}
}

func TestAgentReasoningRejectsMissingModelAndUnsupportedEffort(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("unexpected") })
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"low", "high"}})
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []bool{false, true} {
		for _, model := range []string{"", profile.ID} {
			agent := f.agent
			if probe {
				agent.ProbeModelID, agent.ProbeReasoning = model, &core.ReasoningOptions{Effort: "unsupported"}
			} else {
				agent.DefaultModelID, agent.DefaultReasoning = model, &core.ReasoningOptions{Effort: "unsupported"}
			}
			if err := f.service.SaveAgent(f.ctx, &agent); err == nil {
				t.Fatalf("accepted invalid reasoning (probe=%v model=%s)", probe, model)
			}
		}
	}
	if f.count() != 0 {
		t.Fatal("saving defaults sent a provider request")
	}
}

func TestExplicitInterfaceDefaultBypassesProfileDefaultsAcrossRestart(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"low", "high"}})
	profile.Overrides = map[string]json.RawMessage{"defaults": encode(core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "high"}})}
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		id        string
		selection *core.ReasoningOptions
		want      string
		restart   bool
	}{
		{"inherit", nil, "high", false},
		{"api-default", &core.ReasoningOptions{}, "", true},
		{"inherit-again", nil, "high", false},
	} {
		if err := f.service.SelectModel(f.ctx, f.session.ID, profile.ID, step.selection); err != nil {
			t.Fatal(err)
		}
		if step.restart {
			f.restart(t)
		}
		saved := sessionSelection(t, f, f.session.ID)
		if (saved.Reasoning == nil) != (step.selection == nil) {
			t.Fatalf("%s: empty override and inheritance were conflated", step.id)
		}
		if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: step.id, Text: "hello"}); err != nil {
			t.Fatal(err)
		}
		f.idle(t, f.session.ID)
		f.mu.Lock()
		var body map[string]any
		err := json.Unmarshal(f.requests[len(f.requests)-1], &body)
		f.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		got, exists := body["reasoning_effort"]
		if step.want == "" && exists || step.want != "" && got != step.want {
			t.Fatalf("%s: actual reasoning_effort=%v (present=%v), want %q", step.id, got, exists, step.want)
		}
	}
}

func TestSessionReasoningEffortPreservesReportedBudgetAcrossRestart(t *testing.T) {
	const inputTokens, outputTokens = 62013, 137
	const oldVersion, newVersion = "0.1.45", "0.1.48"
	f := newFixture(t, 0, func(_ context.Context, n int, request map[string]any) any {
		want := "high"
		if n == 2 {
			want = "max"
		}
		if got := request["reasoning_effort"]; got != want {
			t.Errorf("generation %d sent reasoning_effort=%v, want %s", n, got, want)
		}
		reply := textReply("done").(map[string]any)
		reply["usage"] = map[string]any{"prompt_tokens": inputTokens, "completion_tokens": outputTokens}
		return reply
	})

	f.service.Models.Version = oldVersion
	versions := []string{"Astral/" + oldVersion, "Astral/" + newVersion}
	transport := f.service.Models.HTTPClient.Transport
	var uploads atomic.Int32
	f.service.Models.HTTPClient.Transport = modelRecoveryTransport(func(req *http.Request) (*http.Response, error) {
		index := int(uploads.Add(1)) - 1
		if index >= len(versions) || req.UserAgent() != versions[index] {
			t.Errorf("unexpected User-Agent %q on request %d", req.UserAgent(), index+1)
		}
		return transport.RoundTrip(req)
	})
	var profile domain.ModelProfile
	if err := f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	profile.Recommendations["reasoning"] = encode(map[string]any{"efforts": []string{"high", "max"}})
	profile.Overrides = map[string]json.RawMessage{"defaults": encode(core.GenerationOptions{Reasoning: core.ReasoningOptions{Effort: "high"}})}
	if err := f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "first", Text: "First turn"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if err := f.service.SelectModel(f.ctx, f.session.ID, profile.ID, &core.ReasoningOptions{Effort: "max"}); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	f.service.Models.Version = newVersion
	view, err := f.service.InspectContext(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.EstimatedInput != inputTokens+outputTokens {
		t.Fatalf("after changing effort, estimated %d tokens for the unchanged history, want reported %d", view.EstimatedInput, inputTokens+outputTokens)
	}
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "second", Text: "Second turn"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	page, err := f.coreStore.ScanAttempts(f.ctx, f.session.ID, core.AttemptQuery{Purpose: "generation"})
	if err != nil || len(page.Attempts) != len(versions) || f.count() != len(versions) {
		t.Fatalf("expected two generations, got %d requests and %d attempts (%v)", f.count(), len(page.Attempts), err)
	}
	for i, attempt := range page.Attempts {
		var snapshot struct {
			UserAgent string `json:"user_agent"`
		}
		if err := json.Unmarshal(attempt.Input.Snapshot, &snapshot); err != nil || snapshot.UserAgent != versions[i] {
			t.Fatalf("attempt %d froze User-Agent %q, want %q (%v)", i+1, snapshot.UserAgent, versions[i], err)
		}
	}
}

func sessionSelection(t *testing.T, f *fixture, id string) core.ModelSelection {
	t.Helper()
	selection, err := f.service.ModelSelection(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return selection
}
