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
	receipt, err := r.core.LookupInput(ctx, id)
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
	return r.accept(ctx, core.Input{ID: input.ID, Digest: hash, Message: message}, core.InputKind(input.Mode))
}

// Deliver admits each observation/trigger group atomically. Observations alone
// remain passive, including when the session has no driving conversation yet.
func (s *Service) Deliver(ctx context.Context, id string, observation core.Message, trigger *core.Message) (InputReceipt, error) {
	r, err := s.open(ctx, id)
	if err != nil {
		return InputReceipt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var metadata messages.Metadata
	if err = json.Unmarshal(observation.Metadata, &metadata); err != nil || metadata.EventID == "" {
		return InputReceipt{}, domain.Invalid("observation needs an event identity")
	}
	inputID, kind := metadata.EventID, core.InputContext
	if trigger != nil {
		inputID += ":trigger"
		kind = core.InputFollowUp
	}
	hash := digest(struct {
		Observation core.Message
		Trigger     *core.Message
	}{observation, trigger})
	if receipt, exists, err := r.existingReceipt(ctx, inputID, hash); err != nil || exists {
		return receipt, err
	}
	input := core.Input{ID: inputID, Digest: hash, Contexts: []core.Message{observation}}
	if trigger != nil {
		input.Message = *trigger
		prior, err := r.core.LookupInput(ctx, metadata.EventID)
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
	return r.accept(ctx, input, kind)
}
func (r *runtime) accept(ctx context.Context, input core.Input, kind core.InputKind) (InputReceipt, error) {
	var receipt core.InputReceipt
	var err error
	switch kind {
	case core.InputContext:
		receipt, err = r.core.InjectContext(ctx, input)
	case core.InputSteer:
		receipt, err = r.core.Steer(ctx, input)
	default:
		receipt, err = r.core.FollowUp(ctx, input)
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
	receipt, err := r.core.LookupInput(ctx, inputID)
	if errors.Is(err, core.ErrNotFound) {
		return InputReceipt{}, domain.Missing("inputs", inputID)
	}
	return r.receiptView(receipt), err
}
