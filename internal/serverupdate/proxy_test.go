package serverupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedProxyUnitAndDetachedJobPreserveExplicitSelection(t *testing.T) {
	for _, proxy := range []string{"", "https://proxy.example/prefix%20/$()/"} {
		t.Run("proxy="+proxy, func(t *testing.T) {
			t.Setenv("ASTRAL_GITHUB_PROXY", proxy)
			f := managedFixture(t)
			var marker installation
			markerPath := filepath.Join(f.m.root, "installation.json")
			if err := readJSON(markerPath, &marker); err != nil {
				t.Fatal(err)
			}
			marker.GitHubProxy = &proxy
			if err := writeJSON(markerPath, marker); err != nil {
				t.Fatal(err)
			}
			unit := unitDefinition(f.m.root, proxy)
			wantEnvironment := `Environment="ASTRAL_GITHUB_PROXY=` + strings.ReplaceAll(proxy, "%", "%%") + "\"\nExecStart=:"
			if !strings.Contains(unit, wantEnvironment) {
				t.Fatalf("unit does not match installer's quoted Environment format: %s", unit)
			}
			if err := os.WriteFile(f.m.deps.unitPath, []byte(unit), 0600); err != nil {
				t.Fatal(err)
			}
			pending(t, f)
			f.mu.Lock()
			defer f.mu.Unlock()
			found := false
			for _, args := range f.commands {
				if args[0] != "systemd-run" {
					continue
				}
				for _, arg := range args[1:] {
					if arg == "--setenv=ASTRAL_GITHUB_PROXY="+proxy {
						found = true
					}
				}
			}
			if !found {
				t.Fatal("detached updater did not receive the exact proxy value as one argument")
			}
		})
	}
}

func TestManagedProxyMarkerCannotAdoptUnexpectedUnitOrInvalidPrefix(t *testing.T) {
	for _, proxy := range []string{"https://proxy.example/", "https://proxy.example", "http://proxy.example/", "https://user:secret@proxy.example/"} {
		t.Run(proxy, func(t *testing.T) {
			f := managedFixture(t)
			var marker installation
			markerPath := filepath.Join(f.m.root, "installation.json")
			if err := readJSON(markerPath, &marker); err != nil {
				t.Fatal(err)
			}
			marker.GitHubProxy = &proxy
			if err := writeJSON(markerPath, marker); err != nil {
				t.Fatal(err)
			}
			// The legacy unit lacks the marker's new explicit Environment line.
			state, err := f.m.Apply(context.Background())
			if err != nil || state.Manager != "manual" || f.latestCalls.Load() != 0 {
				t.Fatalf("invalid marker or mismatched unit was adopted: %+v %v", state, err)
			}
		})
	}
}
