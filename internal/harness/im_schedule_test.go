package harness

import (
	"context"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/memory"
	"github.com/oines/astral/internal/messages"
)

type imSummaryRequestModel struct {
	core.Model
	summaryMode string
}

func (m imSummaryRequestModel) PrepareRequest(_ context.Context, request core.Request) (core.Request, error) {
	if request.SystemPrompt != "main generation" {
		return request, nil
	}
	var prepared []core.Message
	for _, message := range request.Messages {
		if message.ContextKind == "summary" {
			switch m.summaryMode {
			case "removed":
				continue
			case "replaced":
				message.Content = core.Text("An unrelated replacement summary.")
			}
		}
		prepared = append(prepared, message)
	}
	request.Messages = prepared
	return request, nil
}

func TestIMInspectorRequiresSummaryInOrdinaryGenerationRequest(t *testing.T) {
	for _, mode := range []string{"retained", "removed", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			store := memory.New()
			requests := make(chan core.Request, 1)
			const summary = "The earlier IM observation was summarized."
			model := imSummaryRequestModel{summaryMode: mode, Model: core.ModelFunc{
				Specification: core.ModelSpec{ID: "summary-evidence", ContextWindow: 128000, MaxOutputTokens: 4096},
				Generate: func(_ context.Context, request core.Request, _ func(core.ModelEvent)) (core.Reply, error) {
					text := summary
					if request.SystemPrompt == "main generation" {
						requests <- request
						text = "Finished."
					}
					return core.Reply{Message: core.Message{Role: core.RoleAssistant, Content: core.Text(text), FinishReason: core.FinishStop}}, nil
				},
			}}
			session, err := core.NewSession(ctx, core.Config{
				Model: model, Store: store, SystemPrompt: "main generation",
				EstimateTokens: func(_ context.Context, _ core.Model, request core.Request) (int, error) {
					return 100 * len(request.Messages), nil
				},
				Compaction: core.CompactionPolicy{Disabled: true, SystemPrompt: "summarize", KeepRecentTokens: 1},
			}, core.SessionOptions{ID: "summary-evidence"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := session.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			observation := core.User("The IM observation must not wake twice.")
			observation.ContextKind = "im.observation"
			observation.Metadata = encode(messages.Metadata{Source: "im", ConversationID: "chat", EventID: "event"})
			for _, input := range []core.Input{
				{ID: "event", Contexts: []core.Message{observation}},
				{ID: "later-context", Contexts: []core.Message{core.User("Recent context retained after compaction.")}},
			} {
				if _, err := session.InjectContext(ctx, input); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := session.Compact(ctx, ""); err != nil {
				t.Fatal(err)
			}
			view, err := session.ReadContext(ctx)
			if err != nil || len(view.Request.Messages) == 0 || view.Request.Messages[0].ContextKind != "summary" {
				t.Fatalf("fixture did not create a canonical summary: %+v %v", view, err)
			}
			for _, message := range view.Request.Messages {
				if message.ContextKind == "im.observation" {
					t.Fatal("fixture retained the observation outside the summary")
				}
			}
			if handled, err := NewIMInspector(session, store).Handled(ctx, "event"); err != nil || handled {
				t.Fatalf("compaction alone claimed main-model coverage: handled=%v err=%v", handled, err)
			}
			if _, err := session.Steer(ctx, core.Input{ID: "ordinary-request", Message: core.User("Continue.")}); err != nil {
				t.Fatal(err)
			}
			run, err := session.Continue(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := run.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			request := <-requests
			retained := false
			for _, message := range request.Messages {
				retained = retained || message.ContextKind == "summary" && digest(message.Content) == digest(core.Text(summary))
			}
			if retained != (mode == "retained") {
				t.Fatalf("fixture prepared the wrong actual generation request: %+v", request)
			}
			if handled, err := NewIMInspector(session, store).Handled(ctx, "event"); err != nil || handled != retained {
				t.Fatalf("coverage did not match the actual generation summary: handled=%v want=%v err=%v", handled, retained, err)
			}
		})
	}
}
