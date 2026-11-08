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
	f := setup(t, nil)
	e := f.create(t)
	if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "start-worker", Text: "Establish the worker watcher"}); err != nil {
		t.Fatal(err)
	}
	f.advance(MergeWindow)
	receive(t, f.delivered)
	configuration, unsubscribe := f.s.SubscribeConfiguration()
	defer unsubscribe()
	// This path bypasses the service's explicit configurationChanged call.
	// The Store watch must still invalidate persistent directory subscribers.
	e.Notes = "/opt/example/changed-externally.py"
	if err := f.store.Save(f.ctx, Kind, &e.Meta, &e); err != nil {
		t.Fatal(err)
	}
	select {
	case <-configuration:
	case <-time.After(3 * time.Second):
		t.Fatal("external configuration change did not reach the directory subscription")
	}
}
