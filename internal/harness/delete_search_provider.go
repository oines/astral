package harness

import (
	"context"
	"encoding/json"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// DeleteSearchProvider disables search for every Bot selecting this service.
// The service and its references change together; stale Bot writes cannot
// restore the deleted selection after this transaction commits.
func (s *Service) DeleteSearchProvider(ctx context.Context, id string, revision int64) error {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.Delete(ctx, "search", id, revision); err != nil {
			return err
		}
		agents, err := tx.List(ctx, "agents")
		if err != nil {
			return err
		}
		for _, item := range agents {
			var agent domain.Agent
			if err := json.Unmarshal(item.Data, &agent); err != nil {
				return err
			}
			if agent.SearchProviderID != id {
				continue
			}
			agent.SearchProviderID = ""
			if err := tx.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
				return err
			}
		}
		return nil
	})
}
