package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestDaemonBuildExcludesServerDependencies(t *testing.T) {
	cmd := exec.Command("go", "list", "-tags", "daemon", "-f", "{{join .Deps \"\\n\"}}", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -tags daemon failed: %v\n%s", err, out)
	}
	deps := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		deps[strings.TrimSpace(line)] = true
	}
	forbidden := []string{
		"modernc.org/sqlite",
		"github.com/oines/astral/internal/web",
		"github.com/oines/astral/internal/httpapi",
		"github.com/oines/astral/internal/harness",
		"github.com/oines/astral/internal/channels",
		"github.com/oines/astral/internal/modelbridge",
		"github.com/oines/astral/internal/persistence",
	}
	for _, pkg := range forbidden {
		if deps[pkg] {
			t.Errorf("standalone daemon build (-tags daemon) unexpectedly depends on %s", pkg)
		}
	}
}
