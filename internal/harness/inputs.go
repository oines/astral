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
	InputID    string    `json:"input_id"`
	SessionID  string    `json:"session_id"`
	Digest     string    `json:"digest"`
	AcceptedAt time.Time `json:"accepted_at"`
	EntryID    string    `json:"entry_id,omitempty"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
}

func digest(value any) string { sum := sha256.Sum256(encode(value)); return hex.EncodeToString(sum[:]) }
func (r *runtime) receiptView(receipt core.InputReceipt) InputReceipt {
	return InputReceipt{InputID: receipt.ID, SessionID: r.id, Digest: receipt.Digest, AcceptedAt: receipt.AcceptedAt, EntryID: receipt.EntryID, State: string(receipt.State)}
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
	kind := core.InputKind(input.Mode)
	files := make([]domain.FileRef, 0, len(input.Files))
	for _, uri := range input.Files {
		file, err := s.Files.Get(ctx, r.agentID, uri)
		if err != nil {
			return InputReceipt{}, err
		}
		files = append(files, file)
	}
	location, err := s.Timezone.Location(ctx)
	if err != nil {
		return InputReceipt{}, err
	}
	message := messages.Web(input.ID, input.Text, "Owner", time.Now().In(location), files)
	if err = s.Files.FreezeImages(ctx, r.agentID, id, &message); err != nil {
		return InputReceipt{}, err
	}
	receipt, err := r.accept(ctx, core.Input{ID: input.ID, Digest: hash, Message: message}, kind)
	if err == nil {
		r.maybeGenerateSessionTitle(input.Text)
	}
	return receipt, err
}

// Deliver keeps idle direct triggers atomic. During a running turn, all IM
// messages are observations for its next request rather than another driver.
func (s *Service) Deliver(ctx context.Context, id string, observation core.Message, trigger *core.Message, bindingID ...string) (InputReceipt, error) {
	var target domain.Session
	if err := s.Store.Get(ctx, "sessions", id, &target); err != nil {
		return InputReceipt{}, err
	}
	if target.SubagentParentID != "" {
		return InputReceipt{}, domain.Forbidden("new IM context is only delivered to user-facing root Sessions")
	}
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
	inputID, kind := metadata.EventID, core.InputContext
	if trigger != nil {
		inputID += ":trigger"
		canProbe, probeErr := s.IMCanProbe(ctx, id, r.core.Load())
		if probeErr != nil {
			return InputReceipt{}, probeErr
		}
		if canProbe {
			kind = core.InputSteer
		}
	}
	hash := digest(struct {
		Observation core.Message
		Trigger     *core.Message
	}{observation, trigger})
	if receipt, exists, err := r.existingReceipt(ctx, inputID, hash); err != nil || exists {
		return receipt, err
	}
	input := core.Input{ID: inputID, Digest: hash, Contexts: []core.Message{observation}}
	if kind == core.InputSteer {
		input.Message = *trigger
		prior, err := r.core.Load().LookupInput(ctx, metadata.EventID)
		if err == nil {
			passiveHash := digest(struct {
				Observation core.Message
				Trigger     *core.Message
			}{Observation: observation})
			if prior.Digest != passiveHash {
				return InputReceipt{}, domain.Conflict("referenced observation has different content")
			}
			if prior.State != core.InputApplied {
				return InputReceipt{}, domain.Conflict("referenced observation has not been applied yet")
			}
			input.Contexts = nil
		} else if !errors.Is(err, core.ErrNotFound) {
			return InputReceipt{}, err
		}
	}
	for i := range input.Contexts {
		if err = s.Files.FreezeImages(ctx, r.agentID, id, &input.Contexts[i]); err != nil {
			return InputReceipt{}, err
		}
	}
	receipt, err := r.accept(ctx, input, kind)
	if err == nil && kind == core.InputSteer {
		r.maybeGenerateSessionTitle(metadata.DisplayText)
	}
	if err == nil && trigger != nil && kind == core.InputContext {
		if pending, checkErr := s.ModelFallbackPending(ctx, id); checkErr != nil || pending {
			return receipt, checkErr
		}
		// A new explicit message can resume a stopped queue or an attempt left
		// by a crash. Passive delivery makes it available to the resumed run.
		snapshot := r.core.Load().Snapshot()
		if snapshot.Running || snapshot.Maintenance || snapshot.Stopping {
			return receipt, nil
		}
		stopped, checkErr := IMInputStopped(ctx, r.core.Load(), s.CoreStore, inputID)
		if checkErr != nil {
			return receipt, checkErr
		}
		if !stopped {
			err = r.continueLocked(ctx)
			if errors.Is(err, core.ErrBusy) || errors.Is(err, core.ErrNotContinuable) {
				err = nil
			}
		}
	}
	return receipt, err
}

type IMWakeup struct {
	InputID string
	Message core.Message
}

// WakeObservations hands off a page of direct observations under the same Host
// admission lock. Frozen request evidence is shared across the page, rather
// than reconstructing the same history separately for every recovered message.
// A busy observation ends the batch so the caller can retain its scan position.
func (s *Service) WakeObservations(ctx context.Context, id string, inputs []IMWakeup, bindingID string) ([]IMWakeStatus, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var record domain.Session
	if err = s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return nil, err
	}
	if record.BindingID != bindingID {
		return nil, domain.Conflict("the inbound binding is no longer active")
	}
	inspector := NewIMInspector(r.core.Load(), s.CoreStore)
	statuses := make([]IMWakeStatus, 0, len(inputs))
	for _, input := range inputs {
		// Starting work can reopen Core when the binding configuration changes.
		if inspector.session != r.core.Load() {
			inspector = NewIMInspector(r.core.Load(), s.CoreStore)
		}
		status, err := r.wakeObservation(ctx, input.InputID, input.Message, inspector)
		if err != nil {
			return statuses, err
		}
		statuses = append(statuses, status)
		if status == IMWakeBusy {
			break
		}
	}
	return statuses, nil
}

func (r *runtime) wakeObservation(ctx context.Context, inputID string, trigger core.Message, inspector *IMInspector) (IMWakeStatus, error) {
	handled, err := inspector.Handled(ctx, inputID)
	if err != nil || handled {
		return IMWakeCovered, err
	}
	if pending, err := r.service.ModelFallbackPending(ctx, r.id); err != nil || pending {
		return IMWakeBusy, err
	}
	wakeupID := inputID + ":wakeup"
	var metadata messages.Metadata
	if err = json.Unmarshal(trigger.Metadata, &metadata); err != nil {
		return "", err
	}
	metadata.DeliveryID = wakeupID
	trigger.Metadata = encode(metadata)
	status, err := admitIMWakeup(ctx, r.core.Load(), r.service.CoreStore, core.Input{ID: wakeupID, Message: trigger}, []string{inputID}, true, inspector)
	if err != nil {
		return "", err
	}
	if status == IMWakeAccepted || status == IMWakeExisting {
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = r.continueLocked(persist)
		cancel()
		if errors.Is(err, core.ErrBusy) || errors.Is(err, core.ErrNotContinuable) {
			err = nil
		}
	}
	return status, err
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
