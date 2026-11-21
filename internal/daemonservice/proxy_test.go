package daemonservice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallPersistsGitHubProxyAcrossPlatforms(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "astral-daemon")
	creds := filepath.Join(dir, "credentials.json")
	for _, path := range []string{exe, creds} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mirror := "https://edgeone.gh-proxy.org"
	spec := Spec{
		Executable:      exe,
		Credentials:     creds,
		EnvironmentPATH: "/usr/bin:/bin",
		GitHubProxy:     &mirror,
	}

	// Linux systemd
	linuxRoot := filepath.Join(dir, "linux")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	lm, err := newManager(linuxRoot, dir, "linux", "1000", (&fakeService{platform: "linux"}).run)
	if err != nil {
		t.Fatal(err)
	}
	if err := lm.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(lm.definition)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), `Environment="ASTRAL_GITHUB_PROXY=https://edgeone.gh-proxy.org/"`) {
		t.Fatalf("systemd unit missing persisted proxy:\n%s", string(unit))
	}

	// macOS launchd
	macRoot := filepath.Join(dir, "darwin")
	mm, err := newManager(macRoot, dir, "darwin", "501", (&fakeService{platform: "darwin"}).run)
	if err != nil {
		t.Fatal(err)
	}
	if err := mm.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(mm.definition)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plist), `<key>ASTRAL_GITHUB_PROXY</key><string>https://edgeone.gh-proxy.org/</string>`) {
		t.Fatalf("launchd plist missing persisted proxy:\n%s", string(plist))
	}

	// Windows Task Scheduler launcher script
	winRoot := filepath.Join(dir, "windows")
	wm, err := newManager(winRoot, dir, "windows", "S-1-5-21-1-2-3-1001", (&fakeService{platform: "windows"}).run)
	if err != nil {
		t.Fatal(err)
	}
	if err := wm.Install(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(wm.windowsLauncher())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), `$env:ASTRAL_GITHUB_PROXY = 'https://edgeone.gh-proxy.org/'`) {
		t.Fatalf("windows launcher missing persisted proxy:\n%s", string(script))
	}

	// Explicit direct survives restart even if the service manager has a proxy.
	direct := ""
	spec.GitHubProxy = &direct
	for _, manager := range []*Manager{lm, mm, wm} {
		if err := manager.Install(context.Background(), spec); err != nil {
			t.Fatal(err)
		}
	}
	unit, _ = os.ReadFile(lm.definition)
	plist, _ = os.ReadFile(mm.definition)
	script, _ = os.ReadFile(wm.windowsLauncher())
	if !strings.Contains(string(unit), `Environment="ASTRAL_GITHUB_PROXY="`) ||
		!strings.Contains(string(plist), `<key>ASTRAL_GITHUB_PROXY</key><string></string>`) ||
		!strings.Contains(string(script), `$env:ASTRAL_GITHUB_PROXY = ''`) {
		t.Fatal("service no longer overrides the manager's environment for explicit direct downloads")
	}

	// Invalid proxy rejected
	bad := spec
	bad.GitHubProxy = new("http://insecure.example/")
	if err := lm.Install(context.Background(), bad); err == nil {
		t.Fatal("expected insecure GitHub proxy to be rejected")
	}
}
