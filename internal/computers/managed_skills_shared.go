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

func validSharedRemoval(version skills.Version, name string) bool {
	return version.Name == name && version.Revision > 0 && version.Digest == ""
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
	removed := fetch == nil
	if !skills.ValidName(name) || removed && !validSharedRemoval(version, name) || !removed && !validInstalledSkill(version, name) {
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
	var stage string
	if !removed {
		archive, err := fetch()
		if err != nil {
			return err
		}
		stage, err = m.stageSkill(ctx, m.sharedStageRoot(), version, archive)
		if err != nil {
			return err
		}
	} else if base.Digest != "" {
		if err := m.directory(m.sharedStageRoot(), true); err != nil {
			return err
		}
		stage, err = os.MkdirTemp(m.sharedStageRoot(), ".staging-")
		if err != nil {
			return err
		}
	}
	keepStage := false
	if stage != "" {
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
		// Remember the removal revision without touching a never-installed directory.
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
	err = m.activateShared(m.sharedPath(name), stage, removed)
	return errors.Join(err, m.recoverSharedLocked(name, current))
}

func (m *managedSkills) activateShared(path, stage string, removed bool) error {
	m.sharedMu.Lock()
	defer m.sharedMu.Unlock()
	err := m.directory(path, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hadOld := err == nil
	backup := filepath.Join(stage, ".previous")
	if hadOld {
		if err := moveManagedSkillPath(path, backup, false); err != nil {
			return err
		}
	}
	if removed {
		return nil
	}
	err = moveManagedSkillPath(filepath.Join(stage, filepath.Base(path)), path, false)
	if err != nil && hadOld {
		err = errors.Join(err, moveManagedSkillPath(backup, path, false))
	}
	return err
}

// Renames mark which operation reached the stable path. Complete that
// operation or restore its backup before accepting another update.
func (m *managedSkills) recoverSharedLocked(name string, entry installedShared) error {
	install := entry.Pending
	path := m.sharedPath(name)
	stage := filepath.Join(m.sharedStageRoot(), install.Stage)
	backup := filepath.Join(stage, ".previous")
	entry.Pending = nil
	if err := m.directory(m.sharedRoot(), true); err != nil {
		return err
	}
	_, pathErr := os.Lstat(path)
	if pathErr != nil && !errors.Is(pathErr, os.ErrNotExist) {
		return pathErr
	}
	if install.Version.Digest == "" {
		backupErr := m.directory(backup, false)
		if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
			return backupErr
		}
		// A backup proves the removal happened even if the user has since
		// created a new local directory with the same name.
		if backupErr == nil || pathErr != nil {
			entry.Version = install.Version
		}
	} else {
		stageErr := m.directory(filepath.Join(stage, name), false)
		if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) {
			return stageErr
		}
		if pathErr != nil {
			if err := m.restoreShared(path, backup); err != nil {
				return err
			}
		} else if stageErr != nil {
			entry.Version = install.Version
		}
	}
	if err := syncManagedSkillDir(m.sharedRoot()); err != nil {
		return err
	}
	if err := syncManagedSkillDir(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := m.saveSharedEntryLocked(name, entry); err != nil {
		return err
	}
	return os.RemoveAll(stage)
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
		return err
	}
	return moveManagedSkillPath(backup, path, false)
}
