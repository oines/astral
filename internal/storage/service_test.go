package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type historyStore struct {
	core.Store
	load     func(context.Context, string) (core.RuntimeState, error)
	scan     func(context.Context, string, core.Query) (core.Page, error)
	attempts func(context.Context, string, core.AttemptQuery) (core.AttemptPage, error)
	reclaim  func(context.Context) error
}

func (h *historyStore) LoadState(ctx context.Context, id string) (core.RuntimeState, error) {
	if h.load != nil {
		return h.load(ctx, id)
	}
	return core.RuntimeState{}, core.ErrNotFound
}
func (h *historyStore) Scan(ctx context.Context, id string, q core.Query) (core.Page, error) {
	if h.scan != nil {
		return h.scan(ctx, id, q)
	}
	return core.Page{}, nil
}
func (h *historyStore) ScanAttempts(ctx context.Context, id string, q core.AttemptQuery) (core.AttemptPage, error) {
	if h.attempts != nil {
		return h.attempts(ctx, id, q)
	}
	return core.AttemptPage{}, nil
}
func (h *historyStore) Reclaim(ctx context.Context) error {
	if h.reclaim != nil {
		return h.reclaim(ctx)
	}
	return nil
}
func fixture(t *testing.T, h *historyStore) (*Service, *persistence.Store) {
	t.Helper()
	data := filepath.Join(t.TempDir(), "data", "server")
	store, err := persistence.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	s := New(context.Background(), Config{Directory: data, Store: store, Core: h, reclaimInterval: 20 * time.Millisecond})
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
		store.Close()
	})
	return s, store
}
func save(t *testing.T, store *persistence.Store, kind, id string, value any) {
	t.Helper()
	raw, _ := json.Marshal(value)
	var obj map[string]any
	json.Unmarshal(raw, &obj)
	obj["id"] = id
	meta := domain.Meta{ID: id}
	if err := store.Save(context.Background(), kind, &meta, obj); err != nil {
		t.Fatal(err)
	}
}
func await(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func write(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal(err)
	}
}
func backup(t *testing.T, s *Service, name string) string {
	t.Helper()
	path := filepath.Join(filepath.Dir(s.config.Directory), "upgrade-backups", name)
	raw, _ := json.Marshal(map[string]any{"schema": 1, "data": s.config.Directory})
	write(t, filepath.Join(path, "manifest.json"), raw)
	write(t, filepath.Join(path, "files", "astral.db"), []byte("backup bytes"))
	return path
}

func TestLogicalEstimatePagesAllRecordsAndAttemptsAndKeepsOldUnboundSession(t *testing.T) {
	var calls atomic.Int64
	h := &historyStore{load: func(_ context.Context, id string) (core.RuntimeState, error) {
		return core.RuntimeState{SessionID: id, RolloutID: "rollout-" + id, Version: 9}, nil
	}}
	h.scan = func(_ context.Context, id string, q core.Query) (core.Page, error) {
		calls.Add(1)
		if q.HistoryOnly || q.PathHead != "" {
			t.Error("operational and pending records were filtered out")
		}
		if q.MaxBytes < 8<<20 {
			return core.Page{}, core.ErrBudget
		}
		if q.Cursor == "" {
			return core.Page{Records: []core.Record{{Entry: core.Entry{ID: "input", Kind: core.EntryInputQueued}}}, Next: "2"}, nil
		}
		if q.Cursor != "2" {
			t.Errorf("bad cursor %q", q.Cursor)
		}
		return core.Page{Records: []core.Record{{Entry: core.Entry{ID: "response", Kind: core.EntryMessage}}}}, nil
	}
	h.attempts = func(_ context.Context, id string, q core.AttemptQuery) (core.AttemptPage, error) {
		if q.Cursor == "" {
			return core.AttemptPage{Attempts: []core.ModelAttempt{{ID: "frozen-input", SessionID: id, Input: core.RequestInput{Snapshot: json.RawMessage(`{"frozen":"request"}`)}}}, Next: "5"}, nil
		}
		return core.AttemptPage{Attempts: []core.ModelAttempt{{ID: "failed-reply", SessionID: id, Outcome: &core.AttemptOutcome{Error: strings.Repeat("failed", 40)}}}}, nil
	}
	s, store := fixture(t, h)
	save(t, store, "agents", "bot", domain.Agent{Name: "Bot"})
	save(t, store, "connections", "account", domain.Connection{AgentID: "bot", Name: "My account", Platform: "telegram"})
	save(t, store, "conversations", "chat", domain.Conversation{ConnectionID: "account", Title: "Chat"})
	save(t, store, "sessions", "current", domain.Session{AgentID: "bot", Title: "Current", BindingID: "binding"})
	save(t, store, "sessions", "old", domain.Session{AgentID: "bot", Title: "Old", ConversationID: "chat", Source: "im", Archived: true})
	save(t, store, "bindings", "binding", domain.ConversationBinding{AgentID: "bot", ConversationID: "chat", SessionID: "current", Enabled: true})
	s.Refresh(context.Background())
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.estimate.State == "complete" })
	out, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Agents) != 1 || len(out.Agents[0].Chats) != 1 || len(out.Agents[0].Sessions) != 1 {
		t.Fatalf("wrong ownership: %+v", out.Agents)
	}
	agent := out.Agents[0]
	if agent.Chats[0].SessionID != "current" || agent.Sessions[0].SessionID != "old" || agent.Chats[0].RolloutBytes == 0 || agent.Sessions[0].LogicalBytes == 0 {
		t.Fatalf("lost current/old history: %+v", agent)
	}
	if agent.LogicalBytes != agent.Chats[0].LogicalBytes+agent.Sessions[0].LogicalBytes {
		t.Fatalf("wrong aggregate: %+v", agent)
	}
	before := calls.Load()
	s.Refresh(context.Background())
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.estimate.State == "complete" })
	if calls.Load() != before {
		t.Fatal("unchanged immutable version was scanned again")
	}
}

func TestOversizedBudgetAndCancellationStayExplicit(t *testing.T) {
	var maxBudget int
	h := &historyStore{load: func(context.Context, string) (core.RuntimeState, error) {
		return core.RuntimeState{RolloutID: "r", Version: 1}, nil
	}, scan: func(_ context.Context, _ string, q core.Query) (core.Page, error) {
		maxBudget = max(maxBudget, q.MaxBytes)
		return core.Page{}, core.ErrBudget
	}}
	s, _ := fixture(t, h)
	e := s.measureSession(context.Background(), "s")
	if e.State != "partial" || e.Error == "" || maxBudget != 64<<20 {
		t.Fatalf("budget was hidden or unbounded: %+v, %d", e, maxBudget)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e = s.measureSession(ctx, "s")
	if e.State != "partial" || !strings.Contains(e.Error, "canceled") {
		t.Fatalf("cancellation was hidden: %+v", e)
	}
}

func TestCleanupRemovesOnlyOwnedUnprotectedFiles(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	old, latest := backup(t, s, "old"), backup(t, s, "latest")
	raw, _ := json.Marshal(map[string]any{"schema": 1, "backup": latest})
	data := s.config.Directory
	write(t, filepath.Join(data, "last-upgrade.json"), raw)
	model := filepath.Join(data, "cache", "models.dev.json")
	log := filepath.Join(data, "logs", "server.log.1")
	write(t, model, []byte("model"))
	write(t, log, []byte("log"))
	kept := []string{latest, filepath.Join(data, "cache", "project.txt"), filepath.Join(data, "logs", "server.log"), filepath.Join(data, "logs", "project.log.1")}
	for _, p := range kept[1:] {
		write(t, p, []byte("keep"))
	}
	oldSize, err := inspectTree(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	want := oldSize + 8
	if physical := s.physical(t.Context()); physical.CleanableBytes != want || physical.LogBytes != 7 {
		t.Fatalf("incorrect cleanable bytes: %+v", physical)
	}
	result := s.Cleanup(t.Context())
	if result.Error != "" || result.RemovedBytes != want {
		t.Fatalf("cleanup: %+v want %d", result, want)
	}
	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{old, model, log} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists: %v", p, err)
		}
	}
	if result := s.Cleanup(t.Context()); result.RemovedBytes != 0 || result.Error != "" {
		t.Fatal(result)
	}
}

func TestCleanupProtectsMigrationAndUnknownFiles(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	path := backup(t, s, "old")
	write(t, filepath.Join(s.config.Directory, "migration-state.json"), []byte("bad recovery journal"))
	outside := filepath.Join(t.TempDir(), "work")
	write(t, outside, []byte("keep"))
	cache := filepath.Join(s.config.Directory, "cache")
	if err := os.MkdirAll(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cache, "models.dev.json")); err != nil {
		t.Skip(err)
	}
	result := s.Cleanup(t.Context())
	if result.Error == "" || result.RemovedBytes != 0 {
		t.Fatal(result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "keep" {
		t.Fatalf("unowned file: %q %v", raw, err)
	}
	if len(s.physical(t.Context()).Errors) == 0 {
		t.Fatal("missing diagnostics")
	}
}

// A file can change between discovery and removal. A partial cleanup must
// report only removed files and leave the backup discoverable without a journal.
func TestInterruptedBackupRetainsManifestForFreshCleanup(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	path := backup(t, s, "interrupted")
	outside := filepath.Join(t.TempDir(), "untouched")
	write(t, outside, []byte("untouched"))
	link := filepath.Join(path, "x-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	n, err := removeBackup(t.Context(), path)
	if err == nil || n != int64(len("backup bytes")) || !s.ownsBackup(path) {
		t.Fatalf("partial backup removal: %d %v", n, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	result := s.Cleanup(t.Context())
	if result.Error != "" || result.RemovedBytes == 0 {
		t.Fatalf("fresh cleanup failed: %+v", result)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "untouched" {
		t.Fatalf("symbolic link target changed: %q, %v", raw, err)
	}
}

func TestReclaimWaitsForIdleRetainsFailureAndRetries(t *testing.T) {
	var busy atomic.Bool
	busy.Store(true)
	var attempt atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	h := &historyStore{reclaim: func(context.Context) error {
		attempt.Add(1)
		if fail.Load() {
			return errors.New("vacuum failed")
		}
		return nil
	}}
	s, _ := fixture(t, h)
	s.config.Busy = busy.Load
	s.RequestReclaim()
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.reclaim.State == "waiting" })
	if attempt.Load() != 0 {
		t.Fatal("reclaim ran while Bot was active")
	}
	busy.Store(false)
	await(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.reclaim.State == "failed" && s.reclaim.Error == "vacuum failed"
	})
	fail.Store(false)
	s.RequestReclaim()
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.reclaim.State == "complete" })
	if attempt.Load() != 2 {
		t.Fatalf("unexpected retries: %d", attempt.Load())
	}
}

func TestPhysicalIncludesRecallAndSQLiteSidecars(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	for name, bytes := range map[string]int{"core.db": 301, "core.db-wal": 71, "recall.db": 211, "recall.db-wal": 53} {
		write(t, filepath.Join(s.config.Directory, name), make([]byte, bytes))
	}
	physical := s.physical(context.Background())
	items := map[string]int64{}
	for _, item := range physical.Items {
		items[item.ID] = item.Bytes
	}
	if items["core.db"] != 372 || items["recall.db"] != 264 {
		t.Fatalf("missing physical history/index files: %+v", physical)
	}
	if physical.DatabaseBytes != items["astral.db"]+items["core.db"]+items["recall.db"] || physical.DiskTotalBytes <= 0 || physical.AvailableBytes <= 0 {
		t.Fatalf("wrong disk total: %+v", physical)
	}
}

func TestCanceledRefreshReleasesFollowingMeasurement(t *testing.T) {
	entered := make(chan struct{})
	var calls atomic.Int64
	h := &historyStore{load: func(ctx context.Context, id string) (core.RuntimeState, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return core.RuntimeState{}, ctx.Err()
		}
		return core.RuntimeState{SessionID: id, RolloutID: "fresh", Version: 2}, nil
	}}
	s, store := fixture(t, h)
	save(t, store, "sessions", "root", domain.Session{AgentID: "a"})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- s.Refresh(ctx) }()
	<-entered
	go func() { second <- s.Refresh(t.Context()) }()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if s.cache["root"].State != "complete" || s.cache["root"].Version != 2 {
		t.Fatalf("second caller received stale estimate: %+v", s.cache["root"])
	}
}

func TestExternalCleanupReturnsPartialProgress(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	s.config.ExternalCache = func(context.Context) (int64, int64, error) { return 150, 50, nil }
	s.config.CleanExternal = func(context.Context) (int64, error) { return 20, errors.New("Desktop file is locked") }
	physical := s.physical(t.Context())
	if physical.CacheBytes != 150 || physical.CleanableBytes != 50 {
		t.Fatal(physical)
	}
	result := s.Cleanup(t.Context())
	if result.RemovedBytes != 20 || result.Error != "Desktop file is locked" {
		t.Fatal(result)
	}
}
func TestReclaimRequestDuringRunningMaintenanceIsNotLost(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	s, _ := fixture(t, &historyStore{reclaim: func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}})
	s.RequestReclaim()
	<-entered
	returned := make(chan struct{})
	go func() { s.RequestReclaim(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("new cleanup blocked behind existing maintenance")
	}
	close(release)
	await(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return calls.Load() == 2 && s.reclaim.State == "complete"
	})
}

func TestWaitForReclaimJoinsConcurrentRequestsAndCanCancel(t *testing.T) {
	entered, release := make(chan struct{}, 2), make(chan struct{})
	var passes atomic.Int64
	s, _ := fixture(t, &historyStore{reclaim: func(ctx context.Context) error {
		pass := passes.Add(1)
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
		if pass == 2 {
			return errors.New("disk failure")
		}
		return nil
	}})
	if err := s.WaitForReclaim(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.RequestReclaim()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.WaitForReclaim(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.WaitForReclaim(t.Context()) }()
	s.RequestReclaim()
	release <- struct{}{}
	<-entered
	select {
	case <-done:
		t.Fatal("waiter returned before the pending second pass")
	default:
	}
	release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out, err := s.Snapshot(t.Context())
	if err != nil || out.Reclaim.State != "failed" || out.Reclaim.Error != "disk failure" {
		t.Fatalf("terminal state: %+v %v", out.Reclaim, err)
	}
}
func TestManagementStatusDoesNotQueueBehindHostMaintenanceConnection(t *testing.T) {
	s, store := fixture(t, &historyStore{})
	save(t, store, "agents", "bot", domain.Agent{Name: "Cached Bot"})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.reclaim.State = "running"
	s.mu.Unlock()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- store.Update(context.Background(), func(*persistence.Tx) error { close(entered); <-release; return nil })
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	out, err := s.Snapshot(ctx)
	if err != nil || out.Reclaim.State != "running" || len(out.Agents) != 1 || out.Agents[0].Name != "Cached Bot" {
		t.Fatalf("management status waited on Host database: %+v %v", out, err)
	}
}
