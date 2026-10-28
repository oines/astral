package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
)

type InputRequest struct {
	ID    string   `json:"id"`
	Text  string   `json:"text"`
	Files []string `json:"files,omitempty"`
	Mode  string   `json:"mode,omitempty"`
}

type PendingEdit struct {
	Text *string `json:"text,omitempty"`
	Mode string  `json:"mode,omitempty"`
}

// UpdatePending changes one accepted input. Attachments are already frozen;
// editing its text never reads them from the Computer again.
func (s *Service) UpdatePending(ctx context.Context, id, inputID string, edit PendingEdit) error {
	if edit.Mode != "" && edit.Mode != "steer" && edit.Mode != "follow_up" {
		return domain.Invalid("input mode must be steer or follow_up")
	}
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var message *core.Message
	if edit.Text != nil {
		receipt, err := r.core.Load().LookupInput(ctx, inputID)
		if err != nil {
			return err
		}
		var metadata messages.Metadata
		if receipt.Message == nil {
			return domain.Invalid("input is not editable")
		}
		if err = json.Unmarshal(receipt.Message.Metadata, &metadata); err != nil {
			return err
		}
		if metadata.Source != "web" {
			return domain.Invalid("only owner messages can be edited")
		}
		if strings.TrimSpace(*edit.Text) == "" && len(metadata.Files) == 0 {
			return domain.Invalid("message or attachment is required")
		}
		updated := messages.Web(inputID, *edit.Text, metadata.Sender.Name, receipt.AcceptedAt, metadata.Files)
		updated.Content = append(updated.Content[:1], receipt.Message.Content[1:]...)
		message = &updated
	}
	if err = r.core.Load().UpdateQueuedInput(ctx, inputID, core.InputKind(edit.Mode), message); err != nil {
		return err
	}
	if edit.Mode == "steer" {
		err = r.continueLocked(ctx)
		if errors.Is(err, core.ErrBusy) || errors.Is(err, core.ErrNotContinuable) {
			err = nil
		}
	}
	s.notify(id)
	return err
}

func (s *Service) RemovePending(ctx context.Context, id, inputID string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err = r.core.Load().ClearQueuedInput(ctx, inputID); err != nil {
		return err
	}
	s.notify(id)
	return nil
}

func (s *Service) MovePending(ctx context.Context, id, inputID, beforeID string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err = r.core.Load().MoveQueuedInput(ctx, inputID, beforeID); err != nil {
		return err
	}
	s.notify(id)
	return nil
}

// InputReceipt is a presentation of Core's authoritative receipt. There is no
// Host admission journal or history scan to reconstruct input state.
type InputReceipt struct {
	InputID       string                `json:"input_id"`
	SessionID     string                `json:"session_id"`
	Digest        string                `json:"digest"`
	AcceptedAt    time.Time             `json:"accepted_at"`
	EntryID       string                `json:"entry_id,omitempty"`
	State         string                `json:"state"`
	Error         string                `json:"error,omitempty"`
	Generation    *core.InputGeneration `json:"generation,omitempty"`
	AcceptedRunID string                `json:"accepted_run_id,omitempty"`
}

func digest(value any) string { sum := sha256.Sum256(encode(value)); return hex.EncodeToString(sum[:]) }
func (r *runtime) receiptView(receipt core.InputReceipt) InputReceipt {
	return InputReceipt{InputID: receipt.ID, SessionID: r.id, Digest: receipt.Digest, AcceptedAt: receipt.AcceptedAt, EntryID: receipt.EntryID, State: string(receipt.State), Generation: receipt.Generation, AcceptedRunID: receipt.AcceptedRunID}
}
func (r *runtime) existingReceipt(ctx context.Context, id, hash string) (InputReceipt, bool, error) {
	receipt, err := r.core.Load().LookupInput(ctx, id)
	if err == nil {
		if receipt.Digest != hash {
			return InputReceipt{}, false, domain.Conflict("input ID was already used for different content")
		}
		return r.receiptView(receipt), true, nil
	}
	if errors.Is(err, core.ErrNotFound) {
		return InputReceipt{}, false, nil
	}
	return InputReceipt{}, false, err
}
func (s *Service) Submit(ctx context.Context, id string, input InputRequest) (InputReceipt, error) {
	if input.ID == "" || len(input.ID) > 200 {
		return InputReceipt{}, domain.Invalid("a stable input ID is required")
	}
	if strings.TrimSpace(input.Text) == "" && len(input.Files) == 0 {
		return InputReceipt{}, domain.Invalid("message or attachment is required")
	}
	if input.Mode == "" {
		input.Mode = "follow_up"
	}
	if input.Mode != "steer" && input.Mode != "follow_up" {
		return InputReceipt{}, domain.Invalid("input mode must be steer or follow_up")
	}
	r, err := s.open(ctx, id)
	if err != nil {
		return InputReceipt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	hash := digest(input)
	if receipt, exists, err := r.existingReceipt(ctx, input.ID, hash); err != nil || exists {
		return receipt, err
	}
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return InputReceipt{}, err
	}
	kind := core.InputKind(input.Mode)
	if record.BindingID != "" {
		kind = core.InputSteer
	}
	files := make([]domain.FileRef, 0, len(input.Files))
	for _, uri := range input.Files {
		file, err := s.Files.Get(ctx, r.agentID, uri)
		if err != nil {
			return InputReceipt{}, err
		}
		files = append(files, file)
	}
	message := messages.Web(input.ID, input.Text, "Owner", time.Now().UTC(), files)
	if err = s.Files.FreezeImages(ctx, r.agentID, id, &message); err != nil {
		return InputReceipt{}, err
	}
	receipt, err := r.accept(ctx, core.Input{ID: input.ID, Digest: hash, Message: message}, kind)
	if err == nil {
		r.maybeGenerateSessionTitle(input.Text)
	}
	return receipt, err
}

// Deliver admits each observation once. A working Session reads it at its next
// generation boundary; only an idle Session receives a separate direct wakeup.
func (s *Service) Deliver(ctx context.Context, id string, observation core.Message, trigger *core.Message, bindingID ...string) (InputReceipt, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return InputReceipt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(bindingID) > 0 {
		var session domain.Session
		if err = s.Store.Get(ctx, "sessions", id, &session); err != nil {
			return InputReceipt{}, err
		}
		if session.BindingID != bindingID[0] {
			return InputReceipt{}, domain.Conflict("the inbound binding is no longer active")
		}
	}

	var metadata messages.Metadata
	if err = json.Unmarshal(observation.Metadata, &metadata); err != nil || metadata.EventID == "" {
		return InputReceipt{}, domain.Invalid("observation needs an event identity")
	}
	inputID := metadata.EventID
	if trigger != nil {
		inputID += ":trigger"
	}
	hash := digest(struct {
		Observation core.Message
		Trigger     *core.Message
	}{observation, trigger})
	if receipt, exists, err := r.existingReceipt(ctx, inputID, hash); err != nil || exists {
		if err == nil && trigger != nil {
			_, err = r.wakeObservation(ctx, inputID, *trigger)
		}
		return receipt, err
	}
	input := core.Input{ID: inputID, Digest: hash, Contexts: []core.Message{observation}}
	for i := range input.Contexts {
		if err = s.Files.FreezeImages(ctx, r.agentID, id, &input.Contexts[i]); err != nil {
			return InputReceipt{}, err
		}
	}
	receipt, err := r.accept(ctx, input, core.InputContext)
	if err == nil && trigger != nil {
		var status core.InputAdmissionStatus
		status, err = r.wakeObservation(ctx, inputID, *trigger)
		if err == nil && status == core.InputAccepted {
			r.maybeGenerateSessionTitle(metadata.DisplayText)
		}
	}
	return receipt, err
}

// WakeObservation completes the idle handoff of a direct IM input. Core checks
// request ownership and idle state in the same lock as conditional admission.
func (s *Service) WakeObservation(ctx context.Context, id, inputID string, trigger core.Message, bindingID string) (core.InputAdmissionStatus, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var record domain.Session
	if err = s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return "", err
	}
	if record.BindingID != bindingID {
		return "", domain.Conflict("the inbound binding is no longer active")
	}
	return r.wakeObservation(ctx, inputID, trigger)
}

func (r *runtime) wakeObservation(ctx context.Context, inputID string, trigger core.Message) (core.InputAdmissionStatus, error) {
	session := r.core.Load()
	receipt, err := session.LookupInput(ctx, inputID)
	if err != nil {
		return "", err
	}
	if receipt.Kind != core.InputContext {
		// An input that already owns a driving role must not acquire another
		// driver. Pending owner work retains its normal Stop/Continue policy.
		return core.InputCovered, nil
	}
	if ObservationSuspended(session, receipt.AcceptedRunID) {
		return core.InputCovered, nil
	}
	wakeupID := inputID + ":wakeup"
	suspended, err := WakeupSuspended(ctx, session, r.service.CoreStore, wakeupID)
	if err != nil {
		return "", err
	}
	if suspended {
		return core.InputCovered, nil
	}
	var metadata messages.Metadata
	if err = json.Unmarshal(trigger.Metadata, &metadata); err != nil {
		return "", err
	}
	metadata.DeliveryID = wakeupID
	trigger.Metadata = encode(metadata)
	admission, err := session.SteerIfIdle(ctx, core.Input{ID: wakeupID, Message: trigger}, []string{inputID})
	if err != nil {
		return "", err
	}
	if admission.Status == core.InputBusy {
		// A fresh explicit IM request may resume a queue preserved by Stop.
		// Existing work already supplies the driver; retain this observation's
		// handoff until a generation actually claims it.
		if err = r.resumeQueueForNewObservation(ctx, inputID, receipt); err != nil {
			return "", err
		}
	}
	if admission.Status == core.InputExisting && admission.Receipt.State != core.InputPending {
		return core.InputCovered, nil
	}
	if admission.Status == core.InputAccepted || admission.Status == core.InputExisting {
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = r.continueLocked(persist)
		cancel()
		if errors.Is(err, core.ErrBusy) || errors.Is(err, core.ErrNotContinuable) {
			err = nil
		}
	}
	return admission.Status, err
}

func (r *runtime) resumeQueueForNewObservation(ctx context.Context, inputID string, receipt core.InputReceipt) error {
	session := r.core.Load()
	snapshot := session.Snapshot()
	state := snapshot.State
	if receipt.Generation != nil || receipt.AcceptedRunID != "" || snapshot.Running || snapshot.Maintenance || state.CurrentAttemptID != "" || !state.Branch.Complete() || state.PendingInputs[core.InputSteer]+state.PendingInputs[core.InputFollowUp] == 0 {
		return nil
	}
	queued, err := r.service.CoreStore.Scan(ctx, state.RolloutID, core.Query{SessionID: r.id, Kind: core.EntryInputQueued, Reference: inputID, Limit: 1, MaxBytes: 4 << 20})
	if err != nil || len(queued.Records) == 0 {
		return err
	}
	last, err := r.service.CoreStore.Scan(ctx, state.RolloutID, core.Query{SessionID: r.id, Kind: core.EntryRun, Reverse: true, Limit: 1, MaxBytes: 1 << 20})
	if err != nil {
		return err
	}
	if len(last.Records) != 0 {
		terminal := last.Records[0].Entry
		if terminal.Sequence > queued.Records[0].Entry.Sequence && (terminal.Run.Status == "cancelled" || terminal.Run.Status == "failed") {
			return nil
		}
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = r.continueLocked(persist)
	if errors.Is(err, core.ErrBusy) || errors.Is(err, core.ErrNotContinuable) {
		return nil
	}
	return err
}

// ObservationSuspended keeps Stop and failed runs from becoming implicit
// retries. A subsequent explicit input may still consume their passive context.
func ObservationSuspended(session *core.Session, acceptedRunID string) bool {
	last := session.Snapshot().State.LastRun
	return acceptedRunID != "" && last != nil && last.ID == acceptedRunID && (last.Status == "cancelled" || last.Status == "failed")
}

// WakeupSuspended distinguishes a crash before Continue from an explicit stop
// or failure after this accepted wakeup began. Both can leave it in the queue.
func WakeupSuspended(ctx context.Context, session *core.Session, store core.Store, inputID string) (bool, error) {
	state := session.State()
	if state.LastRun == nil || state.LastRun.Status != "cancelled" && state.LastRun.Status != "failed" {
		return false, nil
	}
	pending, err := store.Pending(ctx, session.ID(), core.PendingQuery{Kind: core.EntryInputQueued, InputKind: core.InputSteer, InputID: inputID, Limit: 1, MaxBytes: 4 << 20})
	if err != nil || len(pending.Records) == 0 {
		return false, err
	}
	last, err := store.Scan(ctx, state.RolloutID, core.Query{SessionID: session.ID(), Kind: core.EntryRun, Reverse: true, Limit: 1, MaxBytes: 1 << 20})
	if err != nil || len(last.Records) == 0 {
		return false, err
	}
	terminal := last.Records[0].Entry
	return terminal.Sequence > pending.Records[0].Entry.Sequence && (terminal.Run.Status == "cancelled" || terminal.Run.Status == "failed"), nil
}
func (r *runtime) accept(ctx context.Context, input core.Input, kind core.InputKind) (InputReceipt, error) {
	var record domain.Session
	if err := r.service.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return InputReceipt{}, err
	}
	if record.Deletion != nil {
		return InputReceipt{}, domain.Missing("Session", r.id)
	}
	if err := r.service.agentAvailable(ctx, record.AgentID); err != nil {
		return InputReceipt{}, err
	}
	var receipt core.InputReceipt
	var err error
	switch kind {
	case core.InputContext:
		receipt, err = r.core.Load().InjectContext(ctx, input)
	case core.InputSteer:
		receipt, err = r.core.Load().Steer(ctx, input)
	default:
		receipt, err = r.core.Load().FollowUp(ctx, input)
	}
	if err != nil {
		return r.receiptView(receipt), err
	}
	value := r.receiptView(receipt)
	if kind != core.InputContext {
		// Acceptance is durable. An already active worker will reach the next
		// eligible boundary; a completed/idle worker is explicitly activated here.
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		launchErr := r.continueLocked(persist)
		cancel()
		if launchErr != nil && !errors.Is(launchErr, core.ErrBusy) && !errors.Is(launchErr, core.ErrNotContinuable) {
			value.Error = launchErr.Error()
		}
	}
	r.service.notify(r.id)
	return value, nil
}
func (s *Service) Receipt(ctx context.Context, sessionID, inputID string) (InputReceipt, error) {
	r, err := s.open(ctx, sessionID)
	if err != nil {
		return InputReceipt{}, err
	}
	receipt, err := r.core.Load().LookupInput(ctx, inputID)
	if errors.Is(err, core.ErrNotFound) {
		return InputReceipt{}, domain.Missing("inputs", inputID)
	}
	return r.receiptView(receipt), err
}

// ResumeContext re-admits an already durable item to resume Core's passive
// maintenance after Host recovery. It neither reconstructs media nor drives a turn.
func (s *Service) ResumeContext(ctx context.Context, id string) error {
	return s.WithSession(ctx, id, func(session *core.Session) error {
		if session.State().PendingInputs[core.InputContext] == 0 {
			return nil
		}
		page, err := s.CoreStore.Pending(ctx, id, core.PendingQuery{Kind: core.EntryInputQueued, InputKind: core.InputContext, Limit: 1, MaxBytes: 128 << 20})
		if err != nil || len(page.Records) == 0 {
			return err
		}
		q := page.Records[0].Entry.Input
		_, err = session.InjectContext(ctx, core.Input{ID: q.ID, Digest: q.Digest, Contexts: q.Contexts})
		return err
	})
}
