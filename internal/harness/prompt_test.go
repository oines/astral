package harness

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func wireSystemPrompt(request map[string]any) string {
	for _, raw := range request["messages"].([]any) {
		message := raw.(map[string]any)
		if message["role"] == "system" {
			text, _ := message["content"].(string)
			return text
		}
	}
	return ""
}

func savePersona(t *testing.T, f *fixture, persona string) {
	t.Helper()
	var agent domain.Agent
	if err := f.store.Get(f.ctx, "agents", f.agent.ID, &agent); err != nil {
		t.Fatal(err)
	}
	agent.Persona = persona
	if err := f.service.SaveAgent(f.ctx, &agent); err != nil {
		t.Fatal(err)
	}
}

func TestSystemPromptMatchesCurrentBindingAndExposedTools(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("done") })
	persona := "You are a fictional photographer named River."
	savePersona(t, f, persona)
	check := func(label, id string, bound bool) {
		t.Helper()
		if _, err := f.service.Submit(f.ctx, id, InputRequest{ID: domain.ID("input"), Text: "continue the conversation"}); err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)
		f.mu.Lock()
		wire := append([]byte(nil), f.requests[len(f.requests)-1]...)
		f.mu.Unlock()
		var request map[string]any
		if err := json.Unmarshal(wire, &request); err != nil {
			t.Fatal(err)
		}
		prompt := wireSystemPrompt(request)
		hasSend := false
		for _, raw := range request["tools"].([]any) {
			function := raw.(map[string]any)["function"].(map[string]any)
			hasSend = hasSend || function["name"] == "send_message"
		}
		if hasSend != bound || strings.Contains(prompt, "send_message") != bound || strings.Contains(prompt, "IM observations") != bound {
			t.Fatalf("%s: prompt and tool availability disagree with binding: %s", label, prompt)
		}
		if !strings.HasSuffix(prompt, personaSeparator+persona) || strings.Contains(prompt, "Probe") || strings.Contains(prompt, "wake") || strings.Contains(prompt, "Host context") {
			t.Fatalf("%s: prompt lost persona or included irrelevant runtime instructions: %s", label, prompt)
		}
		if !strings.Contains(prompt, `"`+environmentSnapshotHeading+`"`) || len(wireEnvironments(request)) == 0 {
			t.Fatalf("%s: prompt does not name the actual environment snapshot", label)
		}
	}
	setBinding := func(binding, source string) {
		t.Helper()
		if err := f.service.WithStoppedSessions(f.ctx, []string{f.session.ID}, func() error {
			var record domain.Session
			if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
				return err
			}
			record.BindingID, record.ConversationID = binding, ""
			if binding != "" {
				record.ConversationID = "conversation-" + binding
			}
			if source != "" {
				record.Source = source
			}
			return f.store.Save(f.ctx, "sessions", &record.Meta, &record)
		}); err != nil {
			t.Fatal(err)
		}
	}
	check("ordinary Web Session", f.session.ID, false)
	setBinding("first", "")
	check("Web-created Session bound to IM", f.session.ID, true)
	persona = "You are a fictional gardener named Brook."
	savePersona(t, f, persona)
	check("persona updated in a bound Session", f.session.ID, true)
	f.restart(t)
	check("reopened bound Session", f.session.ID, true)
	parent, err := f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.service.Fork(f.ctx, f.session.ID, parent.Head(), "unbound fork", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	check("unbound fork of an IM Session", child.ID, false)
	setBinding("", "")
	check("unbound original Session", f.session.ID, false)
	setBinding("second", "im")
	check("IM-created Session", f.session.ID, true)
	setBinding("", "")
	check("IM-created Session after unbinding", f.session.ID, false)
}

func TestPersonaAppliesToNextRequestInEverySessionWithoutContextChanges(t *testing.T) {
	const original = "你是住在海边的小说家。\n说话简洁。"
	const updated = "你是住在山里的画家。\n喜欢讨论色彩。"
	var expected atomic.Value
	expected.Store(original)
	entered, release := make(chan struct{}), make(chan struct{})
	f := newFixture(t, 1, func(ctx context.Context, n int, request map[string]any) any {
		if got, want := wireSystemPrompt(request), composeSystemPrompt(webSystemPrompt, expected.Load().(string)); got != want {
			t.Errorf("request %d did not use current persona: %q", n, got)
		}
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil
			}
			return toolReply("continue", "exec_command", map[string]any{"command": "true"})
		}
		return textReply("done")
	})
	other, err := f.service.CreateSession(f.ctx, f.agent.ID, "already open")
	if err != nil {
		t.Fatal(err)
	}
	savePersona(t, f, original)
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "active", Text: "continue after a tool"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	session, _ := f.service.Core(f.ctx, f.session.ID)
	head := session.Head()
	savePersona(t, f, updated)
	expected.Store(updated)
	if session.Head() != head || f.count() != 1 {
		t.Fatal("saving a persona rewrote context, reconfigured Core or woke a model")
	}
	if _, err = f.service.Submit(f.ctx, other.ID, InputRequest{ID: "other", Text: "use the current persona"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, other.ID)
	close(release)
	f.idle(t, f.session.ID)
	if f.count() != 3 || len(environmentEntries(t, f, f.session.ID)) != 1 {
		t.Fatal("persona change added an environment snapshot or failed to affect the active run")
	}

	// Clearing the field removes the entire optional block in both existing
	// Sessions, and reopening must not restore a copied per-Session persona.
	savePersona(t, f, " \n\t")
	expected.Store("")
	f.restart(t)
	for _, id := range []string{f.session.ID, other.ID} {
		if _, err = f.service.Submit(f.ctx, id, InputRequest{ID: "cleared", Text: "continue"}); err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)
	}

	// Reconstructed requests retain exactly the persona used at the time, even
	// after the saved field has changed twice and the runtime was reopened.
	session, _ = f.service.Core(f.ctx, f.session.ID)
	page, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "generation", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, attempt := range page.Attempts {
		request, err := session.Attempts().Request(f.ctx, attempt.ID)
		if err != nil {
			t.Fatal(err)
		}
		seen[request.SystemPrompt] = true
	}
	for _, persona := range []string{original, updated, ""} {
		if !seen[composeSystemPrompt(webSystemPrompt, persona)] {
			t.Fatalf("frozen request lost persona %q", persona)
		}
	}
}

func TestPersonaDoesNotReplaceCompactionPromptOrLeakAcrossBots(t *testing.T) {
	const persona = "You are a fictional human painter named River."
	f := newFixture(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		return textReply("The prior conversation concerned painting.")
	})
	savePersona(t, f, persona)
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "long", Text: strings.Repeat("conversation about painting ", 3500)}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	if err := f.service.Compact(f.ctx, f.session.ID, ""); err != nil {
		t.Fatal(err)
	}
	session, _ := f.service.Core(f.ctx, f.session.ID)
	page, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "compaction", Limit: 1})
	if err != nil || len(page.Attempts) != 1 {
		t.Fatalf("missing compaction: %v", err)
	}
	request, err := session.Attempts().Request(f.ctx, page.Attempts[0].ID)
	if err != nil || strings.Contains(request.SystemPrompt, persona) || strings.HasPrefix(request.SystemPrompt, commonSystemPrompt) {
		t.Fatalf("persona replaced the summary instruction: %v", err)
	}
	view, err := f.service.InspectContext(f.ctx, f.session.ID)
	if err != nil || view.Request.SystemPrompt != composeSystemPrompt(webSystemPrompt, persona) {
		t.Fatalf("compaction lost current persona: %v", err)
	}
	other := f.agent
	other.Meta, other.Name = domain.Meta{}, "Another Bot"
	if err = f.service.SaveAgent(f.ctx, &other); err != nil {
		t.Fatal(err)
	}
	otherSession, err := f.service.CreateSession(f.ctx, other.ID, "independent")
	if err != nil {
		t.Fatal(err)
	}
	view, err = f.service.InspectContext(f.ctx, otherSession.ID)
	if err != nil || view.Request.SystemPrompt != webSystemPrompt {
		t.Fatalf("persona leaked into another Bot: %v", err)
	}
}
