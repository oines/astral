package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
)

func TestTimelineProjectionKeepsPresentationAndLoadsToolDetailsSeparately(t *testing.T) {
	original := core.Entry{ID: "tool", Kind: core.EntryToolResult, Tool: &core.ToolRecord{Invocation: core.Invocation{Call: core.ToolCall{Name: "exec_command", Arguments: json.RawMessage(`{"cmd":"echo ok","large":"` + strings.Repeat("x", 10000) + `"}`)}}, Result: &core.ToolResult{Outcome: core.OutcomeSucceeded, Content: core.Text(strings.Repeat("output", 10000)), Details: json.RawMessage(`{"output":"large"}`)}}}
	projected := timelineEntry(original)
	raw, _ := json.Marshal(projected)
	if len(raw) > 1000 || !strings.Contains(string(raw), "echo ok") || projected.Tool.Result.Outcome != core.OutcomeSucceeded {
		t.Fatalf("bad presentation projection: %s", raw)
	}
	if len(original.Tool.Result.Content) == 0 || len(original.Tool.Result.Details) == 0 || !strings.Contains(string(original.Tool.Invocation.Call.Arguments), "large") {
		t.Fatal("presentation changed persisted entry")
	}
}

func TestTimelineProjectionPreservesImageGeometryAndDisplayTextWithoutModelEnvelope(t *testing.T) {
	original := core.Entry{Kind: core.EntryMessage, Message: &core.Message{Role: core.RoleUser, Metadata: json.RawMessage(`{"source":"schedule","display_text":"visible"}`), Content: []core.Content{{Type: "text", Text: "model envelope"}, {Type: "image", Data: json.RawMessage(`"base64"`), Metadata: json.RawMessage(`{"width":1200,"height":800}`)}}}}
	projected := timelineEntry(original)
	if projected.Message.Content[0].Text != "" || len(projected.Message.Content[1].Data) != 0 || string(projected.Message.Content[1].Metadata) != `{"width":1200,"height":800}` {
		t.Fatalf("bad projection: %+v", projected.Message)
	}
	if original.Message.Content[0].Text != "model envelope" || len(original.Message.Content[1].Data) == 0 {
		t.Fatal("original content changed")
	}
	original.Kind = core.EntryContext
	if timelineEntry(original).Message != nil {
		t.Fatal("passive context leaked into timeline transfer")
	}
}
