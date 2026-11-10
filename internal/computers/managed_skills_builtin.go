package computers

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/skills"
)

const builtinDigestFile = ".astral-builtin-digest"

func (m *managedSkills) builtinBackupPath() string {
	return filepath.Join(m.root, "."+builtinSkillScope+".previous")
}

func writeBuiltinDigest(path, digest string) error {
	f, err := os.OpenFile(filepath.Join(path, builtinDigestFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(digest)
	return errors.Join(err, f.Close())
}

// A process may stop between moving the old tree and activating the new one,
// or after activation but before writing state.json. The marker travels with
// the staged tree, so it identifies the version that actually reached the path.
func (m *managedSkills) recoverBuiltin(bundle *skills.Version) error {
	if err := m.restoreBuiltin(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	marker := filepath.Join(m.builtinRoot(), builtinDigestFile)
	info, err := os.Lstat(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil // A local edit may have removed the marker.
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != 64 {
		return domain.Invalid("invalid installed built-in skill digest")
	}
	f, err := os.Open(marker)
	if err != nil {
		return err
	}
	digest, err := io.ReadAll(io.LimitReader(f, 65))
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	version := skills.Version{Digest: string(digest)}
	if !validInstalledSkill(version, "") {
		return domain.Invalid("invalid installed built-in skill digest")
	}
	*bundle = version
	return os.RemoveAll(m.builtinBackupPath())
}

func (m *managedSkills) restoreBuiltin() error {
	path := m.builtinRoot()
	err := m.directory(path, false)
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	backup := m.builtinBackupPath()
	if err := m.directory(backup, false); err != nil {
		return err
	}
	m.builtinMu.Lock()
	err = moveManagedSkillPath(backup, path, false)
	m.builtinMu.Unlock()
	if err != nil {
		return err
	}
	return syncManagedSkillDir(m.root)
}

func (m *managedSkills) replaceBuiltin(staged string) error {
	path := m.builtinRoot()
	err := m.directory(path, false)
	hasOld := err == nil
	if !hasOld && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	backup := m.builtinBackupPath()
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	m.builtinMu.Lock()
	defer m.builtinMu.Unlock()
	if hasOld {
		if err := moveManagedSkillPath(path, backup, false); err != nil {
			return err
		}
	}
	err = moveManagedSkillPath(staged, path, false)
	if err != nil && hasOld {
		return errors.Join(err, moveManagedSkillPath(backup, path, false))
	}
	return err
}

func (m *managedSkills) applyBuiltin(ctx context.Context, version skills.Version, fetch func() ([]byte, error)) error {
	if !validInstalledSkill(version, "") {
		return domain.Invalid("invalid built-in skill bundle version")
	}
	state, err := m.snapshot()
	if err != nil {
		return err
	}
	err = m.restoreBuiltin()
	if !errors.Is(err, os.ErrNotExist) {
		if err != nil || state.Builtin == version {
			return err // A matching installation keeps any local changes.
		}
	}
	archive, err := fetch()
	if err != nil {
		return err
	}
	staged, err := m.stageSkill(ctx, m.root, version, archive)
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.replaceBuiltin(staged); err != nil {
		return err
	}
	err = m.syncSkillDirs(m.root)
	m.mu.Lock()
	next := *m.state.Load()
	next.Builtin = version
	m.state.Store(&next)
	if err == nil {
		err = m.saveLocked(next)
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return os.RemoveAll(m.builtinBackupPath())
}
