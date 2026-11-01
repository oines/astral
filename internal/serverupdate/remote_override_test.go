package serverupdate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedRemoteOverrideUnitAndDetachedEnvironment(t *testing.T) {
	for _, test := range []struct {
		name, environment, launchArgument string
		override                          *bool
	}{
		{"default", "", "--property=UnsetEnvironment=ASTRAL_ALLOW_REMOTE_MANAGEMENT", nil},
		{"disabled", "false", "--setenv=ASTRAL_ALLOW_REMOTE_MANAGEMENT=false", remotePointer(false)},
		{"enabled", "true", "--setenv=ASTRAL_ALLOW_REMOTE_MANAGEMENT=true", remotePointer(true)},
		{"canonical-environment", "0", "--setenv=ASTRAL_ALLOW_REMOTE_MANAGEMENT=false", remotePointer(false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("ASTRAL_ALLOW_REMOTE_MANAGEMENT", test.environment)
			if test.environment == "" {
				if err := os.Unsetenv("ASTRAL_ALLOW_REMOTE_MANAGEMENT"); err != nil {
					t.Fatal(err)
				}
			}
			f := managedFixture(t)
			markerPath := filepath.Join(f.m.root, "installation.json")
			var marker installation
			if err := readJSON(markerPath, &marker); err != nil {
				t.Fatal(err)
			}
			proxy := ""
			marker.GitHubProxy, marker.AllowRemoteManagement = &proxy, test.override
			if err := writeJSON(markerPath, marker); err != nil {
				t.Fatal(err)
			}
			unit := unitDefinitionWithEnvironment(f.m.root, marker.GitHubProxy, marker.AllowRemoteManagement)
			if test.override == nil {
				if strings.Contains(unit, "ASTRAL_ALLOW_REMOTE_MANAGEMENT") {
					t.Fatal("default unit persisted an explicit remote override")
				}
			} else {
				want := "Environment=\"ASTRAL_GITHUB_PROXY=\"\nEnvironment=\"ASTRAL_ALLOW_REMOTE_MANAGEMENT=" + strings.TrimPrefix(test.launchArgument, "--setenv=ASTRAL_ALLOW_REMOTE_MANAGEMENT=") + "\"\nExecStart=:"
				if !strings.Contains(unit, want) {
					t.Fatal("remote override does not match installer's exact unit format")
				}
			}
			if err := os.WriteFile(f.m.deps.unitPath, []byte(unit), 0600); err != nil {
				t.Fatal(err)
			}
			state := pending(t, f)
			f.mu.Lock()
			found := false
			for _, args := range f.commands {
				if args[0] == "systemd-run" {
					for _, argument := range args[1:] {
						found = found || argument == test.launchArgument
					}
				}
			}
			f.mu.Unlock()
			if !found {
				t.Fatal("detached updater lost the explicit remote setting or manager isolation")
			}
			if err := f.m.run(context.Background(), state.OperationID, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			var retained installation
			if err := readJSON(markerPath, &retained); err != nil || retained.AllowRemoteManagement == nil != (test.override == nil) {
				t.Fatalf("update lost the optional remote marker: %+v %v", retained, err)
			}
			if test.override != nil && *retained.AllowRemoteManagement != *test.override {
				t.Fatal("update changed the persisted explicit remote choice")
			}
		})
	}
}

func TestManagedRemoteOverrideRejectsMismatchedUnit(t *testing.T) {
	for _, markerOverride := range []*bool{nil, remotePointer(false), remotePointer(true)} {
		f := managedFixture(t)
		var marker installation
		markerPath := filepath.Join(f.m.root, "installation.json")
		if err := readJSON(markerPath, &marker); err != nil {
			t.Fatal(err)
		}
		marker.AllowRemoteManagement = markerOverride
		if err := writeJSON(markerPath, marker); err != nil {
			t.Fatal(err)
		}
		unitOverride := remotePointer(false)
		if markerOverride != nil && !*markerOverride {
			unitOverride = remotePointer(true)
		}
		if err := os.WriteFile(f.m.deps.unitPath, []byte(unitDefinitionWithEnvironment(f.m.root, nil, unitOverride)), 0600); err != nil {
			t.Fatal(err)
		}
		state, err := f.m.Apply(context.Background())
		if err != nil || state.Manager != "manual" || f.latestCalls.Load() != 0 {
			t.Fatalf("unexpected remote unit override was adopted: %+v %v", state, err)
		}
	}
}

func remotePointer(value bool) *bool { return &value }
