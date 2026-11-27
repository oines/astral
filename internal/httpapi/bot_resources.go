package httpapi

import (
	"net/http"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/modelbridge"
)

type BotSelf struct {
	ID                string                 `json:"id"`
	Revision          int64                  `json:"revision"`
	Name              string                 `json:"name"`
	Avatar            string                 `json:"avatar,omitempty"`
	Persona           string                 `json:"persona"`
	DefaultModelID    string                 `json:"default_model_id,omitempty"`
	DefaultReasoning  *core.ReasoningOptions `json:"default_reasoning,omitempty"`
	DefaultComputerID string                 `json:"default_computer_id,omitempty"`
}

type BotSelfPatch struct {
	Revision          int64                  `json:"revision"`
	Name              *string                `json:"name,omitempty"`
	Persona           *string                `json:"persona,omitempty"`
	DefaultModelID    *string                `json:"default_model_id,omitempty"`
	DefaultReasoning  *core.ReasoningOptions `json:"default_reasoning,omitempty"`
	DefaultComputerID *string                `json:"default_computer_id,omitempty"`
}

func (a *API) botResourceRoutes() {
	botRoute[httpcontract.Empty, BotSelf](a, "GET", "/self", "getSelf", "Bot", func(r *http.Request, _ httpcontract.Empty) (BotSelf, error) {
		bot, err := a.currentBot(r)
		return botSelfView(bot), err
	})
	botRoute[BotSelfPatch, BotSelf](a, "PATCH", "/self", "updateSelf", "Bot", a.updateBotSelf)
	botRoute[httpcontract.Empty, []modelbridge.AvailableModel](a, "GET", "/models", "listModels", "Models", func(r *http.Request, _ httpcontract.Empty) ([]modelbridge.AvailableModel, error) {
		return a.Config.Harness.Models.AvailableProfiles(r.Context())
	})
	botRoute[httpcontract.Empty, []domain.ComputerStatus](a, "GET", "/computers", "listComputers", "Computers", a.botComputers)
}

func (a *API) currentBot(r *http.Request) (domain.Agent, error) {
	id := botID(r)
	var bot domain.Agent
	if id == "" {
		return bot, domain.Forbidden("a Bot access token is required")
	}
	if err := a.Config.Harness.Store.Get(r.Context(), "agents", id, &bot); err != nil {
		return bot, err
	}
	if bot.Deletion {
		return bot, domain.Missing("Bot", id)
	}
	return bot, nil
}

func botSelfView(bot domain.Agent) BotSelf {
	return BotSelf{
		ID: bot.ID, Revision: bot.Revision, Name: bot.Name, Avatar: bot.Avatar,
		Persona: bot.Persona, DefaultModelID: bot.DefaultModelID,
		DefaultReasoning: bot.DefaultReasoning, DefaultComputerID: bot.DefaultComputerID,
	}
}

func (a *API) updateBotSelf(r *http.Request, input BotSelfPatch) (BotSelf, error) {
	if input.Revision <= 0 {
		return BotSelf{}, domain.Invalid("revision must be a positive integer")
	}
	if input.Name == nil && input.Persona == nil && input.DefaultModelID == nil && input.DefaultReasoning == nil && input.DefaultComputerID == nil {
		return BotSelf{}, domain.Invalid("at least one Bot setting is required")
	}
	bot, err := a.currentBot(r)
	if err != nil {
		return BotSelf{}, err
	}
	if input.Revision != bot.Revision {
		return BotSelf{}, domain.Conflict("this Bot changed; reload it before saving")
	}
	if input.Name != nil {
		bot.Name = *input.Name
	}
	if input.Persona != nil {
		bot.Persona = *input.Persona
	}
	if input.DefaultModelID != nil && *input.DefaultModelID != bot.DefaultModelID {
		bot.DefaultModelID = *input.DefaultModelID
		bot.DefaultReasoning = nil
	}
	if input.DefaultReasoning != nil {
		bot.DefaultReasoning = input.DefaultReasoning
		if *input.DefaultReasoning == (core.ReasoningOptions{}) {
			bot.DefaultReasoning = nil
		}
	}
	if input.DefaultComputerID != nil {
		bot.DefaultComputerID = *input.DefaultComputerID
	}
	if err := a.Config.Harness.SaveAgent(r.Context(), &bot); err != nil {
		return BotSelf{}, err
	}
	return botSelfView(bot), nil
}

func (a *API) botComputers(r *http.Request, _ httpcontract.Empty) ([]domain.ComputerStatus, error) {
	bot, err := a.currentBot(r)
	if err != nil {
		return nil, err
	}
	h := a.Config.Harness
	result := make([]domain.ComputerStatus, 0, len(bot.AllowedComputers))
	for _, id := range bot.AllowedComputers {
		var computer domain.Computer
		if err := h.Store.Get(r.Context(), "computers", id, &computer); domain.IsMissing(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		if computer.Revoked || computer.Temporary {
			continue
		}
		computer.TokenID = ""
		result = append(result, h.Hub.Status(computer))
	}
	return result, nil
}

func listedBotID(r *http.Request) (string, error) {
	if id := botID(r); id != "" {
		if r.URL.Query().Has("agent_id") {
			return "", domain.Invalid("agent_id is selected by the Bot credential")
		}
		return id, nil
	}
	return r.URL.Query().Get("agent_id"), nil
}
