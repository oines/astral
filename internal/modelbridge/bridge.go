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
	"time"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-models"
	"github.com/oines/astral-models/provider/openai/completions"
	"github.com/oines/astral-models/provider/openai/responses"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type Environment func(context.Context) (string, error)

type Bridge struct {
	Store      *persistence.Store
	HTTPClient *http.Client
}

func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return domain.Invalid("a provider URL must contain a host, without credentials, query or fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())) {
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
	case models.OpenAI, models.DeepSeek, models.Qwen, models.OpenRouter:
	default:
		return domain.Invalid("unsupported provider dialect")
	}
	if p.Protocol != completions.Protocol && p.Protocol != responses.Protocol {
		return domain.Invalid("unsupported provider protocol")
	}
	if p.Protocol == responses.Protocol && p.Dialect != string(models.OpenAI) && p.Dialect != string(models.DeepSeek) {
		return domain.Invalid("this provider does not have a supported Responses adapter")
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
		return tx.Save(ctx, "providers", &p.Meta, p)
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
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", p.ProviderID, &provider); err != nil {
		return "", core.GenerationOptions{}, err
	}
	v, _, err := p.Effective()
	if err != nil {
		return "", v.Defaults, err
	}
	check := p.Verification
	if check == nil || check.ProfileRevision != p.Revision || check.ProviderRevision != provider.Revision ||
		check.ToolCalling != "supported" || check.ToolReplay != "supported" {
		return "", v.Defaults, domain.Conflict("verify tool calling and replay for this model configuration before selecting it")
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
	_, err = EstimateTokens(ctx, model, core.Request{ModelID: ref, ModelRevision: ref,
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
	config := models.Config{
		Spec:  core.ModelSpec{ID: ref, Revision: ref, ContextWindow: v.ContextWindow, MaxInputTokens: v.MaxInputTokens, MaxOutputTokens: v.MaxOutputTokens},
		Model: p.ModelID, BaseURL: provider.BaseURL, APIKey: key, Headers: headers, HTTPClient: b.HTTPClient,
		Dialect: models.Dialect(provider.Dialect), ReplayScope: provider.ID + ":" + strconv.FormatInt(provider.Revision, 10),
		Streaming: v.Streaming, ReasoningReplay: v.ReasoningReplay, RequireReasoningReplay: v.RequireReasoningReplay, ImageTokenBudget: v.ImageTokenBudget,
	}
	if len(v.Reasoning) > 0 && string(v.Reasoning) != "null" {
		config.Reasoning = &models.ReasoningCapabilities{}
		if err = json.Unmarshal(v.Reasoning, config.Reasoning); err != nil {
			return nil, domain.Invalid("invalid reasoning capabilities")
		}
	}
	switch provider.Protocol {
	case completions.Protocol:
		return completions.New(config)
	case responses.Protocol:
		return responses.New(config)
	default:
		return nil, domain.Invalid("unsupported model protocol")
	}
}

func (b *Bridge) Resolve(ctx context.Context, ref string, environment Environment) (core.Model, error) {
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
	return &boundModel{adapter: model, environment: environment, images: values.Images}, nil
}

type boundModel struct {
	adapter
	environment Environment
	images      string
}

// PrepareRequest freezes dynamic host context before Core estimates and records
// the actual model request. Stream never fetches or alters external media.
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
	if m.environment != nil {
		env, err := m.environment(ctx)
		if err != nil {
			return req, err
		}
		req.SystemPrompt += "\n\n" + env
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
			if !strings.HasPrefix(part.URL, "astral-file:") {
				return req, domain.Invalid("image content must retain its Computer attachment reference")
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
