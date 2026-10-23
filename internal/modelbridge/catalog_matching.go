package modelbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

// Models.dev identities describe the catalog source, never the wire format.
func catalogProviderIDs(provider domain.Provider) []string {
	switch provider.PresetID {
	case "":
		return nil
	case "azure-openai":
		return []string{"azure"}
	case "cloudflare":
		return []string{"cloudflare-workers-ai"}
	case "codex":
		return []string{"openai"}
	case "qwen":
		if strings.Contains(provider.BaseURL, "dashscope-intl") {
			return []string{"alibaba", "alibaba-cn"}
		}
		return []string{"alibaba-cn", "alibaba"}
	case "bailian-coding-plan":
		return []string{"alibaba-coding-plan", "alibaba-cn"}
	case "mimo", "mimo-token-plan":
		return []string{"xiaomi"}
	case "moonshot":
		if strings.Contains(provider.BaseURL, "moonshot.cn") {
			return []string{"moonshotai-cn", "moonshotai"}
		}
		return []string{"moonshotai", "moonshotai-cn"}
	case "minimax-token-plan":
		return []string{"minimax-coding-plan", "minimax"}
	case "zhipu":
		return []string{"zhipuai"}
	case "zhipu-coding-plan":
		return []string{"zhipuai-coding-plan"}
	case "together":
		return []string{"togetherai"}
	default:
		return []string{provider.PresetID}
	}
}

// Recognizing a model's maker only ranks metadata candidates. In particular it
// must never select a dialect, reasoning format, or reasoning replay rule.
func makerCatalogIDs(slug string) []string {
	switch {
	case strings.HasPrefix(slug, "claude-"):
		return []string{"anthropic"}
	case strings.HasPrefix(slug, "gpt-"), strings.HasPrefix(slug, "chatgpt-"), strings.HasPrefix(slug, "o1"), strings.HasPrefix(slug, "o3"), strings.HasPrefix(slug, "o4"):
		return []string{"openai"}
	case strings.HasPrefix(slug, "gemini-"), strings.HasPrefix(slug, "gemma-"):
		return []string{"google"}
	case strings.HasPrefix(strings.ToLower(slug), "qwen"):
		return []string{"alibaba-cn", "alibaba"}
	case strings.HasPrefix(strings.ToLower(slug), "deepseek-"):
		return []string{"deepseek"}
	case strings.HasPrefix(slug, "mimo-"):
		return []string{"xiaomi"}
	case strings.HasPrefix(slug, "kimi-"):
		return []string{"moonshotai", "moonshotai-cn"}
	case strings.HasPrefix(slug, "glm-"):
		return []string{"zai", "zhipuai"}
	case strings.HasPrefix(strings.ToLower(slug), "minimax-"):
		return []string{"minimax", "minimax-cn"}
	case strings.HasPrefix(slug, "grok-"):
		return []string{"xai"}
	case strings.HasPrefix(slug, "mistral-"), strings.HasPrefix(slug, "magistral-"), strings.HasPrefix(slug, "codestral-"), strings.HasPrefix(slug, "devstral-"):
		return []string{"mistral"}
	}
	return nil
}

func (s *catalogSnapshot) recommend(provider domain.Provider, modelID string) Discovery {
	result := Discovery{ID: modelID, Name: modelID, Recommendations: map[string]json.RawMessage{}, RecommendationSources: map[string]domain.RecommendationSource{}}
	if s == nil {
		return result
	}
	candidates := s.bySlug[modelSlug(modelID)]
	// Apply lower-confidence sources first so each higher tier can fill or
	// replace individual fields without losing unrelated published metadata.
	result = s.discovery(modelID, candidates)
	providerIDs := catalogProviderIDs(provider)
	for _, ids := range [][]string{makerCatalogIDs(modelSlug(modelID)), providerIDs} {
		for i := len(ids) - 1; i >= 0; i-- {
			pid := ids[i]
			var matches []catalogEntry
			for _, entry := range candidates {
				if entry.Provider == pid {
					matches = append(matches, entry)
				}
			}
			if len(matches) > 0 {
				result = mergeCatalogDiscovery(provider, result, s.discovery(modelID, matches))
			}
		}
	}
	for i := len(providerIDs) - 1; i >= 0; i-- {
		for _, entry := range candidates {
			if entry.Provider == providerIDs[i] && entry.ID == modelID {
				result = mergeCatalogDiscovery(provider, result, s.discovery(modelID, []catalogEntry{entry}))
			}
		}
	}
	return result
}

func (s *catalogSnapshot) discovery(modelID string, entries []catalogEntry) Discovery {
	result := Discovery{ID: modelID, Name: modelID, Recommendations: map[string]json.RawMessage{}, RecommendationSources: map[string]domain.RecommendationSource{}}
	if len(entries) == 0 {
		return result
	}
	// Candidate order is stable. A conflict is not resolved by majority vote:
	// keep only agreeing values, with the first agreeing source as provenance.
	values := map[string]json.RawMessage{}
	conflicts := map[string]bool{}
	sources := map[string]domain.RecommendationSource{}
	for _, entry := range entries {
		source := modelsDevURL + "#" + entry.Provider + "/" + entry.ID
		flat := flattenRecommendations(entry.Values)
		for field, value := range flat {
			for old := range values {
				if strings.HasPrefix(old, field+".") || strings.HasPrefix(field, old+".") {
					conflicts[field], conflicts[old] = true, true
				}
			}
			if previous, exists := values[field]; exists && !sameJSON(previous, value) {
				conflicts[field] = true
				continue
			}
			if _, exists := values[field]; !exists {
				values[field] = value
				sources[field] = domain.RecommendationSource{Source: source, UpdatedAt: s.FetchedAt}
			}
		}
	}
	for field := range conflicts {
		delete(values, field)
		delete(sources, field)
	}
	if conflicts["reasoning.supported"] {
		conflicts["reasoning"] = true
		for field := range values {
			if strings.HasPrefix(field, "reasoning.") {
				delete(values, field)
				delete(sources, field)
			}
		}
	}
	result.Recommendations = unflattenRecommendations(values)
	result.RecommendationSources = sources
	result.conflicts = conflicts
	result.Name = entries[0].Name
	result.Source = modelsDevURL + "#" + entries[0].Provider + "/" + entries[0].ID
	return result
}

func sameJSON(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

func flattenRecommendations(values map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	var walk func(string, json.RawMessage)
	walk = func(path string, value json.RawMessage) {
		var object map[string]json.RawMessage
		value = bytes.TrimSpace(value)
		if len(value) > 0 && value[0] == '{' && json.Unmarshal(value, &object) == nil && len(object) > 0 {
			for key, child := range object {
				walk(path+"."+key, child)
			}
			return
		}
		out[path] = append(json.RawMessage(nil), value...)
	}
	for field, value := range values {
		walk(field, value)
	}
	return out
}

func unflattenRecommendations(flat map[string]json.RawMessage) map[string]json.RawMessage {
	root := map[string]any{}
	for path, value := range flat {
		parts := strings.Split(path, ".")
		current := root
		for _, part := range parts[:len(parts)-1] {
			child, ok := current[part].(map[string]any)
			if !ok {
				child = map[string]any{}
				current[part] = child
			}
			current = child
		}
		current[parts[len(parts)-1]] = value
	}
	out := map[string]json.RawMessage{}
	for key, value := range root {
		out[key] = raw(value)
	}
	return out
}

// mergeCatalogDiscovery is field-wise, including fields within reasoning. Null,
// false, zero and an explicitly empty list remain values, never "missing".
func mergeCatalogDiscovery(provider domain.Provider, fallback, live Discovery) Discovery {
	values := flattenRecommendations(fallback.Recommendations)
	liveValues := flattenRecommendations(live.Recommendations)
	sources := map[string]domain.RecommendationSource{}
	for field, source := range fallback.RecommendationSources {
		sources[field] = source
	}
	for conflict := range live.conflicts {
		for field := range values {
			if field == conflict || strings.HasPrefix(field, conflict+".") || strings.HasPrefix(conflict, field+".") {
				delete(values, field)
				delete(sources, field)
			}
		}
	}
	if supported, ok := explicitBool(liveValues["reasoning.supported"]); ok && !supported {
		for field := range values {
			if strings.HasPrefix(field, "reasoning.") {
				delete(values, field)
				delete(sources, field)
			}
		}
	}
	for field, value := range liveValues {
		if bytes.Equal(value, raw("unknown")) {
			continue
		}
		// An explicit parent value replaces its inherited children, and vice
		// versa. This also prevents map iteration order affecting null merges.
		for old := range values {
			if strings.HasPrefix(old, field+".") || strings.HasPrefix(field, old+".") {
				delete(values, old)
				delete(sources, old)
			}
		}
		values[field] = value
		delete(sources, field)
		if source, ok := live.RecommendationSources[field]; ok {
			sources[field] = source
		}
	}
	live.Recommendations = unflattenRecommendations(values)
	live.RecommendationSources = sources
	if live.Name == "" || live.Name == live.ID {
		if fallback.Name != "" {
			live.Name = fallback.Name
		}
	}
	if live.Protocol == "" {
		live.Protocol = provider.Protocol
	}
	if live.Dialect == "" {
		live.Dialect = provider.Dialect
	}
	if live.Source == "" {
		live.Source = fallback.Source
	}
	return live
}

func markRecommendationSources(item *Discovery, at time.Time) {
	item.RecommendationSources = map[string]domain.RecommendationSource{}
	for field := range flattenRecommendations(item.Recommendations) {
		item.RecommendationSources[field] = domain.RecommendationSource{Source: item.Source, UpdatedAt: at}
	}
}

// Known interface rules sit above public model metadata, but below the current
// endpoint's own explicit fields. The rule hook accepts a complete model ID
// and the selected protocol; a slug match never selects a wire contract.
func enrichDiscovery(provider domain.Provider, snapshot *catalogSnapshot, live Discovery) Discovery {
	// Curated model exceptions apply only to a new recommendation on the
	// preset's default connection. Explicit endpoint metadata and a different
	// user-selected provider protocol retain priority.
	if live.Protocol == "" {
		if preset := FindPreset(provider.PresetID); preset != nil && provider.Protocol == preset.Protocol {
			live.Protocol = presetModelProtocol(provider.PresetID, live.ID)
			if live.Protocol != "" {
				live.Dialect = "openai"
			}
		}
	}
	fallback := snapshot.recommend(provider, live.ID)
	protocol := live.Protocol
	if protocol == "" {
		protocol = provider.Protocol
	}
	if protocol == "openai-responses" && cloudflareNonStreamingResponses(provider.PresetID, live.ID) {
		if live.Recommendations == nil {
			live.Recommendations = map[string]json.RawMessage{}
		}
		live.Recommendations["streaming"] = raw(false)
	}
	if caps, source := presetReasoningRecommendations(provider, live.ID, protocol); len(caps) > 0 {
		known := Discovery{ID: live.ID, Name: live.ID, Source: source, Recommendations: map[string]json.RawMessage{"reasoning": raw(caps)}}
		markRecommendationSources(&known, time.Now().UTC())
		fallback = mergeCatalogDiscovery(provider, fallback, known)
	}
	return mergeCatalogDiscovery(provider, fallback, live)
}

func (b *Bridge) publicCatalog(ctx context.Context) *catalogSnapshot {
	if b.ModelCatalog == nil {
		return nil
	}
	// Public recommendations are optional: failed refreshes retain the last
	// valid snapshot and never make a successful live discovery fail.
	snapshot, _ := b.ModelCatalog.load(ctx)
	return snapshot
}

func (b *Bridge) ModelRecommendation(ctx context.Context, providerID, modelID string) (Discovery, error) {
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", providerID, &provider); err != nil {
		return Discovery{}, err
	}
	if strings.TrimSpace(modelID) == "" {
		return Discovery{}, domain.Invalid("model ID is required")
	}
	item := enrichDiscovery(provider, b.publicCatalog(ctx), Discovery{ID: modelID, Name: modelID})
	return item, nil
}

// PresetCatalog offers public catalog suggestions before a connection exists.
// They are not an inventory of models an account can actually call.
func (b *Bridge) PresetCatalog(ctx context.Context, id string) ([]Discovery, error) {
	preset := FindPreset(id)
	if preset == nil {
		return nil, domain.Missing("provider preset", id)
	}
	provider := domain.Provider{PresetID: id, Protocol: preset.Protocol, Dialect: preset.Dialect, BaseURL: preset.BaseURL}
	result := []Discovery{}
	snapshot := b.publicCatalog(ctx)
	if snapshot == nil {
		return result, nil
	}
	seen := map[string]bool{}
	for _, pid := range catalogProviderIDs(provider) {
		for _, entryList := range snapshot.bySlug {
			for _, entry := range entryList {
				if entry.Provider == pid && !seen[entry.ID] {
					seen[entry.ID] = true
					item := enrichDiscovery(provider, snapshot, Discovery{ID: entry.ID, Name: entry.Name})
					result = append(result, item)
				}
			}
		}
	}
	sortDiscoveries(result)
	return result, nil
}

func sortDiscoveries(items []Discovery) {
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
}
