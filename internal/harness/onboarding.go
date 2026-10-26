package harness

import (
	"context"
	"slices"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// Onboarding belongs to the installation, not a browser or the Bot's name.
// Keeping this record also prevents deleted or renamed Bots being recreated.
type Onboarding struct {
	domain.Meta
	AgentID   string `json:"agent_id,omitempty"`
	Completed bool   `json:"completed"`
}

type OnboardingInput struct {
	ModelID    string `json:"model_id"`
	ComputerID string `json:"computer_id"`
}

func (s *Service) InitializeOnboarding(ctx context.Context) error {
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var state Onboarding
		if err := tx.Get(ctx, "onboarding", "owner", &state); err == nil {
			return nil
		} else if !domain.IsMissing(err) {
			return err
		}
		agents, err := tx.List(ctx, "agents")
		if err != nil {
			return err
		}
		state = Onboarding{Meta: domain.Meta{ID: "owner"}, Completed: len(agents) > 0}
		if len(agents) == 0 {
			agent := domain.Agent{Meta: domain.Meta{ID: domain.ID("agent")}, Name: "Astral", AllowedComputers: []string{}}
			if err = tx.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
				return err
			}
			state.AgentID = agent.ID
		}
		return tx.Save(ctx, "onboarding", &state.Meta, &state)
	})
}

func (s *Service) Onboarding(ctx context.Context) (Onboarding, error) {
	var state Onboarding
	err := s.Store.Get(ctx, "onboarding", "owner", &state)
	if domain.IsMissing(err) {
		// Services embedded without the platform initializer retain their setup.
		return Onboarding{Completed: true}, nil
	}
	return state, err
}

func (s *Service) CompleteOnboarding(ctx context.Context, input OnboardingInput) (domain.Agent, error) {
	var agent domain.Agent
	if input.ModelID == "" || input.ComputerID == "" {
		return agent, domain.Invalid("choose both a model and a connected Computer")
	}
	err := s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var state Onboarding
		if err := tx.Get(ctx, "onboarding", "owner", &state); err != nil {
			return err
		}
		if state.AgentID == "" {
			return domain.Conflict("this installation is already set up")
		}
		if err := tx.Get(ctx, "agents", state.AgentID, &agent); err != nil {
			return err
		}
		if agent.Deletion {
			return domain.Missing("Bot", agent.ID)
		}
		if state.Completed {
			if agent.DefaultModelID == input.ModelID && agent.DefaultComputerID == input.ComputerID {
				return nil // A lost acknowledgement can be retried without rewriting.
			}
			return domain.Conflict("setup is complete; change this Bot in Settings")
		}
		var profile domain.ModelProfile
		if err := tx.Get(ctx, "models", input.ModelID, &profile); err != nil {
			return err
		}
		var provider domain.Provider
		if err := tx.Get(ctx, "providers", profile.ProviderID, &provider); err != nil {
			return err
		}
		if profile.Enabled != nil && !*profile.Enabled || provider.Enabled != nil && !*provider.Enabled {
			return domain.Conflict("choose an enabled model and provider")
		}
		if _, _, err := profile.Effective(); err != nil {
			return err
		}
		var computer domain.Computer
		if err := tx.Get(ctx, "computers", input.ComputerID, &computer); err != nil {
			return err
		}
		if computer.Revoked || !s.Hub.Status(computer).Online {
			return domain.Conflict("the selected Computer must be connected to finish setup")
		}
		agent.DefaultModelID, agent.DefaultComputerID = profile.ID, computer.ID
		if !slices.Contains(agent.AllowedComputers, computer.ID) {
			agent.AllowedComputers = append(agent.AllowedComputers, computer.ID)
		}
		if err := tx.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
			return err
		}
		state.Completed = true
		return tx.Save(ctx, "onboarding", &state.Meta, &state)
	})
	return agent, err
}
