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

// A logical task remains active between failed model Runs while Host selects
// another candidate or waits for its preferred model to recover.
func (s *Service) IMCanProbe(ctx context.Context, id string, session *core.Session) (bool, error) {
	pending, err := s.ModelFallbackPending(ctx, id)
	if err != nil || pending {
		return false, err
	}
	return IMCanProbe(session), nil
}

// IMInspector retains only evidence derived from immutable attempt
// inputs. Receipt and latest-attempt discovery are always read again, so it
// never caches a decision that a newly received observation is unhandled.
// The owning runtime serializes access, releases it once recovery catches up,
// and replaces it when Core is reopened. No request bodies are retained.
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

// ReleaseIMRecovery drops transient audit evidence after the caller's durable
// covered-input cursor reaches its delivered archive boundary. It must never
// open a cold Session or query its history just to clear a cache.
func (s *Service) ReleaseIMRecovery(id string) {
	s.mu.Lock()
	r := s.runtimes[id]
	s.mu.Unlock()
	if r != nil {
		r.mu.Lock()
		r.imInspector = nil
		r.mu.Unlock()
	}
}

func (i *IMInspector) Handled(ctx context.Context, inputID string) (bool, error) {
	return i.handled(ctx, inputID, false)
}

func (i *IMInspector) handled(ctx context.Context, inputID string, external bool) (bool, error) {
	receipt, err := i.session.LookupInput(ctx, inputID)
	if err != nil {
		return false, err
	}
	if receipt.Kind != core.InputContext || receipt.State == core.InputCleared {
		return true, nil
	}
	// External delivery has its own durable Stop generation. Core cancellation
	// also happens on Server shutdown, so it cannot distinguish an Owner stop
	// from a restart that still owes this external batch a model handoff.
	if !external {
		stopped, err := IMInputStopped(ctx, i.session, i.store, inputID)
		if err != nil || stopped {
			return stopped, err
		}
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
			if audit.events[observedEventKey(metadata)] || source.Entry.Sequence <= audit.through {
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
	var summaryView *core.Context
	for _, message := range request.Messages {
		var metadata messages.Metadata
		if json.Unmarshal(message.Metadata, &metadata) == nil && (message.ContextKind == "im.observation" && metadata.Source == "im" || message.ContextKind == "external.events" && metadata.Source == "external") {
			audit.events[observedEventKey(metadata)] = true
		}
		if message.ContextKind != "summary" {
			continue
		}
		if summaryView == nil {
			view, err := core.ReadContext(ctx, i.store, attempt.Input.RolloutID, attempt.Input.HeadID, 64<<20)
			if err != nil {
				return audit, err
			}
			summaryView = &view
		}
		for at, original := range summaryView.Request.Messages {
			if original.ContextKind != "summary" || digest(original.Content) != digest(message.Content) || at >= len(summaryView.Sources) {
				continue
			}
			checkpoint, err := i.store.Get(ctx, attempt.Input.RolloutID, summaryView.Sources[at])
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

func observedEventKey(metadata messages.Metadata) string {
	if metadata.Source == "external" {
		return "\x00external\x00" + metadata.EventID
	}
	return metadata.ConversationID + "\x00" + metadata.EventID
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
	return admitIMWakeup(ctx, session, store, input, dependencies, allowQueued, NewIMInspector(session, store))
}

func admitIMWakeup(ctx context.Context, session *core.Session, store core.Store, input core.Input, dependencies []string, allowQueued bool, inspector *IMInspector) (IMWakeStatus, error) {
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
