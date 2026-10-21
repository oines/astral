package modelbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	models "github.com/oines/astral-models"
	"github.com/oines/astral/internal/domain"
)

type Discovery struct {
	ID              string                     `json:"id"`
	Name            string                     `json:"name"`
	Protocol        string                     `json:"protocol,omitempty"`
	Dialect         string                     `json:"dialect,omitempty"`
	Source          string                     `json:"source,omitempty"`
	Recommendations map[string]json.RawMessage `json:"recommendations"`
}

func raw(value any) json.RawMessage { b, _ := json.Marshal(value); return b }

// Catalog reads only metadata explicitly supplied by the configured endpoint.
// A discovery identity is not evidence that its tools or replay have passed.
func (b *Bridge) Catalog(ctx context.Context, providerID string) ([]Discovery, error) {
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", providerID, &provider); err != nil {
		return nil, err
	}
	key, err := b.Store.Secret(ctx, provider.CredentialID)
	if err != nil {
		return nil, err
	}
	headerJSON, err := b.Store.Secret(ctx, provider.HeadersCredentialID)
	if err != nil {
		return nil, err
	}
	var headers map[string]string
	if headerJSON != "" {
		if err = json.Unmarshal([]byte(headerJSON), &headers); err != nil {
			return nil, err
		}
	}
	client := b.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	if preset := FindPreset(provider.PresetID); preset != nil && preset.Auth == "oauth" {
		client = b.oauthClient(provider)
		key = ""
	}
	result := []Discovery{}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		catalogURL := strings.TrimRight(provider.BaseURL, "/") + "/models"
		if strings.HasPrefix(provider.PresetID, "opencode") {
			catalogURL = strings.TrimRight(provider.BaseURL, "/") + "/v1/models"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
		if err != nil {
			return nil, err
		}
		if cursor != "" {
			query := req.URL.Query()
			field := "after"
			if provider.Protocol == "google-generative-ai" {
				field = "pageToken"
			}
			if provider.Protocol == "anthropic-messages" {
				field = "after_id"
			}
			query.Set(field, cursor)
			req.URL.RawQuery = query.Encode()
		}
		if provider.PresetID == "codex" {
			q := req.URL.Query()
			q.Set("client_version", "0.1.0")
			req.URL.RawQuery = q.Encode()
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
			if provider.Protocol == "anthropic-messages" {
				req.Header.Set("X-Api-Key", key)
			}
			if provider.Protocol == "google-generative-ai" {
				req.Header.Set("X-Goog-Api-Key", key)
			}
		}
		if provider.Protocol == "anthropic-messages" {
			req.Header.Set("Anthropic-Version", "2023-06-01")
		}
		req.Header.Set("User-Agent", "Astral/0.1.0")
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		response, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("model catalog request failed: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, fmt.Errorf("provider model catalog returned HTTP %d", response.StatusCode)
		}
		if len(data) > 8<<20 {
			return nil, domain.Invalid("model catalog exceeds 8 MiB")
		}
		var envelope struct {
			Data          []json.RawMessage `json:"data"`
			Models        []json.RawMessage `json:"models"`
			NextPageToken string            `json:"nextPageToken"`
			HasMore       bool              `json:"has_more"`
			LastID        string            `json:"last_id"`
		}
		if err = json.Unmarshal(data, &envelope); err != nil {
			return nil, fmt.Errorf("invalid model catalog JSON: %w", err)
		}
		if envelope.Data == nil {
			envelope.Data = envelope.Models
		}
		if envelope.NextPageToken != "" {
			envelope.HasMore = true
			envelope.LastID = envelope.NextPageToken
		}
		for _, item := range envelope.Data {
			var v struct {
				ID               string   `json:"id"`
				Name             string   `json:"name"`
				DisplayName      string   `json:"displayName"`
				DisplayNameSnake string   `json:"display_name"`
				Slug             string   `json:"slug"`
				InputLimit       int      `json:"inputTokenLimit"`
				OutputLimit      int      `json:"outputTokenLimit"`
				Methods          []string `json:"supportedGenerationMethods"`
				Endpoints        []string `json:"supported_endpoints"`
				Capabilities     struct {
					Limits struct {
						Context int `json:"max_context_window_tokens"`
						Input   int `json:"max_prompt_tokens"`
						Output  int `json:"max_output_tokens"`
					} `json:"limits"`
					Supports struct {
						Tools  *bool `json:"tool_calls"`
						Vision *bool `json:"vision"`
					} `json:"supports"`
				} `json:"capabilities"`
				ContextWindow   int      `json:"context_window"`
				ContextLength   int      `json:"context_length"`
				MaxOutputTokens int      `json:"max_output_tokens"`
				InputModalities []string `json:"input_modalities"`
				Effort          *struct {
					Levels  []string `json:"supported_levels"`
					Default string   `json:"default_level"`
				} `json:"effort"`
				Architecture struct {
					Inputs []string `json:"input_modalities"`
				} `json:"architecture"`
				TopProvider struct {
					MaxCompletionTokens int `json:"max_completion_tokens"`
				} `json:"top_provider"`
				Pricing struct {
					Prompt     string `json:"prompt"`
					Completion string `json:"completion"`
				} `json:"pricing"`
			}
			if err = json.Unmarshal(item, &v); err != nil {
				return nil, err
			}
			if v.ID == "" && v.Slug != "" {
				v.ID = v.Slug
				v.Name = v.DisplayNameSnake
			}
			if provider.Protocol == "google-generative-ai" {
				if !slices.Contains(v.Methods, "generateContent") {
					continue
				}
				v.ID = strings.TrimPrefix(v.Name, "models/")
				v.Name = v.DisplayName
				v.ContextWindow = v.InputLimit + v.OutputLimit
				v.MaxOutputTokens = v.OutputLimit
			}
			if v.ID == "" || seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			if v.Name == "" {
				v.Name = v.ID
			}
			r := map[string]json.RawMessage{"tool_calling": raw("unknown")}
			if v.Capabilities.Limits.Context > 0 {
				v.ContextWindow = v.Capabilities.Limits.Context
			}
			if v.Capabilities.Limits.Output > 0 {
				v.MaxOutputTokens = v.Capabilities.Limits.Output
			}
			if v.Capabilities.Limits.Input > 0 {
				r["max_input_tokens"] = raw(v.Capabilities.Limits.Input)
			}
			if v.InputLimit > 0 {
				r["max_input_tokens"] = raw(v.InputLimit)
			}
			if v.Capabilities.Supports.Tools != nil {
				value := "unsupported"
				if *v.Capabilities.Supports.Tools {
					value = "supported"
				}
				r["tool_calling"] = raw(value)
			}
			if v.Capabilities.Supports.Vision != nil {
				value := "unsupported"
				if *v.Capabilities.Supports.Vision {
					value = "supported"
				}
				r["images"] = raw(value)
			}
			if v.ContextWindow > 0 {
				r["context_window"] = raw(v.ContextWindow)
			} else if v.ContextLength > 0 {
				r["context_window"] = raw(v.ContextLength)
			}
			if v.MaxOutputTokens > 0 {
				r["max_output_tokens"] = raw(v.MaxOutputTokens)
			} else if v.TopProvider.MaxCompletionTokens > 0 {
				r["max_output_tokens"] = raw(v.TopProvider.MaxCompletionTokens)
			}
			inputs := v.InputModalities
			if inputs == nil {
				inputs = v.Architecture.Inputs
			}
			if inputs != nil {
				value := "unsupported"
				if slices.Contains(inputs, "image") {
					value = "supported"
				}
				r["images"] = raw(value)
			}
			if v.Effort != nil {
				caps := models.ReasoningCapabilities{Efforts: v.Effort.Levels}
				if provider.Dialect == "deepseek" {
					yes := true
					caps.Toggle, caps.Disable, caps.DefaultOn = &yes, &yes, &yes
				}
				r["reasoning"] = raw(caps)
				if v.Effort.Default != "" {
					r["defaults"] = raw(map[string]any{"reasoning": map[string]any{"effort": v.Effort.Default}})
				}
			}
			for name, price := range map[string]string{"input_price": v.Pricing.Prompt, "output_price": v.Pricing.Completion} {
				if value, err := strconv.ParseFloat(price, 64); err == nil && value >= 0 {
					r[name] = raw(value * 1_000_000)
				}
			}
			discovery := Discovery{ID: v.ID, Name: v.Name, Recommendations: r, Source: catalogURL}
			if slices.Contains(v.Endpoints, "/responses") {
				discovery.Protocol = "openai-responses"
				discovery.Dialect = "openai"
			} else if slices.Contains(v.Endpoints, "/messages") {
				discovery.Protocol = "anthropic-messages"
				discovery.Dialect = "anthropic"
			} else if slices.Contains(v.Endpoints, "/chat/completions") {
				discovery.Protocol = "openai-completions"
				discovery.Dialect = "compatible"
			}
			result = append(result, mergePresetDiscovery(provider, discovery))
		}
		if !envelope.HasMore {
			return result, nil
		}
		if envelope.LastID == "" || envelope.LastID == cursor {
			return nil, domain.Invalid("provider returned an invalid model catalog cursor")
		}
		cursor = envelope.LastID
	}
	return nil, domain.Invalid("provider model catalog exceeded 20 pages")
}

func (b *Bridge) Refresh(ctx context.Context, id string) (domain.ModelProfile, error) {
	var p domain.ModelProfile
	if err := b.Store.Get(ctx, "models", id, &p); err != nil {
		return p, err
	}
	catalog, err := b.Catalog(ctx, p.ProviderID)
	if err != nil {
		return p, err
	}
	for _, item := range catalog {
		if item.ID != p.ModelID {
			continue
		}
		now := time.Now().UTC()
		p.Recommendations, p.CatalogAt, p.CatalogSource = item.Recommendations, &now, p.ProviderID+"/models"
		// Overrides are deliberately not merged into or replaced by discovery.
		err = b.SaveProfile(ctx, &p)
		return p, err
	}
	return p, domain.Missing("model in provider catalog", p.ModelID)
}
