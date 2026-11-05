package harness

import (
	"context"
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// Worker-owned routing contains only IDs. Durable Host changes rebuild it;
// records, Core state and message bodies are always read by reconciliation.
type subagentWork struct {
	parents map[string]string
	waiting map[string]map[string]struct{}
}

func newSubagentWork() *subagentWork {
	return &subagentWork{parents: map[string]string{}, waiting: map[string]map[string]struct{}{}}
}

func (w *subagentWork) clearWaiting(id string) {
	parent := w.parents[id]
	delete(w.waiting[parent], id)
	if len(w.waiting[parent]) == 0 {
		delete(w.waiting, parent)
	}
}

func (w *subagentWork) forget(id string) {
	w.clearWaiting(id)
	delete(w.parents, id)
}

func (w *subagentWork) observe(record domain.Session, err error) {
	if w.parents[record.ID] != record.SubagentParentID {
		w.clearWaiting(record.ID)
	}
	w.parents[record.ID] = record.SubagentParentID
	if err == nil {
		w.clearWaiting(record.ID)
	} else if errors.Is(err, errSubagentReportsPending) {
		if w.waiting[record.SubagentParentID] == nil {
			w.waiting[record.SubagentParentID] = map[string]struct{}{}
		}
		w.waiting[record.SubagentParentID][record.ID] = struct{}{}
	}
}

func (w *subagentWork) affected(dirty map[string]struct{}) map[string]struct{} {
	var result map[string]struct{}
	add := func(id string) {
		if result == nil {
			result = map[string]struct{}{}
		}
		result[id] = struct{}{}
	}
	for id := range dirty {
		if _, child := w.parents[id]; child {
			add(id)
		}
		for child := range w.waiting[id] {
			add(child)
		}
	}
	return result
}

func (s *Service) reconcileSubagentIDs(ctx context.Context, work *subagentWork, dirty map[string]struct{}) error {
	var failures []error
	for id := range work.affected(dirty) {
		var record domain.Session
		err := s.Store.Get(ctx, "sessions", id, &record)
		if domain.IsMissing(err) || err == nil && (record.Deletion != nil || record.SubagentParentID == "" && record.BindingID == "") {
			work.forget(id)
			continue
		}
		if err == nil {
			if record.SubagentParentID == "" {
				err = s.reconcileIMReply(ctx, record)
			} else {
				err = s.reconcileSubagent(ctx, record)
			}
			work.observe(record, err)
		}
		if err != nil && !errors.Is(err, core.ErrBusy) && !errors.Is(err, errSubagentReportsPending) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
