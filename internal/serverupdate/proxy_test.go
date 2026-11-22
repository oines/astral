package serverupdate

import (
	"context"
	"path/filepath"
	"testing"
)

func TestManagedProxyDetachedJobPreservesExplicitSelection(t *testing.T) {
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

func TestManagedProxyMarkerRejectsInvalidPrefix(t *testing.T) {
	for _, proxy := range []string{"https://proxy.example", "http://proxy.example/", "https://user:secret@proxy.example/"} {
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
			state, err := f.m.Apply(context.Background())
			if err != nil || state.Manager != "manual" || f.latestCalls.Load() != 0 {
				t.Fatalf("invalid marker was adopted: %+v %v", state, err)
			}
		})
	}
}
