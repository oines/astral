package serverupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/releasefeed"
)

const serviceName = "astral-server.service"
const jobBudget = 60 * time.Minute

type installation struct {
	Schema        int    `json:"schema"`
	Root          string `json:"root"`
	DataDirectory string `json:"data_directory"`
	UID           int    `json:"uid"`
	Service       string `json:"service"`
	UnitPath      string `json:"unit_path"`
}
type operation struct {
	Schema int `json:"schema"`
	State
	InstallerSHA string    `json:"installer_sha256,omitempty"`
	StartedAt    time.Time `json:"started_at"`
}
type dependencies struct {
	command  func(context.Context, string, ...string) ([]byte, error)
	install  func(context.Context, string, []string, io.Writer, io.Writer) error
	latest   func(context.Context) (releaseTarget, error)
	fetch    func(context.Context, releaseTarget, string) error
	owned    func(string, int) bool
	args     func(int) ([]string, error)
	platform string
	uid, pid int
	unitPath string
}
type managed struct {
	mu                              sync.Mutex
	root, data, executable, version string
	deps                            dependencies
	preparing                       bool
}

// NewManaged recognizes only this user's explicitly installer-owned service.
// Missing/foreign installations remain manual; no unit is adopted by name.
func NewManaged(root, data, executable, version string) Controller {
	home, _ := os.UserHomeDir()
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	}
	m := &managed{root: root, data: data, executable: executable, version: version}
	m.deps = dependencies{command: command, install: installCommand, latest: officialLatest, fetch: fetchInstaller, owned: ownedFile, args: processArguments, platform: runtime.GOOS, uid: os.Getuid(), pid: os.Getpid(), unitPath: filepath.Join(config, "systemd/user", serviceName)}
	return m
}

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return exec.CommandContext(bounded, name, args...).CombinedOutput()
}
func cleanAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
func (m *managed) validate(ctx context.Context, running bool) error {
	if m.deps.platform != "linux" || !cleanAbsolute(m.root) || m.data != filepath.Join(m.root, "data/server") {
		return errors.New("not an official Linux user-service installation")
	}
	root, err := filepath.EvalSymlinks(m.root)
	if err != nil || root != m.root || !m.deps.owned(m.root, m.deps.uid) {
		return errors.New("installation directory ownership is invalid")
	}
	actualData, dataErr := filepath.EvalSymlinks(m.data)
	if dataErr != nil || actualData != m.data || !m.deps.owned(m.data, m.deps.uid) {
		return errors.New("the Server data directory is not the matching owned installation directory")
	}
	var marker installation
	markerPath := filepath.Join(m.root, "installation.json")
	if !m.deps.owned(markerPath, m.deps.uid) || readJSON(markerPath, &marker) != nil || marker.Schema != 1 || marker.Root != m.root || marker.DataDirectory != m.data || marker.UID != m.deps.uid || marker.Service != serviceName || marker.UnitPath != m.deps.unitPath {
		return errors.New("the official installation marker does not match this user, root and service")
	}
	unit, err := os.ReadFile(marker.UnitPath)
	if err != nil || !m.deps.owned(marker.UnitPath, m.deps.uid) || string(unit) != unitDefinition(m.root) {
		return errors.New("the user service definition is not the matching official installer definition")
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(m.root, "current/bin/astral"))
	if err != nil || !strings.HasPrefix(expected, filepath.Join(m.root, "releases")+string(os.PathSeparator)) {
		return errors.New("current does not select an installed release")
	}
	if running {
		actual, e := filepath.EvalSymlinks(m.executable)
		if e != nil || actual != expected {
			return errors.New("the running executable is not this installation's current program")
		}
	}
	raw, err := m.deps.command(ctx, "systemctl", "--user", "show", serviceName, "--property=Id,LoadState,ActiveState,MainPID,FragmentPath,DropInPaths,ExecStart", "--no-pager")
	if err != nil {
		return errors.New("the systemd user manager could not verify this installation")
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if values["Id"] != serviceName || values["LoadState"] != "loaded" || values["FragmentPath"] != marker.UnitPath || values["DropInPaths"] != "" || !strings.Contains(values["ExecStart"], "--installation-root") {
		return errors.New("the loaded user service does not match the installer")
	}
	if running {
		if values["ActiveState"] != "active" || values["MainPID"] != strconv.Itoa(m.deps.pid) {
			return errors.New("this Server is not the current systemd user-service process")
		}
		args, e := m.deps.args(m.deps.pid)
		if e != nil || !sameArguments(args, installationArguments(m.root)) {
			return errors.New("the service process arguments do not match its installation and data directory")
		}
	}
	return nil
}
func sameArguments(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func installationArguments(root string) []string {
	return []string{root + "/current/bin/astral", "serve", "--installation-root", root, "--data", root + "/data/server", "--computers-dir", root + "/data/computers", "--distribution-dir", root + "/current/distribution", "--ready-file", root + "/run/ready.json"}
}
func unitDefinition(root string) string {
	quote := func(s string) string {
		s = strings.ReplaceAll(s, "\\", "\\\\")
		s = strings.ReplaceAll(s, "\"", "\\\"")
		s = strings.ReplaceAll(s, "%", "%%")
		return "\"" + s + "\""
	}
	args := installationArguments(root)
	for i := range args {
		args[i] = quote(args[i])
	}
	return "# Astral Server installer root: " + root + "\n[Unit]\nDescription=Astral Server\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\nWorkingDirectory=/\nExecStart=:" + strings.Join(args, " ") + "\nRestart=on-failure\nRestartSec=3\nTimeoutStopSec=30\nUMask=0077\n\n[Install]\nWantedBy=default.target\n"
}
func (m *managed) unavailable() State {
	return State{Phase: "unavailable", CurrentVersion: m.version, Manager: "manual", Unavailable: "unmanaged"}
}
func (m *managed) path() string { return filepath.Join(m.root, "updates/state.json") }
func (m *managed) load() (operation, error) {
	var op operation
	err := readJSON(m.path(), &op)
	if errors.Is(err, os.ErrNotExist) {
		return operation{Schema: 1, State: State{Phase: "idle", CurrentVersion: m.version, Manager: "systemd-user"}}, nil
	}
	if err != nil {
		return op, err
	}
	if op.Schema != 1 || op.Manager != "systemd-user" || (op.OperationID != "" && !validOperation(op.OperationID)) {
		return op, errors.New("invalid managed update state")
	}
	if m.version != "" {
		op.CurrentVersion = m.version
	}
	return op, nil
}
func validOperation(value string) bool {
	raw, e := hex.DecodeString(value)
	return e == nil && len(raw) == 16 && hex.EncodeToString(raw) == value
}
func (m *managed) state(ctx context.Context) (State, error) {
	if m.validate(ctx, true) != nil {
		return m.unavailable(), nil
	}
	op, err := m.load()
	if err != nil {
		return State{}, err
	}
	if op.OperationState == "running" && !m.preparing {
		// A newly detached job may still be queued by the user manager.
		if m.jobStopped(ctx, op.OperationID) && time.Since(op.StartedAt) > 2*time.Minute {
			// The helper may have committed its result while systemd was queried.
			current, e := m.load()
			if e != nil {
				return State{}, e
			}
			if current.OperationID != op.OperationID || current.OperationState != "running" {
				return current.State, nil
			}
			op.Phase, op.Error, op.Retry, op.OperationState = "error", "The independent update job stopped before reporting a result. Inspect its systemd user journal.", "update", "failed"
			if err = writeJSON(m.path(), op); err != nil {
				return State{}, err
			}
		}
	}
	return op.State, nil
}
func (m *managed) jobStopped(ctx context.Context, id string) bool {
	raw, err := m.deps.command(ctx, "systemctl", "--user", "show", jobName(id), "--property=LoadState,ActiveState,MainPID")
	if err != nil {
		// A failed manager query cannot prove that the helper has exited.
		return false
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = value
		}
	}
	if values["MainPID"] != "0" {
		return false
	}
	// In particular, deactivating includes the installer's TERM rollback grace.
	active := values["ActiveState"]
	return active == "inactive" || active == "failed" || active == "" && values["LoadState"] == "not-found"
}
func (m *managed) State(ctx context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state(ctx)
}
func (m *managed) Check(ctx context.Context) (State, error)          { return m.check(ctx, false) }
func (m *managed) CheckAutomatic(ctx context.Context) (State, error) { return m.check(ctx, true) }

// CheckAutomatic preserves a failed user update until an explicit user action.
func CheckAutomatic(ctx context.Context, c Controller) (State, error) {
	if automatic, ok := c.(interface {
		CheckAutomatic(context.Context) (State, error)
	}); ok {
		return automatic.CheckAutomatic(ctx)
	}
	return c.Check(ctx)
}
func (m *managed) check(ctx context.Context, automatic bool) (State, error) {
	m.mu.Lock()
	state, err := m.state(ctx)
	if err != nil || state.Manager != "systemd-user" {
		m.mu.Unlock()
		return state, err
	}
	if m.preparing || state.OperationState == "running" || automatic && state.Phase == "error" && state.Retry == "update" {
		m.mu.Unlock()
		return state, nil
	}
	op, err := m.load()
	if err != nil {
		m.mu.Unlock()
		return State{}, err
	}
	op.Phase, op.Error, op.Retry = "checking", "", ""
	if err = writeJSON(m.path(), op); err != nil {
		m.mu.Unlock()
		return State{}, err
	}
	m.preparing = true
	m.mu.Unlock()
	target, err := m.deps.latest(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	defer func() { m.preparing = false }()
	if err != nil {
		op.Phase, op.Error, op.Retry = "error", err.Error(), "check"
	} else {
		comparison, e := releasefeed.Compare(target.Version, m.version)
		if e != nil {
			return State{}, e
		}
		op.Phase, op.Error, op.Retry, op.Version = "current", "", "", ""
		if comparison > 0 {
			op.Phase, op.Version = "available", target.Version
		}
	}
	if e := writeJSON(m.path(), op); e != nil {
		return State{}, e
	}
	return op.State, nil
}
func jobName(id string) string { return "astral-server-update-" + id + ".service" }
func (m *managed) Apply(ctx context.Context) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state, err := m.state(ctx)
	if err != nil || state.Manager != "systemd-user" {
		return state, err
	}
	if state.OperationState == "running" {
		return state, nil
	}
	if m.preparing {
		return state, domain.Conflict("A metadata check is still running. Wait for it to finish, then request the update again.")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return State{}, err
	}
	op := operation{Schema: 1, State: State{Phase: "checking", CurrentVersion: m.version, Manager: "systemd-user", OperationID: hex.EncodeToString(nonce[:]), OperationState: "running"}, StartedAt: time.Now().UTC()}
	if err = writeJSON(m.path(), op); err != nil {
		return State{}, err
	}
	m.preparing = true
	go m.prepare(op)
	return op.State, nil
}
func (m *managed) prepare(op operation) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	defer func() { m.mu.Lock(); m.preparing = false; m.mu.Unlock() }()
	fail := func(err error) {
		op.Phase, op.Error, op.Retry, op.OperationState = "error", err.Error(), "update", "failed"
		_ = writeJSON(m.path(), op)
	}
	target, err := m.deps.latest(ctx)
	if err != nil {
		fail(err)
		return
	}
	comparison, err := releasefeed.Compare(target.Version, m.version)
	if err != nil {
		fail(err)
		return
	}
	op.Version, op.OperationVersion = target.Version, target.Version
	if comparison <= 0 {
		op.Version, op.OperationVersion = m.version, m.version
		op.Phase, op.OperationState = "current", "complete"
		_ = writeJSON(m.path(), op)
		return
	}
	op.InstallerSHA = target.InstallerSHA
	op.Phase = "downloading"
	if err = writeJSON(m.path(), op); err != nil {
		return
	}
	executable, err := filepath.EvalSymlinks(m.executable)
	if err != nil {
		fail(err)
		return
	}
	args := []string{"--user", "--collect", "--unit=" + jobName(op.OperationID), "--description=Astral Server update", "--property=Type=exec", "--property=RuntimeMaxSec=3900", "--property=KillMode=mixed", "--property=TimeoutStopSec=180", "--setenv=HOME=" + os.Getenv("HOME"), "--setenv=XDG_CONFIG_HOME=" + filepath.Dir(filepath.Dir(filepath.Dir(m.deps.unitPath))), "--", executable, "server", "update", "--root", m.root, "--operation", op.OperationID, "--managed-helper"}
	if _, err = m.deps.command(ctx, "systemd-run", args...); err != nil {
		// Launch responses can be lost after the independent process has started.
		if m.jobStopped(ctx, op.OperationID) {
			latest, readErr := m.load()
			if readErr == nil && latest.OperationID == op.OperationID && latest.OperationState == "running" {
				fail(errors.New("the independent Server update job could not be started"))
			}
		}
	}
}

func readJSON(file string, value any) error {
	raw, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
func writeJSON(file string, value any) error {
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".update-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(raw, '\n')); err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}

// Run is invoked only by a separate bounded systemd user job. The installer
// owns service replacement and rollback; its complete exit precedes terminal state.
func Run(ctx context.Context, root, id string, out, stderr io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	controller := NewManaged(root, filepath.Join(root, "data/server"), executable, "").(*managed)
	return controller.run(ctx, id, out, stderr)
}
func (m *managed) run(ctx context.Context, id string, out, stderr io.Writer) (result error) {
	if !validOperation(id) {
		return errors.New("invalid Server update operation")
	}
	if err := m.validate(ctx, false); err != nil {
		return err
	}
	op, err := m.load()
	if err != nil {
		return err
	}
	if op.OperationID != id || op.OperationState != "running" || op.Phase != "downloading" {
		return errors.New("server update operation is not pending")
	}
	if comparison, e := releasefeed.Compare(op.OperationVersion, op.CurrentVersion); e != nil || comparison <= 0 {
		return errors.New("server update target is not newer")
	}
	bounded, cancel := context.WithTimeout(ctx, jobBudget)
	defer cancel()
	stage := filepath.Join(m.root, "updates", id)
	created := false
	defer func() {
		if created {
			result = errors.Join(result, os.RemoveAll(stage))
		}
		var current operation
		if err := readJSON(m.path(), &current); err != nil {
			result = errors.Join(result, err)
			return
		}
		if current.OperationID != id {
			result = errors.Join(result, errors.New("server update operation was superseded; preserving its current result"))
			return
		}
		op.Phase, op.OperationState, op.Error, op.Retry = "current", "complete", "", ""
		if result != nil {
			op.Phase, op.OperationState, op.Error, op.Retry = "error", "failed", result.Error(), "update"
		}
		// Always persist after installer traps and staging cleanup have completed.
		result = errors.Join(result, writeJSON(m.path(), op))
	}()
	if err = os.Mkdir(stage, 0700); err != nil {
		return err
	}
	created = true
	actual, err := m.deps.command(bounded, filepath.Join(m.root, "current/bin/astral"), "version")
	if err != nil {
		return errors.New("cannot verify the currently installed Server version")
	}
	current := strings.TrimSpace(string(actual))
	comparison, err := releasefeed.Compare(op.OperationVersion, current)
	if err != nil {
		return err
	}
	if comparison < 0 {
		return errors.New("a newer Server is already installed; refusing a downgrade")
	}
	if comparison == 0 {
		op.CurrentVersion = current
		return nil
	}
	installer := filepath.Join(stage, "install-server.sh")
	if err = m.deps.fetch(bounded, releaseTarget{Version: op.OperationVersion, InstallerSHA: op.InstallerSHA}, installer); err != nil {
		return err
	}
	op.Phase = "installing"
	if err = writeJSON(m.path(), op); err != nil {
		return err
	}
	fmt.Fprintln(out, "Installing official Astral Server", op.OperationVersion)
	if err = m.deps.install(bounded, "/bin/bash", []string{installer, "--root", m.root, "--tag", "v" + op.OperationVersion}, out, stderr); err != nil {
		return fmt.Errorf("server installation failed; inspect the update job journal for rollback diagnostics: %w", err)
	}
	return nil
}
