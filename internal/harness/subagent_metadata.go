package harness

import (
	"context"
	"encoding/json"
)

// Keep only admission metadata. The frozen prompt, tool declarations and
// initial task remain in their authoritative document, not in this memo.
type subagentMetadata struct {
	revision    int64
	started     bool
	selection   json.RawMessage
	fingerprint string
}

func (s *Service) childMetadata(ctx context.Context, id string) (subagentMetadata, error) {
	revision, err := s.Store.CurrentRevision(ctx, "subagent_settings", id)
	if err != nil {
		return subagentMetadata{}, err
	}
	s.mu.Lock()
	cached, ok := s.childSettings[id]
	s.mu.Unlock()
	if ok && cached.revision == revision {
		return cached, nil
	}
	var settings subagentSettings
	if err := s.Store.Get(ctx, "subagent_settings", id, &settings); err != nil {
		return subagentMetadata{}, err
	}
	selection, err := json.Marshal(settings.Configuration.Selection)
	if err != nil {
		return subagentMetadata{}, err
	}
	cached = subagentMetadata{revision: settings.Revision, started: settings.Started, selection: selection, fingerprint: digest(settings.Configuration.Selection)}
	s.mu.Lock()
	if s.childSettings == nil {
		s.childSettings = map[string]subagentMetadata{}
	}
	s.childSettings[id] = cached
	s.mu.Unlock()
	return cached, nil
}
