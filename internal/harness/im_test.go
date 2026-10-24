package harness

import (
	"context"
	"errors"
	"testing"

	core "github.com/oines/astral-core"
)

func TestInterruptedMessageCallResumesAfterReopen(t *testing.T) {
	f := newFixture(t, 0, func(_ context.Context, n int, _ map[string]any) any {
		if n == 1 {
			return toolReply("send", "send_message", map[string]string{"text": "hello"})
		}
		return textReply("finished")
	})
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	model, err := r.resolveModel(f.ctx, f.session.ModelRef)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.core.Load().Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	sends := 0
	config := r.coreConfig(model, f.session.Options)
	config.Tools = []core.Tool{core.ToolFunc{Spec: DefaultMessageTool(), Run: func(context.Context, core.Invocation, func(core.ToolProgress)) (core.ToolExecution, error) {
		sends++
		return core.ToolExecution{Result: &core.ToolResult{Content: core.Text("connection interrupted"), Outcome: core.OutcomeUnknown, IsError: true}}, nil
	}}}
	session, err := core.OpenSession(f.ctx, config, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.core.Store(session)
	configuration := session.Configuration()
	configuration.Tools = []core.ToolDefinition{DefaultMessageTool()}
	if err = session.Configure(f.ctx, configuration); err != nil {
		t.Fatal(err)
	}
	prompt, err := session.Prompt(f.ctx, core.Input{ID: "send", Message: core.Message{Role: "user", Content: core.Text("hello")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prompt.Run.Wait(f.ctx); !errors.Is(err, core.ErrOutcomeUnknown) {
		t.Fatalf("interrupted call: %v", err)
	}
	f.restart(t)
	view, err := f.service.View(f.ctx, f.session.ID)
	if err != nil || view.Status != "idle" || !view.CanContinue || len(view.Unresolved) != 0 || sends != 1 || f.count() != 1 {
		t.Fatalf("reopened call: %+v, %v, sends=%d", view, err, sends)
	}
	if err = f.service.Continue(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
}
