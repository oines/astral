// Package modelbridge owns immutable provider/profile snapshots and converts
// frozen rollout media for the provider. Adapters use Core messages directly.
package modelbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-models"
	"github.com/oines/astral-models/provider/anthropic"
	"github.com/oines/astral-models/provider/google"
	"github.com/oines/astral-models/provider/openai/completions"
	"github.com/oines/astral-models/provider/openai/responses"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type RequestValidator func(context.Context) error

type Bridge struct {
	Store      *persistence.Store
	HTTPClient *http.Client
	authMu     sync.Mutex
	authFlows  map[string]*authFlow
	refreshMu  sync.Mutex
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
	if p.Protocol == responses.Protocol && p.Dialect != string(models.OpenAI) && p.Dialect != string(models.DeepSeek) && p.Dialect != string(models.Compatible) && p.Dialect != string(models.MiMo) {
		return domain.Invalid("this provider does not have a supported Responses adapter")
	}
	if p.PresetID != "" && FindPreset(p.PresetID) == nil {
		return domain.Invalid("unknown provider preset")
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
		// Labels and enable switches do not change the tested API connection.
		if connectionUnchanged {
			records, err := tx.List(ctx, "models")
			if err != nil {
				return err
			}
			for _, record := range records {
				var profile domain.ModelProfile
				if err := json.Unmarshal(record.Data, &profile); err != nil {
					return err
				}
				check := profile.Verification
				if profile.ProviderID != p.ID || check == nil || check.ProviderRevision != old.Revision || check.ProfileRevision != profile.Revision {
					continue
				}
				check.ProviderRevision = p.Revision
				check.ProfileRevision = profile.Revision + 1
				if err := tx.Save(ctx, "models", &profile.Meta, &profile); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (b *Bridge) SaveProfile(ctx context.Context, p *domain.ModelProfile) error {
	if p.ProviderID == "" || strings.TrimSpace(p.ModelID) == "" || strings.TrimSpace(p.Name) == "" {
		return domain.Invalid("provider, model ID and display name are required")
	}
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", p.ProviderID, &provider); err != nil {
		return err
	}
	if values, _, err := p.Effective(); err != nil {
		return err
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
	if _, err := b.construct(ctx, *p, provider); err != nil {
		return err
	}
	return b.Store.Save(ctx, "models", &p.Meta, p)
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
		MaxOutputTokens: min(256, model.Spec().MaxOutputTokens), Options: options})
	return err
}

func (b *Bridge) construct(ctx context.Context, p domain.ModelProfile, provider domain.Provider) (adapter, error) {
	v, _, err := p.Effective()
	if err != nil {
		return nil, err
	}
	key, err := b.Store.Secret(ctx, provider.CredentialID)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{}
	headerJSON, err := b.Store.Secret(ctx, provider.HeadersCredentialID)
	if err != nil {
		return nil, err
	}
	if headerJSON != "" {
		if err = json.Unmarshal([]byte(headerJSON), &headers); err != nil {
			return nil, fmt.Errorf("invalid saved provider headers: %w", err)
		}
	}
	ref := Reference(p, provider)
	protocol, dialect, baseURL, err := modelRoute(p, provider)
	if err != nil {
		return nil, err
	}
	headers["User-Agent"] = "Astral/0.1.0"
	if strings.HasPrefix(provider.PresetID, "opencode") {
		// OpenCode's native Messages/Gemini gateways authenticate with Bearer.
		headers["Authorization"] = "Bearer " + key
	}
	replayRevision := provider.ReplayRevision
	if replayRevision == 0 {
		replayRevision = provider.Revision
	}
	config := models.Config{
		Spec:  core.ModelSpec{ID: ref, Revision: ref, ContextWindow: v.ContextWindow, MaxInputTokens: v.MaxInputTokens, MaxOutputTokens: v.MaxOutputTokens},
		Model: p.ModelID, BaseURL: baseURL, APIKey: key, Headers: headers, HTTPClient: b.HTTPClient,
		Dialect: models.Dialect(dialect), ReplayScope: provider.ID + ":" + strconv.FormatInt(replayRevision, 10),
		Streaming: v.Streaming, ReasoningReplay: v.ReasoningReplay, RequireReasoningReplay: v.RequireReasoningReplay, ImageTokenBudget: v.ImageTokenBudget,
	}
	if preset := FindPreset(provider.PresetID); preset != nil && preset.Auth == "oauth" {
		config.APIKey = ""
		config.HTTPClient = b.oauthClient(provider)
		config.Codex = provider.PresetID == "codex"
	}
	if len(v.Reasoning) > 0 && string(v.Reasoning) != "null" {
		config.Reasoning = &models.ReasoningCapabilities{}
		if err = json.Unmarshal(v.Reasoning, config.Reasoning); err != nil {
			return nil, domain.Invalid("invalid reasoning capabilities")
		}
	}
	switch protocol {
	case completions.Protocol:
		return completions.New(config)
	case responses.Protocol:
		return responses.New(config)
	case anthropic.Protocol:
		return anthropic.New(config)
	case google.Protocol:
		return google.New(config)
	default:
		return nil, domain.Invalid("unsupported model protocol")
	}
}

func (b *Bridge) Resolve(ctx context.Context, ref string, validate RequestValidator) (core.Model, error) {
	parts := strings.Split(ref, "~")
	if len(parts) != 3 {
		return nil, domain.Invalid("invalid versioned model reference")
	}
	pv, e1 := strconv.ParseInt(parts[1], 10, 64)
	cv, e2 := strconv.ParseInt(parts[2], 10, 64)
	if e1 != nil || e2 != nil || pv < 1 || cv < 1 {
		return nil, domain.Invalid("invalid model revision")
	}
	var p domain.ModelProfile
	if err := b.Store.Version(ctx, "models", parts[0], pv, &p); err != nil {
		return nil, err
	}
	var provider domain.Provider
	if err := b.Store.Version(ctx, "providers", p.ProviderID, cv, &provider); err != nil {
		return nil, err
	}
	model, err := b.construct(ctx, p, provider)
	if err != nil {
		return nil, err
	}
	values, _, _ := p.Effective()
	return &boundModel{adapter: model, validate: validate, images: values.Images, presetID: provider.PresetID, sessionID: sessionID(ctx), enabled: func(ctx context.Context) error {
		var currentProfile domain.ModelProfile
		if err := b.Store.Get(ctx, "models", p.ID, &currentProfile); err != nil {
			return err
		}
		if currentProfile.Enabled != nil && !*currentProfile.Enabled {
			return domain.Conflict("this model is disabled")
		}
		var current domain.Provider
		if err := b.Store.Get(ctx, "providers", provider.ID, &current); err != nil {
			return err
		}
		if current.Enabled != nil && !*current.Enabled {
			return domain.Conflict("this provider is disabled")
		}
		return nil
	}}, nil
}

type boundModel struct {
	adapter
	validate  RequestValidator
	images    string
	sessionID string
	presetID  string
	enabled   func(context.Context) error
}

// PrepareRequest validates the selected model without adding or rewriting
// conversation context. Host context is persisted through Core.InjectContext.
func (m *boundModel) PrepareRequest(ctx context.Context, req core.Request) (core.Request, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return req, err
	}
	var owned core.Request
	if err = json.Unmarshal(raw, &owned); err != nil {
		return req, err
	}
	req = owned
	if m.validate != nil {
		if err := m.validate(ctx); err != nil {
			return req, err
		}
	}
	return req, nil
}

func (m *boundModel) wireImages(req core.Request, estimate bool) (core.Request, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return req, err
	}
	var owned core.Request
	if err = json.Unmarshal(raw, &owned); err != nil {
		return req, err
	}
	req = owned
	for i := range req.Messages {
		for j := range req.Messages[i].Content {
			part := &req.Messages[i].Content[j]
			if part.Type != "image" {
				continue
			}
			if m.images != "supported" {
				return req, domain.Invalid("image input is not supported by this model profile")
			}
			var encoded string
			if json.Unmarshal(part.Data, &encoded) != nil || encoded == "" || !strings.HasPrefix(part.MediaType, "image/") {
				return req, domain.Invalid("rollout image is missing its frozen pixel payload; reattach the image")
			}
			if base64.StdEncoding.DecodedLen(len(encoded)) > 20<<20 {
				return req, domain.Invalid("rollout image exceeds the 20 MiB model limit")
			}
			if _, err = io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded))); err != nil {
				return req, domain.Invalid("rollout image contains invalid base64")
			}
			if estimate {
				// Adapter reserves ImageTokenBudget separately, excluding URL bytes.
				part.URL = "data:image/png;base64,AA=="
			} else {
				part.URL = "data:" + part.MediaType + ";base64," + encoded
			}
			part.Data = nil
		}
	}
	return req, nil
}

func (m *boundModel) Stream(ctx context.Context, req core.Request, emit func(core.ModelEvent)) (core.Reply, error) {
	if m.enabled != nil {
		if err := m.enabled(ctx); err != nil {
			return core.Reply{}, err
		}
	}
	if m.sessionID != "" {
		ctx = models.WithRequestHeaders(ctx, sessionHeaders(m.presetID, m.sessionID))
	}
	req, err := m.wireImages(req, false)
	if err != nil {
		return core.Reply{}, err
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
	req, err := m.wireImages(req, true)
	if err != nil {
		return err
	}
	return core.ValidateRequest(ctx, m.adapter, req)
}

func (m *boundModel) EstimateTokens(ctx context.Context, req core.Request) (int, error) {
	req, err := m.wireImages(req, true)
	if err != nil {
		return 0, err
	}
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
