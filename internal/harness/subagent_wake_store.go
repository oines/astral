package harness

import (
	"context"

	core "github.com/oines/astral-core"
)

// Only Core Session configuration uses this adapter. Service.CoreStore keeps
// the original Store, including Host-owned optional deletion capabilities.
// Notifications carry no entries or payloads; the worker reads durable facts.
type subagentWakeStore struct {
	core.Store
	service *Service
}

func (s subagentWakeStore) Commit(ctx context.Context, id string, commit core.Commit) (core.Receipt, error) {
	receipt, err := s.Store.Commit(ctx, id, commit)
	if err == nil {
		s.service.wakeSubagents(id)
		s.service.sessionChanges.notify("")
	}
	return receipt, err
}

func (s subagentWakeStore) LookupCommit(ctx context.Context, id, commitID string) (core.Receipt, error) {
	receipt, err := s.Store.LookupCommit(ctx, id, commitID)
	if err == nil {
		// Core also confirms a possibly durable write through LookupCommit when
		// the original Commit/Fork response was lost.
		s.service.wakeSubagents(id)
		s.service.sessionChanges.notify("")
	}
	return receipt, err
}

func (s subagentWakeStore) Fork(ctx context.Context, request core.ForkRequest) (core.RuntimeState, error) {
	state, err := s.Store.Fork(ctx, request)
	if err == nil {
		s.service.wakeSubagents(request.TargetID)
		s.service.sessionChanges.notify("")
	}
	return state, err
}
