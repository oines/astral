package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
)

func TestInterruptedToolResultReachesModel(t *testing.T) {
	for _, test := range []struct {
		name string
		live bool
		err  bool
	}{{"send_message", false, false}, {"exec_command", false, false}, {"write_file", true, false}, {"write_file", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			computers := 0
			if test.live {
				computers = 1
			}
			f := newFixture(t, computers, func(_ context.Context, n int, request map[string]any) any {
				if n == 1 {
					return toolReply("call", test.name, map[string]any{})
				}
				if test.live && !strings.Contains(string(encode(request)), "effects are unconfirmed") {
					t.Error("tool uncertainty did not reach the model")
				}
				return textReply("finished")
			})
			r, err := f.service.open(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.core.Load().Close(f.ctx); err != nil {
				t.Fatal(err)
			}
			calls := 0
			config := r.coreConfig(f.session)
			var tool core.Tool = core.ToolFunc{Spec: core.ToolDefinition{Name: test.name, Version: "fixture", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)}, Run: func(context.Context, core.Invocation, func(core.ToolProgress)) (core.ToolExecution, error) {
				calls++
				if test.err {
					return core.ToolExecution{}, &core.ToolError{Outcome: core.OutcomeUnknown, Cause: errors.New("connection closed")}
				}
				return core.ToolExecution{Result: &core.ToolResult{Content: core.Text("Request interrupted; effects are unconfirmed."), Outcome: core.OutcomeUnknown, IsError: true}}, nil
			}}
			if test.live {
				tool = hostTool{Tool: tool, runtime: r}
			}
			config.Tools = []core.Tool{tool}
			session, err := core.OpenSession(f.ctx, config, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			r.core.Store(session)
			configuration := session.Configuration()
			configuration.Tools = []core.ToolDefinition{tool.Definition()}
			if err = session.Configure(f.ctx, configuration); err != nil {
				t.Fatal(err)
			}
			prompt, err := session.Prompt(f.ctx, core.Input{ID: "send", Message: core.Message{Role: "user", Content: core.Text("hello")}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = prompt.Run.Wait(f.ctx); test.live {
				if err != nil {
					t.Fatal(err)
				}
				f.idle(t, f.session.ID)
				if calls != 1 || f.count() != 2 {
					t.Fatalf("tool completion: calls=%d, model requests=%d", calls, f.count())
				}
				return
			} else if !errors.Is(err, core.ErrOutcomeUnknown) {
				t.Fatalf("interrupted call: %v", err)
			}
			f.restart(t)
			view, err := f.service.View(f.ctx, f.session.ID)
			if err != nil || view.Status != "idle" || !view.CanContinue || len(view.Unresolved) != 0 || calls != 1 || f.count() != 1 {
				t.Fatalf("reopened call: %+v, %v, calls=%d", view, err, calls)
			}
			if err = f.service.Continue(f.ctx, f.session.ID); err != nil {
				t.Fatal(err)
			}
			f.idle(t, f.session.ID)
		})
	}
}
