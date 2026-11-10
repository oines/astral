package computers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/skills"
)

type sharedInstall struct {
	Stage   string         `json:"stage"`
	Version skills.Version `json:"version"`
}

func validSharedStage(stage string) bool {
	suffix, ok := strings.CutPrefix(stage, ".staging-")
	return ok && validIdentity(suffix)
}

func (m *managedSkills) sharedStageRoot() string {
	return filepath.Join(m.root, ".shared-staging")
}

func (m *managedSkills) sharedUpdateDigest(ctx context.Context, path string) (string, bool, error) {
	if err := m.directory(path, false); err != nil {
		return "", false, err
	}
	return skills.DirectoryDigestForUpdate(ctx, path)
}

// Normal installs clean their own staging directory. At startup remove any
// interrupted downloads that were never recorded in the install journal.
func (m *managedSkills) cleanSharedStages() error {
	root := m.sharedStageRoot()
	if err := m.directory(root, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		return err
	}
	pending := map[string]bool{}
	for _, entry := range m.state.Load().Shared {
		if entry.Pending != nil {
			pending[entry.Pending.Stage] = true
		}
	}
	for _, entry := range entries {
		if entry.IsDir() && validSharedStage(entry.Name()) && !pending[entry.Name()] {
			err = errors.Join(err, os.RemoveAll(filepath.Join(root, entry.Name())))
		}
	}
	return err
}

func (m *managedSkills) applyShared(ctx context.Context, version skills.Version, fetch func() ([]byte, error)) error {
	name := version.Name
	if !skills.ValidName(name) || !validInstalledSkill(version, name) {
		return domain.Invalid("invalid shared skill version")
	}
	state, err := m.snapshot()
	if err != nil {
		return err
	}
	entry := state.Shared[name]
	if entry.Pending != nil {
		m.mu.Lock()
		entry = m.state.Load().Shared[name]
		if entry.Pending != nil {
			err = m.recoverSharedLocked(name, entry)
		}
		m.mu.Unlock()
		if err != nil {
			return err
		}
	}
	entry = m.state.Load().Shared[name]
	base := entry.Version
	if base.Revision >= version.Revision {
		if base.Revision == version.Revision && base != version {
			return domain.Conflict("shared skill revision has a different digest than the installed base")
		}
		return nil
	}
	path := m.sharedPath(name)
	digest, hasGit, err := m.sharedUpdateDigest(ctx, path)
	hasOld := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !hasOld && base.Revision > 0 {
		return nil // A local deletion is also an unpublished change.
	}
	if hasOld && digest != version.Digest && (digest != base.Digest || hasGit) {
		return nil
	}
	var stage string
	keepStage := false
	if digest != version.Digest {
		archive, err := fetch()
		if err != nil {
			return err
		}
		stage, err = m.stageSkill(ctx, m.sharedStageRoot(), version, archive)
		if err != nil {
			return err
		}
		defer func() {
			if !keepStage {
				_ = os.RemoveAll(stage)
			}
		}()
		if err := m.syncSkillDirs(m.sharedStageRoot()); err != nil {
			return err
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current := m.state.Load().Shared[name]
	if current.Pending != nil || current.Version != base {
		return domain.Conflict("shared skill base changed during sync; retry on the next refresh")
	}
	if stage == "" {
		// The files already match the published revision. Leave them in place.
		return m.saveSharedEntryLocked(name, installedShared{Version: version})
	}
	if err := m.directory(m.sharedRoot(), true); err != nil {
		return err
	}
	current.Pending = &sharedInstall{Stage: filepath.Base(stage), Version: version}
	if err := m.saveSharedEntryLocked(name, current); err != nil {
		keepStage = m.state.Load().Shared[name].Pending != nil
		return err
	}
	keepStage = true
	err = m.activateShared(ctx, path, filepath.Join(stage, name), hasOld, base.Digest)
	return errors.Join(err, m.recoverSharedLocked(name, current))
}

func (m *managedSkills) activateShared(ctx context.Context, path, staged string, hadOld bool, expectedDigest string) error {
	m.sharedMu.Lock()
	defer m.sharedMu.Unlock()
	err := m.directory(path, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if (err == nil) != hadOld {
		return nil // A draft appeared or was deleted during the download.
	}
	if !hadOld {
		return moveManagedSkillPath(staged, path, false)
	}

	backup := filepath.Join(filepath.Dir(staged), ".previous")
	if _, err := os.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return domain.Conflict("a previous shared skill replacement needs recovery")
	}
	if err := moveManagedSkillPath(path, backup, false); err != nil {
		return err
	}
	// Verify the tree that was actually moved, including edits made while the
	// archive was fetched. This check only runs for an incoming content change.
	digest, hasGit, err := m.sharedUpdateDigest(ctx, backup)
	if err == nil && !hasGit && digest == expectedDigest {
		err = moveManagedSkillPath(staged, path, false)
		if err == nil {
			return nil
		}
	}
	return errors.Join(err, moveManagedSkillPath(backup, path, false))
}

// A staged directory is consumed only by the rename to the stable path. If it
// is still present after an interruption, roll back. If consumed, finish
// recording the new base without inspecting or overwriting any newer edits.
func (m *managedSkills) recoverSharedLocked(name string, entry installedShared) error {
	install := entry.Pending
	path := m.sharedPath(name)
	stage := filepath.Join(m.sharedStageRoot(), install.Stage)
	backup := filepath.Join(stage, ".previous")
	stageErr := m.directory(filepath.Join(stage, name), false)
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) {
		return stageErr
	}
	entry.Pending = nil
	// The canonical directory can also be missing after an interruption. If
	// there is a backup, keep the only working copy instead of discarding it.
	_, pathErr := os.Lstat(path)
	if pathErr != nil && !errors.Is(pathErr, os.ErrNotExist) {
		return pathErr
	}
	if stageErr == nil || errors.Is(pathErr, os.ErrNotExist) {
		if err := m.restoreShared(path, backup); err != nil {
			return err
		}
		if err := m.saveSharedEntryLocked(name, entry); err != nil {
			return err
		}
		return os.RemoveAll(stage)
	}
	if err := syncManagedSkillDir(m.sharedRoot()); err != nil {
		return err
	}
	if err := syncManagedSkillDir(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(backup); err != nil {
		return err
	}
	if err := os.Remove(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncManagedSkillDir(m.sharedStageRoot()); err != nil {
		return err
	}
	return m.saveSharedEntryLocked(name, installedShared{Version: install.Version})
}

func (m *managedSkills) restoreShared(path, backup string) error {
	err := m.directory(backup, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	m.sharedMu.Lock()
	defer m.sharedMu.Unlock()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		return domain.Conflict("shared skill recovery found a new local draft; previous files are at " + backup)
	}
	if err := moveManagedSkillPath(backup, path, false); err != nil {
		return err
	}
	if err := syncManagedSkillDir(m.sharedRoot()); err != nil {
		return err
	}
	return syncManagedSkillDir(filepath.Dir(backup))
}
