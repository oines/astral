package harness

import (
	"context"
	"testing"
	"time"
)

func TestSummaryReadsCommittedSnapshotWhileHostAdmissionIsBlocked(t *testing.T) {
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("directory lookup called the model")
		return nil
	})
	if _, err := f.service.Core(f.ctx, f.session.ID); err != nil {
		t.Fatal(err)
	}
	r := f.service.runtimes[f.session.ID]
	r.mu.Lock()
	locked := true
	defer func() {
		if locked {
			r.mu.Unlock()
		}
	}()
	type result struct {
		view SessionSummary
		err  error
	}
	done := make(chan result, 1)
	go func() {
		view, err := f.service.Summary(f.ctx, f.session.ID)
		done <- result{view, err}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.view.ID != f.session.ID || got.view.Selection.ID == "" {
			t.Fatalf("snapshot lookup failed while admission was locked: %+v %v", got.view, got.err)
		}
	case <-time.After(time.Second):
		r.mu.Unlock()
		locked = false
		<-done
		t.Fatal("directory lookup waited for the Host admission lock")
	}
}
