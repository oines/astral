package harness

import (
	"context"
	"encoding/json"
)

// Keep only admission metadata. Shared instructions and tool declarations are
// never loaded from a child's settings; the initial task stays in its document.
type subagentMetadata struct {
	revision            int64
	started             bool
	selection           json.RawMessage
	fingerprint         string
	archiveStoppedRunID string
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
	cached.archiveStoppedRunID = settings.ArchiveStoppedRunID
	s.mu.Lock()
	if s.childSettings == nil {
		s.childSettings = map[string]subagentMetadata{}
	}
	s.childSettings[id] = cached
	s.mu.Unlock()
	return cached, nil
}
