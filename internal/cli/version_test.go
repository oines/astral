package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/persistence"
)

func TestVersionCommandOutput(t *testing.T) {
	for _, command := range []string{"version", "--version"} {
		t.Run(command, func(t *testing.T) {
			for _, options := range [][]string{nil, {"--data-formats"}, {"--json"}} {
				var out, stderr bytes.Buffer
				args := append([]string{command}, options...)
				if err := run(context.Background(), args, &out, &stderr); err != nil {
					t.Fatalf("run(%q): %v", args, err)
				}
				if stderr.Len() != 0 {
					t.Fatalf("unexpected stderr: %s", &stderr)
				}
				var want string
				switch {
				case len(options) == 0:
					want = Version + "\n"
				case options[0] == "--data-formats":
					want = fmt.Sprintf("%d %d\n", persistence.FormatVersion, core.FormatVersion)
				default:
					versionJSON, err := json.Marshal(Version)
					if err != nil {
						t.Fatal(err)
					}
					want = fmt.Sprintf("{\"version\":%s,\"data_formats\":{\"host\":%d,\"core\":%d},\"migration_status\":\"goose\",\"migration_baselines\":{\"host\":3,\"core\":10},\"migration_targets\":{\"host\":%d,\"core\":%d}}\n", versionJSON, persistence.FormatVersion, core.FormatVersion, persistence.FormatVersion, core.FormatVersion)
				}
				if out.String() != want {
					t.Errorf("run(%q) output = %q, want %q", args, out.String(), want)
				}
			}
		})
	}
}

func TestVersionCommandRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown flag", []string{"--unknown"}, "unknown argument"},
		{"positional argument", []string{"extra"}, "unknown argument"},
		{"argument after mode", []string{"--json", "extra"}, "unknown argument"},
		{"conflicting modes", []string{"--json", "--data-formats"}, "cannot be combined"},
		{"reverse conflicting modes", []string{"--data-formats", "--json"}, "cannot be combined"},
		{"duplicate json", []string{"--json", "--json"}, "may only be specified once"},
		{"duplicate data formats", []string{"--data-formats", "--data-formats"}, "may only be specified once"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := version(test.args, &out); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Errorf("version(%q) error = %v, want %q", test.args, err, test.want)
			}
			if out.Len() != 0 {
				t.Errorf("invalid arguments produced output: %q", out.String())
			}
		})
	}
}

type failingVersionWriter struct{ err error }

func (w failingVersionWriter) Write([]byte) (int, error) { return 0, w.err }

func TestVersionCommandReportsOutputFailure(t *testing.T) {
	want := errors.New("output closed")
	for _, args := range [][]string{nil, {"--json"}, {"--data-formats"}} {
		if err := version(args, failingVersionWriter{want}); !errors.Is(err, want) {
			t.Errorf("version(%q) error = %v, want %v", args, err, want)
		}
	}
}
