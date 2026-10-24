package harness

import (
	"context"
	"errors"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/recall"
)

type deleteFaultStore struct {
	core.Store
	deleter core.SessionDeleter
	after   bool
}

func (s *deleteFaultStore) DeleteSession(ctx context.Context, id string) error {
	if s.after {
		if err := s.deleter.DeleteSession(ctx, id); err != nil {
			return err
		}
	}
	return errors.New("injected deletion interruption")
}

func archiveForDeletion(t *testing.T, f *fixture, id string) domain.Session {
	t.Helper()
	archived := true
	if err := f.service.SaveSessionPresentation(f.ctx, id, nil, nil, &archived); err != nil {
		t.Fatal(err)
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", id, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestPermanentSessionDeletionPreservesCopyAndInvalidatesRecall(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("erase-token reply") })
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "delete-input", Text: "erase-token input"}); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	child, err := f.service.Fork(f.ctx, f.session.ID, view.HeadID, "independent copy", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.service.Recall.Search(f.ctx, f.agent.ID, recall.SearchRequest{Query: "erase-token", SessionIDs: []string{f.session.ID}, Limit: 1})
	if err != nil || len(page.Hits) != 1 || page.NextCursor == "" {
		t.Fatalf("initial recall: %+v %v", page, err)
	}
	beforeRequests := f.count()
	record := archiveForDeletion(t, f, f.session.ID)
	if err = f.service.DeleteSession(f.ctx, record.ID, record.Revision); err != nil {
		t.Fatal(err)
	}
	if err = f.service.DeleteSession(f.ctx, record.ID, record.Revision); err != nil {
		t.Fatal("retry:", err)
	}
	if _, err = f.service.View(f.ctx, record.ID); !domain.IsMissing(err) {
		t.Fatal("deleted runtime reopened:", err)
	}
	var historical domain.Session
	if err = f.store.Version(f.ctx, "sessions", record.ID, 1, &historical); !domain.IsMissing(err) {
		t.Fatal("deleted Host revision retained:", err)
	}
	if _, err = f.coreStore.LoadState(f.ctx, record.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("Core state retained:", err)
	}
	if _, err = f.coreStore.Get(f.ctx, view.RolloutID, view.HeadID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("history retained:", err)
	}
	if _, err = f.service.Recall.Search(f.ctx, f.agent.ID, recall.SearchRequest{Cursor: page.NextCursor}); err == nil {
		t.Fatal("deleted search snapshot remained available")
	}
	hits, err := f.service.Recall.Search(f.ctx, f.agent.ID, recall.SearchRequest{Query: "erase-token"})
	if err != nil || len(hits.Hits) != 2 {
		t.Fatalf("copy lost or source retained: %+v %v", hits, err)
	}
	for _, hit := range hits.Hits {
		if hit.SessionID != child.ID {
			t.Fatal("deleted Session still recalled")
		}
	}
	if f.count() != beforeRequests {
		t.Fatal("deletion invoked a model")
	}
	f.restart(t)
	if _, err = f.service.View(f.ctx, record.ID); !domain.IsMissing(err) {
		t.Fatal("deleted Session resurrected after restart:", err)
	}
	if _, err = f.service.View(f.ctx, child.ID); err != nil {
		t.Fatal("independent copy unavailable:", err)
	}
}

func TestPermanentSessionDeletionResumesBeforeAndAfterCoreCleanup(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-Core", true: "after-Core"}[after], func(t *testing.T) {
			f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
				t.Error("deletion must not invoke a model")
				return nil
			})
			record := archiveForDeletion(t, f, f.session.ID)
			f.service.CoreStore = &deleteFaultStore{Store: f.coreStore, deleter: f.coreStore, after: after}
			if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err == nil {
				t.Fatal("fault not surfaced")
			}
			if err := f.store.Get(f.ctx, "sessions", record.ID, &record); err != nil || record.Deletion == nil {
				t.Fatal("durable deletion intent lost:", err)
			}
			if _, err := f.service.View(f.ctx, record.ID); !domain.IsMissing(err) {
				t.Fatal("partially deleted runtime was opened:", err)
			}
			f.restart(t)
			if err := f.service.ResumeSessionDeletions(f.ctx); err != nil {
				t.Fatal("resume:", err)
			}
			if err := f.store.Get(f.ctx, "sessions", record.ID, &record); !domain.IsMissing(err) {
				t.Fatal("Host record retained:", err)
			}
			if _, err := f.coreStore.LoadState(f.ctx, record.ID); !errors.Is(err, core.ErrNotFound) {
				t.Fatal("Core record retained:", err)
			}
		})
	}
}

func TestPermanentSessionDeletionRequiresArchivedUnboundCurrentRevision(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any { return textReply("unused") })
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err == nil {
		t.Fatal("unarchived Session deleted")
	}
	stale := record.Revision
	record = archiveForDeletion(t, f, record.ID)
	if err := f.service.DeleteSession(f.ctx, record.ID, stale); err == nil {
		t.Fatal("stale confirmation deleted a changed Session")
	}
	record.BindingID = "retained-platform-binding"
	if err := f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err == nil {
		t.Fatal("bound Session deleted")
	}
	if err := f.store.Get(f.ctx, "sessions", record.ID, &record); err != nil || record.Deletion != nil {
		t.Fatal("rejected deletion changed the Session:", err)
	}
	record.BindingID, record.ModelRef = "", "removed-provider~999~1"
	if err := f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err != nil {
		t.Fatal("unavailable historical model prevented deletion:", err)
	}
}

func TestPermanentSessionDeletionStopsActiveRun(t *testing.T) {
	started := make(chan struct{})
	f := newFixture(t, 0, func(ctx context.Context, _ int, _ map[string]any) any {
		close(started)
		<-ctx.Done()
		return nil
	})
	if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "cancel-on-delete", Text: "wait for deletion"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	record := archiveForDeletion(t, f, f.session.ID)
	if err := f.service.DeleteSession(f.ctx, record.ID, record.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.coreStore.LoadState(f.ctx, record.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("active Session survived deletion:", err)
	}
}
