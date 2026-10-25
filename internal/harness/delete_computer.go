package harness

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// DeleteComputer removes only a revoked Server registration. Historical
// Sessions and FileRefs retain their original identity and cannot silently
// start using a newly enrolled Computer.
func (s *Service) DeleteComputer(ctx context.Context, id string, revision int64) error {
	err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var computer domain.Computer
		if err := tx.Get(ctx, "computers", id, &computer); err != nil {
			if domain.IsMissing(err) {
				return nil
			}
			return err
		}
		if computer.Revision != revision {
			return domain.Conflict("电脑已发生变化，请刷新后重试。")
		}
		if !computer.Revoked {
			return domain.Conflict("请先撤销连接，再删除电脑。")
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
			if !slices.Contains(agent.AllowedComputers, id) && agent.DefaultComputerID != id {
				continue
			}
			agent.AllowedComputers = slices.DeleteFunc(agent.AllowedComputers, func(value string) bool { return value == id })
			if agent.DefaultComputerID == id {
				agent.DefaultComputerID = ""
			}
			if err := tx.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
				return err
			}
		}
		if err := tx.DeleteSecret(ctx, computer.TokenID); err != nil {
			return err
		}
		return tx.Purge(ctx, "computers", id, computer.Revision)
	})
	if err != nil {
		return err
	}
	// This drops Server-side RPC state only. No request is sent to stop the
	// Daemon service or change files, containers, volumes, or running programs.
	s.Hub.Forget(id)
	return nil
}
