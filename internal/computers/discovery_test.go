package computers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func discoveryFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryUsesComputerHomeAndExactWorkspace(t *testing.T) {
	home, parent := t.TempDir(), t.TempDir()
	cwd := filepath.Join(parent, "workspace")
	global := filepath.Join(home, ".agents", "skills", "shared", "SKILL.md")
	workspace := filepath.Join(cwd, ".agents", "skills", "team", "shared", "SKILL.md")
	skill := "---\nname: shared\ndescription: >-\n  A useful\n  skill\n---\nprivate skill body\n"
	discoveryFile(t, global, skill)
	discoveryFile(t, workspace, skill)
	discoveryFile(t, filepath.Join(filepath.Dir(workspace), "examples", "SKILL.md"), skill)
	discoveryFile(t, filepath.Join(parent, ".agents", "skills", "parent", "SKILL.md"), skill)
	discoveryFile(t, filepath.Join(parent, "AGENTS.md"), "parent guidance must not appear")
	discoveryFile(t, filepath.Join(cwd, "AGENTS.md"), "current workspace guidance")
	binding := domain.ExecutionBinding{ComputerID: "computer", CWD: cwd}
	first, err := discoverEnvironment(context.Background(), home, binding)
	if err != nil || first.Status != "ok" || len(first.Skills) != 2 {
		t.Fatalf("discovery: %+v, %v", first, err)
	}
	if first.Skills[0].Scope != "global" || first.Skills[0].Path != global || first.Skills[1].Scope != "workspace" || first.Skills[1].Path != workspace {
		t.Fatalf("same-name skills did not retain scope and path: %+v", first.Skills)
	}
	if first.Skills[0].Description != "A useful skill" || first.AgentsMD.Content != "current workspace guidance" {
		t.Fatalf("metadata/guidance: %+v", first)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "private skill body") {
		t.Fatal("catalog eagerly included skill bodies")
	}
	second, err := discoverEnvironment(context.Background(), home, binding)
	stable, _ := json.Marshal(second)
	if err != nil || string(stable) != string(raw) {
		t.Fatal("unchanged discovery is not deterministic")
	}
	discoveryFile(t, global, skill+"changed body only\n")
	second, err = discoverEnvironment(context.Background(), home, binding)
	if err != nil || first.Skills[0].SHA256 == second.Skills[0].SHA256 || first.Skills[0].Description != second.Skills[0].Description {
		t.Fatal("body-only change did not alter the content fingerprint")
	}
	if err = os.Remove(workspace); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(filepath.Dir(workspace)); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(cwd, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	second, err = discoverEnvironment(context.Background(), home, binding)
	if err != nil || len(second.Skills) != 1 || second.AgentsMD.Status != "missing" || second.AgentsMD.Content != "" {
		t.Fatalf("deleted files retained stale content or inherited parents: %+v, %v", second, err)
	}
}

func TestDiscoveryFollowsSkillLinksAndReportsIncompleteCatalogs(t *testing.T) {
	home, cwd, packages := t.TempDir(), t.TempDir(), t.TempDir()
	root := filepath.Join(home, ".agents", "skills")
	if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(packages, root); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	discoveryFile(t, filepath.Join(external, "SKILL.md"), "---\nname: linked\ndescription: External linked package\n---\nbody")
	if err := os.Symlink(external, filepath.Join(packages, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(packages, filepath.Join(packages, "cycle")); err != nil {
		t.Fatal(err)
	}
	discoveryFile(t, filepath.Join(packages, "invalid", "SKILL.md"), "---\nname: broken\n---\nbody")
	discoveryFile(t, filepath.Join(packages, "large", "SKILL.md"), strings.Repeat("x", maxDiscoveryFileBytes+1))
	if err := os.Mkdir(filepath.Join(cwd, "AGENTS.md"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := discoverEnvironment(context.Background(), home, domain.ExecutionBinding{CWD: cwd})
	if err != nil || result.Status != "partial" || len(result.Skills) != 1 || result.Skills[0].Path != filepath.Join(root, "linked", "SKILL.md") {
		t.Fatalf("linked/partial discovery: %+v, %v", result, err)
	}
	codes := map[string]bool{}
	for _, issue := range result.Issues {
		codes[issue.Code] = true
	}
	for _, code := range []string{"symlink_cycle", "skill_invalid", "skill_unreadable", "agents_unreadable"} {
		if !codes[code] {
			t.Errorf("missing explicit issue %q: %+v", code, result.Issues)
		}
	}
	if result.SkillRoots[0].Status != "partial" || result.SkillRoots[1].Status != "missing" {
		t.Fatal("partial/missing roots lost their distinct status")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := discoverEnvironment(cancelled, home, domain.ExecutionBinding{CWD: cwd}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was hidden as missing discovery: %v", err)
	}
}
