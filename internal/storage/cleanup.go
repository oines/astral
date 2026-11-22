package storage

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// A cleanup always uses fresh ownership and recovery information. The backup
// manifest is removed last so an interrupted cleanup remains discoverable.
func (s *Service) Cleanup(ctx context.Context) CleanupResult {
	s.cleanupMu.Lock()
	defer s.cleanupMu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return CleanupResult{Error: err.Error()}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	var out CleanupResult
	items, failure := s.candidates(ctx, s.config.Updating != nil && s.config.Updating(ctx))
	for _, item := range items {
		if item.Protected {
			continue
		}
		if err := ctx.Err(); err != nil {
			failure = errors.Join(failure, err)
			break
		}
		if s.config.Updating != nil && s.config.Updating(ctx) {
			break
		}
		if !safeDirectory(filepath.Dir(item.Path)) {
			failure = errors.Join(failure, errors.New("application directory changed during cleanup"))
			continue
		}
		if item.Kind == "upgrade_backup" || item.Kind == "temporary" && filepath.Dir(item.Path) == s.config.Directory {
			all, latest, err := s.backupsProtected()
			if all || item.Path == latest || err != nil {
				failure = errors.Join(failure, err)
				continue
			}
		}
		var n int64
		var err error
		if item.Kind == "upgrade_backup" {
			if !s.ownsBackup(item.Path) {
				continue
			}
			n, err = removeBackup(ctx, item.Path)
		} else {
			info, statErr := os.Lstat(item.Path)
			switch {
			case errors.Is(statErr, os.ErrNotExist):
				continue
			case statErr != nil:
				err = statErr
			case !info.Mode().IsRegular():
				continue
			case item.Kind == "temporary" && time.Since(info.ModTime()) <= 24*time.Hour:
				continue
			default:
				err = os.Remove(item.Path)
				if err == nil {
					n = info.Size()
				}
			}
		}
		out.RemovedBytes += n
		failure = errors.Join(failure, err)
	}
	if ctx.Err() == nil && s.config.CleanExternal != nil && (s.config.Updating == nil || !s.config.Updating(ctx)) {
		n, err := s.config.CleanExternal(ctx)
		out.RemovedBytes += n
		failure = errors.Join(failure, err)
	}
	if failure != nil {
		out.Error = failure.Error()
	}
	return out
}

func removeBackup(ctx context.Context, path string) (removed int64, err error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	var directories []string
	removeFile := func(name string) error {
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("backup contains a symlink or special file")
		}
		if err = root.Remove(name); err == nil {
			removed += info.Size()
		}
		return err
	}
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." || name == "manifest.json" {
			return nil
		}
		name = filepath.FromSlash(name)
		if entry.IsDir() {
			directories = append(directories, name)
			return nil
		}
		return removeFile(name)
	})
	if err != nil {
		return removed, err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := root.Remove(directories[i]); err != nil {
			return removed, err
		}
	}
	if err := removeFile("manifest.json"); err != nil {
		return removed, err
	}
	if err := root.Close(); err != nil {
		return removed, err
	}
	return removed, os.Remove(path)
}
