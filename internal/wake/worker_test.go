package wake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func workerClock(f *fixture, advance time.Duration) {
	f.s.mu.Lock()
	now := f.s.now().Add(advance)
	f.s.now = func() time.Time { return now }
	f.s.mu.Unlock()
}

// Accept lazily starts the production worker. These tests own scheduler time
// and call cycle directly, so install an already-settled lifecycle instead of
// racing a background timer against deterministic fault injection.
func manualWorkerFixture(t testing.TB, deliver Deliver) *fixture {
	f := setup(t, deliver)
	f.s.mu.Lock()
	f.s.started = true
	f.s.cancel = func() {}
	f.s.done = make(chan struct{})
	close(f.s.done)
	f.s.mu.Unlock()
	return f
}

func TestWorkerUnadoptedInflightAndSharedBufferStayBoundedAcrossHours(t *testing.T) {
	release := false
	unique := map[string]int{}
	visits := map[string]int{}
	f := manualWorkerFixture(t, func(_ context.Context, b Batch) (DeliveryResult, error) {
		visits[b.ID]++
		if len(b.Events) > 0 {
			unique[b.ID]++
		}
		return DeliveryResult{Accepted: true, Handled: release}, nil
	})
	endpoints := []domain.WakeEndpoint{f.create(t), f.create(t)}
	acceptBatch := func(prefix string) {
		for at := range MaxBatchEvents {
			e := endpoints[at%len(endpoints)]
			if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: fmt.Sprintf("%s-%02d", prefix, at), Text: "short event"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	acceptBatch("inflight")
	f.s.cycle(f.ctx)
	workerClock(f, BlockDuration+time.Second)
	acceptBatch("buffered")
	f.s.cycle(f.ctx)
	for at := range 240 {
		workerClock(f, BlockDuration+time.Second)
		e := endpoints[at%len(endpoints)]
		_, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: fmt.Sprintf("rejected-after-minute-%03d", at), Text: "another event"})
		var limit *LimitError
		if !errors.As(err, &limit) {
			t.Fatalf("expired cooldown admitted beyond the full shared buffer: %v", err)
		}
		f.s.cycle(f.ctx)
	}
	f.s.mu.Lock()
	flow := f.s.flows[f.session.ID]
	bounded := flow != nil && flow.Inflight != nil && len(flow.Inflight.Events) == 0 && len(flow.Buffer) == MaxBatchEvents && flow.Bytes <= MaxBatchBytes
	f.s.mu.Unlock()
	if !bounded || len(unique) != 1 {
		t.Fatalf("unadopted Core batch expanded with elapsed time: bounded=%v batches=%v visits=%v", bounded, unique, visits)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.store.Directory, "astral.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var receipts int
	if err := db.QueryRow(`SELECT count(*) FROM operations WHERE kind='wake_receipt'`).Scan(&receipts); err != nil || receipts != 2*MaxBatchEvents {
		t.Fatalf("rejected events created durable receipts: count=%d err=%v", receipts, err)
	}
	release = true
	f.s.WakeSession(f.session.ID)
	f.s.cycle(f.ctx)
	f.s.cycle(f.ctx)
	if len(unique) != 2 {
		t.Fatalf("two accepted batches did not release independently: %v", unique)
	}
	for id, enqueues := range unique {
		if enqueues != 1 {
			t.Fatalf("batch %s was enqueued %d times", id, enqueues)
		}
	}
	f.s.mu.Lock()
	_, pending := f.s.flows[f.session.ID]
	f.s.mu.Unlock()
	if pending {
		t.Fatal("two adopted batches left a pending flow")
	}
}

func benchmarkWakeRowsAndWAL(b *testing.B, f *fixture) {
	b.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.store.Directory, "astral.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM operations WHERE kind IN ('wake_buffer','wake_status','wake_receipt')`).Scan(&rows); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(rows), "operation_rows")
	if info, err := os.Stat(filepath.Join(f.store.Directory, "astral.db-wal")); err == nil {
		b.ReportMetric(float64(info.Size()), "wal_bytes")
	}
}

func BenchmarkBlockedEndpointRejection(b *testing.B) {
	for _, requests := range []int{10000, 100000} {
		b.Run(fmt.Sprintf("requests=%d", requests), func(b *testing.B) {
			f := manualWorkerFixture(b, nil)
			e := f.create(b)
			for at := range MaxBatchEvents {
				if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: fmt.Sprintf("fill-%d", at), Text: "event"}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				for range requests {
					if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "refused-event", Text: "refused"}); err == nil {
						b.Fatal("blocked endpoint accepted an event")
					}
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*requests), "ns/request")
			benchmarkWakeRowsAndWAL(b, f)
		})
	}
}

func BenchmarkPersistentEndpointFullBatch(b *testing.B) {
	for _, handled := range []bool{false, true} {
		name := "unadopted"
		if handled {
			name = "adopted"
		}
		b.Run(name, func(b *testing.B) {
			f := manualWorkerFixture(b, func(context.Context, Batch) (DeliveryResult, error) {
				return DeliveryResult{Accepted: true, Handled: handled}, nil
			})
			e := f.create(b)
			b.ReportAllocs()
			b.ResetTimer()
			for batch := range b.N {
				b.StopTimer()
				// Reset only between measured batches. Model-unadopted batches
				// otherwise keep their single inflight slot intentionally full.
				if !handled {
					if err := f.s.StopSession(f.ctx, e.SessionID); err != nil {
						b.Fatal(err)
					}
				}
				workerClock(f, BlockDuration+MergeWindow)
				b.StartTimer()
				for at := range MaxBatchEvents {
					if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: fmt.Sprintf("batch-%d-%d", batch, at), Text: "Player: hello"}); err != nil {
						b.Fatal(err)
					}
				}
				f.s.cycle(f.ctx)
			}
			b.StopTimer()
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*MaxBatchEvents), "ns/event")
			benchmarkWakeRowsAndWAL(b, f)
		})
	}
}

func TestWorkerRetriesTransientDeliveryAtItsOwnDeadline(t *testing.T) {
	var calls int
	var first Batch
	f := manualWorkerFixture(t, func(_ context.Context, b Batch) (DeliveryResult, error) {
		calls++
		if calls == 1 {
			first = b
			return DeliveryResult{}, errors.New("synthetic temporary admission failure")
		}
		if b.ID != first.ID || b.Digest != first.Digest {
			t.Error("retry rebuilt an immutable event batch")
		}
		return DeliveryResult{Accepted: true, Handled: true}, nil
	})
	e := f.create(t)
	if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "retry-event", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	workerClock(f, MergeWindow)
	if delay := f.s.cycle(f.ctx); calls != 1 || delay != time.Second {
		t.Fatalf("temporary failure did not schedule a bounded retry: calls=%d delay=%s", calls, delay)
	}
	for range 5 {
		f.s.cycle(f.ctx)
	}
	if calls != 1 {
		t.Fatal("worker retried before its deadline")
	}
	workerClock(f, time.Second)
	if delay := f.s.cycle(f.ctx); calls != 2 || delay != time.Hour {
		t.Fatalf("timer retry did not settle the batch: calls=%d delay=%s", calls, delay)
	}
}

func TestWorkerAcceptedBatchHostSaveFailureDoesNotHotLoop(t *testing.T) {
	var calls int
	var f *fixture
	db := (*sql.DB)(nil)
	f = manualWorkerFixture(t, func(_ context.Context, _ Batch) (DeliveryResult, error) {
		calls++
		if calls == 1 {
			// The Core admission is represented by Accepted. Fail only the
			// following Host acknowledgement, after its inflight descriptor
			// already exists, using the real SQLite persistence boundary.
			_, err := db.Exec(`CREATE TRIGGER reject_wake_ack BEFORE UPDATE ON operations WHEN NEW.kind='wake_buffer' BEGIN SELECT RAISE(ABORT, 'synthetic acknowledgement failure'); END`)
			if err != nil {
				t.Error(err)
			}
			return DeliveryResult{Accepted: true}, nil
		}
		return DeliveryResult{Accepted: true, Handled: true}, nil
	})
	var err error
	db, err = sql.Open("sqlite", filepath.Join(f.store.Directory, "astral.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := f.create(t)
	if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "save-retry-event", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	workerClock(f, MergeWindow)
	if delay := f.s.cycle(f.ctx); calls != 1 || delay != time.Second {
		t.Fatalf("Host save failure created a hot deadline: calls=%d delay=%s", calls, delay)
	}
	for range 5 {
		f.s.cycle(f.ctx)
	}
	if calls != 1 {
		t.Fatal("failed Host acknowledgement caused an immediate delivery loop")
	}
	if _, err := db.Exec(`DROP TRIGGER reject_wake_ack`); err != nil {
		t.Fatal(err)
	}
	workerClock(f, time.Second)
	if delay := f.s.cycle(f.ctx); calls != 2 || delay != time.Hour {
		t.Fatalf("Host acknowledgement did not recover: calls=%d delay=%s", calls, delay)
	}
}

func TestWorkerForgottenCallbackCannotRecreateFlowOrRetry(t *testing.T) {
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f := manualWorkerFixture(t, func(context.Context, Batch) (DeliveryResult, error) {
		close(entered)
		<-release
		return DeliveryResult{}, nil
	})
	e := f.create(t)
	if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "forgotten-event", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	workerClock(f, MergeWindow)
	go func() { f.s.cycle(f.ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never entered its callback")
	}
	f.s.ForgetSession(e.SessionID)
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error { return tx.PurgeSessionDeliveries(f.ctx, e.SessionID) }); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stale callback did not return")
	}
	f.s.mu.Lock()
	_, flow := f.s.flows[e.SessionID]
	_, retry := f.s.retry[e.SessionID]
	_, dirty := f.s.dirty[e.SessionID]
	f.s.mu.Unlock()
	if flow || retry || dirty {
		t.Fatalf("stale callback recreated volatile state: flow=%v retry=%v dirty=%v", flow, retry, dirty)
	}
	var row any
	if err := f.store.Maintenance(f.ctx, flowKind, e.SessionID, &row); !domain.IsMissing(err) {
		t.Fatalf("stale callback recreated durable flow: %v %+v", err, row)
	}
	if delay := f.s.cycle(f.ctx); delay != time.Hour {
		t.Fatalf("forgotten Session left a hot timer: %s", delay)
	}
}

func TestWorkerStopClearsRetryAndInvalidatesReturningCallback(t *testing.T) {
	for _, inCallback := range []bool{false, true} {
		name := "scheduled-retry"
		if inCallback {
			name = "returning-callback"
		}
		t.Run(name, func(t *testing.T) {
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			f := manualWorkerFixture(t, func(context.Context, Batch) (DeliveryResult, error) {
				if inCallback {
					close(entered)
					<-release
				}
				return DeliveryResult{}, nil
			})
			e := f.create(t)
			if _, err := f.s.Accept(context.Background(), e.ComputerID, e.ID, Event{EventID: "stopped-event", Text: "hello"}); err != nil {
				t.Fatal(err)
			}
			workerClock(f, MergeWindow)
			if inCallback {
				go func() { f.s.cycle(f.ctx); close(done) }()
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("worker never entered its callback")
				}
			} else {
				f.s.cycle(f.ctx)
			}
			if err := f.s.StopSession(f.ctx, e.SessionID); err != nil {
				t.Fatal(err)
			}
			if inCallback {
				close(release)
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("stopped callback never returned")
				}
			}
			f.s.mu.Lock()
			_, retry := f.s.retry[e.SessionID]
			_, dirty := f.s.dirty[e.SessionID]
			flow := f.s.flows[e.SessionID]
			pending := flow != nil && (flow.Inflight != nil || len(flow.Buffer) > 0)
			f.s.mu.Unlock()
			if retry || dirty || pending {
				t.Fatalf("Stop retained automatic wake work: retry=%v dirty=%v pending=%v", retry, dirty, pending)
			}
			if delay := f.s.cycle(f.ctx); delay != time.Hour {
				t.Fatalf("Stop left a hot timer: %s", delay)
			}
		})
	}
}
