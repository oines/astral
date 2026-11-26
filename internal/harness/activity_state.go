package harness

import (
	"bytes"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
	core "github.com/oines/astral-core"
)

const (
	activityStartAfter     = 2 * time.Second
	activityStartChars     = 300
	activityRefreshChars   = 200
	activityReasoningLimit = 400
	activityGoalLimit      = 150
	activityToolLimit      = 5
	activityToolTargetSize = 60
	activityNativeLimit    = 80
	activitySummaryLimit   = 14
	activityPhraseLimit    = 72
)

type activityChange struct {
	publish  bool
	cancel   bool
	progress bool
}

type activityTool struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Target string `json:"target,omitempty"`
}

type activityCandidate struct {
	runID          string
	segment        uint64
	coreSequence   uint64
	revision       uint64
	generation     uint64
	toolRevision   uint64
	reasoningChars uint64
	userGoal       string
	previous       string
	reasoning      string
	tools          []activityTool
}

type activityToolStep struct {
	id string
	activityTool
}

type activityToolName struct {
	id     string
	name   string
	target string
}

// activityState belongs to one observed Session. Its owner serializes events,
// request admission, and results. No stored value aliases an event payload.
type activityState struct {
	status        ActivityStatus
	startedAt     time.Time
	eventSequence uint64
	revision      uint64
	generation    uint64
	active        bool
	native        bool
	streamedText  bool
	lastSummary   string
	tools         [activityToolLimit]activityToolStep
	toolSize      int
	toolCount     int
	toolRevision  uint64
	declared      [16]activityToolName
	declaredSize  int

	reasoning      [activityReasoningLimit]rune
	reasoningNext  int
	reasoningSize  int
	reasoningChars uint64
	line           [512]byte
	lineSize       int
	lineTooLong    bool

	requested               bool
	requestedToolRevision   uint64
	requestedReasoningChars uint64
}

func (s *activityState) apply(event core.Event, now time.Time) activityChange {
	if event.RunID == "" {
		return activityChange{}
	}
	if event.Type == core.EventRunStart && event.RunID != s.status.RunID {
		s.resetSegment()
		s.lastSummary = ""
		s.streamedText = false
		s.status.RunID, s.status.Segment = event.RunID, 0
		s.status.State = ""
		s.eventSequence = event.Sequence
		return activityChange{publish: s.openSegment(now), cancel: true}
	}
	if s.status.State != "working" || event.RunID != s.status.RunID {
		return activityChange{}
	}
	s.eventSequence = max(s.eventSequence, event.Sequence)
	switch event.Type {
	case core.EventRunEnd:
		return s.finish(event.Sequence)
	case core.EventAttemptFailed:
		if event.Attempt == nil || event.Attempt.Purpose != "generation" {
			break
		}
		s.streamedText = false
		if s.active {
			s.generation++
			s.clearReasoning()
			s.native = false
			return activityChange{publish: s.publish("working", "thinking", "rule", event.Sequence), cancel: true}
		}
	case core.EventMessageStart:
		s.streamedText = false
		if s.active && s.status.Source == "rule" {
			return activityChange{publish: s.publish("working", "thinking", "rule", event.Sequence)}
		}
	case core.EventMessageUpdate:
		if event.Model == nil || event.Model.Text == "" {
			break
		}
		switch event.Model.Type {
		case "text_delta":
			s.streamedText = true
			return s.closeSegment(event.Sequence)
		case "reasoning_delta":
			return s.applyReasoning(event.Model.Text, now)
		}
	case core.EventMessageEnd:
		if event.Error != "" {
			return s.closeSegment(event.Sequence)
		}
		return s.applyMessageEnd(event)
	case core.EventToolStart, core.EventToolUpdate, core.EventToolEnd:
		return s.applyTool(event, now)
	}
	return activityChange{}
}

func (s *activityState) finish(sequence uint64) activityChange {
	if s.status.State != "working" {
		return activityChange{}
	}
	s.eventSequence = max(s.eventSequence, sequence)
	s.resetSegment()
	s.lastSummary = ""
	return activityChange{publish: s.publish("idle", "", "", s.eventSequence), cancel: true}
}

func (s *activityState) openSegment(now time.Time) bool {
	s.status.Segment++
	s.active = true
	s.startedAt = now
	return s.publish("working", "thinking", "rule", s.eventSequence)
}

func (s *activityState) closeSegment(sequence uint64) activityChange {
	if !s.active {
		return activityChange{}
	}
	s.resetSegment()
	return activityChange{publish: s.publish("working", "", "", sequence), cancel: true}
}

func (s *activityState) resetSegment() {
	s.active, s.native, s.requested = false, false, false
	s.generation++
	s.startedAt = time.Time{}
	s.clearReasoning()
	s.toolCount, s.toolRevision, s.requestedToolRevision = 0, 0, 0
	clear(s.tools[:s.toolSize])
	clear(s.declared[:s.declaredSize])
	s.toolSize, s.declaredSize = 0, 0
}

func (s *activityState) clearReasoning() {
	s.reasoningNext, s.reasoningSize, s.reasoningChars, s.requestedReasoningChars = 0, 0, 0, 0
	s.lineSize, s.lineTooLong = 0, false
}

func (s *activityState) publish(state, text, source string, sequence uint64) bool {
	if s.status.State == state && s.status.Text == text && s.status.Source == source {
		return false
	}
	s.status.State, s.status.Text, s.status.Source = state, text, source
	s.status.CoreSequence = sequence
	s.status.Seq++
	return true
}

func (s *activityState) applyReasoning(text string, now time.Time) activityChange {
	var change activityChange
	if !s.active {
		if strings.TrimSpace(text) == "" {
			return change
		}
		change.publish = s.openSegment(now)
	}
	before := s.reasoningChars
	var heading string
	for _, r := range text {
		if !s.native {
			s.reasoning[s.reasoningNext] = r
			s.reasoningNext = (s.reasoningNext + 1) % len(s.reasoning)
			s.reasoningSize = min(s.reasoningSize+1, len(s.reasoning))
			if !unicode.IsSpace(r) {
				s.reasoningChars++
			}
		}
		if r == '\n' {
			if !s.lineTooLong {
				if title := activityHeading(s.line[:s.lineSize]); title != "" {
					heading = title
				}
			}
			s.lineSize, s.lineTooLong = 0, false
			continue
		}
		if !s.lineTooLong && s.lineSize+utf8.RuneLen(r) <= len(s.line) {
			s.lineSize += utf8.EncodeRune(s.line[s.lineSize:], r)
		} else {
			s.lineTooLong = true
		}
	}
	if heading != "" {
		return s.publishHeading(heading, change)
	}
	if s.native {
		return change
	}
	if s.requested {
		change.progress = before-s.requestedReasoningChars < activityRefreshChars && s.reasoningChars-s.requestedReasoningChars >= activityRefreshChars
	} else {
		change.progress = before == 0 && s.reasoningChars > 0 || before < activityStartChars && s.reasoningChars >= activityStartChars
	}
	if change.progress {
		s.revision++
	}
	return change
}

func activityHeading(line []byte) string {
	line = bytes.TrimSpace(line)
	if len(line) < 5 || !bytes.HasPrefix(line, []byte("**")) || !bytes.HasSuffix(line, []byte("**")) {
		return ""
	}
	inside := bytes.TrimSpace(line[2 : len(line)-2])
	if bytes.ContainsRune(inside, '*') {
		return ""
	}
	return cleanActivityText(string(inside), activityNativeLimit)
}

func (s *activityState) publishHeading(heading string, change activityChange) activityChange {
	change.cancel = change.cancel || !s.native
	s.native, s.lastSummary = true, heading
	change.publish = s.publish("working", heading, "native_reasoning", s.eventSequence) || change.publish
	return change
}

func (s *activityState) applyMessageEnd(event core.Event) activityChange {
	if event.Message == nil {
		return activityChange{}
	}
	// An adapter may return the final text without streaming it. In both cases
	// the first body text ends the segment before its tools start.
	var change activityChange
	if !s.streamedText {
		for _, part := range event.Message.Content {
			if part.Type == "text" && part.Text != "" {
				change = s.closeSegment(event.Sequence)
				break
			}
		}
	}
	s.streamedText = false
	if s.active && !s.lineTooLong {
		if heading := activityHeading(s.line[:s.lineSize]); heading != "" {
			change = s.publishHeading(heading, change)
		}
	}
	clear(s.declared[:s.declaredSize])
	s.declaredSize = 0
	s.lineSize, s.lineTooLong = 0, false
	for _, part := range event.Message.Content {
		if part.Type == "tool_call" && part.ToolCall != nil && validActivityToolID(part.ToolCall.ID) {
			call := part.ToolCall
			s.declared[s.declaredSize] = activityToolName{
				id:     strings.Clone(call.ID),
				name:   activityHead(call.Name, 64),
				target: activityToolTarget(call.Name, call.Arguments),
			}
			s.declaredSize++
			if s.declaredSize == len(s.declared) {
				break
			}
		}
	}
	return change
}

func activityToolTarget(name string, arguments []byte) string {
	var key string
	switch name {
	case "read", "write", "edit", "read_file", "write_file", "edit_file":
		key = "path"
	case "web_search":
		key = "query"
	case "exec_command":
		key = "cmd"
	default:
		return ""
	}
	// The hint is optional. Limit lookup work even when a write or edit contains
	// a large body, and only retain the small, owned projection.
	value := ojson.GetBytesNoCopy(arguments[:min(len(arguments), 64<<10)], key)
	if value.Type != ojson.String || len(value.Raw) < 2 || value.Raw[len(value.Raw)-1] != '"' {
		return ""
	}
	target := value.Str
	switch key {
	case "path":
		target = activityBasename(target)
	case "cmd":
		target = activityCommandHead(target)
	}
	target = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, activityText(target, activityToolTargetSize))
	return strings.Clone(strings.TrimSpace(target))
}

func activityBasename(path string) string {
	path = strings.TrimRight(path, "/\\")
	return path[strings.LastIndexAny(path, "/\\")+1:]
}

func activityCommandHead(command string) string {
	if end := strings.IndexAny(command, "\r\n"); end >= 0 {
		command = command[:end]
	}
	var head strings.Builder
	remaining := 40
	for field := range strings.FieldsSeq(command) {
		if head.Len() > 0 {
			head.WriteByte(' ')
			remaining--
		}
		prefix := activityText(field, remaining)
		head.WriteString(prefix)
		remaining -= utf8.RuneCountInString(prefix)
		if remaining < 2 {
			break
		}
	}
	return head.String()
}

func validActivityToolID(id string) bool { return id != "" && len(id) <= 256 }

func (s *activityState) applyTool(event core.Event, now time.Time) activityChange {
	if !validActivityToolID(event.ToolCallID) {
		return activityChange{}
	}
	var change activityChange
	if !s.active {
		change.publish = s.openSegment(now)
	}
	state := "running"
	if event.Type == core.EventToolEnd {
		state = "finished"
	}
	index := 0
	for index < s.toolSize && s.tools[index].id != event.ToolCallID {
		index++
	}
	if event.Type == core.EventToolUpdate && index == s.toolSize && s.toolSize == len(s.tools) {
		return change
	}
	var tool activityToolStep
	if index < s.toolSize {
		tool = s.tools[index]
		if tool.State == state || tool.State == "finished" {
			return change
		}
		copy(s.tools[index:], s.tools[index+1:s.toolSize])
	} else {
		tool = activityToolStep{id: strings.Clone(event.ToolCallID), activityTool: activityTool{Name: "tool"}}
		for _, name := range s.declared[:s.declaredSize] {
			if name.id == event.ToolCallID && name.name != "" {
				tool.Name = name.name
				tool.Target = name.target
				break
			}
		}
		if s.toolSize == len(s.tools) {
			copy(s.tools[:], s.tools[1:])
		} else {
			s.toolSize++
		}
		s.toolCount = min(s.toolCount+1, 2)
	}
	tool.State = state
	s.tools[s.toolSize-1] = tool
	s.toolRevision++
	s.revision++
	change.progress = true
	if s.status.Source == "rule" {
		change.publish = s.publish("working", activityToolRule(tool.Name), "rule", event.Sequence) || change.publish
	}
	return change
}

func activityToolRule(name string) string {
	switch name {
	case "web_search", "search", "search_files", "grep":
		return "searching"
	case "read_file", "read":
		return "reading"
	case "exec_command", "write_stdin":
		return "running"
	default:
		return "working"
	}
}

func (s *activityState) ready(now time.Time) bool {
	if !s.active || s.native || s.toolCount == 0 && s.reasoningChars == 0 {
		return false
	}
	if s.requested {
		return s.toolRevision != s.requestedToolRevision || s.reasoningChars-s.requestedReasoningChars >= activityRefreshChars
	}
	return now.Sub(s.startedAt) >= activityStartAfter || s.toolCount >= 2 || s.reasoningChars >= activityStartChars
}

func (s *activityState) candidate(now time.Time, userGoal string) (activityCandidate, bool) {
	if !s.ready(now) {
		return activityCandidate{}, false
	}
	var reasoning string
	if s.reasoningSize < len(s.reasoning) {
		reasoning = string(s.reasoning[:s.reasoningSize])
	} else {
		var ordered [activityReasoningLimit]rune
		count := copy(ordered[:], s.reasoning[s.reasoningNext:])
		copy(ordered[count:], s.reasoning[:s.reasoningNext])
		reasoning = string(ordered[:])
	}
	reasoning = strings.TrimSpace(reasoning)
	if len(reasoning) == 0 && s.toolCount == 0 {
		return activityCandidate{}, false
	}
	tools := make([]activityTool, s.toolSize)
	for i, tool := range s.tools[:s.toolSize] {
		tools[i] = tool.activityTool
	}
	return activityCandidate{
		runID: s.status.RunID, segment: s.status.Segment, coreSequence: s.eventSequence, revision: s.revision, generation: s.generation,
		toolRevision: s.toolRevision, reasoningChars: s.reasoningChars, userGoal: activityHead(userGoal, activityGoalLimit),
		previous: s.lastSummary, reasoning: reasoning, tools: tools,
	}, true
}

func (s *activityState) markRequested(candidate activityCandidate) {
	if s.isCandidateCurrent(candidate) {
		s.requested = true
		s.requestedToolRevision = candidate.toolRevision
		s.requestedReasoningChars = candidate.reasoningChars
	}
}

func (s *activityState) isCandidateCurrent(candidate activityCandidate) bool {
	return s.active && !s.native && candidate.runID == s.status.RunID && candidate.segment == s.status.Segment && candidate.generation == s.generation
}

func (s *activityState) setModelSummary(candidate activityCandidate, text string) bool {
	if !s.isCandidateCurrent(candidate) {
		return false
	}
	text = cleanActivityText(text, activityPhraseLimit)
	if strings.ContainsFunc(text, func(r rune) bool {
		return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
	}) {
		text = activityHead(text, activitySummaryLimit)
	}
	if text == "" || text == s.status.Text {
		return false
	}
	s.lastSummary = text
	return s.publish("working", text, "summarizer", max(candidate.coreSequence, s.status.CoreSequence))
}

func cleanActivityText(text string, limit int) string {
	if end := strings.IndexAny(text, "\r\n"); end >= 0 {
		text = text[:end]
	}
	text = strings.Trim(text, " \t\"'`‘’“”*_#~.。")
	text = strings.TrimSpace(strings.TrimLeft(text, "-+>"))
	return strings.TrimRight(activityHead(text, limit), " \t.。")
}

func activityHead(text string, limit int) string {
	for i := range text {
		if limit == 0 {
			return strings.Clone(text[:i])
		}
		limit--
	}
	return strings.Clone(text)
}
