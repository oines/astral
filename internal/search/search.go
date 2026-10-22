// Package search routes each invocation to exactly the Agent's selected backend.
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

type Service struct {
	Store      *persistence.Store
	HTTPClient *http.Client
}

type Request struct {
	Query   string   `json:"query"`
	Domains []string `json:"domains,omitempty"`
	Recency *int     `json:"recency,omitempty"`
	Limit   *int     `json:"limit,omitempty"`
}

type Result struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Summary     string `json:"summary"`
	PublishedAt string `json:"published_at,omitempty"`
	Author      string `json:"author,omitempty"`
}

type Response struct {
	ProviderID string   `json:"provider_id"`
	Query      string   `json:"query"`
	Results    []Result `json:"results"`
}

type credentials struct {
	Version int               `json:"version"`
	Values  map[string]string `json:"values"`
}

// Existing Tavily/Exa records contain a single API key in the encrypted store.
func decodeCredentials(raw string) map[string]string {
	var envelope credentials
	if json.Unmarshal([]byte(raw), &envelope) == nil && envelope.Version == 1 && envelope.Values != nil {
		return envelope.Values
	}
	if raw != "" {
		return map[string]string{"api_key": raw}
	}
	return map[string]string{}
}

func (s *Service) Save(ctx context.Context, p *domain.SearchProvider, updates map[string]string) error {
	p.Name, p.BaseURL = strings.TrimSpace(p.Name), strings.TrimSpace(p.BaseURL)
	if p.Name == "" {
		return domain.Invalid("search provider name is required")
	}
	adapter, ok := adapterByID(p.Adapter)
	if !ok {
		return domain.Invalid("search adapter is not supported")
	}
	if adapter.BaseURLRequired && p.BaseURL == "" {
		return domain.Invalid(adapter.Name + " requires an instance Base URL")
	}
	if err := modelbridge.ValidateURL(first(p.BaseURL, adapter.BaseURL)); err != nil {
		return err
	}
	var old domain.SearchProvider
	keys := map[string]string{}
	if p.Revision > 0 {
		if err := s.Store.Get(ctx, "search", p.ID, &old); err != nil {
			return err
		}
		if old.Revision != p.Revision {
			return domain.Conflict("this record changed; reload it before saving")
		}
		if old.Adapter == p.Adapter {
			raw, err := s.Store.Secret(ctx, old.CredentialID)
			if err != nil {
				return err
			}
			keys = decodeCredentials(raw)
		}
	}
	for name := range p.Config {
		if !slices.ContainsFunc(adapter.Fields, func(f Field) bool { return f.Name == name && !f.Secret }) {
			return domain.Invalid("unsupported search configuration field: " + name)
		}
	}
	for name, value := range updates {
		if !slices.ContainsFunc(adapter.Fields, func(f Field) bool { return f.Name == name && f.Secret }) {
			return domain.Invalid("unsupported search credential field: " + name)
		}
		if value == "" {
			delete(keys, name)
		} else {
			keys[name] = value
		}
	}
	p.CredentialFields = nil
	for _, field := range adapter.Fields {
		value := strings.TrimSpace(p.Config[field.Name])
		if field.Secret {
			value = keys[field.Name]
			if value != "" {
				p.CredentialFields = append(p.CredentialFields, field.Name)
			}
		} else if p.Config != nil {
			p.Config[field.Name] = value
		}
		if field.Required && strings.TrimSpace(value) == "" {
			return domain.Invalid(adapter.Name + " requires " + field.Label)
		}
		if value != "" && len(field.Options) > 0 && !slices.Contains(field.Options, value) {
			return domain.Invalid("invalid " + field.Label)
		}
	}
	if p.ID == "" {
		p.ID = domain.ID("search")
	}
	// tx.Save rechecks the revision. A concurrent edit rolls back the new secret
	// as well as the document, so the credential read above cannot erase it.
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		p.CredentialID = ""
		if p.Adapter == old.Adapter {
			p.CredentialID = old.CredentialID
		}
		if len(keys) > 0 && (len(updates) > 0 || p.CredentialID == "") {
			raw, err := json.Marshal(credentials{Version: 1, Values: keys})
			if err != nil {
				return err
			}
			p.CredentialID, err = tx.PutSecret(ctx, string(raw))
			if err != nil {
				return err
			}
		} else if len(keys) == 0 {
			p.CredentialID = ""
		}
		return tx.Save(ctx, "search", &p.Meta, p)
	})
}

func (s *Service) Search(ctx context.Context, agentID string, input Request) (Response, error) {
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", agentID, &agent); err != nil {
		return Response{}, err
	}
	if agent.SearchProviderID == "" {
		return Response{}, domain.Conflict("this Agent has no search provider selected")
	}
	return s.Query(ctx, agent.SearchProviderID, input)
}

func (s *Service) Query(ctx context.Context, id string, input Request) (Response, error) {
	input, err := normalizeRequest(input)
	if err != nil {
		return Response{}, err
	}
	var provider domain.SearchProvider
	if err = s.Store.Get(ctx, "search", id, &provider); err != nil {
		return Response{}, err
	}
	adapter, ok := adapterByID(provider.Adapter)
	if !ok {
		return Response{}, domain.Invalid("search adapter is not supported")
	}
	key, err := s.Store.Secret(ctx, provider.CredentialID)
	if err != nil {
		return Response{}, err
	}
	keys := decodeCredentials(key)
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	req, err := buildRequest(ctx, adapter, provider, keys, input, time.Now().UTC())
	if err != nil {
		return Response{}, err
	}
	client := s.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		// A URL in a transport error may contain Google's API key.
		return Response{}, domain.Offline("the selected search provider could not be reached")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return Response{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := fmt.Sprintf("%s returned HTTP %d", adapter.Name, response.StatusCode)
		if detail := errorMessage(data); detail != "" {
			message += ": " + redact(detail, keys)
		}
		return Response{}, domain.Offline(message)
	}
	results, err := parseResults(adapter.ID, data)
	if err != nil {
		return Response{}, domain.Invalid(adapter.Name + ": " + redact(err.Error(), keys))
	}
	return Response{ProviderID: id, Query: input.Query, Results: results}, nil
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func redact(message string, keys map[string]string) string {
	values := slices.Collect(maps.Values(keys))
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	for _, value := range values {
		if value != "" {
			message = strings.ReplaceAll(message, value, "[redacted]")
		}
	}
	return message
}
