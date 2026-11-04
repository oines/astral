package channels

import (
	"context"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const inboxHandoffKind = "im_direct_handoff"

// Only a contiguous prefix whose handoff is covered by existing Core receipts
// and request evidence is durable. A merely accepted conditional wakeup must
// be inspected again after a restart.
type inboxHandoff struct {
	Protocol       int    `json:"protocol"`
	AgentID        string `json:"agent_id"`
	SessionID      string `json:"session_id"`
	BindingID      string `json:"binding_id"`
	ConversationID string `json:"conversation_id"`
	StartSequence  int64  `json:"start_sequence"`
	Sequence       int64  `json:"sequence"`
}

func (p inboxHandoff) matches(b domain.ConversationBinding) bool {
	return p.Protocol == 1 && p.AgentID == b.AgentID && p.SessionID == b.SessionID && p.BindingID == b.ID && p.ConversationID == b.ConversationID && p.StartSequence == b.StartSequence
}

func (s *Service) handoffPosition(ctx context.Context, b domain.ConversationBinding) (int64, error) {
	var position inboxHandoff
	if err := s.Store.Maintenance(ctx, inboxHandoffKind, b.ID, &position); domain.IsMissing(err) {
		return b.StartSequence, nil
	} else if err != nil {
		return 0, err
	}
	if !position.matches(b) {
		return b.StartSequence, nil
	}
	return max(b.StartSequence, position.Sequence), nil
}

func (s *Service) saveHandoffPosition(ctx context.Context, b domain.ConversationBinding, sequence int64) error {
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var current domain.ConversationBinding
		if err := tx.Get(ctx, "bindings", b.ID, &current); err != nil {
			return err
		}
		position := inboxHandoff{Protocol: 1, AgentID: b.AgentID, SessionID: b.SessionID, BindingID: b.ID, ConversationID: b.ConversationID, StartSequence: b.StartSequence, Sequence: sequence}
		if !current.Enabled || !position.matches(current) {
			return domain.Conflict("the inbound binding is no longer active")
		}
		var previous inboxHandoff
		if err := tx.Maintenance(ctx, inboxHandoffKind, b.ID, &previous); err != nil && !domain.IsMissing(err) {
			return err
		}
		if previous.matches(b) {
			position.Sequence = max(position.Sequence, previous.Sequence)
		}
		return tx.SaveMaintenance(ctx, inboxHandoffKind, b.ID, "complete", position)
	})
}
