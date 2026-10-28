package harness

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/messages"
)

type IMWakeStatus string

const (
	IMWakeAccepted IMWakeStatus = "accepted"
	IMWakeExisting IMWakeStatus = "existing"
	IMWakeBusy     IMWakeStatus = "busy"
	IMWakeCovered  IMWakeStatus = "covered"
)

func IMWorking(session *core.Session) bool {
	snapshot := session.Snapshot()
	return snapshot.Running || snapshot.Maintenance || snapshot.Stopping || snapshot.State.CurrentAttemptID != "" || !snapshot.State.Branch.Complete()
}

func IMCanProbe(session *core.Session) bool {
	state := session.State()
	return !IMWorking(session) && state.PendingInputs[core.InputSteer]+state.PendingInputs[core.InputFollowUp] == 0
}

// IMInspector reads existing execution evidence. Its cache lasts only for one
// Host scheduling operation; it adds no receipt fields or persisted state.
type IMInspector struct {
	session *core.Session
	store   core.Store
	audits  map[string]imAudit
}

type imAudit struct {
	events  map[string]bool
	through uint64
}

func NewIMInspector(session *core.Session, store core.Store) *IMInspector {
	return &IMInspector{session: session, store: store, audits: map[string]imAudit{}}
}

func (i *IMInspector) Handled(ctx context.Context, inputID string) (bool, error) {
	receipt, err := i.session.LookupInput(ctx, inputID)
	if err != nil {
		return false, err
	}
	if receipt.Kind != core.InputContext || receipt.State == core.InputCleared {
		return true, nil
	}
	stopped, err := IMInputStopped(ctx, i.session, i.store, inputID)
	if err != nil || stopped {
		return stopped, err
	}
	if receipt.State != core.InputApplied || receipt.EntryID == "" {
		return false, nil
	}
	state := i.session.State()
	source, err := i.store.Get(ctx, state.RolloutID, receipt.EntryID)
	if err != nil {
		return false, err
	}
	var metadata messages.Metadata
	if source.Entry.Message == nil || json.Unmarshal(source.Entry.Message.Metadata, &metadata) != nil || metadata.EventID == "" {
		return false, nil
	}
	var cursor string
	for {
		page, err := i.session.Attempts().Scan(ctx, core.AttemptQuery{Purpose: "generation", Reverse: true, Cursor: cursor, Limit: 64, MaxBytes: 8 << 20})
		if err != nil {
			return false, err
		}
		for _, attempt := range page.Attempts {
			if attempt.Version < source.Entry.Version {
				return false, nil
			}
			audit, ok := i.audits[attempt.ID]
			if !ok {
				audit, err = i.readAttempt(ctx, attempt)
				if err != nil {
					return false, err
				}
				i.audits[attempt.ID] = audit
			}
			if audit.events[metadata.ConversationID+"\x00"+metadata.EventID] || source.Entry.Sequence <= audit.through {
				return true, nil
			}
		}
		if page.Next == "" {
			return false, nil
		}
		cursor = page.Next
	}
}

func (i *IMInspector) readAttempt(ctx context.Context, attempt core.ModelAttempt) (imAudit, error) {
	audit := imAudit{events: map[string]bool{}}
	request, err := i.session.Attempts().Request(ctx, attempt.ID)
	if err != nil {
		return audit, err
	}
	for _, message := range request.Messages {
		var metadata messages.Metadata
		if message.ContextKind == "im.observation" && json.Unmarshal(message.Metadata, &metadata) == nil && metadata.Source == "im" {
			audit.events[metadata.ConversationID+"\x00"+metadata.EventID] = true
		}
		if message.ContextKind != "summary" {
			continue
		}
		view, err := core.ReadContext(ctx, i.store, attempt.Input.RolloutID, attempt.Input.HeadID, 64<<20)
		if err != nil {
			return audit, err
		}
		for at, original := range view.Request.Messages {
			if original.ContextKind != "summary" || digest(original.Content) != digest(message.Content) || at >= len(view.Sources) {
				continue
			}
			checkpoint, err := i.store.Get(ctx, attempt.Input.RolloutID, view.Sources[at])
			if err != nil {
				return audit, err
			}
			if checkpoint.Entry.Compaction != nil {
				through, err := i.store.Get(ctx, attempt.Input.RolloutID, checkpoint.Entry.Compaction.ThroughID)
				if err != nil {
					return audit, err
				}
				audit.through = max(audit.through, through.Entry.Sequence)
			}
		}
	}
	return audit, nil
}

// IMInputStopped compares existing operational entry order, not wall-clock time.
func IMInputStopped(ctx context.Context, session *core.Session, store core.Store, inputID string) (bool, error) {
	state := session.State()
	if state.LastRun == nil || state.LastRun.Status != "cancelled" && state.LastRun.Status != "failed" {
		return false, nil
	}
	queued, err := store.Scan(ctx, state.RolloutID, core.Query{SessionID: session.ID(), Kind: core.EntryInputQueued, Reference: inputID, Limit: 1, MaxBytes: 4 << 20})
	if err != nil || len(queued.Records) == 0 {
		return false, err
	}
	last, err := store.Scan(ctx, state.RolloutID, core.Query{SessionID: session.ID(), Kind: core.EntryRun, Reverse: true, Limit: 1, MaxBytes: 1 << 20})
	if err != nil || len(last.Records) == 0 {
		return false, err
	}
	terminal := last.Records[0].Entry
	return terminal.Sequence > queued.Records[0].Entry.Sequence && (terminal.Run.Status == "cancelled" || terminal.Run.Status == "failed"), nil
}

// AdmitIMWakeup requires the caller's Host admission lock. Genuine idle means
// Core has no active generation to race this audit; all Host starts share that
// lock. Maintenance racing from an existing Host operation is checked again.
func AdmitIMWakeup(ctx context.Context, session *core.Session, store core.Store, input core.Input, dependencies []string, allowQueued bool) (IMWakeStatus, error) {
	if existing, err := session.LookupInput(ctx, input.ID); err == nil {
		stopped, err := IMInputStopped(ctx, session, store, input.ID)
		if err != nil {
			return "", err
		}
		if existing.State != core.InputPending || stopped {
			return IMWakeCovered, nil
		}
		if IMWorking(session) {
			return IMWakeBusy, nil
		}
		return IMWakeExisting, nil
	} else if !errors.Is(err, core.ErrNotFound) {
		return "", err
	}
	before := session.Snapshot()
	if IMWorking(session) || !allowQueued && !IMCanProbe(session) {
		return IMWakeBusy, nil
	}
	inspector := NewIMInspector(session, store)
	for _, dependency := range dependencies {
		handled, err := inspector.Handled(ctx, dependency)
		if err != nil {
			return "", err
		}
		if handled {
			return IMWakeCovered, nil
		}
		receipt, err := session.LookupInput(ctx, dependency)
		if err != nil {
			return "", err
		}
		if receipt.State != core.InputApplied {
			if _, err = session.InjectContext(ctx, core.Input{ID: receipt.ID, Digest: receipt.Digest, Contexts: receipt.Contexts}); err != nil {
				return "", err
			}
			return IMWakeBusy, nil
		}
	}
	if IMWorking(session) || session.State().Version != before.State.Version {
		return IMWakeBusy, nil
	}
	if allowQueued && before.State.PendingInputs[core.InputSteer]+before.State.PendingInputs[core.InputFollowUp] > 0 {
		// The resumed owner input will read these applied observations. Adding
		// another driver would handle them twice when steering is bounded.
		return IMWakeExisting, nil
	}
	_, err := session.Steer(ctx, input)
	return IMWakeAccepted, err
}
