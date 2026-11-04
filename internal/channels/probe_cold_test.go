package channels

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

type coldProbeStore struct {
	core.Store
	mu                 sync.Mutex
	pathReads          int
	configurationReads int
	windowRollouts     map[string]bool
	pendingKinds       []core.InputKind
}

func (s *coldProbeStore) Get(ctx context.Context, rollout, id string) (core.Record, error) {
	record, err := s.Store.Get(ctx, rollout, id)
	if err == nil && record.Entry.Kind == core.EntryConfiguration {
		s.mu.Lock()
		s.configurationReads++
		s.mu.Unlock()
	}
	return record, err
}

func drainProbeChanges(service *harness.Service) int {
	count := 0
	for {
		select {
		case <-service.Changes():
			count++
		default:
			return count
		}
	}
}

func (s *coldProbeStore) Scan(ctx context.Context, rollout string, query core.Query) (core.Page, error) {
	s.mu.Lock()
	if query.PathHead != "" && query.Cursor == "" {
		s.pathReads++
		s.windowRollouts[rollout] = true
	}
	s.mu.Unlock()
	return s.Store.Scan(ctx, rollout, query)
}

func (s *coldProbeStore) Pending(ctx context.Context, id string, query core.PendingQuery) (core.Page, error) {
	s.mu.Lock()
	s.pendingKinds = append(s.pendingKinds, query.InputKind)
	s.mu.Unlock()
	// Exercise real Store cursors without constructing a large artificial
	// queue: the Store may return fewer records than the caller's maximum.
	if query.InputKind == core.InputSteer && query.MaxBytes == 4<<20 {
		query.Limit = 1
	}
	return s.Store.Pending(ctx, id, query)
}

func cloneProbeFixture(t *testing.T, hostDB, key, coreDB []byte, client *http.Client) (*Service, *coldProbeStore) {
	t.Helper()
	dir := t.TempDir()
	host := filepath.Join(dir, "host")
	if err := os.Mkdir(host, 0700); err != nil {
		t.Fatal(err)
	}
	for file, bytes := range map[string][]byte{filepath.Join(host, "astral.db"): hostDB, filepath.Join(host, "master.key"): key, filepath.Join(dir, "core.db"): coreDB} {
		if err := os.WriteFile(file, bytes, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := persistence.Open(host)
	if err != nil {
		t.Fatal(err)
	}
	sql, err := sqlite.Open(filepath.Join(dir, "core.db"))
	if err != nil {
		t.Fatal(err)
	}
	spy := &coldProbeStore{Store: sql, windowRollouts: map[string]bool{}}
	hub := computers.NewHub(store)
	service := harness.New(store, spy, &modelbridge.Bridge{Store: store, HTTPClient: client}, hub)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Close(ctx)
		_ = hub.Close()
		_ = sql.Close()
		_ = store.Close()
	})
	return New(store, service), spy
}

func TestProbeRecoveryChecksWorkBeforeOpeningIdenticalColdFixtures(t *testing.T) {
	for _, state := range []string{"no-probe-owner-queued", "completed-probe", "accepted-probe"} {
		t.Run(state, func(t *testing.T) {
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
				return testkit.Text("Handled the fixture Probe observation.")
			})
			s := New(p.Store, p.Harness)
			binding := activeDeliveryChat(t, p, s, true, false)
			session, err := p.Harness.Core(p.Context, binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = session.InjectContext(p.Context, core.Input{ID: "visible-history", Contexts: []core.Message{core.User(strings.Repeat("Already-confirmed conversation context. ", 64))}}); err != nil {
				t.Fatal(err)
			}
			var through int64
			if state == "no-probe-owner-queued" {
				for _, id := range []string{"first-owner-input", "second-owner-input"} {
					if _, err = session.Steer(p.Context, core.Input{ID: id, Message: core.User("Owner work stays queued; this is not a Probe.")}); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				event, seq := activeDeliveryArchive(t, p, binding, "probe-observation", "")
				through = seq
				activeDeliveryAcceptWakeup(t, p, binding, event, through)
				if state == "completed-probe" {
					run, err := session.Continue(p.Context)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = run.Wait(p.Context); err != nil {
						t.Fatal(err)
					}
					if err = s.coverProbe(p.Context, binding, through); err != nil {
						t.Fatal(err)
					}
				}
			}
			version := session.State().Version
			client := p.Harness.Models.HTTPClient
			if err = p.Harness.Close(p.Context); err != nil {
				t.Fatal(err)
			}
			if err = p.CoreStore.Close(); err != nil {
				t.Fatal(err)
			}
			if err = p.Store.Close(); err != nil {
				t.Fatal(err)
			}
			read := func(file string) []byte {
				bytes, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				return bytes
			}
			hostDB := read(filepath.Join(p.Store.Directory, "astral.db"))
			key := read(filepath.Join(p.Store.Directory, "master.key"))
			coreDB := read(filepath.Join(p.Directory, "core.db"))
			for _, legacy := range []bool{true, false} {
				fixture, spy := cloneProbeFixture(t, hostDB, key, coreDB, client)
				calls := p.Requests.Load()
				// The controlled baseline restores the old opening order on the
				// exact same DB bytes. The subsequent recovery logic is shared.
				if legacy {
					current, openErr := fixture.Harness.Core(p.Context, binding.SessionID)
					if openErr != nil {
						t.Fatal(openErr)
					}
					if current.State().PendingInputs[core.InputSteer] != 0 {
						err = fixture.resumeAcceptedProbeSession(p.Context, binding)
					}
				} else {
					err = fixture.resumeAcceptedProbe(p.Context, binding)
				}
				if err != nil {
					t.Fatal(err)
				}
				if state == "accepted-probe" {
					testkit.Await(t, p.Context, func() bool {
						snapshot, err := fixture.Harness.View(p.Context, binding.SessionID)
						return err == nil && snapshot.LastRun != nil && snapshot.LastRun.Status == "completed"
					})
					if err = fixture.resumeAcceptedProbe(p.Context, binding); err != nil {
						t.Fatal(err)
					}
				}
				drainProbeChanges(fixture.Harness)
				var window probeWindow
				if legacy {
					err = fixture.Harness.WithSession(p.Context, binding.SessionID, func(current *core.Session) error {
						if !harness.IMCanProbe(current) {
							return nil
						}
						var err error
						window, err = fixture.probeWindow(p.Context, binding, through, time.Now().Add(time.Minute), current)
						return err
					})
				} else {
					window, err = fixture.pendingProbe(p.Context, binding, through, time.Now().Add(time.Minute))
				}
				notifications := drainProbeChanges(fixture.Harness)
				if err != nil || window.Through != 0 {
					t.Fatalf("Probe result changed: through=%d err=%v", window.Through, err)
				}
				spy.mu.Lock()
				paths, coldSessions, configurations := spy.pathReads, len(spy.windowRollouts), spy.configurationReads
				kinds := append([]core.InputKind(nil), spy.pendingKinds...)
				spy.mu.Unlock()
				modelCalls := p.Requests.Load() - calls
				wantCalls := int64(0)
				if state == "accepted-probe" {
					wantCalls = 1
				}
				if modelCalls != wantCalls {
					t.Fatalf("Probe model calls=%d want=%d", modelCalls, wantCalls)
				}
				if state != "accepted-probe" && !legacy {
					if paths != 0 || coldSessions != 0 || configurations != 0 || notifications != 0 {
						t.Fatalf("empty recovery did work: paths=%d sessions=%d configurations=%d notifications=%d", paths, coldSessions, configurations, notifications)
					}
					for _, kind := range kinds {
						if kind != core.InputSteer {
							t.Fatalf("preflight read unrelated %s queue", kind)
						}
					}
					if state == "no-probe-owner-queued" && len(kinds) != 2 {
						t.Fatalf("preflight did not exhaust both real queue pages: %v", kinds)
					}
				} else if coldSessions != 1 || paths == 0 {
					t.Fatalf("fixture did not observe its real cold window load: paths=%d sessions=%d", paths, coldSessions)
				}
				if state != "accepted-probe" && legacy && notifications == 0 {
					t.Fatal("legacy no-op WithSession did not emit its real Harness notification")
				}
				if state != "accepted-probe" {
					current, err := fixture.Harness.CoreStore.LoadState(p.Context, binding.SessionID)
					if err != nil || current.Version != version {
						t.Fatalf("no-work inspection changed durable state: version=%d expected=%d err=%v", current.Version, version, err)
					}
				}
				t.Logf("PROBE_COLD_ORDER state=%s legacy=%t cold_sessions_with_window_reads=%d path_reads=%d configuration_entry_gets=%d pending_probe_notifications=%d model_calls=%d", state, legacy, coldSessions, paths, configurations, notifications, modelCalls)
			}
		})
	}
}

func TestBusyProbeRecoveryDoesNotReadQueuedOwnerBodies(t *testing.T) {
	entered := make(chan struct{})
	gate, release := activeDeliveryGate(t)
	p := testkit.New(t, 0, func(ctx context.Context, index int, _ map[string]any) any {
		if index == 1 {
			close(entered)
			select {
			case <-gate:
			case <-ctx.Done():
				return nil
			}
		}
		return testkit.Text("Owner work completed.")
	})
	spy := &coldProbeStore{Store: p.Harness.CoreStore, windowRollouts: map[string]bool{}}
	p.Harness.CoreStore = spy
	s := New(p.Store, p.Harness)
	binding := activeDeliveryChat(t, p, s, true, false)
	if _, err := p.Harness.Submit(p.Context, binding.SessionID, harness.InputRequest{ID: "running-owner-input", Text: "Run until the local test releases the request."}); err != nil {
		t.Fatal(err)
	}
	activeDeliveryWait(t, p.Context, entered, "held local model request")
	session, err := p.Harness.Core(p.Context, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	const body = "Queued owner instructions must not be decoded while this Session is busy. "
	if _, err = session.Steer(p.Context, core.Input{ID: "large-owner-steer", Message: core.User(strings.Repeat(body, 4096))}); err != nil {
		t.Fatal(err)
	}
	spy.mu.Lock()
	spy.pendingKinds = nil
	spy.mu.Unlock()
	drainProbeChanges(p.Harness)
	// Both checks see the same running Session and the same durable owner input.
	// The original locked guard and the new memory guard must read no queue body.
	if err = s.resumeAcceptedProbeSession(p.Context, binding); err != nil {
		t.Fatal(err)
	}
	drainProbeChanges(p.Harness)
	for range 3 {
		if err = s.resumeAcceptedProbe(p.Context, binding); err != nil {
			t.Fatal(err)
		}
	}
	spy.mu.Lock()
	reads := len(spy.pendingKinds)
	spy.mu.Unlock()
	if reads != 0 || drainProbeChanges(p.Harness) != 0 || p.Requests.Load() != 1 {
		t.Fatalf("busy recovery performed work: pending_pages=%d model_calls=%d", reads, p.Requests.Load())
	}
	if got := session.State().PendingInputs[core.InputSteer]; got != 1 {
		t.Fatalf("busy preflight changed queued owner work: pending=%d", got)
	}
	t.Logf("PROBE_HOT_BUSY queued_owner_bytes=%d pending_pages=%d model_calls=%d", len(body)*4096, reads, p.Requests.Load())
	release()
	p.Idle(t, binding.SessionID)
}

var _ core.Store = (*coldProbeStore)(nil)
