//go:build !windows

package timezone

import (
	"archive/zip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func timezoneData(t *testing.T, name string) []byte {
	t.Helper()
	goRoot, err := exec.Command("go", "env", "GOROOT").Output()
	if err != nil {
		t.Fatalf("locate Go timezone test data: %v", err)
	}
	archive, err := zip.OpenReader(filepath.Join(strings.TrimSpace(string(goRoot)), "lib", "time", "zoneinfo.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if file.Name != name {
			continue
		}
		reader, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	t.Fatalf("missing test timezone %q", name)
	return nil
}

func TestUnixSystemTimezoneReadsNamedAndCopiedZoneinfo(t *testing.T) {
	for _, mode := range []string{"symlink", "configuration", "copy", "standalone"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			localtime := filepath.Join(root, "localtime")
			zoneRoot := filepath.Join(root, "zoneinfo")
			zonePath := filepath.Join(zoneRoot, "America", "New_York")
			if err := os.MkdirAll(filepath.Dir(zonePath), 0o700); err != nil {
				t.Fatal(err)
			}
			data := timezoneData(t, "America/New_York")
			if mode != "standalone" {
				if err := os.WriteFile(zonePath, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				if err := os.Symlink(zonePath, localtime); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(localtime, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var configurations []string
			if mode == "configuration" {
				path := filepath.Join(root, "clock")
				if err := os.WriteFile(path, []byte("ZONE=\"America/New_York\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				configurations = append(configurations, path)
			}
			name, location, err := unixSystemLocation(localtime, configurations, []string{zoneRoot})
			expected := "America/New_York"
			if mode == "standalone" {
				expected = "System"
			}
			if err != nil || name != expected {
				t.Fatalf("%s localtime resolved as %q: %v", mode, name, err)
			}
			for _, test := range []struct {
				month time.Month
				want  int
			}{{time.January, -5 * 3600}, {time.July, -4 * 3600}} {
				_, offset := time.Date(2026, test.month, 15, 12, 0, 0, 0, time.UTC).In(location).Zone()
				if offset != test.want {
					t.Fatalf("%s %s lost DST: offset=%d", mode, test.month, offset)
				}
			}
		})
	}
}
