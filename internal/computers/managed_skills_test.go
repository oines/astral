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
	data, err := skills.Pack(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return skills.Version{Name: name, Revision: revision, Digest: skills.Digest(data)}, func() ([]byte, error) { return data, nil }
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
	if err := moveManagedSkillPath(path, m.builtinBackupPath(), false); err != nil {
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
	if err := m.replaceBuiltin(staged); err != nil {
		t.Fatal(err)
	}
	// The replacement reached the fixed path, but the fingerprint in state.json
	// still refers to the previous tree.
	m = newManagedSkills(root)
	state, err := m.snapshot()
	if err != nil || state.Builtin != second {
		t.Fatalf("restart did not recover the installed version: %+v %v", state.Builtin, err)
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
	if _, err := os.Stat(m.builtinBackupPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary backup remains after recovery: %v", err)
	}
}

func TestManagedSharedUpdatesAtTheSamePath(t *testing.T) {
	ctx := t.Context()
	root, home, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	m := newManagedSkills(root)
	const name = "shared-test"
	path := filepath.Join(m.sharedRoot(), name)
	entry := filepath.Join(path, "SKILL.md")
	writeManagedSkillTestFile(t, filepath.Join(home, ".agents", "skills", "global", "SKILL.md"), managedTestSkillMD("global", "global"))
	writeManagedSkillTestFile(t, filepath.Join(cwd, ".agents", "skills", "project", "SKILL.md"), managedTestSkillMD("project", "project"))
	for i, contents := range []string{"version one", "version two", "version three"} {
		version, fetch := managedTestArchive(t, name, int64(i+1), contents)
		if err := m.applyShared(ctx, version, fetch); err != nil {
			t.Fatal(err)
		}
		m = newManagedSkills(root)
		if err := m.applyShared(ctx, version, func() ([]byte, error) {
			return nil, errors.New("current revision started a download")
		}); err != nil {
			t.Fatal(err)
		}
		status, err := m.status(ctx)
		if err != nil || len(status.Shared) != 1 {
			t.Fatalf("expected one working copy after restart: %+v %v", status, err)
		}
		if local := status.Shared[0]; local.Path != path ||
			local.Revision != version.Revision || local.Digest != version.Digest {
			t.Fatalf("working copy does not match revision %d at %s: %+v", version.Revision, path, local)
		}
		if data, err := os.ReadFile(filepath.Join(path, "reference.txt")); err != nil || string(data) != contents {
			t.Fatalf("reference at the same path did not receive revision %d: %q %v", version.Revision, data, err)
		}
		discovered, err := discoverEnvironment(ctx, home, domain.ExecutionBinding{CWD: cwd}, m)
		if err != nil || len(discovered.Skills) != 3 || discovered.Skills[0].Scope != sharedSkillScope ||
			discovered.Skills[0].Path != entry || discovered.Skills[0].SHA256 != fileDigest([]byte(managedTestSkillMD(name, contents))) {
			t.Fatalf("discovery did not pick up the new content at the fixed path: %+v %v", discovered, err)
		}
		entries, err := os.ReadDir(m.sharedRoot())
		if err != nil || len(entries) != 1 || entries[0].Name() != name {
			t.Fatalf("synchronization left an old copy or temporary directory: %v %v", entries, err)
		}
	}
}

func TestManagedSharedOverwritesLocalEditsOnNewRevision(t *testing.T) {
	ctx := t.Context()
	m := newManagedSkills(t.TempDir())
	first, firstData := managedTestArchive(t, "shared-test", 1, "version one")
	second, secondData := managedTestArchive(t, first.Name, 2, "version two")
	third, thirdData := managedTestArchive(t, first.Name, 3, "version three")
	if err := m.applyShared(ctx, first, firstData); err != nil {
		t.Fatal(err)
	}
	path := m.sharedPath(first.Name)
	reference := filepath.Join(path, "reference.txt")
	gitHead := filepath.Join(path, ".git", "HEAD")
	noDownload := func() ([]byte, error) { return nil, errors.New("unexpected download") }
	writeManagedSkillTestFile(t, reference, "local edit")
	writeManagedSkillTestFile(t, gitHead, "ref: refs/heads/local-work")
	if err := m.applyShared(ctx, first, noDownload); err != nil {
		t.Fatal(err)
	}
	if err := m.applyShared(ctx, second, func() ([]byte, error) { return []byte("invalid archive"), nil }); err == nil {
		t.Fatal("accepted an invalid archive")
	}
	if data, err := os.ReadFile(reference); err != nil || string(data) != "local edit" {
		t.Fatalf("unchanged revision or failed download changed local files: %q %v", data, err)
	}
	if err := m.applyShared(ctx, second, func() ([]byte, error) {
		writeManagedSkillTestFile(t, reference, "edit during download")
		return secondData()
	}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(reference); err != nil || string(data) != "version two" {
		t.Fatalf("new revision did not overwrite edited files: %q %v", data, err)
	}
	if _, err := os.Stat(gitHead); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old Git metadata survived replacement: %v", err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := m.applyShared(ctx, second, noDownload); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("same revision recreated a locally deleted directory: %v", err)
	}
	if err := m.applyShared(ctx, third, thirdData); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(reference); err != nil || string(data) != "version three" {
		t.Fatalf("new revision did not reinstall locally deleted files: %q %v", data, err)
	}
}

func TestManagedSharedRemovalAndRepublication(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	m := newManagedSkills(root)
	first, firstData := managedTestArchive(t, "shared-test", 1, "version one")
	removed := skills.Version{Name: first.Name, Revision: 2}
	third, thirdData := managedTestArchive(t, first.Name, 3, "version three")
	if err := m.applyShared(ctx, first, firstData); err != nil {
		t.Fatal(err)
	}
	path := m.sharedPath(first.Name)
	entry := filepath.Join(path, "SKILL.md")
	writeManagedSkillTestFile(t, entry, managedTestSkillMD(first.Name, "local edit"))
	if err := m.applyShared(ctx, removed, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("global deletion did not remove installed files: %v", err)
	}

	// An older version can arrive after the deletion. A second copy of the
	// removal marker must also leave newly created local files alone.
	m = newManagedSkills(root)
	local := managedTestSkillMD(first.Name, "new local content")
	writeManagedSkillTestFile(t, entry, local)
	if err := m.applyShared(ctx, first, func() ([]byte, error) { return nil, errors.New("stale publish downloaded") }); err != nil {
		t.Fatal(err)
	}
	if err := m.applyShared(ctx, removed, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(entry); err != nil || string(data) != local {
		t.Fatalf("stale sync changed new local files: %q %v", data, err)
	}
	if data, revision, err := m.export(ctx, first.Name); err != nil || len(data) == 0 || revision != removed.Revision {
		t.Fatalf("export did not use the recorded deletion as its base: %d %v", revision, err)
	}
	if err := m.applyShared(ctx, third, thirdData); err != nil {
		t.Fatal(err)
	}
	if err := m.applyShared(ctx, removed, nil); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(entry); err != nil || string(data) != managedTestSkillMD(first.Name, "version three") {
		t.Fatalf("republication was lost: %q %v", data, err)
	}

	name := "only-local"
	localPath := filepath.Join(m.sharedPath(name), "SKILL.md")
	writeManagedSkillTestFile(t, localPath, managedTestSkillMD(name, "never installed"))
	if err := m.applyShared(ctx, skills.Version{Name: name, Revision: 2}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("global deletion removed a never-installed directory: %v", err)
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
	path := filepath.Join(m.sharedPath("test-skill"), "SKILL.md")
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
			root := m.sharedPath("shared-test")
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
