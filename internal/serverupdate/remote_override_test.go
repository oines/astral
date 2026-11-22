package serverupdate

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedRemoteOverrideDetachedEnvironment(t *testing.T) {
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

func remotePointer(value bool) *bool { return &value }
