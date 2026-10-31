package storage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	s.Refresh(false)
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.estimate.State == "complete" })
	out, err := s.Snapshot(context.Background(), "")
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
	s.Refresh(false)
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

func TestInventoryProtectsRecoveryAndRefusesUnknownFilesAndChangedTokens(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	old := backup(t, s, "server-old")
	latest := backup(t, s, "server-latest")
	raw, _ := json.Marshal(map[string]any{"schema": 1, "backup": latest})
	write(t, filepath.Join(s.config.Directory, "last-upgrade.json"), raw)
	write(t, filepath.Join(s.config.Directory, "project.txt"), []byte("user work"))
	write(t, filepath.Join(s.config.Directory, "cache", "models.dev.json"), []byte("catalog"))
	write(t, filepath.Join(s.config.Directory, "cache", "unknown-user-file"), []byte("preserve"))
	inventory := s.Inventory(context.Background())
	if inventory.Error != "" {
		t.Fatal(inventory.Error)
	}
	var oldToken, latestToken, modelToken string
	for _, item := range inventory.Items {
		switch item.Name {
		case filepath.Base(old):
			oldToken = item.Token
			if item.Protected {
				t.Fatal("old backup is protected")
			}
		case filepath.Base(latest):
			latestToken = item.Token
			if !item.Protected {
				t.Fatal("last recovery backup is deletable")
			}
		case filepath.Join("cache", "models.dev.json"):
			modelToken = item.Token
		default:
			t.Fatalf("unknown cleanup candidate: %+v", item)
		}
	}
	if oldToken == "" || modelToken == "" || latestToken == "" {
		t.Fatalf("incomplete inventory: %+v", inventory)
	}
	if _, err := s.Cleanup(context.Background(), CleanupRequest{OperationID: "protected", Tokens: []string{latestToken}}); err == nil {
		t.Fatal("protected backup accepted")
	}
	write(t, filepath.Join(s.config.Directory, "cache", "models.dev.json"), []byte("changed catalog"))
	if _, err := s.Cleanup(context.Background(), CleanupRequest{OperationID: "stale", Tokens: []string{modelToken}}); err == nil {
		t.Fatal("stale token accepted")
	}
	op, err := s.Cleanup(context.Background(), CleanupRequest{OperationID: "clear-old", Tokens: []string{oldToken}})
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		op, err = s.CleanupOperation(context.Background(), op.OperationID)
		return err == nil && op.State != "running"
	})
	if op.State != "complete" || op.RemovedBytes == 0 {
		t.Fatalf("cleanup failed: %+v %v", op, err)
	}
	for _, path := range []string{latest, filepath.Join(s.config.Directory, "project.txt"), filepath.Join(s.config.Directory, "cache", "unknown-user-file")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unselected file removed: %s: %v", path, err)
		}
	}
	replay, err := s.Cleanup(context.Background(), CleanupRequest{OperationID: op.OperationID, Tokens: []string{oldToken}})
	if err != nil || replay.RemovedBytes != op.RemovedBytes || replay.State != "complete" {
		t.Fatalf("bad replay: %+v %v", replay, err)
	}
	if _, err := s.Cleanup(context.Background(), CleanupRequest{OperationID: op.OperationID, Tokens: []string{latestToken}}); err == nil {
		t.Fatal("operation identity reused with new payload")
	}
}

func TestBackupSelectionTracksFilesAndDirectoryMembershipInsteadOfDirectoryTimestamps(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	path := backup(t, s, "server-stable")
	selected, err := s.candidate(context.Background(), path, "upgrade_backup", filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-48 * time.Hour)
	for _, directory := range []string{path, filepath.Join(path, "files")} {
		if err = os.Chtimes(directory, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	unchanged, err := s.candidate(context.Background(), path, "upgrade_backup", filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Token != selected.Token || unchanged.Bytes != selected.Bytes {
		t.Fatal("unchanged backup files were invalidated by directory timestamps")
	}
	if err = os.Mkdir(filepath.Join(path, "new-empty-directory"), 0700); err != nil {
		t.Fatal(err)
	}
	addedDirectory, err := s.candidate(context.Background(), path, "upgrade_backup", filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if addedDirectory.Token == unchanged.Token {
		t.Fatal("new directory was not included in selection identity")
	}
	file := filepath.Join(path, "files", "astral.db")
	if err = os.Chtimes(file, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	changedFile, err := s.candidate(context.Background(), path, "upgrade_backup", filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if changedFile.Token == addedDirectory.Token {
		t.Fatal("changed file metadata was not rejected")
	}
	write(t, filepath.Join(path, "files", "new.db"), []byte("new file"))
	addedFile, err := s.candidate(context.Background(), path, "upgrade_backup", filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	if addedFile.Token == changedFile.Token {
		t.Fatal("new file was not included in selection identity")
	}
}

func TestMigrationAndSymlinksProtectApplicationFiles(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	backup(t, s, "old")
	write(t, filepath.Join(s.config.Directory, "migration-state.json"), []byte("invalid recovery journal"))
	outside := filepath.Join(t.TempDir(), "outside")
	write(t, outside, []byte("outside"))
	if err := os.MkdirAll(filepath.Join(s.config.Directory, "cache"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.config.Directory, "cache", "models.dev.json")); err != nil {
		t.Skip(err)
	}
	inventory := s.Inventory(context.Background())
	if inventory.Error == "" {
		t.Fatal("malformed recovery journal was not reported")
	}
	for _, item := range inventory.Items {
		if item.Kind == "upgrade_backup" && !item.Protected {
			t.Fatal("backup not protected during unknown migration")
		}
		if item.Kind == "model_cache" {
			t.Fatal("symlink cache adopted")
		}
	}
	physical := s.physical(context.Background())
	if len(physical.Errors) == 0 {
		t.Fatal("incomplete physical measurement was hidden")
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "outside" {
		t.Fatal("external file changed")
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
	s, store := fixture(t, h)
	s.config.Busy = busy.Load
	s.RequestReclaim()
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.reclaim.State == "waiting" })
	if attempt.Load() != 0 {
		t.Fatal("reclaim ran while Bot was active")
	}
	busy.Store(false)
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.reclaim.State == "failed" })
	var persisted Reclaim
	if err := store.Maintenance(context.Background(), "storage_reclaim", "owner", &persisted); err != nil || persisted.State != "failed" || persisted.Error == "" {
		t.Fatalf("failure was not durable: %+v %v", persisted, err)
	}
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

func TestRefreshCancelDoesNotKeepWorkerRunning(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	h := &historyStore{load: func(ctx context.Context, _ string) (core.RuntimeState, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return core.RuntimeState{}, ctx.Err()
	}}
	s, store := fixture(t, h)
	save(t, store, "sessions", "s", domain.Session{AgentID: "a"})
	s.Refresh(false)
	<-started
	s.Refresh(true)
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.estimate.State == "canceled" })
}

func TestExternalCleanupIsCountedAndUsesDurableOperationIdentity(t *testing.T) {
	s, _ := fixture(t, &historyStore{})
	var calls atomic.Int64
	s.config.ExternalCleanup = func(context.Context) ([]CleanupItem, error) {
		return []CleanupItem{{Token: "desktop:old-installer", Kind: "update_cache", Name: "Astral-old.zip", Bytes: 654}}, nil
	}
	s.config.CleanExternal = func(_ context.Context, id, token string) (int64, error) {
		calls.Add(1)
		if id != "external-op" || token != "desktop:old-installer" {
			t.Errorf("lost identity: %q %q", id, token)
		}
		return 654, nil
	}
	if physical := s.physical(context.Background()); physical.CacheBytes != 654 {
		t.Fatalf("external cache omitted: %+v", physical)
	}
	op, err := s.Cleanup(context.Background(), CleanupRequest{OperationID: "external-op", Tokens: []string{"desktop:old-installer"}})
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		op, err = s.CleanupOperation(context.Background(), op.OperationID)
		return err == nil && op.State == "complete"
	})
	if op.RemovedBytes != 654 {
		t.Fatalf("external outcome lost: %+v", op)
	}
	if _, err = s.Cleanup(context.Background(), CleanupRequest{OperationID: "external-op", Tokens: []string{"desktop:old-installer"}}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("replay repeated external deletion")
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

func TestInterruptedBackupCleanupResumesWithoutDeletedManifest(t *testing.T) {
	s, store := fixture(t, &historyStore{})
	path := backup(t, s, "server-interrupted")
	item, err := s.candidate(context.Background(), path, "upgrade_backup", filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	item.Started = true
	record := cleanupRecord{Meta: domain.Meta{ID: "interrupted"}, CleanupOperation: CleanupOperation{OperationID: "interrupted", State: "failed", RemovedTokens: []string{}}, Tokens: []string{item.Token}, Candidates: []candidate{item}}
	if err = s.saveCleanup(context.Background(), &record); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(path, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted := New(context.Background(), Config{Directory: s.config.Directory, Store: store, Core: &historyStore{}})
	defer restarted.Close(context.Background())
	if physical := restarted.physical(context.Background()); physical.BackupBytes == 0 {
		t.Fatal("partially deleted backup disappeared from physical accounting")
	}
	op, err := restarted.RetryCleanup(context.Background(), "interrupted")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		op, err = restarted.CleanupOperation(context.Background(), op.OperationID)
		return err == nil && op.State != "running"
	})
	if op.State != "complete" || op.RemovedBytes == 0 {
		t.Fatalf("interrupted backup did not resume: %+v %v", op, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial backup remains")
	}
}

func TestBusyLaterDatabaseDoesNotRepeatCompletedCoreVacuum(t *testing.T) {
	var calls atomic.Int64
	var busy atomic.Bool
	busy.Store(true)
	s, _ := fixture(t, &historyStore{reclaim: func(context.Context) error { calls.Add(1); return nil }})
	s.config.Reclaimers = []func(context.Context) error{func(context.Context) error {
		if busy.Load() {
			return core.ErrBusy
		}
		return nil
	}}
	s.RequestReclaim()
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.reclaim.State == "waiting" })
	busy.Store(false)
	await(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.reclaim.State == "complete" })
	if calls.Load() != 1 {
		t.Fatalf("completed Core vacuum was repeated %d times", calls.Load())
	}
}

func TestManagementStatusDoesNotQueueBehindHostMaintenanceConnection(t *testing.T) {
	s, store := fixture(t, &historyStore{})
	save(t, store, "agents", "bot", domain.Agent{Name: "Cached Bot"})
	if _, err := s.Snapshot(context.Background(), ""); err != nil {
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
	out, err := s.Snapshot(ctx, "")
	if err != nil || out.Reclaim.State != "running" || len(out.Agents) != 1 || out.Agents[0].Name != "Cached Bot" {
		t.Fatalf("management status waited on Host database: %+v %v", out, err)
	}
}
