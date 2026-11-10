package computers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oines/astral/internal/skills"
)

func TestManagedSharedRecoversInterruptedReplacement(t *testing.T) {
	for _, label := range []string{"before-activation", "after-activation", "backup-only", "recreated-before-activation"} {
		t.Run(label, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			m := newManagedSkills(root)
			first, firstData := managedTestArchive(t, "example", 1, "first")
			second, secondData := managedTestArchive(t, "example", 2, "second")
			if err := m.applyShared(ctx, first, firstData); err != nil {
				t.Fatal(err)
			}
			archive, err := secondData()
			if err != nil {
				t.Fatal(err)
			}
			stage, err := m.stageSkill(ctx, m.sharedStageRoot(), second, archive)
			if err != nil {
				t.Fatal(err)
			}
			entry := m.state.Load().Shared[first.Name]
			entry.Pending = &sharedInstall{Stage: filepath.Base(stage), Version: second}
			if err := m.saveSharedEntryLocked(first.Name, entry); err != nil {
				t.Fatal(err)
			}
			path := m.sharedPath(first.Name)
			backup := filepath.Join(stage, ".previous")
			if err := moveManagedSkillPath(path, backup, false); err != nil {
				t.Fatal(err)
			}
			want := first
			contents := "first"
			activate := label == "after-activation"
			if activate {
				if err := moveManagedSkillPath(filepath.Join(stage, second.Name), path, false); err != nil {
					t.Fatal(err)
				}
				want, contents = second, "edit after activation"
				writeManagedSkillTestFile(t, filepath.Join(path, "reference.txt"), contents)
			}
			if label == "backup-only" {
				if err := os.RemoveAll(filepath.Join(stage, second.Name)); err != nil {
					t.Fatal(err)
				}
			}
			if label == "recreated-before-activation" {
				contents = "recreated before activation"
				writeManagedSkillTestFile(t, filepath.Join(path, "reference.txt"), contents)
			}
			m = newManagedSkills(root)
			status, err := m.status(ctx)
			if err != nil || status.SyncError != "" || len(status.Shared) != 1 || status.Shared[0].Revision != want.Revision {
				t.Fatalf("unexpected recovered version: %+v %v", status, err)
			}
			if data, err := os.ReadFile(filepath.Join(path, "reference.txt")); err != nil || string(data) != contents {
				t.Fatalf("restart lost the working copy: %q %v", data, err)
			}
			for _, temporary := range []string{stage, backup} {
				if _, err := os.Stat(temporary); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("temporary directory remains: %s, %v", temporary, err)
				}
			}
		})
	}
}

func TestManagedSharedRecoversInterruptedRemoval(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	m := newManagedSkills(root)
	first, firstData := managedTestArchive(t, "example", 1, "first")
	removed := skills.Version{Name: first.Name, Revision: 2}
	if err := m.applyShared(ctx, first, firstData); err != nil {
		t.Fatal(err)
	}
	stage, err := os.MkdirTemp(m.sharedStageRoot(), ".staging-")
	if err != nil {
		t.Fatal(err)
	}
	entry := m.state.Load().Shared[first.Name]
	entry.Pending = &sharedInstall{Stage: filepath.Base(stage), Version: removed}
	if err := m.saveSharedEntryLocked(first.Name, entry); err != nil {
		t.Fatal(err)
	}
	path := m.sharedPath(first.Name)
	if err := moveManagedSkillPath(path, filepath.Join(stage, ".previous"), false); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(path, "SKILL.md")
	newBody := managedTestSkillMD(first.Name, "new local copy")
	writeManagedSkillTestFile(t, newPath, newBody)

	m = newManagedSkills(root)
	if err := m.applyShared(ctx, removed, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(newPath); err != nil || string(data) != newBody {
		t.Fatalf("replayed removal touched files created after the rename: %q %v", data, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary directory remains after removal: %s, %v", stage, err)
	}
}
