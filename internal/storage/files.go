package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type candidate struct {
	CleanupItem
	Path        string `json:"path"`
	Fingerprint string `json:"fingerprint"`
	External    bool   `json:"external,omitempty"`
	Started     bool   `json:"started,omitempty"`
}

// inspectTree never follows a symlink and fingerprints every entry. A token
// therefore stops matching when files change after the user selected them.
func inspectTree(ctx context.Context, path string) (bytes int64, fingerprint string, err error) {
	h := sha256.New()
	count := 0
	err = filepath.WalkDir(path, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 100000 {
			return errors.New("file inventory exceeded 100000 entries")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink %s", name)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("refusing special file %s", name)
		}
		rel, err := filepath.Rel(path, name)
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Directory size/timestamps describe filesystem bookkeeping and can
			// settle after child handles close on Windows. Relative paths retain
			// membership identity; only regular files contribute content metadata.
			fmt.Fprintf(h, "%s\x00directory\n", rel)
			return nil
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00%d\n", rel, info.Size(), info.ModTime().UnixNano(), info.Mode())
		bytes += info.Size()
		return nil
	})
	return bytes, hex.EncodeToString(h.Sum(nil)), err
}
func (s *Service) candidate(ctx context.Context, path, kind, name string) (candidate, error) {
	n, fingerprint, err := inspectTree(ctx, path)
	if err != nil {
		return candidate{}, err
	}
	sum := sha256.Sum256([]byte(path + "\x00" + fingerprint))
	return candidate{CleanupItem: CleanupItem{Token: hex.EncodeToString(sum[:]), Kind: kind, Name: name, Bytes: n}, Path: path, Fingerprint: fingerprint}, nil
}
func safeDirectory(path string) bool {
	actual, err := filepath.EvalSymlinks(path)
	abs, absErr := filepath.Abs(path)
	return err == nil && absErr == nil && actual == abs
}

// An interrupted RemoveAll may already have removed the manifest. A durable
// started item still proves ownership, but current recovery/update protection
// always wins over that earlier cleanup authorization.
func (s *Service) resumableBackup(ctx context.Context, item candidate) error {
	if item.Kind != "upgrade_backup" || filepath.Dir(item.Path) != filepath.Join(filepath.Dir(s.config.Directory), "upgrade-backups") || !safeDirectory(filepath.Dir(item.Path)) {
		return errors.New("cleanup backup ownership changed")
	}
	if s.config.Updating != nil && s.config.Updating(ctx) {
		return errors.New("application update is running")
	}
	if _, err := os.Stat(filepath.Join(s.config.Directory, "migration-state.json")); err == nil {
		return errors.New("migration recovery protects upgrade backups")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(s.config.Directory, "last-upgrade.json"))
	if err == nil {
		var record struct {
			Schema int    `json:"schema"`
			Backup string `json:"backup"`
		}
		if json.Unmarshal(raw, &record) != nil || record.Schema != 1 || !filepath.IsAbs(record.Backup) {
			return errors.New("cannot validate the latest upgrade recovery point")
		}
		if filepath.Clean(record.Backup) == item.Path {
			return errors.New("latest upgrade recovery backup is protected")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	info, err := os.Lstat(item.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("cleanup backup was replaced")
	}
	return nil
}

func (s *Service) validateCandidate(ctx context.Context, item candidate) (bool, error) {
	if _, err := os.Lstat(item.Path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if s.config.Updating != nil && s.config.Updating(ctx) {
		return true, errors.New("application update is running")
	}
	switch item.Kind {
	case "upgrade_backup":
		if err := s.resumableBackup(ctx, item); err != nil {
			return true, err
		}
		raw, err := os.ReadFile(filepath.Join(item.Path, "manifest.json"))
		if err != nil {
			return true, err
		}
		var manifest struct {
			Schema int    `json:"schema"`
			Data   string `json:"data"`
		}
		if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != 1 || filepath.Clean(manifest.Data) != s.config.Directory {
			return true, errors.New("backup ownership changed")
		}
	case "model_cache":
		if item.Path != filepath.Join(s.config.Directory, "cache", "models.dev.json") || !safeDirectory(filepath.Dir(item.Path)) {
			return true, errors.New("model cache ownership changed")
		}
	case "temporary":
		parent, base := filepath.Dir(item.Path), filepath.Base(item.Path)
		if !safeDirectory(parent) {
			return true, errors.New("temporary directory ownership changed")
		}
		if parent == s.config.Directory && strings.HasPrefix(base, ".migration-") {
			if _, err := os.Stat(filepath.Join(parent, "migration-state.json")); err == nil {
				return true, errors.New("migration is running")
			} else if !errors.Is(err, os.ErrNotExist) {
				return true, err
			}
		} else if parent != filepath.Join(s.config.Directory, "cache") || !strings.HasPrefix(base, ".models-dev-") {
			return true, errors.New("temporary file ownership changed")
		}
		info, err := os.Lstat(item.Path)
		if err != nil {
			return true, err
		}
		if time.Since(info.ModTime()) <= 24*time.Hour {
			return true, errors.New("temporary file is no longer stale")
		}
	default:
		return true, errors.New("unknown application cleanup category")
	}
	current, err := s.candidate(ctx, item.Path, item.Kind, item.Name)
	if err != nil {
		return true, err
	}
	if current.Token != item.Token {
		return true, errors.New("cleanup item changed; refresh the inventory")
	}
	return true, nil
}
func (s *Service) candidates(ctx context.Context) ([]candidate, error) {
	data := s.config.Directory
	if !safeDirectory(data) {
		return nil, errors.New("data directory contains a symlink or is unavailable")
	}
	items := []candidate{}
	var problems []error
	protected := map[string]bool{}
	migrationPending := false
	for _, name := range []string{"migration-state.json", "last-upgrade.json"} {
		raw, err := os.ReadFile(filepath.Join(data, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if name == "migration-state.json" {
			migrationPending = true
		}
		var record struct {
			Schema int    `json:"schema"`
			Backup string `json:"backup"`
		}
		if err != nil || json.Unmarshal(raw, &record) != nil || record.Schema != 1 || !filepath.IsAbs(record.Backup) {
			migrationPending = true
			problems = append(problems, fmt.Errorf("cannot validate %s; upgrade backups are protected", name))
			continue
		}
		protected[filepath.Clean(record.Backup)] = true
	}
	updating := s.config.Updating != nil && s.config.Updating(ctx)
	parent := filepath.Join(filepath.Dir(data), "upgrade-backups")
	entries, err := os.ReadDir(parent)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, err)
	}
	if err == nil && !safeDirectory(parent) {
		problems = append(problems, errors.New("upgrade backup directory contains a symlink"))
		entries = nil
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(parent, entry.Name())
		if !safeDirectory(path) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(path, "manifest.json"))
		var manifest struct {
			Schema int    `json:"schema"`
			Data   string `json:"data"`
		}
		if err != nil || json.Unmarshal(raw, &manifest) != nil || manifest.Schema != 1 || filepath.Clean(manifest.Data) != data {
			// Backups for another Server may share this parent. Never adopt them.
			continue
		}
		item, err := s.candidate(ctx, path, "upgrade_backup", entry.Name())
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if protected[path] || migrationPending || updating {
			item.Protected = true
			item.Reason = "upgrade_recovery"
		}
		items = append(items, item)
	}
	cache := filepath.Join(data, "cache")
	if info, err := os.Lstat(cache); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			problems = append(problems, errors.New("cache directory is a symlink"))
		} else {
			entries, err := os.ReadDir(cache)
			if err != nil {
				problems = append(problems, err)
			}
			for _, entry := range entries {
				kind := ""
				if entry.Name() == "models.dev.json" {
					kind = "model_cache"
				} else if strings.HasPrefix(entry.Name(), ".models-dev-") {
					info, err := entry.Info()
					if err == nil && time.Since(info.ModTime()) > 24*time.Hour {
						kind = "temporary"
					}
				}
				if kind == "" || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				item, err := s.candidate(ctx, filepath.Join(cache, entry.Name()), kind, filepath.Join("cache", entry.Name()))
				if err != nil {
					problems = append(problems, err)
					continue
				}
				if updating {
					item.Protected = true
					item.Reason = "update_running"
				}
				items = append(items, item)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, err)
	}
	entries, err = os.ReadDir(data)
	if err != nil {
		problems = append(problems, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".migration-") || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) <= 24*time.Hour {
			continue
		}
		item, err := s.candidate(ctx, filepath.Join(data, entry.Name()), "temporary", entry.Name())
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if migrationPending || updating {
			item.Protected = true
			item.Reason = "upgrade_recovery"
		}
		items = append(items, item)
	}
	if s.config.ExternalCleanup != nil {
		external, err := s.config.ExternalCleanup(ctx)
		if err != nil {
			problems = append(problems, err)
		} else {
			seen := map[string]bool{}
			for _, item := range external {
				if !strings.HasPrefix(item.Token, "desktop:") || item.Kind != "update_cache" || item.Bytes < 0 || seen[item.Token] {
					problems = append(problems, errors.New("invalid Desktop cleanup inventory"))
					continue
				}
				seen[item.Token] = true
				if updating {
					item.Protected = true
					item.Reason = "update_running"
				}
				items = append(items, candidate{CleanupItem: item, External: true})
			}
		}
	}
	return items, errors.Join(problems...)
}
func (s *Service) Inventory(ctx context.Context) CleanupInventory {
	items, err := s.candidates(ctx)
	out := CleanupInventory{Items: []CleanupItem{}}
	for _, item := range items {
		out.Items = append(out.Items, item.CleanupItem)
		if !item.Protected {
			out.TotalBytes += item.Bytes
		}
	}
	if err != nil {
		out.Error = err.Error()
	}
	return out
}
func (s *Service) physical(ctx context.Context) Physical {
	out := Physical{Items: []PhysicalItem{}, Errors: []string{}}
	add := func(id string, n int64) {
		out.Items = append(out.Items, PhysicalItem{ID: id, Bytes: n})
		out.TotalBytes += n
	}
	for _, database := range []string{"astral.db", "core.db", "recall.db"} {
		var bytes int64
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			info, err := os.Lstat(filepath.Join(s.config.Directory, database+suffix))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				out.Errors = append(out.Errors, err.Error())
				continue
			}
			if !info.Mode().IsRegular() {
				out.Errors = append(out.Errors, database+suffix+" is not a regular file")
				continue
			}
			bytes += info.Size()
		}
		add(database, bytes)
		out.DatabaseBytes += bytes
	}
	items, err := s.candidates(ctx)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
	}
	countedBackups := map[string]bool{}
	for _, item := range items {
		if item.Kind == "upgrade_backup" {
			out.BackupBytes += item.Bytes
			countedBackups[item.Path] = true
		} else if item.External {
			out.CacheBytes += item.Bytes
		} else if item.Kind == "temporary" && filepath.Dir(item.Path) == s.config.Directory {
			out.TemporaryBytes += item.Bytes
		}
	}
	s.mu.Lock()
	partial := make([]string, 0, len(s.partialBackups))
	for path := range s.partialBackups {
		partial = append(partial, path)
	}
	s.mu.Unlock()
	for _, path := range partial {
		if countedBackups[path] {
			continue
		}
		if filepath.Dir(path) != filepath.Join(filepath.Dir(s.config.Directory), "upgrade-backups") {
			out.Errors = append(out.Errors, "unfinished backup cleanup has invalid ownership")
			continue
		}
		n, _, err := inspectTree(ctx, path)
		if errors.Is(err, os.ErrNotExist) {
			s.mu.Lock()
			delete(s.partialBackups, path)
			s.mu.Unlock()
			continue
		}
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
		}
		out.BackupBytes += n
	}
	cachePath := filepath.Join(s.config.Directory, "cache")
	if _, err := os.Lstat(cachePath); err == nil {
		n, _, err := inspectTree(ctx, cachePath)
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
		}
		out.CacheBytes += n
	} else if !errors.Is(err, os.ErrNotExist) {
		out.Errors = append(out.Errors, err.Error())
	}
	add("upgrade_backups", out.BackupBytes)
	add("cache", out.CacheBytes)
	add("temporary", out.TemporaryBytes)
	out.DiskTotalBytes, out.DiskUsedBytes, out.AvailableBytes, err = diskUsage(s.config.Directory)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
	}
	return out
}
