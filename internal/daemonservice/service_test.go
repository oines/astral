package daemonservice

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type fakeService struct {
	platform           string
	installed, running bool
	calls              []string
	stopQueries        int
	failRegister       bool
}

func (f *fakeService) run(_ context.Context, name string, args ...string) (commandResult, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if strings.Contains(call, "sudo") || strings.Contains(call, "enable-linger") {
		return commandResult{}, errors.New("unexpected elevation")
	}
	if name == "schtasks.exe" {
		switch args[0] {
		case "/create":
			if f.failRegister {
				return commandResult{code: 1, output: "task registration denied"}, nil
			}
			f.installed = true
		case "/run":
			f.running = true
		case "/end":
			f.running = false
			f.stopQueries = 2
		case "/delete":
			f.installed = false
		}
	}
	if name == "powershell.exe" {
		if !strings.Contains(call, `-TaskPath '\Astral\'`) {
			return commandResult{}, errors.New("Windows task lookup used a different folder from registration")
		}
		if !f.installed {
			return commandResult{output: "not-installed"}, nil
		}
		if f.stopQueries > 0 {
			f.stopQueries--
			return commandResult{output: "running"}, nil
		}
		if f.running {
			return commandResult{output: "Running"}, nil
		}
		return commandResult{output: "Ready"}, nil
	}
	if name == "systemctl" {
		switch args[1] {
		case "enable":
			f.installed = true
		case "start":
			f.running = true
		case "stop":
			f.running = false
		case "disable":
			f.installed = false
		case "show":
			if !f.installed {
				return commandResult{output: "LoadState=not-found"}, nil
			}
			if f.running {
				return commandResult{output: "LoadState=loaded\nActiveState=active\nSubState=running\nMainPID=42"}, nil
			}
			return commandResult{output: "LoadState=loaded\nActiveState=inactive\nSubState=dead\nMainPID=0"}, nil
		}
	}
	if name == "/bin/launchctl" {
		switch args[0] {
		case "print":
			if f.running {
				return commandResult{output: "state = running\npid = 42"}, nil
			}
			return commandResult{code: 113}, nil
		case "bootstrap":
			f.running = true
		case "bootout":
			f.running = false
		}
	}
	return commandResult{}, nil
}

func serviceFixture(t *testing.T, platform string) (*Manager, Spec, *fakeService) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, "中文 projects", "daemon $% ' directory")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	uid := "501"
	if platform == "windows" {
		uid = "S-1-5-21-123456-1001"
	}
	fake := &fakeService{platform: platform}
	m, err := newManager(root, home, platform, uid, fake.run)
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{Executable: filepath.Join(root, "astral"), Credentials: filepath.Join(root, "credentials.json"), EnvironmentPATH: "/tools with spaces/$bin:/usr/bin"}
	for _, path := range []string{spec.Executable, spec.Credentials, filepath.Join(root, "daemon.log"), filepath.Join(root, "project.txt")} {
		if err = os.WriteFile(path, []byte("preserve"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return m, spec, fake
}

func TestUserServicesInstallIdempotentlyAndPreserveData(t *testing.T) {
	for _, platform := range []string{"darwin", "linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			m, spec, fake := serviceFixture(t, platform)
			ctx := context.Background()
			if err := m.Install(ctx, spec); err != nil {
				t.Fatal(err)
			}
			definition, err := os.ReadFile(m.definition)
			if err != nil {
				t.Fatal(err)
			}
			if platform == "windows" {
				if len(definition) < 2 || definition[0] != 0xff || definition[1] != 0xfe {
					t.Fatal("task XML is not UTF-16LE with BOM")
				}
				text := m.windowsTask(spec)
				for _, required := range []string{"<RunLevel>LeastPrivilege</RunLevel>", "<LogonType>InteractiveToken</LogonType>", "<Hidden>false</Hidden>", "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>"} {
					if !strings.Contains(text, required) {
						t.Fatalf("missing %s", required)
					}
				}
				var document any
				if err = xml.Unmarshal([]byte(strings.Replace(text, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)), &document); err != nil {
					t.Fatal(err)
				}
				script, err := os.ReadFile(m.windowsLauncher())
				if err != nil || !strings.Contains(string(script), "$env:PATH = '") {
					t.Fatalf("PATH was not persisted: %v", err)
				}
				if !strings.HasPrefix(string(script), "\ufeff$ErrorActionPreference = 'Stop'\r\n") ||
					!strings.Contains(string(script), "$LASTEXITCODE = 1\r\n$ErrorActionPreference = 'Continue'\r\n& ") ||
					!strings.HasSuffix(string(script), "2>&1\r\nexit $LASTEXITCODE\r\n") {
					t.Fatal("native diagnostics must not terminate Windows PowerShell 5.1; retain strict setup and the native exit code")
				}
				if !strings.Contains(string(script), "'--parent-pid' $PID >> ") {
					t.Fatal("Daemon must watch the actual PowerShell launcher PID when its task is stopped")
				}
			} else if platform == "darwin" {
				var document any
				if err = xml.Unmarshal(definition, &document); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(definition), m.name) {
					t.Fatal("launchd does not use recognizable Astral Daemon launcher")
				}
			} else if !strings.Contains(string(definition), "$$") || !strings.Contains(string(definition), "%%") {
				t.Fatal("systemd argument expansion was not escaped")
			}
			if err = m.Start(ctx); err != nil {
				t.Fatal(err)
			}
			state, err := m.Status(ctx)
			if err != nil || state.State != "running" {
				t.Fatalf("not running: %+v %v", state, err)
			}
			before := len(fake.calls)
			if err = m.Install(ctx, spec); err != nil {
				t.Fatal(err)
			}
			for _, call := range fake.calls[before:] {
				if strings.Contains(call, "/create") || strings.Contains(call, " daemon-reload") || strings.Contains(call, "bootout") {
					t.Fatalf("identical install disrupted running service: %s", call)
				}
			}
			if err = m.Restart(ctx); err != nil {
				t.Fatal(err)
			}
			if err = m.Uninstall(ctx); err != nil {
				t.Fatal(err)
			}
			state, err = m.Status(ctx)
			if err != nil || state.State != "not-installed" {
				t.Fatalf("service still installed: %+v %v", state, err)
			}
			for _, path := range []string{spec.Credentials, filepath.Join(m.root, "daemon.log"), filepath.Join(m.root, "project.txt")} {
				raw, err := os.ReadFile(path)
				if err != nil || string(raw) != "preserve" {
					t.Fatalf("uninstall damaged user data %s: %v", path, err)
				}
			}
		})
	}
}

func TestRegistrationFailureDoesNotClaimInstalledOrStart(t *testing.T) {
	m, spec, fake := serviceFixture(t, "windows")
	fake.failRegister = true
	if err := m.Install(context.Background(), spec); err == nil {
		t.Fatal("registration failure was ignored")
	}
	if _, err := os.Stat(filepath.Join(m.root, "service", "spec.json")); !os.IsNotExist(err) {
		t.Fatal("failed registration persisted a successful manifest")
	}
	for _, call := range fake.calls {
		if strings.Contains(call, "/run") {
			t.Fatal("failed registration started a process")
		}
	}
}

func TestLaunchdRepeatedBootstrapFailureIsReturned(t *testing.T) {
	m, spec, _ := serviceFixture(t, "darwin")
	if err := m.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	m.run = func(_ context.Context, _ string, args ...string) (commandResult, error) {
		if args[0] == "print" {
			return commandResult{code: 113}, nil
		}
		if args[0] == "bootstrap" {
			return commandResult{code: 5, output: "Input/output error"}, nil
		}
		return commandResult{}, nil
	}
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("all bootstrap attempts failed but Start reported success")
	}
}

func TestInstancesAndAccountsCannotOverwriteEachOther(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	fake := &fakeService{}
	a, _ := newManager(root, home, "windows", "S-1-5-21-1", fake.run)
	b, _ := newManager(root+"other", home, "windows", "S-1-5-21-1", fake.run)
	c, _ := newManager(root, home, "windows", "S-1-5-21-2", fake.run)
	if a.target == b.target || a.target == c.target {
		t.Fatal("task identity collides across roots or users")
	}
	if _, err := newManager(root, home, "windows", "S-1--", fake.run); err == nil {
		t.Fatal("invalid SID accepted")
	}
}

func TestInterruptedRegistrationReinstallsActualPreviousDefinition(t *testing.T) {
	m, previous, fake := serviceFixture(t, "windows")
	ctx := context.Background()
	if err := m.Install(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	next := previous
	next.Executable = filepath.Join(m.root, "new-astral")
	if err := os.WriteFile(next.Executable, []byte("next"), 0700); err != nil {
		t.Fatal(err)
	}
	fake.failRegister = true
	if err := m.Install(ctx, next); err == nil {
		t.Fatal("native task registration unexpectedly succeeded")
	}
	if m.definitionMatches(previous) {
		t.Fatal("fixture did not create the partial-registration state")
	}
	fake.failRegister = false
	// The old manifest still matches. Install must inspect the definition and
	// launcher as well, rather than treating a stopped service as a good copy.
	if err := m.Install(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if !m.definitionMatches(previous) {
		t.Fatal("rollback retained new launcher behind the old manifest")
	}
	before := len(fake.calls)
	if err := m.Reinstall(ctx, previous); err != nil {
		t.Fatal(err)
	}
	registered := false
	for _, call := range fake.calls[before:] {
		registered = registered || strings.Contains(call, "/create")
	}
	if !registered {
		t.Fatal("forced reinstall skipped native definition registration")
	}
}

func TestLinuxConfigurationDirectoryFollowsXDGAbsolutePathSemantics(t *testing.T) {
	for _, choice := range []string{"absent", "relative", "literal-home", "absolute"} {
		t.Run(choice, func(t *testing.T) {
			home := t.TempDir()
			expected := filepath.Join(home, ".config")
			value := ""
			switch choice {
			case "relative":
				value = "relative-config"
			case "literal-home":
				value = "$HOME/.config"
			case "absolute":
				expected = filepath.Join(t.TempDir(), "custom config % '$ directory")
				value = expected
			}
			t.Setenv("XDG_CONFIG_HOME", value)
			manager, err := newManager(t.TempDir(), home, "linux", "0", (&fakeService{}).run)
			if err != nil {
				t.Fatal(err)
			}
			if manager.configHome != expected || !filepath.IsAbs(manager.definition) || manager.definition != filepath.Join(expected, "systemd", "user", manager.target) {
				t.Fatalf("XDG configuration directory was not resolved correctly for %s", choice)
			}
		})
	}
	for _, bad := range []string{"/tmp/config\n[Service]", "/tmp/config\rvalue", "/tmp/config\x00value"} {
		if strings.ContainsRune(bad, 0) {
			// os.Setenv itself refuses NUL; New also validates every directory
			// after fallback, so exercise this through the supplied home.
			t.Setenv("XDG_CONFIG_HOME", "")
			if _, err := newManager(t.TempDir(), bad, "linux", "0", (&fakeService{}).run); err == nil {
				t.Fatal("control character in fallback directory was accepted")
			}
			continue
		}
		t.Setenv("XDG_CONFIG_HOME", bad)
		if _, err := newManager(t.TempDir(), t.TempDir(), "linux", "0", (&fakeService{}).run); err == nil {
			t.Fatal("control character in resolved configuration directory was accepted")
		}
	}
}

func TestLinuxDaemonAndUpdateHelperRetainResolvedConfigurationAcrossManagerEnvironments(t *testing.T) {
	original, spec, fake := serviceFixture(t, "linux")
	chosen := filepath.Join(t.TempDir(), "custom config % '$ directory")
	t.Setenv("XDG_CONFIG_HOME", chosen)
	manager, err := newManager(original.root, original.home, "linux", original.uid, fake.run)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	definition, err := os.ReadFile(manager.definition)
	if err != nil {
		t.Fatal(err)
	}
	var unitConfig string
	for _, line := range strings.Split(string(definition), "\n") {
		if encoded, ok := strings.CutPrefix(line, "Environment="); ok {
			variable, err := strconv.Unquote(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if value, ok := strings.CutPrefix(variable, "XDG_CONFIG_HOME="); ok {
				unitConfig = strings.ReplaceAll(value, "%%", "%")
			}
		}
	}
	if unitConfig != chosen {
		t.Fatal("Daemon unit did not pin the chosen configuration directory")
	}
	// The manager can have a different inherited HOME/XDG environment. Only
	// the explicitly pinned unit/helper setting may select the registration.
	t.Setenv("XDG_CONFIG_HOME", unitConfig)
	daemonManager, err := newManager(manager.root, t.TempDir(), "linux", manager.uid, fake.run)
	if err != nil || daemonManager.definition != manager.definition {
		t.Fatalf("Daemon selected a different registration: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", "$HOME/.config")
	var helperConfig string
	daemonManager.run = func(_ context.Context, name string, args ...string) (commandResult, error) {
		if name != "systemd-run" {
			t.Fatalf("unexpected update launcher %s", name)
		}
		for _, arg := range args {
			if value, ok := strings.CutPrefix(arg, "--setenv=XDG_CONFIG_HOME="); ok {
				helperConfig = value
			}
		}
		return commandResult{}, nil
	}
	if err := daemonManager.StartUpdate(context.Background(), spec.Executable); err != nil {
		t.Fatal(err)
	}
	if helperConfig != chosen {
		t.Fatal("independent update job inherited the manager's unrelated configuration directory")
	}
	t.Setenv("XDG_CONFIG_HOME", helperConfig)
	helperManager, err := newManager(manager.root, t.TempDir(), "linux", manager.uid, fake.run)
	if err != nil || helperManager.definition != manager.definition {
		t.Fatalf("update helper selected a different registration: %v", err)
	}
}
