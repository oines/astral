package computers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/skills"
)

func writeManagedSkillTestFile(t testing.TB, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func managedTestSkillMD(name, text string) string {
	return "---\nname: " + name + "\ndescription: Test this skill.\n---\n\n" + text + "\n"
}

func managedTestArchive(t testing.TB, name string, revision int64, text string) (skills.Version, func() ([]byte, error)) {
	t.Helper()
	root := t.TempDir()
	path, frontmatterName := root, name
	if name == "" {
		frontmatterName = "builtin-test"
		path = filepath.Join(root, frontmatterName)
	}
	writeManagedSkillTestFile(t, filepath.Join(path, "SKILL.md"), managedTestSkillMD(frontmatterName, text))
	writeManagedSkillTestFile(t, filepath.Join(path, "reference.txt"), text)
	data, digest, err := skills.Pack(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return skills.Version{Name: name, Revision: revision, Digest: digest}, func() ([]byte, error) { return data, nil }
}

func TestManagedBuiltinUpdatesAtTheSamePath(t *testing.T) {
	ctx := t.Context()
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	m := newManagedSkills(root)
	first, firstData := managedTestArchive(t, "", 0, "version one")
	second, secondData := managedTestArchive(t, "", 0, "version two")
	original := m.builtinRoot()
	entry := filepath.Join(original, "builtin-test", "SKILL.md")
	reference := filepath.Join(original, "builtin-test", "reference.txt")
	obsolete := filepath.Join(original, "builtin-test", "obsolete.txt")
	discover := func() DiscoveredSkill {
		t.Helper()
		result, err := discoverEnvironment(ctx, home, domain.ExecutionBinding{CWD: cwd}, m)
		if err != nil || len(result.Skills) != 1 || result.Skills[0].Path != entry || result.Skills[0].Scope != builtinSkillScope {
			t.Fatalf("expected built-in skill at %s: %+v %v", entry, result, err)
		}
		return result.Skills[0]
	}
	assertReference := func(want string) {
		t.Helper()
		if data, err := os.ReadFile(reference); err != nil || string(data) != want {
			t.Fatalf("expected %q at %s, got %q: %v", want, reference, data, err)
		}
	}
	noDownload := func() ([]byte, error) { return nil, errors.New("unexpected download") }

	if err := m.applyBuiltin(ctx, first, firstData); err != nil {
		t.Fatal(err)
	}
	before := discover()
	writeManagedSkillTestFile(t, reference, "local edit")
	writeManagedSkillTestFile(t, obsolete, "only in the old install")
	if err := m.applyBuiltin(ctx, first, noDownload); err != nil {
		t.Fatal(err)
	}
	// Removing a file from an existing bundle is a local edit. Reopening the
	// Daemon data directory must retain both the marker and that edit.
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	m = newManagedSkills(root)
	if err := m.applyBuiltin(ctx, first, noDownload); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(entry); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local deletion was lost: %v", err)
	}
	assertReference("local edit")

	malformed := []byte("invalid built-in archive")
	invalid := skills.Version{Digest: skills.Digest(malformed)}
	if err := m.applyBuiltin(ctx, invalid, func() ([]byte, error) { return malformed, nil }); err == nil {
		t.Fatal("accepted a malformed built-in archive")
	}
	assertReference("local edit")

	if err := m.applyBuiltin(ctx, second, secondData); err != nil {
		t.Fatal(err)
	}
	status, err := m.status(ctx)
	if err != nil || status.Builtin == nil || status.Builtin.Path != original || status.Builtin.Digest != second.Digest {
		t.Fatalf("built-in bundle did not update at the original path: %+v %v", status, err)
	}
	if after := discover(); after.SHA256 == before.SHA256 {
		t.Fatal("skill index did not pick up the new contents")
	}
	assertReference("version two")
	if _, err := os.Stat(obsolete); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete file survived the replacement: %v", err)
	}

	if err := os.RemoveAll(original); err != nil {
		t.Fatal(err)
	}
	if err := m.applyBuiltin(ctx, second, secondData); err != nil {
		t.Fatal(err)
	}
	discover()
	assertReference("version two")
}

func TestManagedBuiltinKeepsLegacyInstallPath(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	m := newManagedSkills(root)
	first, _ := managedTestArchive(t, "", 0, "version one")
	second, secondData := managedTestArchive(t, "", 0, "version two")
	legacy := installedBundle{ID: "generation-test", Version: first}
	path := filepath.Join(m.builtinRoot(), legacy.ID)
	reference := filepath.Join(path, "builtin-test", "reference.txt")
	entry := filepath.Join(path, "builtin-test", "SKILL.md")
	writeManagedSkillTestFile(t, entry, managedTestSkillMD("builtin-test", "legacy install"))
	writeManagedSkillTestFile(t, reference, "legacy local edit")
	m.mu.Lock()
	err := m.saveLocked(installedSkills{Builtin: legacy})
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	m = newManagedSkills(root)
	if err := m.applyBuiltin(ctx, first, func() ([]byte, error) { return nil, errors.New("unexpected legacy download") }); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(reference); err != nil || string(data) != "legacy local edit" {
		t.Fatalf("legacy edit lost at %s: %q %v", reference, data, err)
	}
	if err := m.applyBuiltin(ctx, second, secondData); err != nil {
		t.Fatal(err)
	}
	m = newManagedSkills(root)
	status, err := m.status(ctx)
	if err != nil || status.Builtin == nil || status.Builtin.Path != path || status.Builtin.Digest != second.Digest {
		t.Fatalf("legacy built-in path changed after update: %+v %v", status, err)
	}
	if data, err := os.ReadFile(reference); err != nil || string(data) != "version two" {
		t.Fatalf("legacy path did not receive the update: %q %v", data, err)
	}
	result, err := discoverEnvironment(ctx, t.TempDir(), domain.ExecutionBinding{CWD: t.TempDir()}, m)
	if err != nil || len(result.Skills) != 1 || result.Skills[0].Path != entry {
		t.Fatalf("skill index lost the legacy path: %+v %v", result, err)
	}
}

func TestManagedBuiltinRecoversInterruptedReplacement(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	m := newManagedSkills(root)
	first, firstData := managedTestArchive(t, "", 0, "first")
	second, secondData := managedTestArchive(t, "", 0, "second")
	if err := m.applyBuiltin(ctx, first, firstData); err != nil {
		t.Fatal(err)
	}
	path := m.builtinRoot()
	reference := filepath.Join(path, "builtin-test", "reference.txt")
	writeManagedSkillTestFile(t, reference, "local edit")
	if err := moveManagedSkillPath(path, builtinBackupPath(path), false); err != nil {
		t.Fatal(err)
	}
	m = newManagedSkills(root)
	if data, err := os.ReadFile(reference); err != nil || string(data) != "local edit" {
		t.Fatalf("restart did not restore original directory: %q %v", data, err)
	}
	archive, err := secondData()
	if err != nil {
		t.Fatal(err)
	}
	staged, err := m.stageSkill(ctx, filepath.Dir(path), second, archive)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.replaceBuiltin(path, staged); err != nil {
		t.Fatal(err)
	}
	// The replacement reached the fixed path, but the fingerprint in state.json
	// still refers to the previous tree.
	m = newManagedSkills(root)
	state, err := m.snapshot()
	if err != nil || state.Builtin.Version != second {
		t.Fatalf("restart did not recover the installed version: %+v %v", state.Builtin.Version, err)
	}
	if data, err := os.ReadFile(reference); err != nil || string(data) != "second" {
		t.Fatalf("restart lost the installed update: %q %v", data, err)
	}
	if err := os.Remove(filepath.Join(path, builtinDigestFile)); err != nil {
		t.Fatal(err)
	}
	m = newManagedSkills(root)
	if err := m.applyBuiltin(ctx, second, func() ([]byte, error) {
		return nil, errors.New("unexpected download after recovering the new fingerprint")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(builtinBackupPath(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary backup remains after recovery: %v", err)
	}
}

func TestManagedSharedChecksForEditsAndFindsRetiredDrafts(t *testing.T) {
	ctx := t.Context()
	m := newManagedSkills(t.TempDir())
	first, firstData := managedTestArchive(t, "shared-test", 1, "version one")
	second, secondData := managedTestArchive(t, "shared-test", 2, "version two")
	if err := m.applyShared(ctx, first, firstData); err != nil {
		t.Fatal(err)
	}
	state, _ := m.snapshot()
	oldID := state.Shared[first.Name].Active
	oldPath := m.draftPath(first.Name, oldID)
	reference := filepath.Join(oldPath, "reference.txt")
	writeManagedSkillTestFile(t, reference, "already dirty")
	if err := m.applyShared(ctx, second, func() ([]byte, error) { return nil, errors.New("dirty draft started a download") }); err != nil {
		t.Fatal(err)
	}
	writeManagedSkillTestFile(t, reference, "version one")
	if err := m.applyShared(ctx, second, func() ([]byte, error) {
		writeManagedSkillTestFile(t, reference, "edit during download")
		return secondData()
	}); err != nil {
		t.Fatal(err)
	}
	status, err := m.status(ctx)
	if err != nil || len(status.Shared) != 1 || !status.Shared[0].Dirty || status.Shared[0].Path != oldPath {
		t.Fatalf("second dirty check did not protect the draft: %+v %v", status, err)
	}
	writeManagedSkillTestFile(t, reference, "version one")
	if err := m.applyShared(ctx, second, secondData); err != nil {
		t.Fatal(err)
	}
	writeManagedSkillTestFile(t, reference, "late save via old path")
	m = newManagedSkills(filepath.Dir(m.root))
	status, err = m.status(ctx)
	if err != nil || len(status.Shared) != 2 || status.Shared[0].Revision != 2 || status.Shared[0].Dirty ||
		!status.Shared[0].Active || status.Shared[0].Path == oldPath || status.Shared[1].Active ||
		!status.Shared[1].Dirty || status.Shared[1].DraftID != oldID || status.Shared[1].Revision != 1 {
		t.Fatalf("late save is not independently selectable after restart: %+v %v", status, err)
	}
	got, err := m.export(ctx, first.Name, oldID)
	if err != nil || skills.Digest(got) != status.Shared[1].ContentDigest {
		t.Fatalf("export selected the wrong draft: %v", err)
	}
	home, cwd := t.TempDir(), t.TempDir()
	writeManagedSkillTestFile(t, filepath.Join(home, ".agents", "skills", "global", "SKILL.md"), managedTestSkillMD("global", "global"))
	writeManagedSkillTestFile(t, filepath.Join(cwd, ".agents", "skills", "project", "SKILL.md"), managedTestSkillMD("project", "project"))
	discovered, err := discoverEnvironment(ctx, home, domain.ExecutionBinding{CWD: cwd}, m)
	if err != nil || len(discovered.Skills) != 3 || discovered.Skills[0].Scope != sharedSkillScope ||
		discovered.Skills[0].Path != filepath.Join(status.Shared[0].Path, "SKILL.md") {
		t.Fatalf("discovery exposed a retired version or lost local roots: %+v %v", discovered, err)
	}
}

func TestManagedSharedAcknowledgementAdvancesOnlyTheExportedDraft(t *testing.T) {
	ctx := t.Context()
	m := newManagedSkills(t.TempDir())
	name := "new-skill"
	path := filepath.Join(m.draftPath(name, localSkillDraft), "SKILL.md")
	writeManagedSkillTestFile(t, path, managedTestSkillMD(name, "first draft"))
	data, err := m.export(ctx, name, localSkillDraft)
	if err != nil {
		t.Fatal(err)
	}
	first := skills.Version{Name: name, Revision: 1, Digest: skills.Digest(data)}
	newBody := managedTestSkillMD(name, "continued editing")
	writeManagedSkillTestFile(t, path, newBody)
	if err := m.acknowledge(ctx, name, localSkillDraft, first); err != nil {
		t.Fatal(err)
	}
	status, err := m.status(ctx)
	if err != nil || len(status.Shared) != 1 || status.Shared[0].Revision != 1 || status.Shared[0].Digest != first.Digest ||
		!status.Shared[0].Dirty || status.Shared[0].ContentDigest == first.Digest {
		t.Fatalf("continued editing did not remain dirty: %+v %v", status, err)
	}
	if current, err := os.ReadFile(path); err != nil || string(current) != newBody {
		t.Fatalf("publish acknowledgement wrote working files: %q %v", current, err)
	}
	second := skills.Version{Name: name, Revision: 2, Digest: status.Shared[0].ContentDigest}
	if err := m.applyShared(ctx, second, func() ([]byte, error) { return nil, errors.New("matching contents must not download") }); err != nil {
		t.Fatal(err)
	}
	if err := m.acknowledge(ctx, name, localSkillDraft, first); err != nil {
		t.Fatal(err)
	}
	status, err = m.status(ctx)
	if err != nil || len(status.Shared) != 1 || status.Shared[0].Revision != 2 || status.Shared[0].Dirty || status.Shared[0].Path != filepath.Dir(path) {
		t.Fatalf("fast-forward failed or delayed receipt moved the base backwards: %+v %v", status, err)
	}
}

func TestManagedSkillDiscoveryDoesNotWaitForStateWriter(t *testing.T) {
	m := newManagedSkills(t.TempDir())
	m.mu.Lock()
	defer m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		_, _ = m.discoveryRoots()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("discovery waited on the installer state lock")
	}
}

func TestManagedSkillDiscoveryUpdatesCachedMetadataAndBody(t *testing.T) {
	m := newManagedSkills(t.TempDir())
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(m.draftPath("test-skill", localSkillDraft), "SKILL.md")
	file := managedTestSkillMD("test-skill", "first body")
	writeManagedSkillTestFile(t, path, file)
	discover := func() DiscoveredSkill {
		t.Helper()
		result, err := discoverEnvironment(t.Context(), home, domain.ExecutionBinding{CWD: cwd}, m)
		if err != nil || len(result.Skills) != 1 {
			t.Fatalf("discover: %+v %v", result, err)
		}
		return result.Skills[0]
	}
	first := discover()
	if again := discover(); again != first || len(m.metadata.entries) != 1 {
		t.Fatalf("repeated discovery changed the catalog or missed its cache: %+v", again)
	}
	file = strings.Replace(file, "first body", "other body", 1)
	writeManagedSkillTestFile(t, path, file)
	second := discover()
	if second.Description != first.Description || second.SHA256 == first.SHA256 {
		t.Fatalf("body-only change was not detected: %+v", second)
	}
	file = strings.Replace(file, "Test this skill.", "Changed summary.", 1)
	writeManagedSkillTestFile(t, path, file)
	third := discover()
	if third.Description != "Changed summary." || third.SHA256 == second.SHA256 {
		t.Fatalf("metadata change was not detected: %+v", third)
	}
	for range managedSkillCacheEntries + 1 {
		file += "x"
		if _, _, err := m.metadata.frontmatter(fileDigest([]byte(file)), []byte(file)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(m.metadata.entries); got != managedSkillCacheEntries {
		t.Fatalf("managed metadata cache contains %d entries", got)
	}
}

func BenchmarkManagedSkillDiscovery(b *testing.B) {
	for _, size := range []int{0, 4 << 20} {
		name := "without-large-reference"
		if size != 0 {
			name = "with-4MiB-reference"
		}
		b.Run(name, func(b *testing.B) {
			home, cwd := b.TempDir(), b.TempDir()
			m := newManagedSkills(b.TempDir())
			root := m.draftPath("shared-test", localSkillDraft)
			writeManagedSkillTestFile(b, filepath.Join(root, "SKILL.md"), managedTestSkillMD("shared-test", "body"))
			if size != 0 {
				writeManagedSkillTestFile(b, filepath.Join(root, "reference.txt"), strings.Repeat("x", size))
			}
			binding := domain.ExecutionBinding{CWD: cwd}
			b.ReportAllocs()
			for b.Loop() {
				result, err := discoverEnvironment(b.Context(), home, binding, m)
				if err != nil || len(result.Skills) != 1 {
					b.Fatalf("discover: %+v %v", result, err)
				}
			}
		})
	}
}
