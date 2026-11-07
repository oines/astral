package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
)

const imReplyWatchKind = "im_reply_watch"
const imReplyReminderKind = "im.reply_reminder"
const imMissedReplyReminder = "你刚刚输出的文字只显示在网页，IM 中的用户看不到。请调用 send_message 把回复发送给用户。如果你决定不回复，请忽略此提醒。"

var errIMReplyAdmissionBusy = errors.New("IM reply reconciliation is awaiting Host admission")

type imReplyInput struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

// One replaceable operation per root retains only unresolved direct input IDs.
// Registering at admission deliberately excludes all pre-upgrade chat history.
type imReplyWatch struct {
	AgentID        string         `json:"agent_id"`
	SessionID      string         `json:"session_id"`
	BindingID      string         `json:"binding_id"`
	ConversationID string         `json:"conversation_id"`
	Inputs         []imReplyInput `json:"inputs,omitempty"`
	CheckedVersion uint64         `json:"checked_version,omitempty"`
	ReminderID     string         `json:"reminder_id,omitempty"`
	RunID          string         `json:"run_id,omitempty"`
}

// Called only by IM delivery under the runtime admission lock, before accepting
// the input. Web input IDs, even matching a trigger suffix, never register here.
func (r *runtime) watchIMReply(ctx context.Context, inputID string, observation messages.Metadata, trigger *core.Message) error {
	if trigger == nil || observation.Source != "im" {
		return nil
	}
	var source messages.Metadata
	if json.Unmarshal(trigger.Metadata, &source) != nil || source.Source != "im.trigger" || source.EventID != observation.EventID || source.ConversationID != observation.ConversationID {
		return nil
	}
	var record domain.Session
	if err := r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return err
	}
	if record.BindingID == "" || record.SubagentParentID != "" || record.ConversationID != observation.ConversationID {
		return nil
	}
	direct, err := r.service.Store.IsDirectObservation(ctx, observation.EventID, record.ConversationID)
	if err != nil || !direct {
		return err
	}
	var watch imReplyWatch
	if err := r.service.Store.Maintenance(ctx, imReplyWatchKind, r.id, &watch); err != nil && !domain.IsMissing(err) {
		return err
	}
	if watch.BindingID != record.BindingID || watch.ConversationID != record.ConversationID {
		watch = imReplyWatch{AgentID: r.agentID, SessionID: r.id, BindingID: record.BindingID, ConversationID: record.ConversationID}
	}
	for _, input := range watch.Inputs {
		if input.ID == inputID {
			return nil
		}
	}
	watch.Inputs = append(watch.Inputs, imReplyInput{ID: inputID, Version: r.core.Load().State().Version})
	watch.CheckedVersion = 0
	if err := r.service.Store.SaveMaintenance(ctx, imReplyWatchKind, r.id, "pending", watch); err != nil {
		return err
	}
	r.service.wakeSubagents(r.id)
	return nil
}

func (s *Service) reconcileIMReply(ctx context.Context, record domain.Session) error {
	var watch imReplyWatch
	if err := s.Store.Maintenance(ctx, imReplyWatchKind, record.ID, &watch); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	clear := func() error { return s.Store.DeleteMaintenance(ctx, imReplyWatchKind, record.ID) }
	r, err := s.open(ctx, record.ID)
	if err != nil {
		return err
	}
	if !r.mu.TryLock() {
		// A read-only Host callback may release mu without another Core commit.
		// Retain the existing worker's bounded error retry for that narrow case.
		return errIMReplyAdmissionBusy
	}
	defer r.mu.Unlock()
	// Admission may have replaced the watch while this worker was opening.
	if err = s.Store.Maintenance(ctx, imReplyWatchKind, record.ID, &watch); domain.IsMissing(err) {
		return nil
	} else if err != nil {
		return err
	}
	var current domain.Session
	if err = s.Store.Get(ctx, "sessions", record.ID, &current); err != nil {
		return err
	}
	if current.Deletion != nil || current.BindingID != watch.BindingID || current.ConversationID != watch.ConversationID {
		return clear()
	}
	snapshot, err := r.snapshot()
	if err != nil {
		return err
	}
	if snapshot.Running || snapshot.Maintenance || snapshot.Stopping {
		return nil
	}
	state := snapshot.State
	if state.LastRun == nil {
		return nil
	}
	if pending, err := s.ModelFallbackPending(ctx, record.ID); err != nil || pending {
		// A failed Core Run can be an internal fallback boundary. Keep the
		// direct-input watch until the logical task completes or really stops.
		return err
	}
	if state.LastRun.Status == "cancelled" || state.LastRun.Status == "failed" {
		return clear()
	}
	if watch.ReminderID != "" {
		return r.resumeIMReply(ctx, &watch)
	}
	if state.LastRun.Status != "completed" || state.PendingInputs[core.InputSteer]+state.PendingInputs[core.InputFollowUp] != 0 || watch.CheckedVersion == state.Version {
		return nil
	}
	// Attempt metadata pins the actual generation boundary without rebuilding
	// its context/images. A final idle flush after that boundary is not a read.
	attempts, err := s.CoreStore.ScanAttempts(ctx, record.ID, core.AttemptQuery{Purpose: "generation", Reverse: true, Limit: 1, MaxBytes: 1 << 20})
	if err != nil {
		return err
	}
	if len(attempts.Attempts) == 0 || attempts.Attempts[0].RunID != state.LastRun.ID || attempts.Attempts[0].Outcome == nil || attempts.Attempts[0].Outcome.Status != "completed" {
		return nil
	}
	consumed, boundary, err := s.consumedIMReplies(ctx, state, watch.Inputs, attempts.Attempts[0].Version)
	if err != nil {
		return err
	}
	watch.CheckedVersion = state.Version
	if len(consumed) == 0 {
		return s.Store.SaveMaintenance(ctx, imReplyWatchKind, record.ID, "pending", watch)
	}
	sent := false
	if boundary != 0 {
		sent, err = s.imReplyToolStarted(ctx, state, boundary)
		if err != nil {
			return err
		}
	}
	remaining := watch.Inputs[:0]
	for _, input := range watch.Inputs {
		if !consumed[input.ID] {
			remaining = append(remaining, input)
		}
	}
	watch.Inputs = remaining
	if boundary != 0 && !sent {
		watch.RunID = state.LastRun.ID
		watch.ReminderID = "im_reply_reminder_" + watch.RunID
	}
	if len(watch.Inputs) == 0 && watch.ReminderID == "" {
		return clear()
	}
	if err := s.Store.SaveMaintenance(ctx, imReplyWatchKind, record.ID, "pending", watch); err != nil {
		return err
	}
	if watch.ReminderID != "" {
		return r.resumeIMReply(ctx, &watch)
	}
	return nil
}

func (s *Service) consumedIMReplies(ctx context.Context, state core.RuntimeState, inputs []imReplyInput, generationVersion uint64) (map[string]bool, uint64, error) {
	wanted := make(map[string]uint64, len(inputs))
	before := state.Version
	for _, input := range inputs {
		wanted[input.ID] = input.Version
		before = min(before, input.Version)
	}
	consumed := map[string]bool{}
	var boundary uint64
	var cursor string
	for len(wanted) > 0 {
		page, err := s.CoreStore.Scan(ctx, state.RolloutID, core.Query{SessionID: state.SessionID, Kind: core.EntryInputConsumed, Reverse: true, Cursor: cursor, Limit: 64, MaxBytes: 1 << 20})
		if err != nil {
			return nil, 0, err
		}
		for _, row := range page.Records {
			entry := row.Entry
			if entry.Version <= before {
				return consumed, boundary, nil
			}
			if entry.Input == nil || entry.Version > generationVersion {
				continue
			}
			id := entry.Input.ID
			// An idle handoff consumes this small direct driver after its passive
			// observation was applied outside the generation's own Run.
			if _, ok := wanted[id]; !ok {
				id = strings.TrimSuffix(id, ":wakeup")
			}
			if registered, ok := wanted[id]; ok && entry.Version > registered {
				consumed[id] = true
				if entry.RunID == state.LastRun.ID {
					boundary = max(boundary, entry.Sequence)
				}
				// A different Run's old observation cannot turn a later Probe,
				// Web input or subagent notification into a direct reply request.
				delete(wanted, id)
			}
		}
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	return consumed, boundary, nil
}

func (s *Service) imReplyToolStarted(ctx context.Context, state core.RuntimeState, after uint64) (bool, error) {
	var cursor string
	for {
		page, err := s.CoreStore.Scan(ctx, state.RolloutID, core.Query{SessionID: state.SessionID, Kind: core.EntryToolStarted, Reverse: true, Cursor: cursor, Limit: 64, MaxBytes: 1 << 20})
		if err != nil {
			return false, err
		}
		for _, row := range page.Records {
			entry := row.Entry
			if entry.Sequence <= after {
				return false, nil
			}
			if entry.RunID == state.LastRun.ID && entry.Tool != nil && entry.Tool.Invocation.Call.Name == "send_message" {
				return true, nil
			}
		}
		if page.Next == "" {
			return false, nil
		}
		cursor = page.Next
	}
}

func (r *runtime) resumeIMReply(ctx context.Context, watch *imReplyWatch) error {
	s := r.service
	receipt, err := r.core.Load().LookupInput(ctx, watch.ReminderID)
	if err != nil && !errors.Is(err, core.ErrNotFound) {
		return err
	}
	if errors.Is(err, core.ErrNotFound) {
		message := core.User(imMissedReplyReminder)
		message.ContextKind = imReplyReminderKind
		message.Metadata = encode(messages.Metadata{Source: imReplyReminderKind, DeliveryID: watch.ReminderID, ConversationID: watch.ConversationID})
		receipt, err = r.core.Load().Steer(ctx, core.Input{ID: watch.ReminderID, Message: message})
		if err != nil {
			return err
		}
	}
	if receipt.State == core.InputPending {
		stopped, err := IMInputStopped(ctx, r.core.Load(), s.CoreStore, watch.ReminderID)
		if err != nil {
			return err
		}
		if !stopped {
			if err := r.continueLocked(ctx); err != nil {
				return err
			}
			// Continue only commits Run.started. Retain the recovery identity
			// until asynchronous preparation durably consumes this exact input.
			receipt, err = r.core.Load().LookupInput(ctx, watch.ReminderID)
			if err != nil {
				return err
			}
			if receipt.State == core.InputPending {
				return nil
			}
		}
	}
	watch.ReminderID, watch.RunID, watch.CheckedVersion = "", "", 0
	if len(watch.Inputs) == 0 {
		return s.Store.DeleteMaintenance(ctx, imReplyWatchKind, r.id)
	}
	if err := s.Store.SaveMaintenance(ctx, imReplyWatchKind, r.id, "pending", watch); err != nil {
		return err
	}
	s.wakeSubagents(r.id)
	return nil
}
