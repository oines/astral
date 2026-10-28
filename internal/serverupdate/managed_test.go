package serverupdate

import (
	"context"
	"errors"
	"fmt"
	"github.com/oines/astral/internal/domain"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixture struct {
	m            *managed
	mu           sync.Mutex
	commands     [][]string
	latestCalls  atomic.Int32
	fetchCalls   atomic.Int32
	installCalls atomic.Int32
	target       releaseTarget
	gate         <-chan struct{}
	job          string
	jobPID       string
	jobLoad      string
	jobErr       error
	version      string
	install      func(context.Context, string, []string, io.Writer, io.Writer) error
	fetchErr     error
	launchErr    bool
}

func managedFixture(t *testing.T) *fixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Linux managed-service fixture uses Unix installation symlinks")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "Server 中文 space $%")
	for _, dir := range []string{root, filepath.Join(root, "data/server"), filepath.Join(root, "releases/v0.1.0/bin"), filepath.Join(root, "config/systemd/user")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(root, "releases/v0.1.0/bin/astral")
	if err := os.WriteFile(executable, []byte("fixture, never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "releases/v0.1.0"), filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	f := &fixture{target: releaseTarget{Version: "0.1.2", InstallerSHA: strings.Repeat("a", 64)}, job: "active", jobPID: "0", jobLoad: "loaded", version: "0.1.0"}
	m := &managed{root: root, data: filepath.Join(root, "data/server"), executable: executable, version: "0.1.0"}
	f.m = m
	m.deps = dependencies{platform: "linux", uid: 42, pid: 123, unitPath: filepath.Join(root, "config/systemd/user", serviceName),
		owned: func(path string, uid int) bool {
			info, e := os.Lstat(path)
			return e == nil && uid == 42 && info.Mode()&os.ModeSymlink == 0 && info.Mode().Perm()&0022 == 0
		},
		args: func(int) ([]string, error) { return installationArguments(root), nil },
	}
	m.deps.command = func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.commands = append(f.commands, append([]string{name}, args...))
		if name == "systemd-run" {
			if f.launchErr {
				return nil, errors.New("lost manager response")
			}
			return nil, nil
		}
		if name == filepath.Join(root, "current/bin/astral") {
			return []byte(f.version), nil
		}
		if len(args) > 2 && strings.HasPrefix(args[2], "astral-server-update-") {
			return []byte(fmt.Sprintf("LoadState=%s\nActiveState=%s\nMainPID=%s\n", f.jobLoad, f.job, f.jobPID)), f.jobErr
		}
		return []byte(fmt.Sprintf("Id=%s\nLoadState=loaded\nActiveState=active\nMainPID=123\nFragmentPath=%s\nDropInPaths=\nExecStart={ path=%s/current/bin/astral ; argv[]=%s ; }\n", serviceName, m.deps.unitPath, root, strings.Join(installationArguments(root), " "))), nil
	}
	m.deps.latest = func(ctx context.Context) (releaseTarget, error) {
		f.latestCalls.Add(1)
		if f.gate != nil {
			select {
			case <-f.gate:
			case <-ctx.Done():
				return releaseTarget{}, ctx.Err()
			}
		}
		return f.target, nil
	}
	m.deps.fetch = func(_ context.Context, target releaseTarget, path string) error {
		f.fetchCalls.Add(1)
		if target != f.target {
			return errors.New("target changed")
		}
		if f.fetchErr != nil {
			return f.fetchErr
		}
		return os.WriteFile(path, []byte("verified fixture installer, never executed"), 0600)
	}
	m.deps.install = func(ctx context.Context, name string, args []string, out, stderr io.Writer) error {
		f.installCalls.Add(1)
		if f.install != nil {
			return f.install(ctx, name, args, out, stderr)
		}
		return nil
	}
	if err := os.WriteFile(m.deps.unitPath, []byte(unitDefinition(root)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(root, "installation.json"), installation{Schema: 1, Root: root, DataDirectory: m.data, UID: 42, Service: serviceName, UnitPath: m.deps.unitPath}); err != nil {
		t.Fatal(err)
	}
	return f
}
func pending(t *testing.T, f *fixture) State {
	t.Helper()
	state, err := f.m.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		state, err = f.m.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		f.m.mu.Lock()
		prepared := !f.m.preparing
		f.m.mu.Unlock()
		if state.Phase != "checking" && prepared {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("update preparation did not finish")
	return state
}
func TestUnknownInstallationNeverRunsCommands(t *testing.T) {
	m := NewManaged("", t.TempDir(), "", "0.1.0")
	state, err := m.Apply(context.Background())
	if err != nil || state.Manager != "manual" || state.Phase != "unavailable" {
		t.Fatalf("unmanaged installation adopted: %+v %v", state, err)
	}
}
func TestCapabilityRejectsForeignIdentityAndModifiedUnit(t *testing.T) {
	for _, change := range []func(*fixture){
		func(f *fixture) { f.m.deps.platform = "windows" },
		func(f *fixture) { f.m.deps.uid = 43 },
		func(f *fixture) { f.m.data = filepath.Join(f.m.root, "other-data") },
		func(f *fixture) { f.m.deps.pid = 124 },
		func(f *fixture) { f.m.executable = filepath.Join(f.m.root, "unmanaged") },
		func(f *fixture) {
			f.m.deps.args = func(int) ([]string, error) { return []string{"astral", "serve", "--data", "/other"}, nil }
		},
		func(f *fixture) {
			_ = os.WriteFile(f.m.deps.unitPath, []byte(unitDefinition(f.m.root)+"# changed\n"), 0600)
		},
		func(f *fixture) { _ = os.Remove(filepath.Join(f.m.root, "installation.json")) },
	} {
		f := managedFixture(t)
		change(f)
		state, err := f.m.Apply(context.Background())
		if err != nil || state.Phase != "unavailable" || state.Manager != "manual" {
			t.Fatalf("foreign installation adopted: %+v %v", state, err)
		}
		if f.latestCalls.Load() != 0 || f.fetchCalls.Load() != 0 || f.installCalls.Load() != 0 {
			t.Fatal("untrusted installation contacted the updater")
		}
	}
}
func TestChecksOnlyResolveMetadataAndCanCheckAgain(t *testing.T) {
	f := managedFixture(t)
	for _, version := range []string{"0.1.2", "0.1.4", "0.1.0", "0.0.9"} {
		f.target.Version = version
		state, err := f.m.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		comparison := version > "0.1.0"
		if comparison && state.Phase != "available" || !comparison && state.Phase != "current" {
			t.Fatalf("wrong check state: %+v", state)
		}
	}
	if f.latestCalls.Load() != 4 || f.fetchCalls.Load() != 0 || f.installCalls.Load() != 0 {
		t.Fatal("check downloaded or installed a package")
	}
}
func TestApplyReturnsBeforeNetworkAndDeduplicates(t *testing.T) {
	f := managedFixture(t)
	gate := make(chan struct{})
	f.gate = gate
	first, err := f.m.Apply(context.Background())
	if err != nil || first.Phase != "checking" {
		t.Fatalf("apply did not return checking: %+v %v", first, err)
	}
	second, err := f.m.Apply(context.Background())
	if err != nil || second.OperationID != first.OperationID {
		t.Fatal("concurrent apply created another operation")
	}
	if state, e := f.m.State(context.Background()); e != nil || state.Phase != "checking" {
		t.Fatalf("state polling blocked during lookup: %+v %v", state, e)
	}
	close(gate)
	state := pending(t, f)
	if state.OperationID != first.OperationID || state.OperationVersion != "0.1.2" || f.latestCalls.Load() != 1 {
		t.Fatal("Latest was not locked once for the operation")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var launch []string
	for _, args := range f.commands {
		if args[0] == "systemd-run" {
			launch = args
		}
	}
	joined := strings.Join(launch, "\n")
	for _, want := range []string{"--user", "--collect", "--property=RuntimeMaxSec=3900", "--property=KillMode=mixed", "--property=TimeoutStopSec=180", f.m.executable, "--managed-helper", first.OperationID} {
		if !strings.Contains(joined, want) {
			t.Fatalf("independent bounded launch omitted %s: %s", want, joined)
		}
	}
}
func TestRunnerWritesTerminalOnlyAfterInstallerCleanupAndRetainsIdentity(t *testing.T) {
	f := managedFixture(t)
	state := pending(t, f)
	f.install = func(_ context.Context, name string, args []string, _ io.Writer, _ io.Writer) error {
		if name != "/bin/bash" || args[1] != "--root" || args[2] != f.m.root || args[3] != "--tag" || args[4] != "v0.1.2" {
			t.Fatalf("unlocked installer invocation: %s %v", name, args)
		}
		var op operation
		if err := readJSON(f.m.path(), &op); err != nil || op.OperationState != "running" || op.Phase != "installing" {
			t.Fatal("reported terminal before installer finished")
		}
		if strings.HasPrefix(filepath.Dir(args[0]), f.m.data) {
			t.Fatal("update state was put inside migrated data")
		}
		return nil
	}
	if err := f.m.run(context.Background(), state.OperationID, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.m.root, "updates", state.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("installer staging was retained after success")
	}
	next := &managed{root: f.m.root, data: f.m.data, executable: f.m.executable, version: "0.1.2", deps: f.m.deps}
	resumed, err := next.State(context.Background())
	if err != nil || resumed.OperationID != state.OperationID || resumed.OperationState != "complete" || resumed.OperationVersion != "0.1.2" || resumed.CurrentVersion != "0.1.2" || resumed.Version != "0.1.2" {
		t.Fatalf("new Server lost completion: %+v %v", resumed, err)
	}
	if f.latestCalls.Load() != 1 || f.fetchCalls.Load() != 1 || f.installCalls.Load() != 1 {
		t.Fatal("runner resolved Latest again or executed twice")
	}
}
func TestFailuresFinishAfterRollbackAndRemainVisibleToAutomaticChecks(t *testing.T) {
	f := managedFixture(t)
	state := pending(t, f)
	recovered := false
	f.install = func(context.Context, string, []string, io.Writer, io.Writer) error {
		recovered = true
		return errors.New("fixture rollback finished")
	}
	if err := f.m.run(context.Background(), state.OperationID, io.Discard, io.Discard); err == nil {
		t.Fatal("installer failure was accepted")
	}
	if !recovered {
		t.Fatal("installer recovery was not awaited")
	}
	current, err := f.m.State(context.Background())
	if err != nil || current.Phase != "error" || current.Retry != "update" || current.OperationState != "failed" || current.OperationID != state.OperationID {
		t.Fatalf("failure lost: %+v %v", current, err)
	}
	if _, err = os.Stat(filepath.Join(f.m.root, "updates", state.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed installer staging was not cleaned")
	}
	if _, err = f.m.CheckAutomatic(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.latestCalls.Load() != 1 {
		t.Fatal("automatic check hid the failed update")
	}
	checked, err := f.m.Check(context.Background())
	if err != nil || checked.Phase != "available" || checked.OperationState != "failed" || checked.OperationVersion != "0.1.2" {
		t.Fatalf("manual check changed the operation outcome: %+v %v", checked, err)
	}
}
func TestFailedDownloadAndAlreadyInstalledTargetsNeverStartInstaller(t *testing.T) {
	for _, kind := range []string{"checksum", "same", "newer"} {
		f := managedFixture(t)
		state := pending(t, f)
		if kind == "checksum" {
			f.fetchErr = errors.New("checksum mismatch")
		} else {
			f.version = "0.1.2"
			if kind == "newer" {
				f.version = "0.1.3"
			}
		}
		err := f.m.run(context.Background(), state.OperationID, io.Discard, io.Discard)
		if kind == "same" && err != nil || kind != "same" && err == nil {
			t.Fatalf("wrong %s result: %v", kind, err)
		}
		if f.installCalls.Load() != 0 {
			t.Fatal("failed/downloaded-old target executed installer")
		}
	}
}
func TestAmbiguousLaunchAndAbandonedJob(t *testing.T) {
	f := managedFixture(t)
	f.launchErr = true
	state := pending(t, f)
	if state.OperationState != "running" {
		t.Fatal("lost manager acknowledgement erased a running update")
	}
	var op operation
	if err := readJSON(f.m.path(), &op); err != nil {
		t.Fatal(err)
	}
	op.StartedAt = time.Now().Add(-3 * time.Minute)
	if err := writeJSON(f.m.path(), op); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.job = "inactive"
	f.mu.Unlock()
	result, err := f.m.State(context.Background())
	if err != nil || result.OperationState != "failed" || result.OperationID != state.OperationID {
		t.Fatalf("abandoned operation did not fail: %+v %v", result, err)
	}
}

func TestAbandonmentRequiresConfirmedHelperExit(t *testing.T) {
	for _, tc := range []struct {
		name, active, load, pid string
		queryErr                error
		failed                  bool
	}{
		{name: "rollback grace", active: "deactivating", load: "loaded", pid: "0"},
		{name: "active", active: "active", load: "loaded", pid: "321"},
		{name: "activating", active: "activating", load: "loaded", pid: "0"},
		{name: "manager unavailable", active: "inactive", load: "loaded", pid: "0", queryErr: errors.New("manager disconnected")},
		{name: "failed but process alive", active: "failed", load: "loaded", pid: "321"},
		{name: "missing process information", active: "inactive", load: "loaded"},
		{name: "unknown state", active: "unknown", load: "loaded", pid: "0"},
		{name: "inactive", active: "inactive", load: "loaded", pid: "0", failed: true},
		{name: "failed", active: "failed", load: "loaded", pid: "0", failed: true},
		{name: "not found", load: "not-found", pid: "0", failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := managedFixture(t)
			state := pending(t, f)
			op, err := f.m.load()
			if err != nil {
				t.Fatal(err)
			}
			op.StartedAt = time.Now().Add(-3 * time.Minute)
			if err = writeJSON(f.m.path(), op); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.job, f.jobLoad, f.jobPID, f.jobErr = tc.active, tc.load, tc.pid, tc.queryErr
			f.mu.Unlock()
			got, err := f.m.State(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			want := "running"
			if tc.failed {
				want = "failed"
			}
			if got.OperationID != state.OperationID || got.OperationState != want {
				t.Fatalf("premature or missing terminal state: %+v", got)
			}
			if !tc.failed {
				applied, e := f.m.Apply(context.Background())
				if e != nil || applied.OperationID != state.OperationID {
					t.Fatalf("uncertain job allowed another update: %+v %v", applied, e)
				}
			}
		})
	}
}

func TestOldRunnerCannotOverwriteAnotherOperation(t *testing.T) {
	f := managedFixture(t)
	state := pending(t, f)
	newOperation := operation{Schema: 1, State: State{Manager: "systemd-user", CurrentVersion: "0.1.2", Phase: "checking", OperationID: strings.Repeat("b", 32), OperationState: "running"}, StartedAt: time.Now()}
	f.install = func(context.Context, string, []string, io.Writer, io.Writer) error {
		return writeJSON(f.m.path(), newOperation)
	}
	err := f.m.run(context.Background(), state.OperationID, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "superseded") {
		t.Fatalf("superseded runner did not stop: %v", err)
	}
	var got operation
	if err = readJSON(f.m.path(), &got); err != nil {
		t.Fatal(err)
	}
	if got.OperationID != newOperation.OperationID || got.Phase != "checking" || got.OperationState != "running" {
		t.Fatalf("old helper overwrote a newer operation: %+v", got)
	}
	if _, err = os.Stat(filepath.Join(f.m.root, "updates", state.OperationID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("superseded helper did not finish its own cleanup")
	}
}

func TestExplicitApplyDuringMetadataLookupIsNotSilentlyDiscarded(t *testing.T) {
	f := managedFixture(t)
	gate := make(chan struct{})
	f.gate = gate
	done := make(chan error, 1)
	go func() { _, err := f.m.Check(context.Background()); done <- err }()
	deadline := time.Now().Add(time.Second)
	for f.latestCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	state, err := f.m.Apply(context.Background())
	var conflict *domain.Error
	if !errors.As(err, &conflict) || conflict.Status != 409 || state.OperationID != "" {
		t.Fatalf("explicit update was silently treated as a check: %+v %v", state, err)
	}
	close(gate)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	state = pending(t, f)
	if state.OperationID == "" || state.OperationState != "running" || f.latestCalls.Load() != 2 {
		t.Fatalf("retry did not resolve fresh Latest for its own operation: %+v", state)
	}
}
