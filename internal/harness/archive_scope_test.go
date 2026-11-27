package harness

import (
	"context"
	"errors"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestScopedArchiveRechecksSessionBeforeWriting(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*domain.Session)
		status int
	}{
		{name: "became private", change: func(record *domain.Session) { record.Private = true }, status: 404},
		{name: "became bound", change: func(record *domain.Session) { record.BindingID = "binding" }, status: 409},
	} {
		t.Run(test.name, func(t *testing.T) {
			entered, cancelled := make(chan struct{}), make(chan struct{})
			f := newFixture(t, 0, func(ctx context.Context, n int, _ map[string]any) any {
				if n == 1 {
					close(entered)
					<-ctx.Done()
					close(cancelled)
					return nil
				}
				return textReply("Unexpected request")
			})
			if _, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "running", Text: "Hold this task."}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			r, err := f.service.open(f.ctx, f.session.ID)
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			err = func() error {
				r.mu.Lock()
				defer r.mu.Unlock()
				go func() {
					ctx := persistence.WithSessionScope(f.ctx, f.agent.ID, "")
					archived := true
					result <- f.service.SaveAgentSessionPresentation(ctx, f.agent.ID, f.session.ID, nil, &archived)
				}()
				// Archive has passed its initial check and reached the runtime
				// admission barrier when it interrupts the running model.
				select {
				case <-cancelled:
				case <-f.ctx.Done():
					return f.ctx.Err()
				}
				return f.store.Update(f.ctx, func(tx *persistence.Tx) error {
					var record domain.Session
					if err := tx.Get(f.ctx, "sessions", f.session.ID, &record); err != nil {
						return err
					}
					test.change(&record)
					return tx.Save(f.ctx, "sessions", &record.Meta, record)
				})
			}()
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-result:
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
			var problem *domain.Error
			if !errors.As(err, &problem) || problem.Status != test.status {
				t.Fatalf("archive returned %v, want status %d", err, test.status)
			}
			var record domain.Session
			if err := f.store.Get(f.ctx, "sessions", f.session.ID, &record); err != nil || record.Archived {
				t.Fatalf("archive changed unavailable session: %+v, %v", record, err)
			}
		})
	}
}
