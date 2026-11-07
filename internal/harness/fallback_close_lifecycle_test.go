package harness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
)

func TestCloseCancelsMaintenanceBeforeJoiningFallbackWorkerBlockedOnAdmission(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	releaseModel := sync.OnceFunc(func() { close(release) })
	defer releaseModel()
	f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
		if n <= 2 {
			return textReply("Completed before maintenance.")
		}
		close(entered)
		select {
		case <-release:
			return textReply("Maintenance was manually released.")
		case <-ctx.Done():
			return nil
		}
	})
	for i, text := range []string{strings.Repeat("Retain this history when compacting. ", 2500), "Complete another turn."} {
		if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: []string{"history", "recent"}[i], Text: text}); err != nil {
			t.Fatal(err)
		}
		f.idle(t, f.session.ID)
	}
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	maintenance := make(chan error, 1)
	go func() {
		maintenance <- f.service.WithSession(f.ctx, r.id, func(session *core.Session) error {
			_, err := session.Compact(f.ctx, "Compact while holding Host admission.")
			return err
		})
	}()
	select {
	case <-entered:
	case err := <-maintenance:
		t.Fatalf("maintenance did not reach its independent model request: %v", err)
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	// Pin a counted fallback worker at its real admission boundary. Unlike a
	// polling tick, this ordering guarantees Close has a worker to join while
	// maintenance owns r.mu and cannot finish until Core is cancelled.
	workerEntered, workerDone := make(chan struct{}), make(chan struct{})
	f.service.modelFallbackWorkers.Add(1)
	go func() {
		defer f.service.modelFallbackWorkers.Done()
		close(workerEntered)
		r.mu.Lock()
		close(workerDone)
		r.mu.Unlock()
	}()
	<-workerEntered
	closing := make(chan error, 1)
	go func() { closing <- f.service.Close(f.ctx) }()
	select {
	case err := <-maintenance:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Close did not cancel maintenance before joining its blocked worker: %v", err)
		}
	case <-time.After(5 * time.Second):
		// Release the fixture for cleanup, without treating this deadline as
		// synchronization of the worker/maintenance relationship above.
		releaseModel()
		<-maintenance
		<-closing
		t.Fatal("Close joined the admission-blocked fallback worker before cancelling maintenance")
	}
	select {
	case err := <-closing:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after independent maintenance was cancelled")
	}
	<-workerDone
	if _, err := r.snapshot(); !errors.Is(err, core.ErrClosed) {
		t.Fatalf("Close bypassed the Core snapshot lifecycle gate: %v", err)
	}
}
