package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type candidate struct {
	Path      string
	Kind      string
	Bytes     int64
	Protected bool
}

// Only regular files under application-owned paths count toward cleanup.
func inspectTree(ctx context.Context, path string) (size int64, err error) {
	count := 0
	err = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 100000 {
			return errors.New("file inventory exceeded 100000 entries")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing symlink or special file %s", entry.Name())
		}
		size += info.Size()
		return nil
	})
	return size, err
}
func rotatedLog(name string) bool {
	base, n, ok := strings.Cut(name, ".log.")
	if !ok || base != "astral-server" && base != "server" {
		return false
	}
	index, err := strconv.ParseUint(n, 10, 32)
	return err == nil && index > 0 && strconv.FormatUint(index, 10) == n
}
func safeDirectory(path string) bool {
	actual, err := filepath.EvalSymlinks(path)
	abs, absErr := filepath.Abs(path)
	return err == nil && absErr == nil && actual == abs
}

// A recovery record with an unknown shape protects all backups. Multiple servers
// may share the backup parent; each manifest must identify this data directory.
func (s *Service) backupsProtected() (all bool, latest string, err error) {
	for _, name := range []string{"migration-state.json", "last-upgrade.json"} {
		raw, readErr := os.ReadFile(filepath.Join(s.config.Directory, name))
		if errors.Is(readErr, os.ErrNotExist) {
			continue
		}
		if name == "migration-state.json" {
			all = true
		}
		var record struct {
			Schema int
			Backup string
		}
		if readErr != nil || json.Unmarshal(raw, &record) != nil || record.Schema != 1 || !filepath.IsAbs(record.Backup) {
			return true, "", fmt.Errorf("cannot validate %s; upgrade backups are protected", name)
		}
		if name == "last-upgrade.json" {
			latest = filepath.Clean(record.Backup)
		}
	}
	return
}

func (s *Service) ownsBackup(path string) bool {
	if !safeDirectory(path) {
		return false
	}
	name := filepath.Join(path, "manifest.json")
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	raw, err := os.ReadFile(name)
	var manifest struct {
		Schema int
		Data   string
	}
	return err == nil && json.Unmarshal(raw, &manifest) == nil && manifest.Schema == 1 && filepath.Clean(manifest.Data) == s.config.Directory
}

func (s *Service) candidates(ctx context.Context, updating bool) (items []candidate, result error) {
	data := s.config.Directory
	if !safeDirectory(data) {
		return nil, errors.New("data directory contains a symlink or is unavailable")
	}
	migrationPending, latest, result := s.backupsProtected()
	add := func(path, kind string, protected bool) {
		size, err := inspectTree(ctx, path)
		if err != nil {
			result = errors.Join(result, err)
			return
		}
		items = append(items, candidate{Path: path, Kind: kind, Bytes: size, Protected: protected || updating})
	}
	entriesIn := func(path string) []os.DirEntry {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			result = errors.Join(result, err)
			return nil
		}
		if !info.IsDir() || !safeDirectory(path) {
			result = errors.Join(result, fmt.Errorf("not a regular application directory: %s", path))
			return nil
		}
		entries, err := os.ReadDir(path)
		result = errors.Join(result, err)
		return entries
	}
	parent := filepath.Join(filepath.Dir(data), "upgrade-backups")
	for _, entry := range entriesIn(parent) {
		path := filepath.Join(parent, entry.Name())
		if entry.IsDir() && s.ownsBackup(path) {
			add(path, "upgrade_backup", migrationPending || path == latest)
		}
	}
	for _, dir := range []string{data, filepath.Join(data, "cache"), filepath.Join(data, "logs")} {
		for _, entry := range entriesIn(dir) {
			if !entry.Type().IsRegular() {
				continue
			}
			name := entry.Name()
			path := filepath.Join(dir, name)
			switch {
			case dir == filepath.Join(data, "logs") && (name == "server.log" || name == "astral-server.log"):
				add(path, "log", true)
			case dir == filepath.Join(data, "logs") && rotatedLog(name):
				add(path, "log", false)
			case dir == filepath.Join(data, "cache") && name == "models.dev.json":
				add(path, "model_cache", false)
			case dir == data && strings.HasPrefix(name, ".migration-") || dir == filepath.Join(data, "cache") && strings.HasPrefix(name, ".models-dev-"):
				info, err := entry.Info()
				if err != nil {
					result = errors.Join(result, err)
					continue
				}
				if time.Since(info.ModTime()) > 24*time.Hour {
					add(path, "temporary", dir == data && migrationPending)
				}
			}
		}
	}
	return
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
	updating := s.config.Updating != nil && s.config.Updating(ctx)
	items, err := s.candidates(ctx, updating)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
	}
	for _, item := range items {
		if !item.Protected {
			out.CleanableBytes += item.Bytes
		}
		switch item.Kind {
		case "upgrade_backup":
			out.BackupBytes += item.Bytes
		case "log":
			out.LogBytes += item.Bytes
		case "temporary":
			if filepath.Dir(item.Path) == s.config.Directory {
				out.TemporaryBytes += item.Bytes
			}
		}
	}
	if s.config.ExternalCache != nil {
		total, cleanable, err := s.config.ExternalCache(ctx)
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
		} else {
			out.CacheBytes += total
			if !updating {
				out.CleanableBytes += cleanable
			}
		}
	}
	cachePath := filepath.Join(s.config.Directory, "cache")
	if _, err := os.Lstat(cachePath); err == nil {
		n, err := inspectTree(ctx, cachePath)
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
	add("logs", out.LogBytes)
	out.DiskTotalBytes, out.DiskUsedBytes, out.AvailableBytes, err = diskUsage(s.config.Directory)
	if err != nil {
		out.Errors = append(out.Errors, err.Error())
	}
	return out
}
