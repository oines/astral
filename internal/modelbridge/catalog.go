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
	result := []Discovery{}
	seen := map[string]bool{}
	cursor := ""
	for page := 0; page < 20; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(provider.BaseURL, "/")+"/models", nil)
		if err != nil {
			return nil, err
		}
		if cursor != "" {
			query := req.URL.Query()
			query.Set("after", cursor)
			req.URL.RawQuery = query.Encode()
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
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
			Data    []json.RawMessage `json:"data"`
			HasMore bool              `json:"has_more"`
			LastID  string            `json:"last_id"`
		}
		if err = json.Unmarshal(data, &envelope); err != nil {
			return nil, fmt.Errorf("invalid model catalog JSON: %w", err)
		}
		for _, item := range envelope.Data {
			var v struct {
				ID              string   `json:"id"`
				Name            string   `json:"name"`
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
			if v.ID == "" || seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			if v.Name == "" {
				v.Name = v.ID
			}
			r := map[string]json.RawMessage{"tool_calling": raw("unknown")}
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
			result = append(result, Discovery{ID: v.ID, Name: v.Name, Recommendations: r})
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
