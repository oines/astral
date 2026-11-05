package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

type heldReopenStateStore struct {
	core.Store
	id      string
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (s *heldReopenStateStore) LoadState(ctx context.Context, id string) (core.RuntimeState, error) {
	if id == s.id {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return core.RuntimeState{}, ctx.Err()
		}
	}
	return s.Store.LoadState(ctx, id)
}

func TestSnapshotAndEventStreamRejectRetiredHandleDuringBindingReload(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		return testkit.Text("No generation is needed for reload.")
	})
	if _, err := p.Harness.Core(p.Context, p.Session.ID); err != nil {
		t.Fatal(err)
	}
	_, server := apiFixture(t, p)
	// A binding changes the compaction policy even with the default message
	// declaration. This fixture needs no external channel account or traffic.
	p.Harness.IM = nil
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishReload := func() { releaseOnce.Do(func() { close(release) }) }
	defer finishReload()
	held := &heldReopenStateStore{Store: p.CoreStore, id: p.Session.ID, entered: make(chan struct{}), release: release}
	p.Harness.CoreStore = held
	finished := make(chan error, 1)
	go func() {
		finished <- p.Harness.WithStoppedSessions(p.Context, []string{p.Session.ID}, func() error {
			return p.Store.Update(p.Context, func(tx *persistence.Tx) error {
				var session domain.Session
				if err := tx.Get(p.Context, "sessions", p.Session.ID, &session); err != nil {
					return err
				}
				session.BindingID, session.ConversationID = "reload-binding", "reload-chat"
				return tx.Save(p.Context, "sessions", &session.Meta, &session)
			})
		})
	}()
	select {
	case <-held.entered:
	case err := <-finished:
		t.Fatalf("reload ended before the replacement handle was opened: %v", err)
	case <-p.Context.Done():
		t.Fatal(p.Context.Err())
	}
	// The old Core window is now closed while the replacement has not yet
	// published. Neither endpoint may call Snapshot on that retired handle.
	for _, suffix := range []string{"/snapshot", "/events"} {
		ctx, cancel := context.WithTimeout(p.Context, 5*time.Second)
		response := request(t, ctx, server, http.MethodGet, "/api/v1/sessions/"+p.Session.ID+suffix, nil, true)
		var body map[string]any
		err := json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		cancel()
		if response.StatusCode != http.StatusServiceUnavailable || err != nil {
			t.Fatalf("%s returned status=%d body=%v error=%v", suffix, response.StatusCode, body, err)
		}
	}
	finishReload()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-p.Context.Done():
		t.Fatal(p.Context.Err())
	}
	response := request(t, p.Context, server, http.MethodGet, "/api/v1/sessions/"+p.Session.ID+"/snapshot", nil, true)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("replacement handle remained unavailable: status=%d", response.StatusCode)
	}
	if p.Requests.Load() != 0 {
		t.Fatal("reading session lifecycle started model generation")
	}
}
