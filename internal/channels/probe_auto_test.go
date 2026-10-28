package channels

import (
	"context"
	"net/http"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/testkit"
)

func TestProbeUsesAutoOnThinkingGatewayRegardlessOfInheritedToolChoice(t *testing.T) {
	for _, inherited := range []string{"", "none", "required"} {
		name := inherited
		if name == "" {
			name = "model-default-required"
		}
		t.Run(name, func(t *testing.T) {
			p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
				// This local Responses gateway reproduces the observed rejection.
				// The old named choice receives 400; auto can return a decision.
				if request["tool_choice"] != "auto" {
					return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						http.Error(w, "Thinking mode does not support this tool_choice", http.StatusBadRequest)
					})
				}
				if request["reasoning"].(map[string]any)["effort"] != "max" {
					t.Error("Probe changed the selected thinking effort")
				}
				tools := request["tools"].([]any)
				if len(tools) != 1 || tools[0].(map[string]any)["name"] != "decide" {
					t.Error("Probe did not retain decide as its only available tool")
				}
				return map[string]any{"id": "local-probe-response", "status": "completed", "output": []any{map[string]any{
					"type": "function_call", "id": "local-item", "call_id": "local-decision", "name": "decide", "arguments": `{"participate":true}`,
				}}}
			})
			if err := p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
				t.Fatal(err)
			}
			// Changing the transport invalidates old recommendations. Keep the
			// fixture's known limits explicit, then select a thinking Responses route.
			p.Profile.Protocol = "openai-responses"
			p.Profile.Overrides = p.Profile.Recommendations
			p.Profile.Overrides["reasoning"] = testkit.JSON(map[string]any{"supported": true, "toggle": true, "efforts": []string{"max"}})
			p.Profile.Overrides["defaults"] = testkit.JSON(core.GenerationOptions{ToolChoice: core.ToolChoice{Mode: "required"}})
			if err := p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
				t.Fatal(err)
			}
			s := New(p.Store, p.Harness)
			bot := newBot(t)
			connection := bot.connection(t, s, p.Agent.ID)
			binding := createBinding(t, p, s, connection, "-100", "")
			enabled := true
			if err := p.Harness.WithSession(p.Context, binding.SessionID, func(session *core.Session) error {
				selection := session.Configuration().Selection
				selection.Reasoning = &core.ReasoningOptions{Enabled: &enabled, Effort: "max"}
				selection.Options.ToolChoice = core.ToolChoice{Mode: inherited}
				return session.SelectModel(p.Context, selection)
			}); err != nil {
				t.Fatal(err)
			}
			if participate, err := s.shouldParticipate(p.Context, binding, core.Context{}); err != nil || !participate {
				t.Fatalf("thinking gateway did not accept the Probe decision: %v %v", participate, err)
			}
			if p.Requests.Load() != 1 || bot.sends.Load() != 0 {
				t.Fatal("Probe retried the request or sent an IM message")
			}
			selection, err := p.Harness.ModelSelection(p.Context, binding.SessionID)
			if err != nil || selection.Options.ToolChoice.Mode != inherited {
				t.Fatalf("Probe changed the main Agent's tool choice: %+v %v", selection, err)
			}
		})
	}
}
