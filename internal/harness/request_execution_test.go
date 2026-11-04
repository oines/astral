package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral-core/models/provider/openai/completions"
)

type hostExecutionProbe struct {
	core.ModelFunc
	prepare   func(core.Request) (core.Request, error)
	execution func(core.Request) (core.RequestExecution, error)
}

type hostMutableValidator struct{ core.ModelFunc }

func (hostMutableValidator) ValidateRequest(_ context.Context, request core.Request) error {
	request.Messages[0].Content[0].Text = "private validator mutation"
	return nil
}

func TestSessionReadonlyValidationPreservesLegacyIsolation(t *testing.T) {
	request := core.Request{Messages: []core.Message{core.User("caller-owned text")}}
	model := sessionModel{Model: hostMutableValidator{}}
	if err := core.ValidateRequest(context.Background(), model, request); err != nil {
		t.Fatal(err)
	}
	if request.Messages[0].Content[0].Text != "caller-owned text" {
		t.Fatal("Host readonly forwarding exposed caller data to a legacy validator")
	}
}

func (m hostExecutionProbe) PrepareRequest(_ context.Context, request core.Request) (core.Request, error) {
	if m.prepare != nil {
		return m.prepare(request)
	}
	return request, nil
}
func (m hostExecutionProbe) PrepareExecution(_ context.Context, request core.Request) (core.RequestExecution, error) {
	return m.execution(request)
}

func TestSessionExecutionUsesPreparedPersonaUntilNextRequest(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("execution wrapper test called provider")
		return textReply("unexpected")
	})
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var prepares atomic.Int32
	var executed []string
	probe := hostExecutionProbe{prepare: func(request core.Request) (core.Request, error) { prepares.Add(1); return request, nil }, execution: func(request core.Request) (core.RequestExecution, error) {
		prompt := request.SystemPrompt
		return core.RequestExecutionFunc(func(context.Context, func(core.ModelEvent)) (core.Reply, error) {
			executed = append(executed, prompt)
			return core.Reply{Message: core.Message{Role: core.RoleAssistant, Content: core.Text("done"), FinishReason: core.FinishStop}}, nil
		}), nil
	}}
	wrapped := sessionModel{Model: probe, runtime: r}
	request := core.Request{ModelID: "execution", SystemPrompt: webSystemPrompt, Messages: []core.Message{core.User("prepare persona")}, MaxOutputTokens: 100}
	savePersona(t, f, "PERSONA_ORIGINAL_EXECUTION")
	prepared, err := core.ProjectRequest(f.ctx, wrapped, request)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := wrapped.PrepareExecution(f.ctx, prepared)
	if err != nil || execution == nil {
		t.Fatalf("forward execution: %v", err)
	}
	savePersona(t, f, "PERSONA_NEXT_EXECUTION")
	if _, err := execution.Stream(f.ctx, func(core.ModelEvent) {}); err != nil {
		t.Fatal(err)
	}
	prepared, err = core.ProjectRequest(f.ctx, wrapped, request)
	if err != nil {
		t.Fatal(err)
	}
	execution, err = wrapped.PrepareExecution(f.ctx, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Stream(f.ctx, func(core.ModelEvent) {}); err != nil {
		t.Fatal(err)
	}
	if prepares.Load() != 2 || len(executed) != 2 || !strings.Contains(executed[0], "PERSONA_ORIGINAL_EXECUTION") || strings.Contains(executed[0], "PERSONA_NEXT_EXECUTION") || !strings.Contains(executed[1], "PERSONA_NEXT_EXECUTION") {
		t.Fatal("prepared execution bypassed Host preparation or refreshed frozen persona")
	}
}

func TestSessionProjectionForwardsSDKReadonlyPixelsAndCurrentPersona(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("projection test called provider")
		return textReply("unexpected")
	})
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := completions.New(models.Config{Spec: core.ModelSpec{ID: "projection", ContextWindow: 64000, MaxOutputTokens: 1024}, Model: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	savePersona(t, f, "READONLY_PERSONA_EXECUTION")
	request := core.Request{ModelID: "projection", SystemPrompt: webSystemPrompt, MaxOutputTokens: 100, Messages: []core.Message{{Role: core.RoleUser, Content: []core.Content{{Type: "image", MediaType: "image/png", Data: json.RawMessage(`"eA=="`)}}}}}
	projected, err := core.ProjectRequest(f.ctx, sessionModel{Model: sdk, runtime: r}, request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(projected.SystemPrompt, "READONLY_PERSONA_EXECUTION") || &projected.Messages[0].Content[0].Data[0] != &request.Messages[0].Content[0].Data[0] {
		t.Fatal("Host projection copied pixels or omitted persona preparation")
	}
	if request.SystemPrompt != webSystemPrompt || string(request.Messages[0].Content[0].Data) != `"eA=="` {
		t.Fatal("readonly Host projection mutated source request")
	}
}

func TestSessionExecutionRetainsSubagentTreeGuardAndSummaryBypass(t *testing.T) {
	var streams atomic.Int32
	probe := hostExecutionProbe{execution: func(core.Request) (core.RequestExecution, error) {
		return core.RequestExecutionFunc(func(context.Context, func(core.ModelEvent)) (core.Reply, error) {
			streams.Add(1)
			return core.Reply{}, nil
		}), nil
	}}
	s := &Service{}
	r := &runtime{service: s, subagent: &subagentSettings{Configuration: core.Configuration{SystemPrompt: "child-system"}}}
	wrapped := sessionModel{Model: probe, runtime: r}
	s.subagentMu.Lock()
	defer s.subagentMu.Unlock()
	execution, err := wrapped.PrepareExecution(context.Background(), core.Request{SystemPrompt: "child-system"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if _, err := execution.Stream(ctx, func(core.ModelEvent) {}); !errors.Is(err, context.DeadlineExceeded) || streams.Load() != 0 {
		t.Fatalf("prepared child skipped tree guard: streams=%d %v", streams.Load(), err)
	}
	summary, err := wrapped.PrepareExecution(context.Background(), core.Request{SystemPrompt: "summary-system"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := summary.Stream(context.Background(), func(core.ModelEvent) {}); err != nil || streams.Load() != 1 {
		t.Fatalf("auxiliary summary acquired child tree guard: streams=%d %v", streams.Load(), err)
	}
}
