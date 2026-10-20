package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	sessions "github.com/oines/astral-core/session"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
)

type InputRequest struct {
	ID    string   `json:"id"`
	Text  string   `json:"text"`
	Files []string `json:"files,omitempty"`
	Mode  string   `json:"mode,omitempty"`
}
type InputReceipt struct {
	domain.Meta
	DeliveryID string `json:"delivery_id"`
	SessionID  string `json:"session_id"`
	Digest     string `json:"digest"`
	InputID    string `json:"input_id,omitempty"`
	EntryID    string `json:"entry_id,omitempty"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
}

func receiptID(session, id string) string {
	sum := sha256.Sum256([]byte(session + "\x00" + id))
	return hex.EncodeToString(sum[:])
}
func digest(value any) string { sum := sha256.Sum256(encode(value)); return hex.EncodeToString(sum[:]) }

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
	receipt, err := r.receipt(ctx, input.ID, digest(input))
	if err != nil {
		return receipt, err
	}
	if receipt.InputID != "" {
		return r.refreshReceipt(ctx, receipt)
	}
	files := make([]domain.FileRef, 0, len(input.Files))
	for _, uri := range input.Files {
		file, getErr := s.Files.Get(ctx, r.agentID, uri)
		if getErr != nil {
			return receipt, getErr
		}
		files = append(files, file)
	}
	message := messages.Web(input.ID, input.Text, "Owner", receipt.CreatedAt, files)
	if err = s.Files.FreezeImages(ctx, r.agentID, id, &message); err != nil {
		return receipt, err
	}
	return r.accept(ctx, receipt, &message, nil, core.InputKind(input.Mode))
}

// Deliver keeps a new observation and trigger in one atomic Core input group.
// A passive delivery never starts a model, including on an empty Session.
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
	deliveryID := metadata.EventID
	kind := core.InputContext
	if trigger != nil {
		deliveryID += ":trigger"
		kind = core.InputFollowUp
	}
	receipt, err := r.receipt(ctx, deliveryID, digest(map[string]any{"event": metadata.EventID, "trigger": trigger != nil}))
	if err != nil {
		return receipt, err
	}
	if receipt.InputID != "" {
		return r.refreshReceipt(ctx, receipt)
	}
	contexts := []core.Message{observation}
	if trigger != nil {
		// If this observation was delivered passively earlier, do not duplicate
		// it. A pending passive record cannot yet support a reference-only prompt.
		var prior InputReceipt
		if findErr := s.Store.Get(ctx, "inputs", receiptID(id, metadata.EventID), &prior); findErr == nil {
			prior, err = r.refreshReceipt(ctx, prior)
			if err != nil {
				return receipt, err
			}
			if prior.State != "applied" {
				return receipt, domain.Conflict("referenced observation has not been applied yet")
			}
			contexts = nil
		} else if !domain.IsMissing(findErr) {
			return receipt, findErr
		}
	}
	for i := range contexts {
		if err = s.Files.FreezeImages(ctx, r.agentID, id, &contexts[i]); err != nil {
			return receipt, err
		}
	}
	return r.accept(ctx, receipt, trigger, contexts, kind)
}

func (r *runtime) receipt(ctx context.Context, id, hash string) (InputReceipt, error) {
	value := InputReceipt{}
	err := r.service.Store.Get(ctx, "inputs", receiptID(r.id, id), &value)
	if err == nil {
		if value.Digest != hash {
			return value, domain.Conflict("input ID was already used for different content")
		}
		return r.refreshReceipt(ctx, value)
	}
	if !domain.IsMissing(err) {
		return value, err
	}
	value = InputReceipt{Meta: domain.Meta{ID: receiptID(r.id, id)}, DeliveryID: id, SessionID: r.id, Digest: hash, State: "admitting"}
	err = r.service.Store.Save(ctx, "inputs", &value.Meta, &value)
	return value, err
}
func (r *runtime) refreshReceipt(ctx context.Context, value InputReceipt) (InputReceipt, error) {
	original := value
	if value.EntryID != "" {
		return value, nil
	}
	if value.InputID == "" {
		cursor := ""
		for {
			page, err := r.core.History().Scan(ctx, sessions.Query{SessionID: r.id, Kind: sessions.EntryInputQueued, Cursor: cursor, MaxBytes: 128 << 20, Reverse: true})
			if err != nil {
				return value, err
			}
			for _, entry := range page.Entries {
				for _, message := range entry.Input.Messages() {
					var metadata messages.Metadata
					if json.Unmarshal(message.Metadata, &metadata) == nil && metadata.DeliveryID == value.DeliveryID {
						value.InputID = entry.Input.ID
						break
					}
				}
				if value.InputID != "" {
					break
				}
			}
			if value.InputID != "" || page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		// The first driving message uses Core.Start: it is committed directly,
		// with its delivery identity, rather than duplicated in a queue.
		if value.InputID == "" {
			cursor = ""
			for {
				page, err := r.core.History().Scan(ctx, sessions.Query{SessionID: r.id, Kind: sessions.EntryMessage, Cursor: cursor, MaxBytes: 128 << 20, Reverse: true})
				if err != nil {
					return value, err
				}
				for _, entry := range page.Entries {
					var metadata messages.Metadata
					if entry.Message.Role == core.RoleUser && json.Unmarshal(entry.Message.Metadata, &metadata) == nil && metadata.DeliveryID == value.DeliveryID {
						value.InputID, value.EntryID, value.State, value.Error = entry.InputID, entry.ID, "applied", ""
						return value, r.service.Store.Save(ctx, "inputs", &value.Meta, &value)
					}
				}
				if page.NextCursor == "" {
					break
				}
				cursor = page.NextCursor
			}
		}
	}
	if value.InputID == "" {
		return value, nil
	}
	page, err := r.core.History().Scan(ctx, sessions.Query{SessionID: r.id, Kind: sessions.EntryInputConsumed, Reference: value.InputID, Limit: 1})
	if err != nil {
		return value, err
	}
	if len(page.Entries) > 0 {
		value.State = "applied"
	} else {
		value.State = "cleared"
		cursor := ""
		for {
			pending, scanErr := r.service.CoreStore.Pending(ctx, r.id, sessions.PendingQuery{Kind: sessions.EntryInputQueued, Cursor: cursor, MaxBytes: 128 << 20})
			if scanErr != nil {
				return value, scanErr
			}
			for _, record := range pending.Records {
				if record.Entry.Input.ID == value.InputID {
					value.State = "waiting"
					break
				}
			}
			if value.State == "waiting" || pending.Next == "" {
				break
			}
			cursor = pending.Next
		}
	}
	if value.State == "applied" || value.State == "cleared" {
		value.Error = ""
	}
	if jsonEqual(original, value) {
		return value, nil
	}
	return value, r.service.Store.Save(ctx, "inputs", &value.Meta, &value)
}

func (r *runtime) accept(ctx context.Context, receipt InputReceipt, message *core.Message, contexts []core.Message, kind core.InputKind) (InputReceipt, error) {
	var inputID string
	var err error
	if kind != core.InputContext && r.run == nil && r.maintenance == "" && r.core.State().Branch.LastMessageID == "" {
		if err = r.syncSelection(ctx); err != nil {
			return receipt, err
		}
		_, _ = r.currentBinding(ctx)
		s := r.service
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return receipt, core.ErrClosed
		}
		run, startErr := r.core.Start(s.ctx, *message, core.InputOptions{Contexts: contexts})
		if startErr == nil {
			r.trackRunLocked(run)
		}
		s.mu.Unlock()
		if startErr != nil {
			return receipt, startErr
		}
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.notify(r.id)
		return r.refreshReceipt(persist, receipt)
	}
	switch kind {
	case core.InputContext:
		inputID, err = r.core.InjectContext(ctx, contexts)
	case core.InputSteer:
		inputID, err = r.core.Steer(ctx, *message, core.InputOptions{Contexts: contexts})
	default:
		inputID, err = r.core.FollowUp(ctx, *message, core.InputOptions{Contexts: contexts})
	}
	if inputID != "" {
		receipt.InputID = inputID
	}
	if err != nil {
		receipt.State, receipt.Error = "admission_failed", err.Error()
	} else {
		receipt.State, receipt.Error = "waiting", ""
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if saveErr := r.service.Store.Save(persist, "inputs", &receipt.Meta, &receipt); saveErr != nil {
		return receipt, saveErr
	}
	if err != nil {
		return receipt, err
	}
	if kind != core.InputContext && r.run == nil && r.maintenance == "" {
		if startErr := r.continueLocked(persist); startErr != nil {
			// Core already accepted this input durably. Report waiting plus the
			// actual launch problem; callers must not submit a different ID.
			receipt.Error = startErr.Error()
			_ = r.service.Store.Save(persist, "inputs", &receipt.Meta, &receipt)
		}
	}
	r.service.notify(r.id)
	return r.refreshReceipt(persist, receipt)
}

func (s *Service) Receipt(ctx context.Context, sessionID, deliveryID string) (InputReceipt, error) {
	r, err := s.open(ctx, sessionID)
	if err != nil {
		return InputReceipt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var receipt InputReceipt
	if err = s.Store.Get(ctx, "inputs", receiptID(sessionID, deliveryID), &receipt); err != nil {
		return receipt, err
	}
	return r.refreshReceipt(ctx, receipt)
}
