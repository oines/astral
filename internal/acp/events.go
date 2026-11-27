package acp

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/persistence"
)

const textChunkBytes = 16 << 10

type eventView struct {
	session        *loadedSession
	subagents      subagentEvents
	title          string
	modelSelection core.ModelSelection
	watermark      uint64
	historyTail    uint64
	snapshotRun    string
	snapshotRead   string
	attempts       map[string]streamedParts
	activeTools    map[string]bool
	toolTurn       string
	lengthRun      string
	source         string
	runID          string
}

type streamedParts map[int]bool

func (v *eventView) replay(events <-chan harness.SessionEvent, watch *persistence.Watcher, forkEntryID string) error {
	c := v.session.connection
	h := c.service.harness
	ctx := v.session.ctx
	coreSession, err := h.Core(ctx, v.session.id)
	if err != nil {
		return err
	}
	snapshot, err := h.Snapshot(ctx, v.session.id)
	if err != nil {
		return err
	}
	v.watermark = snapshot.EventSequence
	v.snapshotRun = snapshot.RunID
	if snapshot.Attempt != nil {
		v.snapshotRead = snapshot.Attempt.AttemptID
	}
	history := coreSession.History()
	if headID := snapshot.State.Branch.HeadID; headID != "" {
		// Detached forks copy only the selected ancestor path and preserve the
		// original writers. Outside that copied prefix, replay only the root's
		// own entries because children may write to the same Rollout.
		if forkEntryID != "" {
			v.historyTail, err = v.replayHistory(history, watch, "", forkEntryID)
			if err != nil {
				return err
			}
		}
		if headID != forkEntryID {
			v.historyTail, err = v.replayHistory(history, watch, v.session.id, headID)
			if err != nil {
				return err
			}
		}
	}
	// Process the bounded backlog before load returns. The same handler retains
	// its history cutoff for events that the shared feed has not forwarded yet.
	for range len(events) {
		event, ok := <-events
		if !ok || !v.handle(event, true) {
			return core.ErrClosed
		}
	}
	return nil
}

func (v *eventView) replayHistory(history core.HistoryReader, watch *persistence.Watcher, writer, headID string) (uint64, error) {
	c := v.session.connection
	ctx := v.session.ctx
	changed := watch.Changes()
	if _, err := c.allowedSession(ctx, v.session.id); err != nil {
		return 0, err
	}
	query := core.Query{SessionID: writer, HistoryOnly: true, Limit: 64, MaxBytes: 256 << 10}
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-changed:
			changed = watch.Changes()
			if _, err := c.allowedSession(ctx, v.session.id); err != nil {
				return 0, err
			}
		default:
		}
		page, err := history.Scan(ctx, query)
		if errors.Is(err, core.ErrBudget) {
			// A valid attached message can exceed the ordinary replay page
			// budget. Read that item by itself, then return to smaller pages.
			large := query
			large.Limit, large.MaxBytes = 1, 128<<20
			page, err = history.Scan(ctx, large)
		}
		if err != nil {
			return 0, err
		}
		for _, entry := range page.Entries {
			if entry.Sequence > v.historyTail && !v.entry(entry, true, false) {
				return 0, core.ErrClosed
			}
			if entry.ID == headID {
				return entry.Sequence, nil
			}
		}
		if page.NextCursor == "" {
			return 0, core.ErrNotFound
		}
		query.Cursor = page.NextCursor
	}
}

func (v *eventView) follow(events <-chan harness.SessionEvent, watch *persistence.Watcher) {
	ctx := v.session.ctx
	changed := watch.Changes()
	// Recheck once after replay so a privacy or archive change during the
	// hand-off cannot be hidden by acquiring a new notification channel.
	if !v.refreshSession() {
		return
	}
	subWatch := v.watchSubagents()
	var subChanged <-chan struct{}
	if subWatch != nil {
		defer subWatch.Close()
		subChanged = subWatch.Changes()
		if v.syncSubagents() != nil {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
			changed = watch.Changes()
			if !v.refreshSession() {
				return
			}
		case <-subChanged:
			subChanged = subWatch.Changes()
			if !v.refreshSession() || v.syncSubagents() != nil {
				return
			}
		case event, ok := <-events:
			select {
			case <-changed:
				changed = watch.Changes()
				if !v.refreshSession() {
					return
				}
			default:
			}
			if ctx.Err() != nil {
				return
			}
			if !ok || !v.handle(event, false) {
				return
			}
		}
	}
}

func (v *eventView) refreshSession() bool {
	record, err := v.session.connection.allowedSession(v.session.ctx, v.session.id)
	if err != nil {
		return false
	}
	if v.title == record.Title {
		return true
	}
	v.title = record.Title
	var title *string
	if record.Title != "" {
		title = &record.Title
	}
	return v.update(map[string]any{"sessionUpdate": "session_info_update", "title": title}, "external", false)
}

func (v *eventView) handle(update harness.SessionEvent, wait bool) bool {
	c := v.session.connection
	if update.Activity != nil {
		return c.notify("_astral/activityStatus", struct {
			SessionID string                  `json:"sessionId"`
			Status    *harness.ActivityStatus `json:"status"`
		}{v.session.id, update.Activity}, wait)
	}
	event := update.Core
	if event == nil {
		return true
	}
	if update.Entry != nil {
		if update.Entry.Sequence <= v.historyTail {
			return true
		}
		return v.entry(*update.Entry, wait, true)
	}
	if event.Sequence <= v.watermark && (event.RunID != v.snapshotRun || v.snapshotRun == "") {
		return true
	}
	switch event.Type {
	case core.EventRunStart:
		if event.RunID != v.runID {
			v.runID, v.source = event.RunID, "external"
		}
	case core.EventRunEnd:
		clear(v.attempts)
		clear(v.activeTools)
		terminal := *event
		if terminal.Status == "completed" && terminal.RunID == v.lengthRun {
			terminal.EndReason = "max_tokens"
		}
		v.lengthRun = ""
		v.session.finishPrompt(terminal)
	case core.EventAttemptFailed:
		delete(v.attempts, event.AttemptID)
	case core.EventMessageUpdate:
		if event.Sequence <= v.watermark && event.AttemptID != v.snapshotRead {
			return true
		}
		return v.delta(event, wait)
	case core.EventToolStart, core.EventToolUpdate:
		id := toolID(event.TurnID, event.ToolCallID)
		if !v.activeTools[id] {
			return true
		}
		update := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id, "status": "in_progress"}
		if event.Progress != nil && event.Progress.Text != "" {
			update["content"] = toolContent(event.Progress.Text)
		}
		return v.update(update, v.trigger(event.RunID), wait)
	}
	return true
}

func (v *eventView) delta(event *core.Event, wait bool) bool {
	model := event.Model
	if model == nil || model.Text == "" || model.ContentIndex == nil {
		return true
	}
	var kind string
	switch model.Type {
	case "text_delta":
		kind = "agent_message_chunk"
	case "reasoning_delta":
		kind = "agent_thought_chunk"
	default:
		return true
	}
	parts := v.attempts[event.AttemptID]
	if parts == nil {
		parts = make(streamedParts)
		v.attempts[event.AttemptID] = parts
	}
	parts[*model.ContentIndex] = true
	return v.text(kind, event.AttemptID, model.Text, v.trigger(event.RunID), wait)
}

func (v *eventView) entry(entry core.Entry, wait, live bool) bool {
	if entry.Kind == core.EntryConfiguration && entry.Configuration != nil && live && !wait {
		return v.refreshConfig(entry.Configuration.Selection)
	}
	if entry.Kind == core.EntryCompaction && entry.Compaction != nil && v.session.connection.sessionCompaction {
		return v.update(map[string]any{"sessionUpdate": "compaction_update", "compactionId": entry.ID, "status": "completed"}, v.trigger(entry.RunID), wait)
	}
	if entry.Kind == core.EntryToolResult || entry.Kind == core.EntryToolControl || entry.Kind == core.EntryToolUnresolved {
		return v.toolResult(entry, wait)
	}
	message := entry.Message
	if message == nil || !visibleMessage(entry) {
		return true
	}
	if message.Role == core.RoleUser {
		var metadata messages.Metadata
		_ = json.Unmarshal(message.Metadata, &metadata)
		var source string
		switch {
		case metadata.Schedule != nil || metadata.Source == "schedule":
			source = "schedule"
		case metadata.Source == "acp" || metadata.Source == "web":
			source = metadata.Source
		default:
			source = "external"
		}
		if entry.RunID != "" {
			v.runID, v.source = entry.RunID, source
		}
		if live && source == "acp" && strings.HasPrefix(entry.InputID, v.session.connection.id+":") {
			return true
		}
		if metadata.Source != "" || metadata.DisplayText != "" {
			if !v.text("user_message_chunk", entry.ID, metadata.DisplayText, source, wait) {
				return false
			}
		} else {
			for _, part := range message.Content {
				if part.Type == "text" && !v.text("user_message_chunk", entry.ID, part.Text, source, wait) {
					return false
				}
			}
		}
		return v.userAttachments(entry, metadata, source, wait)
	}
	if message.Role != core.RoleAssistant || message.Failed() {
		return true
	}
	v.lengthRun = ""
	if message.FinishReason == core.FinishLength {
		v.lengthRun = entry.RunID
	}
	messageID := entry.ID
	var parts streamedParts
	if entry.Generation != nil && entry.Generation.AttemptID != "" {
		messageID = entry.Generation.AttemptID
		parts = v.attempts[messageID]
		delete(v.attempts, messageID)
	}
	source := v.trigger(entry.RunID)
	for index, part := range message.Content {
		switch part.Type {
		case "text", "reasoning":
			if parts[index] {
				continue
			}
			kind := "agent_message_chunk"
			if part.Type == "reasoning" {
				kind = "agent_thought_chunk"
			}
			if !v.text(kind, messageID, part.Text, source, wait) {
				return false
			}
		case "image":
			if image, _, ok := outboundImage(part); ok && !v.messageImage("agent_message_chunk", messageID, image, source, wait) {
				return false
			}
		case "tool_call":
			if part.ToolCall == nil {
				continue
			}
			call := part.ToolCall
			id := toolID(entry.TurnID, call.ID)
			if v.toolTurn != entry.TurnID {
				clear(v.activeTools)
				v.toolTurn = entry.TurnID
			}
			v.activeTools[id] = true
			update := map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "name": call.Name, "title": call.Name, "status": "pending"}
			if len(call.Arguments) > 0 && len(call.Arguments) <= textChunkBytes {
				update["rawInput"] = call.Arguments
			}
			if !v.update(update, source, wait) {
				return false
			}
		}
	}
	if live && entry.Kind == core.EntryMessage && entry.Usage.Available && !entry.Usage.InputMissing && entry.Usage.InputTokens >= 0 && entry.Generation != nil && entry.Generation.Model.ContextWindow > 0 {
		return v.update(map[string]any{
			"sessionUpdate": "usage_update",
			"used":          entry.Usage.InputTokens,
			"size":          entry.Generation.Model.ContextWindow,
		}, source, wait)
	}
	return true
}

func (v *eventView) userAttachments(entry core.Entry, metadata messages.Metadata, source string, wait bool) bool {
	var sentImages map[string]bool
	for _, part := range entry.Message.Content {
		if image, _, ok := outboundImage(part); ok {
			if !v.messageImage("user_message_chunk", entry.ID, image, source, wait) {
				return false
			}
			if part.URL != "" && len(metadata.Files) != 0 {
				if sentImages == nil {
					sentImages = make(map[string]bool)
				}
				sentImages[part.URL] = true
			}
		}
	}
	for _, file := range metadata.Files {
		uri := "astral-file://" + file.ComputerID + "/" + file.ID
		if !sentImages[uri] && !v.update(map[string]any{"sessionUpdate": "user_message_chunk", "messageId": entry.ID, "content": map[string]string{
			"type": "resource_link", "name": file.DisplayName, "uri": uri, "mimeType": file.MediaType,
		}}, source, wait) {
			return false
		}
	}
	return true
}

func (v *eventView) messageImage(kind, id string, image imageContent, source string, wait bool) bool {
	return v.update(map[string]any{"sessionUpdate": kind, "messageId": id, "content": image}, source, wait)
}

func (v *eventView) refreshConfig(changed core.ModelSelection) bool {
	if v.sameModelConfig(changed) {
		return true
	}
	// Several changes can be queued at once. Publish the latest selection
	// instead of rebuilding options for each intermediate configuration.
	selection, profiles, err := v.session.connection.readModelConfig(v.session.ctx, v.session.id)
	if err != nil || v.sameModelConfig(selection) {
		return true
	}
	v.modelSelection = selection
	options, _ := selectOptions(selection, profiles)
	return v.update(map[string]any{"sessionUpdate": "config_option_update", "configOptions": options}, "external", false)
}

func (v *eventView) sameModelConfig(selection core.ModelSelection) bool {
	return v.modelSelection.ID == selection.ID && reflect.DeepEqual(v.modelSelection.Reasoning, selection.Reasoning)
}

func visibleMessage(entry core.Entry) bool {
	message := entry.Message
	if message.Role != core.RoleUser && message.Role != core.RoleAssistant {
		return false
	}
	if entry.Kind == core.EntryContext {
		switch message.ContextKind {
		case "external.events":
			return true
		case "im.observation":
			var metadata messages.Metadata
			return json.Unmarshal(message.Metadata, &metadata) == nil && metadata.EventID != "" && entry.InputID == metadata.EventID+":trigger"
		default:
			return false
		}
	}
	if entry.Kind != core.EntryMessage || message.ContextKind != "" {
		return false
	}
	var metadata struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(message.Metadata, &metadata)
	return metadata.Source != "im.trigger" && metadata.Source != "im.wakeup" && metadata.Source != "external.trigger"
}

func (v *eventView) toolResult(entry core.Entry, wait bool) bool {
	if entry.Tool == nil {
		return true
	}
	inv := entry.Tool.Invocation
	id := toolID(inv.TurnID, inv.Call.ID)
	if !v.activeTools[id] {
		return true
	}
	delete(v.activeTools, id)
	status := "completed"
	update := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": id}
	if entry.Tool.Result != nil {
		result := entry.Tool.Result
		if result.IsError || result.Outcome != core.OutcomeSucceeded {
			status = "failed"
		}
		var text strings.Builder
		var content []map[string]any
		remainingImageBytes := maxOutboundImageBytes
		for _, part := range result.Content {
			if part.Type == "text" && text.Len() < textChunkBytes {
				text.WriteString(cutText(part.Text, textChunkBytes-text.Len()))
			}
			if part.Type == "image" && len(content) < 8 && remainingImageBytes > 0 {
				if image, size, ok := outboundImage(part); ok && size <= remainingImageBytes {
					content = append(content, map[string]any{"type": "content", "content": image})
					remainingImageBytes -= size
				}
			}
		}
		if text.Len() > 0 {
			content = append(toolContent(text.String()), content...)
		}
		if len(content) != 0 {
			update["content"] = content
		}
	}
	update["status"] = status
	return v.update(update, v.trigger(entry.RunID), wait)
}

func toolID(turnID, id string) string { return fmt.Sprintf("%s:%s", turnID, id) }

func toolContent(text string) []map[string]any {
	return []map[string]any{{"type": "content", "content": map[string]string{"type": "text", "text": cutText(text, textChunkBytes)}}}
}

func cutText(text string, size int) string {
	if len(text) <= size {
		return text
	}
	for size > 0 && !utf8.RuneStart(text[size]) {
		size--
	}
	return text[:size]
}

func (v *eventView) trigger(runID string) string {
	if runID != "" && v.runID == runID && v.source != "" {
		return v.source
	}
	return "external"
}

func (v *eventView) text(kind, id, text, source string, wait bool) bool {
	for len(text) > 0 {
		chunk := cutText(text, textChunkBytes)
		if !v.update(map[string]any{"sessionUpdate": kind, "messageId": id, "content": map[string]string{"type": "text", "text": chunk}}, source, wait) {
			return false
		}
		text = text[len(chunk):]
	}
	return true
}

func (v *eventView) update(update map[string]any, source string, wait bool) bool {
	update["_meta"] = map[string]string{"trigger_source": source}
	return v.session.connection.notify("session/update", map[string]any{"sessionId": v.session.id, "update": update}, wait)
}

func (s *loadedSession) finishPrompt(event core.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := s.pending
	if pending == nil {
		return
	}
	if pending.runID == "" {
		// An immediately failing or finishing Run can publish before SubmitACP
		// returns the admitted ID. Hold only recent candidates until it does.
		if len(pending.early) == 4 {
			copy(pending.early, pending.early[1:])
			pending.early = pending.early[:3]
		}
		pending.early = append(pending.early, event)
		return
	}
	if pending.runID == event.RunID {
		select {
		case pending.result <- event:
		default:
		}
	}
}
