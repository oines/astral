// Package search routes each invocation to exactly the Agent's selected backend.
package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

func (s *Service) Save(ctx context.Context, p *domain.SearchProvider, key *string) error {
	if strings.TrimSpace(p.Name) == "" {
		return domain.Invalid("search provider name is required")
	}
	switch p.Adapter {
	case "tavily":
		if p.BaseURL == "" {
			p.BaseURL = "https://api.tavily.com"
		}
	case "exa":
		if p.BaseURL == "" {
			p.BaseURL = "https://api.exa.ai"
		}
	default:
		return domain.Invalid("supported search adapters are tavily and exa")
	}
	if err := modelbridge.ValidateURL(p.BaseURL); err != nil {
		return err
	}
	if p.ID == "" {
		p.ID = domain.ID("search")
	}
	return s.Store.Update(ctx, func(tx *persistence.Tx) error {
		var old domain.SearchProvider
		if p.Revision > 0 {
			if err := tx.Get(ctx, "search", p.ID, &old); err != nil {
				return err
			}
		}
		p.CredentialID = old.CredentialID
		if key != nil {
			var err error
			p.CredentialID, err = tx.PutSecret(ctx, *key)
			if err != nil {
				return err
			}
		}
		return tx.Save(ctx, "search", &p.Meta, p)
	})
}

func (s *Service) Search(ctx context.Context, agentID, query string) (Response, error) {
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", agentID, &agent); err != nil {
		return Response{}, err
	}
	if agent.SearchProviderID == "" {
		return Response{}, domain.Conflict("this Agent has no search provider selected")
	}
	return s.Query(ctx, agent.SearchProviderID, query)
}

func (s *Service) Query(ctx context.Context, id, query string) (Response, error) {
	if strings.TrimSpace(query) == "" || len(query) > 8000 {
		return Response{}, domain.Invalid("search query must contain 1–8000 bytes")
	}
	var provider domain.SearchProvider
	if err := s.Store.Get(ctx, "search", id, &provider); err != nil {
		return Response{}, err
	}
	key, err := s.Store.Secret(ctx, provider.CredentialID)
	if err != nil {
		return Response{}, err
	}
	body := map[string]any{"query": query}
	switch provider.Adapter {
	case "tavily":
		body["max_results"], body["search_depth"] = 5, "basic"
	case "exa":
		body["numResults"], body["type"] = 5, "auto"
		body["contents"] = map[string]any{"highlights": true}
	default:
		return Response{}, domain.Invalid("search adapter is not supported")
	}
	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.BaseURL, "/")+"/search", bytes.NewReader(raw))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if provider.Adapter == "exa" {
		request.Header.Set("x-api-key", key)
	} else {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	client := s.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, domain.Offline("the selected search provider could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Response{}, domain.Offline(fmt.Sprintf("the selected search provider returned HTTP %d", response.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return Response{}, err
	}
	if len(data) > 4<<20 {
		return Response{}, domain.Invalid("search provider response is too large")
	}
	var wire struct {
		Results []struct {
			Title         string   `json:"title"`
			URL           string   `json:"url"`
			Content       string   `json:"content"`
			Summary       string   `json:"summary"`
			Highlights    []string `json:"highlights"`
			PublishedDate string   `json:"publishedDate"`
			Published     string   `json:"published_date"`
			Author        string   `json:"author"`
		} `json:"results"`
	}
	if err = json.Unmarshal(data, &wire); err != nil {
		return Response{}, domain.Invalid("invalid search provider response")
	}
	result := Response{ProviderID: provider.ID, Query: query, Results: []Result{}}
	for _, item := range wire.Results[:min(5, len(wire.Results))] {
		summary := item.Content
		if summary == "" {
			summary = item.Summary
		}
		if summary == "" {
			summary = strings.Join(item.Highlights, "\n")
		}
		published := item.PublishedDate
		if published == "" {
			published = item.Published
		}
		result.Results = append(result.Results, Result{Title: item.Title, URL: item.URL, Summary: summary, PublishedAt: published, Author: item.Author})
	}
	return result, nil
}
