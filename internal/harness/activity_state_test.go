package harness

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
)

func TestActivityStateSegmentsAndTerminal(t *testing.T) {
	var state activityState
	now := time.Unix(100, 0)
	start := state.apply(core.Event{Type: core.EventRunStart, RunID: "run", Sequence: 1}, now)
	if !start.publish || state.status.Segment != 1 || state.status.State != "working" || state.status.Text != "thinking" {
		t.Fatalf("start = %+v, status = %+v", start, state.status)
	}
	state.apply(activityTextEvent("reasoning_delta", strings.Repeat("文", 300), 2), now)
	first, ok := state.candidate(now, "当前任务")
	if !ok {
		t.Fatal("long unstructured reasoning did not qualify")
	}
	state.markRequested(first)
	firstText := state.apply(activityTextEvent("text_delta", "Assistant output", 3), now)
	if !firstText.cancel || !firstText.publish || state.status.Text != "" || state.setModelSummary(first, "迟到摘要") {
		t.Fatalf("first body boundary = %+v, status = %+v", firstText, state.status)
	}
	if duplicate := state.apply(activityTextEvent("text_delta", "continued body", 4), now); duplicate != (activityChange{}) {
		t.Fatalf("second body delta = %+v", duplicate)
	}
	message := core.Message{Role: core.RoleAssistant, Content: []core.Content{
		{Type: "text", Text: "Assistant output"}, {Type: "tool_call", ToolCall: &core.ToolCall{ID: "first", Name: "read_file"}},
		{Type: "tool_call", ToolCall: &core.ToolCall{ID: "second", Name: "exec_command"}},
	}}
	state.apply(core.Event{Type: core.EventMessageEnd, RunID: "run", Sequence: 5, Message: &message}, now)
	firstTool := state.apply(core.Event{Type: core.EventToolStart, RunID: "run", Sequence: 6, ToolCallID: "first"}, now)
	if !firstTool.publish || state.status.Segment != 2 || state.status.Text != "reading" {
		t.Fatalf("first tool = %+v, status = %+v", firstTool, state.status)
	}
	if _, ok = state.candidate(now, ""); ok {
		t.Fatal("a single quick tool qualified")
	}
	state.apply(core.Event{Type: core.EventToolStart, RunID: "run", Sequence: 7, ToolCallID: "second"}, now)
	second, ok := state.candidate(now, "现在准备运行命令")
	if !ok || len(second.tools) != 2 || second.tools[0].Name != "read_file" || second.tools[1].Name != "exec_command" || second.segment != 2 || second.coreSequence != 7 {
		t.Fatalf("tool candidate = %+v, eligible = %t", second, ok)
	}
	if !state.setModelSummary(second, "`正在核对命令输出。`\nThis line is discarded") || state.status.Text != "正在核对命令输出" || state.setModelSummary(second, "正在核对命令输出") {
		t.Fatalf("model summary = %+v", state.status)
	}
	state.apply(activityTextEvent("text_delta", "Second body", 8), now)
	terminal := state.apply(core.Event{Type: core.EventRunEnd, RunID: "run", Sequence: 9}, now)
	if !terminal.publish || !terminal.cancel || state.status.State != "idle" || state.status.CoreSequence != 9 || state.status.Segment != 2 || state.setModelSummary(second, "迟到摘要") {
		t.Fatalf("terminal = %+v, status = %+v", terminal, state.status)
	}
}

func TestActivityStateNativeHeadingAndOpaqueReasoning(t *testing.T) {
	var state activityState
	now := time.Unix(100, 0)
	state.apply(core.Event{Type: core.EventRunStart, RunID: "run", Sequence: 1}, now)
	state.apply(core.Event{Type: core.EventMessageUpdate, RunID: "run", Sequence: 2, Model: &core.ModelEvent{
		Type: "reasoning_delta", Data: json.RawMessage(`{"encrypted_content":"secret"}`), Metadata: json.RawMessage(`{"signature":"private"}`),
	}}, now)
	if _, ok := state.candidate(now.Add(10*time.Second), "只看目标"); ok {
		t.Fatal("opaque reasoning produced a candidate")
	}
	state.apply(activityTextEvent("reasoning_delta", "**原生", 3), now)
	before, ok := state.candidate(now.Add(2*time.Second), "目标")
	if !ok || before.reasoning != "**原生" {
		t.Fatalf("candidate = %+v, eligible = %t", before, ok)
	}
	state.markRequested(before)
	state.apply(activityTextEvent("reasoning_delta", "标题**", 4), now)
	if state.status.Source != "rule" {
		t.Fatalf("partial line became a heading: %+v", state.status)
	}
	state.apply(activityTextEvent("reasoning_delta", "\r\n行内 **加粗**\n", 5), now)
	if state.status.Source != "native_reasoning" || state.status.Text != "原生标题" || state.setModelSummary(before, "旧的旁路请求") {
		t.Fatalf("first title = %+v", state.status)
	}
	state.apply(activityTextEvent("reasoning_delta", strings.Repeat("x", 600)+"**伪标题**\n**第二标题**\n", 6), now)
	if state.status.Text != "第二标题" {
		t.Fatalf("latest title = %+v", state.status)
	}
	state.apply(activityTextEvent("reasoning_delta", "**以消息结束的标题**", 7), now)
	state.apply(core.Event{Type: core.EventMessageEnd, RunID: "run", Sequence: 8, Message: &core.Message{Role: core.RoleAssistant}}, now)
	if state.status.Text != "以消息结束的标题" {
		t.Fatalf("last line = %+v", state.status)
	}
	if _, ok := state.candidate(now.Add(20*time.Second), ""); ok {
		t.Fatal("native title did not suppress the model")
	}
}

func TestActivityStateRefreshUsesNewBoundedEvidence(t *testing.T) {
	var state activityState
	now := time.Unix(100, 0)
	state.apply(core.Event{Type: core.EventRunStart, RunID: "run", Sequence: 1}, now)
	state.apply(activityTextEvent("reasoning_delta", strings.Repeat("旧", 1000), 2), now)
	first, ok := state.candidate(now, strings.Repeat("目", 1000))
	if !ok || first.reasoning != strings.Repeat("旧", 400) || first.userGoal != strings.Repeat("目", 150) {
		t.Fatalf("first candidate has wrong limits, eligible = %t", ok)
	}
	state.markRequested(first)
	state.apply(activityTextEvent("reasoning_delta", strings.Repeat("新", 199), 3), now)
	if state.ready(now.Add(time.Minute)) {
		t.Fatal("fewer than 200 new characters refreshed")
	}
	change := state.apply(activityTextEvent("reasoning_delta", "新", 4), now)
	second, ok := state.candidate(now, "")
	if !change.progress || !ok || second.reasoning != strings.Repeat("旧", 200)+strings.Repeat("新", 200) {
		t.Fatalf("new reasoning did not qualify or was out of order: %+v, eligible = %t", change, ok)
	}
	state.markRequested(second)
	message := core.Message{Role: core.RoleAssistant, Content: []core.Content{
		{Type: "tool_call", ToolCall: &core.ToolCall{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"secret":"token"}`)}},
	}}
	state.apply(core.Event{Type: core.EventMessageEnd, RunID: "run", Sequence: 5, Message: &message}, now)
	state.apply(core.Event{Type: core.EventToolStart, RunID: "run", Sequence: 6, ToolCallID: "read"}, now)
	third, ok := state.candidate(now, "")
	if !ok || len(third.tools) != 1 || third.tools[0] != (activityTool{Name: "read_file", State: "running"}) {
		t.Fatalf("tool skeleton = %+v, eligible = %t", third.tools, ok)
	}
	state.markRequested(third)
	change = state.apply(core.Event{Type: core.EventToolUpdate, RunID: "run", Sequence: 7, ToolCallID: "read", Progress: &core.ToolProgress{Text: "raw file content"}}, now)
	if change.progress || state.ready(now.Add(time.Minute)) {
		t.Fatal("raw tool output refreshed the summary")
	}
	state.apply(core.Event{Type: core.EventToolEnd, RunID: "run", Sequence: 8, ToolCallID: "read"}, now)
	finished, ok := state.candidate(now, "")
	if !ok || len(finished.tools) != 1 || finished.tools[0].State != "finished" {
		t.Fatalf("tool completion = %+v, eligible = %t", finished.tools, ok)
	}
}

func TestActivityStateToolTargetsUseOnlyWhitelistedFields(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"read", map[string]any{"path": "/private/subdir/report.txt"}, "report.txt"},
		{"write_file", map[string]any{"path": `D:\private\subdir\report.txt`, "content": strings.Repeat("raw-file-data ", 1000)}, "report.txt"},
		{"edit", map[string]any{"path": "subdir/report.txt", "edits": []string{"raw-file-data"}}, "report.txt"},
		{"web_search", map[string]any{"query": "password\nrule\u001bhttp://example.test", "domains": []string{"private-subdir.test"}}, "password rule http://example.test"},
		{"web_search", map[string]any{"query": strings.Repeat("文", 100) + "raw-file-data"}, strings.Repeat("文", 60)},
		{"exec_command", map[string]any{"cmd": "  FOO=1\tgo  test ./...\r\necho raw-file-data"}, "FOO=1 go test ./..."},
		{"exec_command", map[string]any{"cmd": `"C:\Program Files\git.exe" status`}, `"C:\Program Files\git.exe" status`},
		{"exec_command", map[string]any{"cmd": "go test ./... && git status"}, "go test ./... && git status"},
		{"exec_command", map[string]any{"cmd": strings.Repeat("文", 40) + "raw-file-data"}, strings.Repeat("文", 40)},
		{"exec_command", map[string]any{"cmd": " \t\necho raw-file-data"}, ""},
		{"mcp__tools__lookup", map[string]any{"path": "/private/raw-file-data", "query": "raw-file-data", "cmd": "raw-file-data"}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var state activityState
			now := time.Unix(100, 0)
			state.apply(core.Event{Type: core.EventRunStart, RunID: "run", Sequence: 1}, now)
			message := &core.Message{Role: core.RoleAssistant, Content: []core.Content{{Type: "tool_call", ToolCall: &core.ToolCall{
				ID: "step", Name: test.name, Arguments: encode(test.args),
			}}}}
			state.apply(core.Event{Type: core.EventMessageEnd, RunID: "run", Sequence: 2, Message: message}, now)
			state.apply(core.Event{Type: core.EventToolStart, RunID: "run", Sequence: 3, ToolCallID: "step"}, now)
			state.apply(core.Event{Type: core.EventToolEnd, RunID: "run", Sequence: 4, ToolCallID: "step", Progress: &core.ToolProgress{Text: "raw-file-data"}}, now)
			candidate, ok := state.candidate(now.Add(activityStartAfter), "")
			if !ok || len(candidate.tools) != 1 || candidate.tools[0] != (activityTool{Name: test.name, State: "finished", Target: test.want}) {
				t.Fatalf("tool projection = %+v, eligible = %v", candidate.tools, ok)
			}
			input := activitySummaryInput(candidate)
			if !strings.Contains(input, "Tool: "+test.name+" finished") ||
				test.want != "" && !strings.Contains(input, " — "+test.want) ||
				strings.Contains(input, "private") || strings.Contains(input, "subdir") || strings.Contains(input, "raw-file-data") {
				t.Fatalf("summary input = %q", input)
			}
		})
	}
}

func TestActivityStateAttemptFailureAndStopInvalidateResults(t *testing.T) {
	var state activityState
	now := time.Unix(100, 0)
	state.apply(core.Event{Type: core.EventRunStart, RunID: "run", Sequence: 1}, now)
	state.apply(activityTextEvent("reasoning_delta", strings.Repeat("思", 300), 2), now)
	beforeFailure, ok := state.candidate(now, "")
	if !ok {
		t.Fatal("expected candidate before simulated model failure")
	}
	change := state.apply(core.Event{Type: core.EventAttemptFailed, RunID: "run", Sequence: 3, Attempt: &core.AttemptStatus{Purpose: "generation"}}, now)
	if !change.cancel || state.setModelSummary(beforeFailure, "失败的模型尝试") || state.ready(now.Add(time.Minute)) {
		t.Fatalf("failure boundary = %+v, status = %+v", change, state.status)
	}
	state.apply(activityTextEvent("reasoning_delta", strings.Repeat("再", 300), 4), now)
	beforeStop, ok := state.candidate(now, "")
	if !ok || beforeStop.segment != 1 {
		t.Fatalf("retry advanced segment or rejected new evidence: %+v, eligible = %t", beforeStop, ok)
	}
	stop := state.finish(4)
	state.apply(activityTextEvent("reasoning_delta", "**迟到的标题**\n", 5), now)
	if !stop.cancel || !stop.publish || state.status.State != "idle" || state.setModelSummary(beforeStop, "停止之后的结果") {
		t.Fatalf("stop boundary = %+v, status = %+v", stop, state.status)
	}
}

func TestActivityStateMessageEndKeepsReasoningAfterStreamedBody(t *testing.T) {
	var state activityState
	now := time.Unix(100, 0)
	state.apply(core.Event{Type: core.EventRunStart, RunID: "run", Sequence: 1}, now)
	state.apply(activityTextEvent("text_delta", "Initial response", 2), now)
	state.apply(activityTextEvent("reasoning_delta", strings.Repeat("Later reasoning ", 40), 3), now)
	message := core.Message{Role: core.RoleAssistant, Content: []core.Content{{Type: "text", Text: "Initial response"}}}
	if change := state.apply(core.Event{Type: core.EventMessageEnd, RunID: "run", Sequence: 4, Message: &message}, now); change.cancel {
		t.Fatal("the committed body closed a segment that started after its stream")
	}
	candidate, ok := state.candidate(now, "")
	if !ok || candidate.segment != 2 || !state.setModelSummary(candidate, "Checking local command results.") || state.status.Text != "Checking local command results" {
		t.Fatalf("candidate = %+v, eligible = %t, status = %+v", candidate, ok, state.status)
	}
}

func activityTextEvent(kind, text string, sequence uint64) core.Event {
	return core.Event{Type: core.EventMessageUpdate, RunID: "run", Sequence: sequence, Model: &core.ModelEvent{Type: kind, Text: text}}
}
