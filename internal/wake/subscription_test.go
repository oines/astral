package wake

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestConfigurationSubscriptionIgnoresEventAndLimitStatusFlood(t *testing.T) {
	f := manualWorkerFixture(t, nil)
	e := f.create(t)
	full, unsubscribeFull := f.s.Subscribe()
	defer unsubscribeFull()
	configuration, unsubscribeConfiguration := f.s.SubscribeConfiguration()
	defer unsubscribeConfiguration()
	for batch := range 5 {
		for event := range MaxBatchEvents {
			if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: fmt.Sprintf("%d-%d", batch, event), Text: "event body"}); err != nil {
				t.Fatal(err)
			}
		}
		f.s.cycle(f.ctx)
		select {
		case <-full:
		default:
			t.Fatal("the default subscription lost receiving/limit status updates")
		}
		select {
		case <-configuration:
			t.Fatal("event or limit status woke the configuration-only subscription")
		default:
		}
		workerClock(f, BlockDuration+time.Second)
	}
	notes := "/opt/example/changed.py"
	if _, err := f.s.Update(context.Background(), f.agent.ID, e.ID, Patch{Revision: e.Revision, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	for _, changes := range []chan struct{}{full, configuration} {
		select {
		case <-changes:
		default:
			t.Fatal("a configuration edit did not notify both subscription modes")
		}
	}
	f.s.InvalidateSession(e.SessionID)
	select {
	case <-configuration:
	default:
		t.Fatal("archive invalidation did not refresh the endpoint directory")
	}
}

func TestConfigurationSubscriptionObservesExternalStoreChanges(t *testing.T) {
	delivering := make(chan struct{})
	finish := make(chan struct{})
	f := setup(t, func(ctx context.Context, _ Batch) (DeliveryResult, error) {
		close(delivering)
		select {
		case <-finish:
			return DeliveryResult{Accepted: true, Handled: true}, nil
		case <-ctx.Done():
			return DeliveryResult{}, ctx.Err()
		}
	})
	e := f.create(t)
	if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "start-worker", Text: "Establish the worker watcher"}); err != nil {
		t.Fatal(err)
	}
	f.advance(MergeWindow)
	select {
	case <-delivering:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start delivering the event")
	}
	configuration, unsubscribe := f.s.SubscribeConfiguration()
	defer unsubscribe()
	// Save while the worker is in its delivery cycle so the invalidation must
	// survive until the worker returns to waiting for Store changes.
	e.Notes = "/opt/example/changed-externally.py"
	if err := f.store.Save(f.ctx, Kind, &e.Meta, &e); err != nil {
		t.Fatal(err)
	}
	close(finish)
	select {
	case <-configuration:
	case <-time.After(3 * time.Second):
		t.Fatal("external configuration change did not reach the directory subscription")
	}
}
