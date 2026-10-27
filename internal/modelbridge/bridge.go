// Package modelbridge owns immutable provider/profile snapshots and converts
// frozen rollout media for the provider. Adapters use Core messages directly.
package modelbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-core/models"
	"github.com/oines/astral-core/models/auth"
	"github.com/oines/astral-core/models/integration"
	"github.com/oines/astral-core/models/provider"
	"github.com/oines/astral-core/models/provider/anthropic"
	"github.com/oines/astral-core/models/provider/google"
	"github.com/oines/astral-core/models/provider/openai/completions"
	"github.com/oines/astral-core/models/provider/openai/responses"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type RequestValidator func(context.Context) error

type Bridge struct {
	Store        *persistence.Store
	HTTPClient   *http.Client
	ModelCatalog *ModelCatalog
	authMu       sync.Mutex
	authFlows    map[string]*authFlow
	oauthSDK     *auth.Client
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return domain.Invalid("a provider URL must contain a host, without credentials, query or fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	secure := u.Scheme == "https"
	localHTTP := u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())
	if !secure && !localHTTP {
		return domain.Invalid("use HTTPS for remote connections; HTTP is only allowed on loopback")
	}
	return nil
}

func (b *Bridge) SaveProvider(ctx context.Context, p *domain.Provider, key *string, headers *map[string]string) error {
	if strings.TrimSpace(p.Name) == "" {
		return domain.Invalid("provider name is required")
	}
	if err := ValidateURL(p.BaseURL); err != nil {
		return err
	}
	switch models.Dialect(p.Dialect) {
	case models.OpenAI, models.DeepSeek, models.Qwen, models.OpenRouter, models.Compatible, models.Anthropic, models.Google, models.MiMo:
	default:
		return domain.Invalid("unsupported provider dialect")
	}
	if p.Protocol != completions.Protocol && p.Protocol != responses.Protocol && p.Protocol != anthropic.Protocol && p.Protocol != google.Protocol {
		return domain.Invalid("unsupported provider protocol")
	}
	if p.PresetID != "" && FindPreset(p.PresetID) == nil {
		return domain.Invalid("unknown provider preset")
	}
	if _, err := models.ResolveReasoningControls(p.Protocol, models.Config{Dialect: models.Dialect(p.Dialect), ReasoningFormat: p.ReasoningFormat, ReasoningMapping: p.ReasoningMapping}); err != nil {
		return domain.Invalid(err.Error())
	}
	if preset := FindPreset(p.PresetID); preset != nil && preset.Auth == "oauth" {
		if p.BaseURL != preset.BaseURL {
			return domain.Invalid("the authorization provider address is fixed")
		}
		if key != nil {
			return domain.Invalid("use the provider sign-in flow")
		}
	}
	if p.ID == "" {
		p.ID = domain.ID("provider")
	}
	return b.Store.Update(ctx, func(tx *persistence.Tx) error {
		var old domain.Provider
		if p.Revision > 0 {
			if err := tx.Get(ctx, "providers", p.ID, &old); err != nil {
				return err
			}
			if old.PresetID != p.PresetID {
				return domain.Invalid("create another provider to change its preset")
			}
		}
		// Credential references are server-owned, even for a complete PUT.
		p.CredentialID, p.HeadersCredentialID, p.HeaderNames = old.CredentialID, old.HeadersCredentialID, old.HeaderNames
		var err error
		if key != nil {
			p.CredentialID, err = tx.PutSecret(ctx, *key)
			if err != nil {
				return err
			}
		}
		if headers != nil {
			for name, value := range *headers {
				if name == "" || strings.ContainsAny(name+value, "\r\n") {
					return domain.Invalid("invalid provider header")
				}
			}
			raw, _ := json.Marshal(headers)
			p.HeadersCredentialID, err = tx.PutSecret(ctx, string(raw))
			if err != nil {
				return err
			}
			p.HeaderNames = []string{}
			for name := range *headers {
				p.HeaderNames = append(p.HeaderNames, name)
			}
		}
		oldReasoning, _ := json.Marshal([]any{old.ReasoningFormat, old.ReasoningMapping})
		newReasoning, _ := json.Marshal([]any{p.ReasoningFormat, p.ReasoningMapping})
		connectionUnchanged := old.Revision > 0 && old.Protocol == p.Protocol && old.Dialect == p.Dialect && old.BaseURL == p.BaseURL && old.CredentialID == p.CredentialID && old.HeadersCredentialID == p.HeadersCredentialID
		// The scope is server-owned. Metadata edits must not invalidate signed
		// model history when the user selects the updated profile revision.
		p.ReplayRevision = p.Revision + 1
		if connectionUnchanged {
			p.ReplayRevision = old.ReplayRevision
			if p.ReplayRevision == 0 {
				p.ReplayRevision = old.Revision
			}
		}
		if err := tx.Save(ctx, "providers", &p.Meta, p); err != nil {
			return err
		}
		routeChanged := old.Revision > 0 && (old.Protocol != p.Protocol || old.Dialect != p.Dialect || old.BaseURL != p.BaseURL || string(oldReasoning) != string(newReasoning))
		verificationUnchanged := connectionUnchanged && string(oldReasoning) == string(newReasoning)
		if routeChanged || verificationUnchanged {
			records, err := tx.List(ctx, "models")
			if err != nil {
				return err
			}
			for _, record := range records {
				var profile domain.ModelProfile
				if err := json.Unmarshal(record.Data, &profile); err != nil {
					return err
				}
				if profile.ProviderID != p.ID {
					continue
				}
				changed := false
				if routeChanged {
					before, _ := json.Marshal([]any{profile.RecommendationTarget, profile.Recommendations})
					if profile.RecommendationTarget == nil {
						target, err := integration.RecommendationTargetFor(sdkProvider(old), sdkModel(profile))
						if err != nil {
							return err
						}
						profile.RecommendationTarget = &target
					}
					if err := rebaseProfileRecommendations(&profile, *p); err != nil {
						return err
					}
					after, _ := json.Marshal([]any{profile.RecommendationTarget, profile.Recommendations})
					changed = string(before) != string(after)
					if changed {
						profile.Verification = nil
					}
				} else if check := profile.Verification; verificationUnchanged && check != nil && check.ProviderRevision == old.Revision && check.ProfileRevision == profile.Revision {
					check.ProviderRevision = p.Revision
					check.ProfileRevision = profile.Revision + 1
					changed = true
				}
				if changed {
					if err := tx.Save(ctx, "models", &profile.Meta, &profile); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

func (b *Bridge) SaveProfile(ctx context.Context, p *domain.ModelProfile) error {
	return b.saveProfile(ctx, p, true)
}

func (b *Bridge) saveProfile(ctx context.Context, p *domain.ModelProfile, userModified bool) error {
	clearProfilePresentation(p)
	p.UserModified = userModified
	if p.ProviderID == "" || strings.TrimSpace(p.ModelID) == "" || strings.TrimSpace(p.Name) == "" {
		return domain.Invalid("provider, model ID and display name are required")
	}
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", p.ProviderID, &provider); err != nil {
		return err
	}
	if err := rebaseProfileRecommendations(p, provider); err != nil {
		return err
	}
	if values, _, err := p.Effective(); err != nil {
		return err
	} else if values.ContextWindow <= 0 || values.MaxOutputTokens <= 0 {
		return domain.Invalid("model token limits must be positive")
	} else if values.Defaults.ToolChoice.Mode == "none" {
		return domain.Invalid("Agent model profiles must allow tool calling")
	}
	if p.ID == "" {
		p.ID = domain.ID("model")
	}
	p.Verification = nil
	if p.Revision > 0 {
		var old domain.ModelProfile
		if err := b.Store.Get(ctx, "models", p.ID, &old); err != nil {
			return err
		}
		oldConfig, _ := json.Marshal([]any{old.ProviderID, old.ModelID, old.Protocol, old.Dialect, old.Recommendations, old.Overrides})
		newConfig, _ := json.Marshal([]any{p.ProviderID, p.ModelID, p.Protocol, p.Dialect, p.Recommendations, p.Overrides})
		if string(oldConfig) == string(newConfig) && old.Verification != nil {
			copy := *old.Verification
			copy.ProfileRevision = p.Revision + 1
			p.Verification = &copy
		}
	}
	// Constructing the adapter also checks native compatibility options.
	model, err := b.construct(ctx, *p, provider)
	if err != nil {
		return err
	}
	values, _, _ := p.Effective()
	if err := core.ValidateRequest(ctx, model, core.Request{ModelID: model.Spec().ID, ModelRevision: model.Spec().Revision,
		Messages: []core.Message{core.User("Validate model defaults.")}, MaxOutputTokens: values.MaxOutputTokens, Options: values.Defaults}); err != nil {
		return domain.Invalid(err.Error())
	}
	return b.Store.Update(ctx, func(tx *persistence.Tx) error {
		var current domain.Provider
		if err := tx.Get(ctx, "providers", provider.ID, &current); err != nil {
			return err
		}
		if current.Revision != provider.Revision {
			return domain.Conflict("provider changed while saving the model; reload it before saving")
		}
		return tx.Save(ctx, "models", &p.Meta, p)
	})
}

func Reference(p domain.ModelProfile, provider domain.Provider) string {
	return p.ID + "~" + strconv.FormatInt(p.Revision, 10) + "~" + strconv.FormatInt(provider.Revision, 10)
}

func (b *Bridge) Selection(ctx context.Context, id string) (string, core.GenerationOptions, error) {
	var p domain.ModelProfile
	if err := b.Store.Get(ctx, "models", id, &p); err != nil {
		return "", core.GenerationOptions{}, err
	}
	if p.Enabled != nil && !*p.Enabled {
		return "", core.GenerationOptions{}, domain.Conflict("this model is disabled")
	}
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", p.ProviderID, &provider); err != nil {
		return "", core.GenerationOptions{}, err
	}
	if provider.Enabled != nil && !*provider.Enabled {
		return "", core.GenerationOptions{}, domain.Conflict("this provider is disabled")
	}
	v, _, err := p.Effective()
	if err != nil {
		return "", v.Defaults, err
	}
	return Reference(p, provider), v.Defaults, nil
}

type adapter interface {
	core.Model
	EstimateTokens(context.Context, core.Request) (int, error)
}

// ValidateOptions checks the actual protocol serializer without sending a
// request, so a Session effort choice cannot silently become another value.
func (b *Bridge) ValidateOptions(ctx context.Context, ref string, options core.GenerationOptions) error {
	if err := options.Validate(); err != nil {
		return domain.Invalid(err.Error())
	}
	model, err := b.Resolve(ctx, ref, nil)
	if err != nil {
		return err
	}
	err = core.ValidateRequest(ctx, model, core.Request{ModelID: ref, ModelRevision: ref,
		Messages:        []core.Message{core.User("Validate generation settings.")},
		MaxOutputTokens: model.Spec().MaxOutputTokens, Options: options})
	return err
}

func sdkProvider(p domain.Provider) integration.ProviderConfig {
	return integration.ProviderConfig{PresetID: p.PresetID, ConnectionScope: p.ID, Protocol: p.Protocol, Dialect: p.Dialect, BaseURL: p.BaseURL, ReasoningFormat: p.ReasoningFormat, ReasoningMapping: p.ReasoningMapping}
}
func sdkModel(p domain.ModelProfile) integration.ModelConfig {
	return integration.ModelConfig{ModelID: p.ModelID, Protocol: p.Protocol, Dialect: p.Dialect, Recommendations: p.Recommendations, RecommendationTarget: p.RecommendationTarget, Overrides: p.Overrides}
}
func (b *Bridge) adapterConfig(ctx context.Context, p domain.ModelProfile, connection domain.Provider) (models.Config, string, error) {
	ref := Reference(p, connection)
	resolved, err := integration.Resolve(sdkProvider(connection), sdkModel(p), core.ModelSpec{ID: ref, Revision: ref})
	if err != nil {
		return models.Config{}, "", err
	}
	key, err := b.Store.Secret(ctx, connection.CredentialID)
	if err != nil {
		return models.Config{}, "", err
	}
	headers, err := b.savedHeaders(ctx, connection.HeadersCredentialID)
	if err != nil {
		return models.Config{}, "", err
	}
	config := resolved.Config
	config.APIKey, config.Headers, config.HTTPClient = key, integration.Headers(sdkProvider(connection), key, headers), b.HTTPClient
	config.Headers["User-Agent"] = "Astral/0.1.0"
	config.ReplayScope = connection.ID + ":" + strconv.FormatInt(connection.ReplayRevision, 10)
	if preset := integration.FindPreset(connection.PresetID); preset != nil && preset.Auth == "oauth" {
		config.APIKey = ""
		config.HTTPClient = b.oauthClient(connection)
	}
	return config, resolved.Protocol, nil
}
func (b *Bridge) savedHeaders(ctx context.Context, id string) (map[string]string, error) {
	text, err := b.Store.Secret(ctx, id)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	if text != "" {
		if err = json.Unmarshal([]byte(text), &headers); err != nil {
			return nil, fmt.Errorf("invalid saved provider headers: %w", err)
		}
	}
	return headers, nil
}
func (b *Bridge) construct(ctx context.Context, p domain.ModelProfile, connection domain.Provider) (adapter, error) {
	config, protocol, err := b.adapterConfig(ctx, p, connection)
	if err != nil {
		return nil, err
	}
	return provider.New(protocol, config)
}

func (b *Bridge) snapshot(ctx context.Context, ref string) (domain.ModelProfile, domain.Provider, error) {
	var p domain.ModelProfile
	var provider domain.Provider
	parts := strings.Split(ref, "~")
	if len(parts) != 3 {
		return p, provider, domain.Invalid("invalid versioned model reference")
	}
	pv, e1 := strconv.ParseInt(parts[1], 10, 64)
	cv, e2 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || pv < 1 || cv < 1 {
		return p, provider, domain.Invalid("invalid model revision")
	}
	if err := b.Store.Version(ctx, "models", parts[0], pv, &p); err != nil {
		return p, provider, err
	}
	if err := b.Store.Version(ctx, "providers", p.ProviderID, cv, &provider); err != nil {
		return p, provider, err
	}
	return p, provider, nil
}

// ModelDescription is the effective, versioned model state shown to the model.
// Capability uncertainty is preserved instead of inferred from a model name.
type ModelDescription struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	ContextWindow int    `json:"context_window"`
	Vision        string `json:"vision"`
}

func (b *Bridge) DescribeModel(ctx context.Context, ref string) (ModelDescription, error) {
	p, _, err := b.snapshot(ctx, ref)
	if err != nil {
		return ModelDescription{}, err
	}
	values, _, err := p.Effective()
	if err != nil {
		return ModelDescription{}, err
	}
	vision := values.Images
	if vision == "" {
		vision = "unknown"
	}
	return ModelDescription{Name: p.Name, ID: p.ModelID, ContextWindow: values.ContextWindow, Vision: vision}, nil
}

// Resolve constructs an immutable resource version for explicit inspections and direct calls.
func (b *Bridge) Resolve(ctx context.Context, ref string, validate RequestValidator) (core.Model, error) {
	p, connection, err := b.snapshot(ctx, ref)
	if err != nil {
		return nil, err
	}
	model, err := b.construct(ctx, p, connection)
	if err != nil {
		return nil, err
	}
	return &boundModel{adapter: model, validate: validate, presetID: connection.PresetID, sessionID: sessionID(ctx)}, nil
}

type frozenModel struct {
	Target              core.ModelSnapshot `json:"target"`
	ProviderID          string             `json:"provider_id"`
	PresetID            string             `json:"preset_id,omitempty"`
	CredentialID        string             `json:"credential_id,omitempty"`
	HeadersCredentialID string             `json:"headers_credential_id,omitempty"`
}

// ResolveSelection reads current resource configuration only for a new logical request.
func (b *Bridge) ResolveSelection(ctx context.Context, selection core.ModelSelection) (core.ResolvedModel, error) {
	if err := selection.Validate(); err != nil {
		return core.ResolvedModel{}, err
	}
	var p domain.ModelProfile
	if err := b.Store.Get(ctx, "models", selection.ID, &p); err != nil {
		return core.ResolvedModel{}, err
	}
	if p.Enabled != nil && !*p.Enabled {
		return core.ResolvedModel{}, domain.Conflict("this model is disabled")
	}
	var connection domain.Provider
	if err := b.Store.Get(ctx, "providers", p.ProviderID, &connection); err != nil {
		return core.ResolvedModel{}, err
	}
	if connection.Enabled != nil && !*connection.Enabled {
		return core.ResolvedModel{}, domain.Conflict("this provider is disabled")
	}
	config, protocol, err := b.adapterConfig(ctx, p, connection)
	if err != nil {
		return core.ResolvedModel{}, err
	}
	values, _, err := p.Effective()
	if err != nil {
		return core.ResolvedModel{}, err
	}
	options, err := integration.Options(values.Defaults, selection.Options, selection.Reasoning)
	if err != nil {
		return core.ResolvedModel{}, err
	}
	if _, err = models.EncodeReasoning(protocol, config, options.Reasoning, config.Spec.MaxOutputTokens); err != nil {
		return core.ResolvedModel{}, err
	}
	model, err := provider.New(protocol, config)
	if err != nil {
		return core.ResolvedModel{}, err
	}
	target, err := provider.Freeze(protocol, config)
	if err != nil {
		return core.ResolvedModel{}, err
	}
	snapshot, err := json.Marshal(frozenModel{Target: target, ProviderID: connection.ID, PresetID: connection.PresetID, CredentialID: connection.CredentialID, HeadersCredentialID: connection.HeadersCredentialID})
	if err != nil {
		return core.ResolvedModel{}, err
	}
	return core.ResolvedModel{Model: &boundModel{adapter: model, presetID: connection.PresetID, sessionID: sessionID(ctx)}, Options: options, Snapshot: snapshot}, nil
}

// RestoreModel never reads live profiles, directory metadata or provider defaults.
func (b *Bridge) RestoreModel(ctx context.Context, snapshot core.ModelSnapshot) (core.Model, error) {
	var frozen frozenModel
	if err := json.Unmarshal(snapshot.Data, &frozen); err != nil {
		return nil, domain.Invalid("invalid frozen model target")
	}
	if frozen.Target.Spec != snapshot.Spec {
		return nil, domain.Invalid("frozen model specification mismatch")
	}
	key, err := b.Store.Secret(ctx, frozen.CredentialID)
	if err != nil {
		return nil, err
	}
	headers, err := b.savedHeaders(ctx, frozen.HeadersCredentialID)
	if err != nil {
		return nil, err
	}
	headers = integration.Headers(integration.ProviderConfig{PresetID: frozen.PresetID}, key, headers)
	client := b.HTTPClient
	if preset := integration.FindPreset(frozen.PresetID); preset != nil && preset.Auth == "oauth" {
		key = ""
		client = b.oauthClient(domain.Provider{Meta: domain.Meta{ID: frozen.ProviderID}, PresetID: frozen.PresetID, CredentialID: frozen.CredentialID})
	}
	model, err := provider.Restore(frozen.Target, provider.Credentials{APIKey: key, Headers: headers, HTTPClient: client})
	if err != nil {
		return nil, err
	}
	return &boundModel{adapter: model, presetID: frozen.PresetID, sessionID: sessionID(ctx)}, nil
}

type boundModel struct {
	adapter
	validate  RequestValidator
	sessionID string
	presetID  string
}

func (m *boundModel) PrepareRequest(ctx context.Context, req core.Request) (core.Request, error) {
	if m.validate != nil {
		if err := m.validate(ctx); err != nil {
			return core.Request{}, err
		}
	}
	return core.PrepareRequest(ctx, m.adapter, req)
}
func (m *boundModel) Stream(ctx context.Context, req core.Request, emit func(core.ModelEvent)) (core.Reply, error) {
	if m.sessionID != "" {
		ctx = models.WithRequestHeaders(ctx, integration.SessionHeaders(m.presetID, m.sessionID))
	}
	reply, err := m.adapter.Stream(ctx, req, emit)
	if err != nil {
		err = safeProviderError(err)
		if reply.Message.Role != "" {
			reply.Message.ErrorMessage = err.Error()
		}
	}
	return reply, err
}

func (m *boundModel) RecoverRequest(ctx context.Context, req core.Request, cause error) (core.Request, bool, error) {
	if recoverer, ok := m.adapter.(core.ModelRequestRecoverer); ok {
		return recoverer.RecoverRequest(ctx, req, cause)
	}
	return core.Request{}, false, nil
}
func (m *boundModel) ValidateRequest(ctx context.Context, req core.Request) error {
	return core.ValidateRequest(ctx, m.adapter, req)
}
func (m *boundModel) EstimateTokens(ctx context.Context, req core.Request) (int, error) {
	return m.adapter.EstimateTokens(ctx, req)
}

func EstimateTokens(ctx context.Context, model core.Model, req core.Request) (int, error) {
	m, ok := model.(interface {
		EstimateTokens(context.Context, core.Request) (int, error)
	})
	if !ok {
		return 0, domain.Invalid("model has no token estimator")
	}
	return m.EstimateTokens(ctx, req)
}

// Verify makes two real requests, including a native tool result replay. It
// executes no OS tool and only persists a capability verdict, not test history.
func (b *Bridge) Verify(ctx context.Context, id string) (domain.ModelVerification, error) {
	var p domain.ModelProfile
	if err := b.Store.Get(ctx, "models", id, &p); err != nil {
		return domain.ModelVerification{}, err
	}
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", p.ProviderID, &provider); err != nil {
		return domain.ModelVerification{}, err
	}
	result := domain.ModelVerification{TestedAt: time.Now().UTC(), ProviderRevision: provider.Revision, ProfileRevision: p.Revision + 1, ToolCalling: "unknown", ToolReplay: "unknown"}
	ctx = models.WithRequestHeaders(ctx, sessionHeaders(provider.PresetID, "verify-"+domain.ID("session")))
	model, err := b.construct(ctx, p, provider)
	if err != nil {
		return result, err
	}
	values, _, _ := p.Effective()
	options := values.Defaults
	// DeepSeek thinking mode rejects required/named tool choices. Verify actual
	// tool behavior with auto selection instead of changing the reasoning mode.
	options.ToolChoice = core.ToolChoice{Mode: "auto"}
	req := core.Request{ModelID: model.Spec().ID, ModelRevision: model.Spec().Revision, SystemPrompt: "Call astral_verify exactly once with value ping. After its result, answer pong.",
		Messages: []core.Message{core.User("Verify the tool now.")}, MaxOutputTokens: min(4096, values.MaxOutputTokens), Options: options,
		Tools: []core.ToolDefinition{{Name: "astral_verify", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string","enum":["ping"]}},"required":["value"],"additionalProperties":false}`)}}}
	reply, testErr := model.Stream(ctx, req, func(core.ModelEvent) {})
	if testErr == nil {
		calls := reply.Message.ToolCalls()
		if reply.Message.FinishReason != core.FinishToolCalls || len(calls) != 1 || calls[0].Name != "astral_verify" {
			testErr = fmt.Errorf("model did not produce the required verification tool call")
		} else {
			var arguments struct {
				Value string `json:"value"`
			}
			if json.Unmarshal(calls[0].Arguments, &arguments) != nil || arguments.Value != "ping" {
				testErr = fmt.Errorf("model produced invalid verification arguments")
			} else {
				result.ToolCalling = "supported"
				req.Messages = append(req.Messages, reply.Message, core.Message{Role: core.RoleTool, ToolCallID: calls[0].ID, Name: calls[0].Name, Content: core.Text("pong")})
				req.Options.ToolChoice = core.ToolChoice{Mode: "none"}
				reply, testErr = model.Stream(ctx, req, func(core.ModelEvent) {})
				if testErr == nil && reply.Message.FinishReason == core.FinishStop && len(reply.Message.ToolCalls()) == 0 {
					result.ToolReplay = "supported"
				} else if testErr == nil {
					testErr = fmt.Errorf("model failed to complete the tool result replay")
				}
			}
		}
	}
	if testErr != nil {
		// Provider errors can echo credentials or request bodies. Persist only a
		// bounded verdict; the returned wrapped error remains at the HTTP boundary.
		result.Message = "Verification failed. Check this provider's credentials, protocol and model settings."
	} else {
		result.Message = "Tool calling and tool result replay passed."
	}
	p.Verification = &result
	if err = b.Store.Save(ctx, "models", &p.Meta, &p); err != nil {
		return result, err
	}
	return result, testErr
}
