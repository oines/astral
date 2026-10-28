package channels

import (
	"context"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestProbeRequestUsesLatestBotPersonaWithoutMainAgentInstructionsOrTools(t *testing.T) {
	wireText := func(message map[string]any) string {
		if text, ok := message["content"].(string); ok {
			return text
		}
		var text strings.Builder
		for _, raw := range message["content"].([]any) {
			part := raw.(map[string]any)
			if part["type"] == "text" {
				text.WriteString(part["text"].(string))
			}
		}
		return text.String()
	}
	requests := make(chan map[string]any, 4)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		requests <- request
		return testkit.Tool("decide", map[string]bool{"participate": false})
	})
	s := New(p.Store, p.Harness)
	bot := newBot(t)
	connection := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, connection, "-100", "")
	// Reuse a frozen main-Agent view across persona edits. Probe must read the
	// Bot rather than copy that view's system instructions or tool definitions.
	view := core.Context{Request: core.Request{
		SystemPrompt: "stale main-Agent persona and operating instructions",
		Tools:        []core.ToolDefinition{{Name: "exec_command"}},
		Messages: []core.Message{
			{Role: core.RoleUser, ContextKind: "im.observation", Content: core.Text("A new relevant group observation.")},
			core.User("private main-Agent input"),
		},
	}}
	for _, persona := range []string{"  你是热爱航海的作家。\n说话直接。  ", "你是研究山地植物的画家。\n说话简洁。", " \n\t"} {
		var agent domain.Agent
		if err := p.Store.Get(p.Context, "agents", p.Agent.ID, &agent); err != nil {
			t.Fatal(err)
		}
		agent.Persona = persona
		if err := p.Harness.SaveAgent(p.Context, &agent); err != nil {
			t.Fatal(err)
		}
		if participate, err := s.shouldParticipate(p.Context, binding, view); err != nil || participate {
			t.Fatalf("Probe did not make its bounded decision: %v %v", participate, err)
		}
		request := <-requests
		messages := request["messages"].([]any)
		if len(messages) != 3 {
			t.Fatalf("Probe received extra main-Agent context: %s", encoded(messages))
		}
		system := messages[0].(map[string]any)
		want := probePrompt
		if strings.TrimSpace(persona) != "" {
			want += "\n\nBot persona:\n" + persona
		}
		if system["role"] != "system" || wireText(system) != want {
			t.Fatalf("Probe lost its stable instructions prefix or latest persona: %s", encoded(system))
		}
		observation := messages[1].(map[string]any)
		if observation["role"] != "user" || wireText(observation) != "A new relevant group observation." {
			t.Fatalf("Probe observation did not follow its stable system prompt: %s", encoded(observation))
		}
		last := messages[2].(map[string]any)
		if text := wireText(last); last["role"] != "user" || !strings.HasPrefix(text, "Current time: ") || !strings.Contains(text, "\nAgent: "+agent.Name+"\nCall decide with only participate.") {
			t.Fatalf("Probe dynamic time and name were not placed last: %s", encoded(last))
		}
		tools := request["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("Probe gained executable tools: %s", encoded(tools))
		}
		function := tools[0].(map[string]any)["function"].(map[string]any)
		parameters := function["parameters"].(map[string]any)
		properties := parameters["properties"].(map[string]any)
		if function["name"] != "decide" || len(properties) != 1 || properties["participate"] == nil || parameters["additionalProperties"] != false {
			t.Fatalf("Probe decision schema changed: %s", encoded(function))
		}
		choice := request["tool_choice"].(map[string]any)
		if choice["type"] != "function" || choice["function"].(map[string]any)["name"] != "decide" {
			t.Fatalf("Probe did not force its sole decide tool: %s", encoded(choice))
		}
		if strings.Contains(string(encoded(request)), "stale main-Agent") || strings.Contains(string(encoded(request)), "private main-Agent input") || strings.Contains(string(encoded(tools)), "exec_command") {
			t.Fatal("Probe inherited main-Agent instructions, inputs or tools")
		}
	}
	if p.Requests.Load() != 3 {
		t.Fatalf("Persona edits triggered extra requests: %d", p.Requests.Load())
	}
}
