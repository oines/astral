// Package daemonservice supervises enrolled Daemons using the current user's
// native service manager. It never elevates, installs a system service, or falls
// back to an unmonitored background process.
package daemonservice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

type Spec struct {
	Executable      string `json:"executable"`
	Credentials     string `json:"credentials"`
	EnvironmentPATH string `json:"environment_path"`
}

type Status struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Backend     string `json:"backend"`
	State       string `json:"state"`
	PID         int    `json:"pid,omitempty"`
	LogPath     string `json:"log_path"`
	Detail      string `json:"detail,omitempty"`
}

type commandResult struct {
	output string
	code   int
}
type commandRunner func(context.Context, string, ...string) (commandResult, error)

type Manager struct {
	root, home, platform, uid, id, name, definition, target, configHome string
	run                                                                 commandRunner
}

func New(root string) (*Manager, error) {
	current, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("identify current service user: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return newManager(root, home, runtime.GOOS, current.Uid, nativeRunner(current.Uid))
}

func newManager(root, home, platform, uid string, run commandRunner) (*Manager, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, value := range []string{absolute, home, uid} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("service path or identity contains a control character")
		}
	}
	hash := sha256.Sum256([]byte(uid + "\x00" + absolute))
	id := hex.EncodeToString(hash[:6])
	m := &Manager{root: absolute, home: home, platform: platform, uid: uid, id: id, name: "Astral Daemon (" + id + ")", run: run}
	switch platform {
	case "darwin":
		m.target = "app.astral.daemon." + id
		m.definition = filepath.Join(home, "Library", "LaunchAgents", m.target+".plist")
	case "linux":
		m.target = "astral-daemon-" + id + ".service"
		config := os.Getenv("XDG_CONFIG_HOME")
		// XDG base-directory values must be absolute. A manager may inherit a
		// literal $HOME/.config from PAM; that is not shell-expanded here.
		if !filepath.IsAbs(config) {
			config = filepath.Join(home, ".config")
		}
		if !filepath.IsAbs(config) || strings.ContainsAny(config, "\x00\r\n") {
			return nil, errors.New("linux user-service configuration directory must be an absolute path without control characters")
		}
		m.configHome = filepath.Clean(config)
		m.definition = filepath.Join(m.configHome, "systemd", "user", m.target)
	case "windows":
		if !validSID(uid) {
			return nil, errors.New("cannot install a user task without a Windows user SID")
		}
		m.target = "\\Astral\\" + m.name
		m.definition = filepath.Join(absolute, "service", "task.xml")
	default:
		return nil, fmt.Errorf("user Daemon services are unsupported on %s", platform)
	}
	return m, nil
}

func validSID(value string) bool {
	parts := strings.Split(value, "-")
	if len(parts) < 3 || parts[0] != "S" || parts[1] != "1" {
		return false
	}
	for _, part := range parts[2:] {
		if part == "" {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func executeEnvironment(ctx context.Context, environment []string, name string, args ...string) (commandResult, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = environment
	raw, err := command.CombinedOutput()
	result := commandResult{output: strings.TrimSpace(string(raw))}
	if len(result.output) > 4096 {
		result.output = result.output[len(result.output)-4096:]
	}
	if err == nil {
		return result, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.code = exit.ExitCode()
		return result, nil
	}
	return result, err
}

func (m *Manager) command(ctx context.Context, name string, args ...string) (string, error) {
	result, err := m.run(ctx, name, args...)
	if err != nil {
		return result.output, fmt.Errorf("%s: %w", name, err)
	}
	if result.code != 0 {
		return result.output, fmt.Errorf("%s exited %d: %s", name, result.code, result.output)
	}
	return result.output, nil
}

func (m *Manager) args(spec Spec) []string {
	return []string{"computer", "daemon", "--root", m.root, "--credentials", spec.Credentials, "--log", filepath.Join(m.root, "daemon.log")}
}

func (m *Manager) Install(ctx context.Context, spec Spec) error {
	return m.install(ctx, spec, false)
}

// Reinstall rewrites every native definition, even if the last manifest matches.
// Update rollback uses it after an interrupted or partially failed registration.
func (m *Manager) Reinstall(ctx context.Context, spec Spec) error {
	return m.install(ctx, spec, true)
}

func (m *Manager) install(ctx context.Context, spec Spec, force bool) error {
	for _, value := range []string{spec.Executable, spec.Credentials, spec.EnvironmentPATH} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("service definition contains a control character")
		}
	}
	if !filepath.IsAbs(spec.Executable) || !filepath.IsAbs(spec.Credentials) {
		return errors.New("service executable and credentials must use absolute paths")
	}
	for _, path := range []string{spec.Executable, spec.Credentials} {
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("service file is not regular: %s", path)
		}
	}
	if spec.EnvironmentPATH == "" {
		spec.EnvironmentPATH = os.Getenv("PATH")
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	manifest := filepath.Join(m.root, "service", "spec.json")
	previous, _ := os.ReadFile(manifest)
	if !force && string(previous) == string(encoded) && m.definitionMatches(spec) {
		state, statusErr := m.Status(ctx)
		if statusErr != nil {
			return statusErr
		}
		if state.State != "not-installed" {
			switch m.platform {
			case "darwin":
				_, err = m.command(ctx, "/bin/launchctl", "enable", m.launchdTarget())
			case "linux":
				_, err = m.command(ctx, "systemctl", "--user", "enable", m.target)
			case "windows":
				_, err = m.command(ctx, "schtasks.exe", "/change", "/tn", m.target, "/enable")
			}
			return err
		}
	}
	if _, err = os.Stat(m.definition); err == nil {
		if err = m.Stop(ctx); err != nil {
			return fmt.Errorf("stop previous user Daemon before reinstalling: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = os.MkdirAll(filepath.Join(m.root, "service"), 0700); err != nil {
		return err
	}
	switch m.platform {
	case "darwin":
		launcher := filepath.Join(m.root, "service", m.name)
		if err = writeAtomic(launcher, []byte("#!/bin/sh\nexec "+shellQuote(spec.Executable)+" \"$@\"\n"), 0700); err != nil {
			return err
		}
		if err = writeAtomic(m.definition, []byte(m.launchd(spec, launcher)), 0600); err != nil {
			return err
		}
		if _, err = m.command(ctx, "/usr/bin/plutil", "-lint", m.definition); err != nil {
			return err
		}
		_, err = m.command(ctx, "/bin/launchctl", "enable", m.launchdTarget())
	case "linux":
		if err = writeAtomic(m.definition, []byte(m.systemd(spec)), 0600); err != nil {
			return err
		}
		if _, err = m.command(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("a running systemd user manager is required: %w", err)
		}
		_, err = m.command(ctx, "systemctl", "--user", "enable", m.target)
	case "windows":
		if err = writeAtomic(m.windowsLauncher(), []byte(m.windowsScript(spec)), 0600); err != nil {
			return err
		}
		if err = writeAtomic(m.definition, utf16XML(m.windowsTask(spec)), 0600); err != nil {
			return err
		}
		_, err = m.command(ctx, "schtasks.exe", "/create", "/xml", m.definition, "/tn", m.target, "/f")
	}
	if err != nil {
		return fmt.Errorf("install %s current-user service: %w", m.name, err)
	}
	return writeAtomic(manifest, encoded, 0600)
}

func (m *Manager) definitionMatches(spec Spec) bool {
	var expected []byte
	switch m.platform {
	case "linux":
		expected = []byte(m.systemd(spec))
	case "darwin":
		launcher := filepath.Join(m.root, "service", m.name)
		raw, err := os.ReadFile(launcher)
		if err != nil || string(raw) != "#!/bin/sh\nexec "+shellQuote(spec.Executable)+" \"$@\"\n" {
			return false
		}
		expected = []byte(m.launchd(spec, launcher))
	case "windows":
		raw, err := os.ReadFile(m.windowsLauncher())
		if err != nil || string(raw) != m.windowsScript(spec) {
			return false
		}
		expected = utf16XML(m.windowsTask(spec))
	}
	raw, err := os.ReadFile(m.definition)
	return err == nil && bytes.Equal(raw, expected)
}

func (m *Manager) Start(ctx context.Context) error {
	if _, err := os.Stat(m.definition); err != nil {
		return fmt.Errorf("daemon service is not installed: %w", err)
	}
	state, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if state.State == "running" {
		return nil
	}
	switch m.platform {
	case "darwin":
		if _, err = m.command(ctx, "/bin/launchctl", "enable", m.launchdTarget()); err != nil {
			return err
		}
		loaded, loadErr := m.run(ctx, "/bin/launchctl", "print", m.launchdTarget())
		if loadErr != nil {
			return loadErr
		}
		if loaded.code == 0 {
			_, err = m.command(ctx, "/bin/launchctl", "kickstart", m.launchdTarget())
			return err
		}
		for attempt := 0; attempt < 5; attempt++ {
			_, err = m.command(ctx, "/bin/launchctl", "bootstrap", "gui/"+m.uid, m.definition)
			if err == nil {
				return nil
			}
			if waitErr := wait(ctx, 200*time.Millisecond); waitErr != nil {
				return waitErr
			}
		}
	case "linux":
		_, err = m.command(ctx, "systemctl", "--user", "start", m.target)
	case "windows":
		_, err = m.command(ctx, "schtasks.exe", "/run", "/tn", m.target)
	}
	return err
}

func (m *Manager) Stop(ctx context.Context) error {
	state, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if state.State == "not-installed" {
		return nil
	}
	switch m.platform {
	case "darwin":
		loaded, runErr := m.run(ctx, "/bin/launchctl", "print", m.launchdTarget())
		if runErr != nil {
			return runErr
		}
		if loaded.code != 0 {
			return nil
		}
		if _, err = m.command(ctx, "/bin/launchctl", "bootout", m.launchdTarget()); err != nil {
			return err
		}
		for attempt := 0; attempt < 50; attempt++ {
			result, runErr := m.run(ctx, "/bin/launchctl", "print", m.launchdTarget())
			if runErr != nil {
				return runErr
			}
			if result.code != 0 {
				return nil
			}
			if err = wait(ctx, 100*time.Millisecond); err != nil {
				return err
			}
		}
		return errors.New("launchd Daemon did not stop before the deadline")
	case "linux":
		_, err = m.command(ctx, "systemctl", "--user", "stop", m.target)
	case "windows":
		if state.State != "stopped" {
			if _, err = m.command(ctx, "schtasks.exe", "/end", "/tn", m.target); err != nil {
				return err
			}
		}
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		for {
			current, statusErr := m.Status(deadline)
			free, lockErr := daemonLockFree(filepath.Join(m.root, "daemon.lock"))
			if statusErr != nil {
				return statusErr
			}
			if lockErr != nil {
				return lockErr
			}
			if current.State != "running" && current.State != "unknown" && free {
				return nil
			}
			if err = wait(deadline, 100*time.Millisecond); err != nil {
				return fmt.Errorf("windows task stopped but its Daemon did not release the data directory; inspect %s: %w", filepath.Join(m.root, "daemon.log"), err)
			}
		}
	}
	return err
}

func (m *Manager) Restart(ctx context.Context) error {
	if err := m.Stop(ctx); err != nil {
		return err
	}
	return m.Start(ctx)
}

func (m *Manager) Uninstall(ctx context.Context) error {
	state, err := m.Status(ctx)
	if err != nil {
		return err
	}
	if state.State != "not-installed" {
		if err = m.Stop(ctx); err != nil {
			return err
		}
		switch m.platform {
		case "linux":
			if _, err = m.command(ctx, "systemctl", "--user", "disable", m.target); err != nil {
				return err
			}
		case "windows":
			if _, err = m.command(ctx, "schtasks.exe", "/delete", "/tn", m.target, "/f"); err != nil {
				return err
			}
		}
	}
	for _, path := range []string{m.definition, filepath.Join(m.root, "service", "spec.json"), filepath.Join(m.root, "service", m.name), m.windowsLauncher()} {
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if m.platform == "linux" {
		_, err = m.command(ctx, "systemctl", "--user", "daemon-reload")
		return err
	}
	return nil
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	s := Status{ID: m.id, DisplayName: m.name, State: "not-installed", LogPath: filepath.Join(m.root, "daemon.log")}
	switch m.platform {
	case "darwin":
		s.Backend = "launchd-user"
	case "linux":
		s.Backend = "systemd-user"
	case "windows":
		s.Backend = "windows-task-scheduler"
	}
	if _, err := os.Stat(m.definition); errors.Is(err, os.ErrNotExist) {
		return s, nil
	} else if err != nil {
		return s, err
	}
	s.State = "unknown"
	switch m.platform {
	case "darwin":
		result, err := m.run(ctx, "/bin/launchctl", "print", m.launchdTarget())
		if err != nil {
			return s, err
		}
		s.State = "stopped"
		if result.code == 0 {
			for _, line := range strings.Split(result.output, "\n") {
				key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
				if !ok {
					continue
				}
				if key == "state" && value == "running" {
					s.State = "running"
				}
				if key == "pid" {
					s.PID, _ = strconv.Atoi(value)
				}
			}
		}
	case "linux":
		result, err := m.run(ctx, "systemctl", "--user", "show", m.target, "--property=LoadState,ActiveState,SubState,MainPID")
		if err != nil {
			return s, err
		}
		fields := map[string]string{}
		for _, line := range strings.Split(result.output, "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				fields[k] = v
			}
		}
		if fields["LoadState"] == "not-found" {
			s.State = "not-installed"
			return s, nil
		}
		if result.code != 0 {
			return s, fmt.Errorf("query systemd user service: %s", result.output)
		}
		s.PID, _ = strconv.Atoi(fields["MainPID"])
		s.Detail = fields["SubState"]
		if fields["ActiveState"] == "active" && fields["SubState"] == "running" && s.PID > 0 {
			s.State = "running"
		} else if fields["ActiveState"] == "inactive" || fields["ActiveState"] == "failed" {
			s.State = "stopped"
		}
	case "windows":
		script := `$ErrorActionPreference='Stop'; [Console]::OutputEncoding=[Text.UTF8Encoding]::new(); $task=Get-ScheduledTask -TaskPath '\Astral\' -TaskName ` + psQuote(m.name) + ` -ErrorAction SilentlyContinue; if($null -eq $task){'not-installed';exit}; $owner=$task.Principal.UserId; if($owner -notmatch '^S-1-'){$owner=([Security.Principal.NTAccount]::new($owner)).Translate([Security.Principal.SecurityIdentifier]).Value}; if($owner -ne ` + psQuote(m.uid) + `){throw 'Astral task belongs to another account'}; $task.State.ToString()`
		output, err := m.command(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
		if err != nil {
			return s, err
		}
		switch strings.ToLower(strings.TrimSpace(output)) {
		case "not-installed":
			s.State = "not-installed"
		case "running":
			s.State = "running"
		case "ready", "disabled":
			s.State = "stopped"
		default:
			s.Detail = output
		}
	}
	return s, nil
}

func (m *Manager) launchdTarget() string { return "gui/" + m.uid + "/" + m.target }
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func shellQuote(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
func psQuote(v string) string    { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
func xmlQuote(v string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}
func systemdQuote(v string, argument bool) string {
	v = strings.ReplaceAll(v, "%", "%%")
	if argument {
		v = strings.ReplaceAll(v, "$", "$$")
	}
	return strconv.Quote(v)
}

func (m *Manager) systemd(spec Spec) string {
	argv := []string{systemdQuote(spec.Executable, false)}
	for _, arg := range m.args(spec) {
		argv = append(argv, systemdQuote(arg, true))
	}
	return "[Unit]\nDescription=" + m.name + "\n\n[Service]\nType=simple\nEnvironment=" + systemdQuote("PATH="+spec.EnvironmentPATH, false) + "\nEnvironment=" + systemdQuote("XDG_CONFIG_HOME="+m.configHome, false) + "\nExecStart=" + strings.Join(argv, " ") + "\nUMask=0077\nRestart=always\nRestartSec=5\nTimeoutStopSec=30\nKillMode=control-group\n\n[Install]\nWantedBy=default.target\n"
}
func (m *Manager) launchd(spec Spec, launcher string) string {
	args := "<string>" + xmlQuote(launcher) + "</string>"
	for _, arg := range m.args(spec) {
		args += "<string>" + xmlQuote(arg) + "</string>"
	}
	return `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + m.target + `</string><key>ProgramArguments</key><array>` + args + `</array><key>EnvironmentVariables</key><dict><key>PATH</key><string>` + xmlQuote(spec.EnvironmentPATH) + `</string></dict><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>5</integer><key>StandardOutPath</key><string>` + xmlQuote(filepath.Join(m.root, "service-launch.log")) + `</string><key>StandardErrorPath</key><string>` + xmlQuote(filepath.Join(m.root, "service-launch.log")) + `</string></dict></plist>`
}
func windowsArg(v string) string {
	// CommandLineToArgvW escaping; a trailing backslash must be doubled before
	// the closing quote, even for otherwise ordinary Windows directory paths.
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, c := range v {
		if c == '\\' {
			slashes++
			continue
		}
		if c == '"' {
			b.WriteString(strings.Repeat("\\", slashes*2+1))
			b.WriteRune(c)
		} else {
			b.WriteString(strings.Repeat("\\", slashes))
			b.WriteRune(c)
		}
		slashes = 0
	}
	b.WriteString(strings.Repeat("\\", slashes*2))
	b.WriteByte('"')
	return b.String()
}
func (m *Manager) windowsTask(spec Spec) string {
	args := []string{}
	for _, arg := range []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-WindowStyle", "Hidden", "-File", m.windowsLauncher()} {
		args = append(args, windowsArg(arg))
	}
	return `<?xml version="1.0" encoding="UTF-16"?><Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><RegistrationInfo><Description>` + xmlQuote(m.name) + `</Description></RegistrationInfo><Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + m.uid + `</UserId></LogonTrigger></Triggers><Principals><Principal id="Author"><UserId>` + m.uid + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals><Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><AllowHardTerminate>true</AllowHardTerminate><StartWhenAvailable>true</StartWhenAvailable><RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable><AllowStartOnDemand>true</AllowStartOnDemand><Enabled>true</Enabled><Hidden>false</Hidden><RunOnlyIfIdle>false</RunOnlyIfIdle><WakeToRun>false</WakeToRun><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><RestartOnFailure><Interval>PT1M</Interval><Count>255</Count></RestartOnFailure></Settings><Actions Context="Author"><Exec><Command>` + xmlQuote(m.windowsPowerShell()) + `</Command><Arguments>` + xmlQuote(strings.Join(args, " ")) + `</Arguments><WorkingDirectory>` + xmlQuote(m.root) + `</WorkingDirectory></Exec></Actions></Task>`
}

func (m *Manager) windowsLauncher() string { return filepath.Join(m.root, "service", m.name+".ps1") }
func (m *Manager) windowsPowerShell() string {
	base := os.Getenv("SystemRoot")
	if base == "" {
		base = `C:\Windows`
	}
	return filepath.Join(base, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}
func (m *Manager) windowsScript(spec Spec) string {
	args := []string{}
	for _, arg := range m.args(spec) {
		args = append(args, psQuote(arg))
	}
	// Task Scheduler stops the PowerShell action; the Daemon watches this
	// actual parent so an ended or crashed launcher cannot leave it running.
	args = append(args, psQuote("--parent-pid"), "$PID")
	// Windows PowerShell 5.1 treats redirected native stderr as ErrorRecords.
	// Daemon startup diagnostics use stderr, so keep only native execution in
	// Continue mode. A failed launch must retain a nonzero exit code.
	return "\ufeff$ErrorActionPreference = 'Stop'\r\n[Console]::OutputEncoding = [Text.UTF8Encoding]::new()\r\n$OutputEncoding = [Console]::OutputEncoding\r\n$env:PATH = " + psQuote(spec.EnvironmentPATH) + "\r\n$LASTEXITCODE = 1\r\n$ErrorActionPreference = 'Continue'\r\n& " + psQuote(spec.Executable) + " " + strings.Join(args, " ") + " >> " + psQuote(filepath.Join(m.root, "service-launch.log")) + " 2>&1\r\nexit $LASTEXITCODE\r\n"
}
func utf16XML(value string) []byte {
	raw := []byte{0xff, 0xfe}
	for _, v := range utf16.Encode([]rune(value)) {
		raw = append(raw, byte(v), byte(v>>8))
	}
	return raw
}
func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".astral-service-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(raw)
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
