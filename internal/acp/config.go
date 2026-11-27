package acp

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const (
	modelConfigID           = "model"
	thoughtConfigID         = "thought_level"
	profileDefaultReasoning = "inherit"
)

type sessionConfigOption struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Category     string                `json:"category"`
	Type         string                `json:"type"`
	CurrentValue string                `json:"currentValue"`
	Options      []sessionConfigChoice `json:"options"`
}

type sessionConfigChoice struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (c *connection) readModelConfig(ctx context.Context, id string) (core.ModelSelection, []domain.ModelProfile, error) {
	h := c.service.harness
	session, err := h.Core(ctx, id)
	if err != nil {
		return core.ModelSelection{}, nil, err
	}
	profiles, err := h.Models.ListProfiles(ctx)
	if err != nil {
		return core.ModelSelection{}, nil, err
	}
	return session.Configuration().Selection, profiles, nil
}

func (c *connection) setConfigOption(request rpcRequest) *rpcError {
	var params struct {
		SessionID string  `json:"sessionId"`
		ConfigID  string  `json:"configId"`
		Type      string  `json:"type"`
		Value     *string `json:"value"`
	}
	if err := decodeParams(request.Params, &params); err != nil {
		return err
	}
	if params.SessionID == "" || params.Value == nil || (params.Type != "" && params.Type != "select") {
		return invalidParams("Expected a session ID and a select option value.")
	}
	if params.ConfigID != modelConfigID && params.ConfigID != thoughtConfigID {
		return invalidParams("Unknown configuration option.")
	}
	loaded := c.session(params.SessionID)
	if loaded == nil {
		return toRPCError(domain.Missing("Session", params.SessionID))
	}
	loaded.mu.Lock()
	ready := loaded.ready && !loaded.closing
	loaded.mu.Unlock()
	if !ready || loaded.ctx.Err() != nil {
		return toRPCError(domain.Missing("Session", params.SessionID))
	}
	if _, err := c.allowedSession(loaded.ctx, loaded.id); err != nil {
		return toRPCError(err)
	}
	selection, profiles, err := c.readModelConfig(loaded.ctx, loaded.id)
	if err != nil {
		return toRPCError(err)
	}
	options, reasoning := selectOptions(selection, profiles)
	index := slices.IndexFunc(options, func(option sessionConfigOption) bool { return option.ID == params.ConfigID })
	if index < 0 || !slices.ContainsFunc(options[index].Options, func(choice sessionConfigChoice) bool { return choice.Value == *params.Value }) {
		return invalidParams("This value is not available for the current configuration.")
	}
	if *params.Value != options[index].CurrentValue {
		h := c.service.harness
		ctx := persistence.WithSessionScope(loaded.ctx, c.botID, "")
		switch params.ConfigID {
		case modelConfigID:
			err = h.SelectModel(ctx, loaded.id, *params.Value)
		case thoughtConfigID:
			err = h.SelectModelReasoning(ctx, loaded.id, selection.ID, reasoning[*params.Value])
		}
		if err != nil {
			return toRPCError(err)
		}
		session, err := h.Core(loaded.ctx, loaded.id)
		if err != nil {
			return toRPCError(err)
		}
		options, _ = selectOptions(session.Configuration().Selection, profiles)
	}
	c.reply(request.ID, struct {
		ConfigOptions []sessionConfigOption `json:"configOptions"`
	}{options}, false)
	return nil
}

func currentProfile(id string, profiles []domain.ModelProfile) domain.ModelProfile {
	for _, profile := range profiles {
		if profile.ID == id {
			return profile
		}
	}
	return domain.ModelProfile{}
}

func selectOptions(selection core.ModelSelection, profiles []domain.ModelProfile) ([]sessionConfigOption, map[string]*core.ReasoningOptions) {
	options := []sessionConfigOption{}
	current := currentProfile(selection.ID, profiles)
	model := sessionConfigOption{ID: modelConfigID, Name: "Model", Category: modelConfigID, Type: "select", CurrentValue: selection.ID}
	for _, profile := range profiles {
		if profile.Available != nil && *profile.Available {
			model.Options = append(model.Options, sessionConfigChoice{Value: profile.ID, Name: profile.Name, Description: profile.ProviderName})
		}
	}
	if len(model.Options) != 0 {
		if current.Available == nil || !*current.Available {
			// Keep the saved selection visible so the client can offer a recovery model.
			name := current.Name
			switch {
			case selection.ID == "":
				name = "No model selected"
			case name == "":
				name = "Unavailable model"
			default:
				name += " (unavailable)"
			}
			model.Options = append(model.Options, sessionConfigChoice{Value: selection.ID, Name: name})
		}
		options = append(options, model)
	}
	thought, reasoning := thoughtOptions(selection, current)
	if len(thought.Options) != 0 {
		options = append(options, thought)
	}
	return options, reasoning
}

func thoughtOptions(selection core.ModelSelection, profile domain.ModelProfile) (sessionConfigOption, map[string]*core.ReasoningOptions) {
	var option sessionConfigOption
	controls := profile.ReasoningControls
	if profile.Available == nil || !*profile.Available || controls == nil {
		return option, nil
	}
	values, _, err := profile.Effective()
	if err != nil {
		return option, nil
	}
	defaults := values.Defaults.Reasoning
	if controls.Budget == nil && selection.Reasoning == nil && defaults == (core.ReasoningOptions{}) &&
		!slices.ContainsFunc(controls.Options, func(choice models.ReasoningOption) bool { return choice.ID != "interface-default" }) {
		return option, nil
	}

	option = sessionConfigOption{ID: thoughtConfigID, Name: "Thinking", Category: thoughtConfigID, Type: "select", CurrentValue: profileDefaultReasoning,
		Options: []sessionConfigChoice{{Value: profileDefaultReasoning, Name: "Model default"}},
	}
	choices := map[string]*core.ReasoningOptions{profileDefaultReasoning: nil}
	for _, choice := range controls.Options {
		name := strings.TrimPrefix(choice.ID, "effort:")
		switch choice.ID {
		case "interface-default":
			name = "Provider default"
		case "on":
			name = "On"
		case "off":
			name = "Off"
		}
		option.Options = append(option.Options, sessionConfigChoice{Value: choice.ID, Name: name})
		choices[choice.ID] = &choice.Value
		if reflect.DeepEqual(selection.Reasoning, &choice.Value) {
			option.CurrentValue = choice.ID
		}
	}
	for _, tokens := range reasoningBudgets(controls.Budget, selection.Reasoning, defaults.BudgetTokens) {
		id := "budget:" + strconv.Itoa(tokens)
		choice := &core.ReasoningOptions{BudgetTokens: &tokens}
		option.Options = append(option.Options, sessionConfigChoice{Value: id, Name: fmt.Sprintf("%d tokens", tokens)})
		choices[id] = choice
		if reflect.DeepEqual(selection.Reasoning, choice) {
			option.CurrentValue = id
		}
	}
	if selection.Reasoning != nil && option.CurrentValue == profileDefaultReasoning {
		option.CurrentValue = "unavailable"
		option.Options = append(option.Options, sessionConfigChoice{Value: option.CurrentValue, Name: "Unavailable setting"})
	}
	return option, choices
}

func reasoningBudgets(bounds *models.ReasoningBudgetControl, selected *core.ReasoningOptions, defaultBudget *int) []int {
	if bounds == nil {
		return nil
	}
	candidates := []int{1024, 4096, 16384}
	for _, budget := range []*int{bounds.Min, bounds.Max, defaultBudget} {
		if budget != nil {
			candidates = append(candidates, *budget)
		}
	}
	if selected != nil && selected.Enabled == nil && selected.Effort == "" && selected.BudgetTokens != nil {
		candidates = append(candidates, *selected.BudgetTokens)
	}
	result := candidates[:0]
	for _, budget := range candidates {
		if budget >= 0 && (bounds.Min == nil || budget >= *bounds.Min) && (bounds.Max == nil || budget <= *bounds.Max) {
			result = append(result, budget)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}
