// Package datamigrate coordinates offline Host/Core upgrades. Callers hold the
// Server's exclusive process lock throughout preparation, startup and commit.
package datamigrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/persistence"
)

const journalName = "migration-state.json"
const resultName = "last-upgrade.json"

type Plan struct {
	Host    persistence.MigrationStatus `json:"host"`
	Core    sqlite.MigrationStatus      `json:"core"`
	Pending bool                        `json:"pending"`
}

func Inspect(ctx context.Context, data string) (Plan, error) {
	var p Plan
	var err error
	p.Host, err = persistence.InspectMigration(ctx, data)
	if err != nil {
		return p, err
	}
	p.Core, err = sqlite.InspectMigration(ctx, filepath.Join(data, "core.db"))
	if err != nil {
		return p, err
	}
	for _, source := range []struct {
		name    string
		missing bool
	}{{"astral.db", p.Host.Missing}, {"core.db", p.Core.Missing}} {
		if !source.missing {
			continue
		}
		for _, suffix := range []string{"-wal", "-journal"} {
			name := source.name + suffix
			info, statErr := os.Stat(filepath.Join(data, name))
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			if statErr != nil {
				return p, statErr
			}
			if !info.Mode().IsRegular() || info.Size() > 0 {
				return p, fmt.Errorf("%s is missing but %s remains; restore the complete data snapshot", source.name, name)
			}
		}
	}
	if p.Host.Missing && !p.Core.Missing {
		return p, fmt.Errorf("missing Host database while Core history exists; restore the complete data snapshot")
	}
	if p.Core.Missing {
		if !p.Host.Missing {
			references, refErr := persistence.HasSessionReferences(ctx, data)
			if refErr != nil {
				return p, refErr
			}
			if references {
				return p, fmt.Errorf("missing Core database while Host session references exist; restore the complete data snapshot")
			}
		}
	}
	p.Pending = p.Host.NeedsMigration || p.Core.NeedsMigration
	return p, nil
}

type fileRecord struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type manifest struct {
	Schema  int          `json:"schema"`
	Data    string       `json:"data"`
	Version string       `json:"version"`
	Created time.Time    `json:"created"`
	Files   []fileRecord `json:"files"`
}
type journal struct {
	Schema int    `json:"schema"`
	Backup string `json:"backup"`
}

type Upgrade struct {
	data    string
	Backup  string `json:"backup"`
	Version string `json:"version"`
	protect func(string) error
}

// Prepare recovers an interrupted attempt before inspecting data. A successful
// migration stays journaled until the new Server has prepared its stores, before
// it accepts any external work. A crash before Commit restores the old snapshot.
func Prepare(ctx context.Context, data, version string, protect func(string) error) (*Upgrade, error) {
	return prepare(ctx, data, version, protect, persistence.Migrate, func(ctx context.Context, data string) error {
		return sqlite.Migrate(ctx, filepath.Join(data, "core.db"))
	})
}

func prepare(ctx context.Context, data, version string, protect func(string) error, hostStep, coreStep func(context.Context, string) error) (*Upgrade, error) {
	data, err := filepath.Abs(data)
	if err != nil {
		return nil, err
	}
	data, err = filepath.EvalSymlinks(data)
	if err != nil {
		return nil, err
	}
	if protect == nil {
		protect = protectPrivate
	}
	journalPath := filepath.Join(data, journalName)
	if raw, readErr := os.ReadFile(journalPath); readErr == nil {
		var pending journal
		if json.Unmarshal(raw, &pending) != nil || pending.Schema != 1 || !filepath.IsAbs(pending.Backup) {
			return nil, fmt.Errorf("invalid migration recovery journal; preserve data and inspect %s", journalPath)
		}
		if err = Restore(ctx, data, pending.Backup, protect); err != nil {
			return nil, fmt.Errorf("recover interrupted migration: %w", err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	plan, err := Inspect(ctx, data)
	if err != nil {
		return nil, err
	}
	if !plan.Host.Missing {
		key, err := os.ReadFile(filepath.Join(data, "master.key"))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("existing Host database requires its original 32-byte master.key; migration stopped")
		}
	}
	if !plan.Pending {
		return nil, nil
	}
	backup, err := snapshot(ctx, data, version, protect)
	if err != nil {
		return nil, err
	}
	upgrade := &Upgrade{data: data, Backup: backup, Version: version, protect: protect}
	if err = writeJSON(journalPath, journal{Schema: 1, Backup: backup}, protect); err != nil {
		return nil, err
	}
	if err = hostStep(ctx, data); err == nil {
		err = coreStep(ctx, data)
	}
	if err == nil {
		var current Plan
		current, err = Inspect(ctx, data)
		if err == nil && current.Pending {
			err = fmt.Errorf("data migration did not reach all target formats")
		}
	}
	if err != nil {
		recovery, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return nil, errors.Join(fmt.Errorf("migration failed: %w", err), upgrade.Rollback(recovery))
	}
	return upgrade, nil
}

func (u *Upgrade) Commit() error {
	if u == nil {
		return nil
	}
	if err := writeJSON(filepath.Join(u.data, resultName), struct {
		Schema  int    `json:"schema"`
		Backup  string `json:"backup"`
		Version string `json:"version"`
	}{1, u.Backup, u.Version}, u.protect); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(u.data, journalName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(u.data)
}

// LastBackup identifies the most recently committed upgrade, for an installer
// that must undo a successfully migrated but subsequently unhealthy release.
func LastBackup(data string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(data, resultName))
	if err != nil {
		return "", err
	}
	var result struct {
		Schema int    `json:"schema"`
		Backup string `json:"backup"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Schema != 1 || !filepath.IsAbs(result.Backup) {
		return "", fmt.Errorf("invalid last-upgrade record")
	}
	return result.Backup, nil
}

func Recover(ctx context.Context, data string, protect func(string) error) error {
	raw, err := os.ReadFile(filepath.Join(data, journalName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j journal
	if json.Unmarshal(raw, &j) != nil || j.Schema != 1 || !filepath.IsAbs(j.Backup) {
		return fmt.Errorf("invalid migration recovery journal")
	}
	return Restore(ctx, data, j.Backup, protect)
}
func (u *Upgrade) Rollback(ctx context.Context) error {
	if u == nil {
		return nil
	}
	return Restore(ctx, u.data, u.Backup, u.protect)
}

func excluded(name string) bool {
	return name == "server.lock" || name == "running.json" || name == "startup-maintenance.json" || name == journalName
}

func snapshot(ctx context.Context, data, version string, protect func(string) error) (string, error) {
	parent := filepath.Join(filepath.Dir(data), "upgrade-backups")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	if err := protect(parent); err != nil {
		return "", err
	}
	backup, err := os.MkdirTemp(parent, filepath.Base(data)+"-"+time.Now().UTC().Format("20060102T150405Z")+"-")
	if err != nil {
		return "", err
	}
	if err = protect(backup); err != nil {
		return "", err
	}
	m := manifest{Schema: 1, Data: data, Version: version, Created: time.Now().UTC()}
	err = filepath.WalkDir(data, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(data, path)
		if err != nil || rel == "." {
			return err
		}
		if excluded(rel) {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration snapshot refuses symlink %s", rel)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("migration snapshot refuses special file %s", rel)
		}
		record, err := copyFile(ctx, path, filepath.Join(backup, "files", rel), protect)
		if err != nil {
			return err
		}
		record.Path = filepath.ToSlash(rel)
		m.Files = append(m.Files, record)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("backup incomplete at %s: %w", backup, err)
	}
	if err = writeJSON(filepath.Join(backup, "manifest.json"), m, protect); err != nil {
		return "", err
	}
	if err = syncDir(backup); err != nil {
		return "", err
	}
	return backup, nil
}

// Restore verifies every snapshot hash before touching live data. The journal
// remains until the complete restore succeeds, so an interrupted copy retries.
func Restore(ctx context.Context, data, backup string, protect func(string) error) error {
	data, err := filepath.Abs(data)
	if err != nil {
		return err
	}
	data, err = filepath.EvalSymlinks(data)
	if err != nil {
		return err
	}
	if protect == nil {
		protect = protectPrivate
	}
	backup, err = filepath.Abs(backup)
	if err != nil {
		return err
	}
	backup, err = filepath.EvalSymlinks(backup)
	if err != nil {
		return err
	}
	if within(data, backup) || within(backup, data) {
		return fmt.Errorf("backup must be outside the live data directory")
	}
	raw, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	if err != nil {
		return err
	}
	var m manifest
	if json.Unmarshal(raw, &m) != nil || m.Schema != 1 || m.Data != data || len(m.Files) == 0 {
		return fmt.Errorf("backup does not describe this data directory")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !fs.ValidPath(f.Path) || strings.ContainsAny(f.Path, "\\:") || !filepath.IsLocal(filepath.FromSlash(f.Path)) || excluded(f.Path) || seen[f.Path] {
			return fmt.Errorf("invalid snapshot path %q", f.Path)
		}
		seen[f.Path] = true
		path := filepath.Join(backup, "files", filepath.FromSlash(f.Path))
		if !within(filepath.Join(backup, "files"), path) || !within(data, filepath.Join(data, filepath.FromSlash(f.Path))) {
			return fmt.Errorf("snapshot path escapes data directory")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("snapshot file unavailable: %s", f.Path)
		}
		actual, err := hashFile(ctx, path)
		if err != nil {
			return err
		}
		if actual.SHA256 != f.SHA256 || actual.Size != f.Size {
			return fmt.Errorf("snapshot checksum mismatch: %s", f.Path)
		}
	}
	// Validate directories too: a changed backup must never redirect file copies.
	if err = filepath.WalkDir(filepath.Join(backup, "files"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot contains a symlink")
		}
		return nil
	}); err != nil {
		return err
	}
	if err = writeJSON(filepath.Join(data, journalName), journal{Schema: 1, Backup: backup}, protect); err != nil {
		return err
	}
	entries, err := os.ReadDir(data)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if excluded(entry.Name()) {
			continue
		}
		if err = os.RemoveAll(filepath.Join(data, entry.Name())); err != nil {
			return err
		}
	}
	for _, f := range m.Files {
		if _, err = copyFile(ctx, filepath.Join(backup, "files", filepath.FromSlash(f.Path)), filepath.Join(data, filepath.FromSlash(f.Path)), protect); err != nil {
			return err
		}
	}
	if err = os.Remove(filepath.Join(data, journalName)); err != nil {
		return err
	}
	return syncDir(data)
}

func copyFile(ctx context.Context, source, target string, protect func(string) error) (fileRecord, error) {
	var record fileRecord
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return record, err
	}
	in, err := os.Open(source)
	if err != nil {
		return record, err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return record, err
	}
	defer out.Close()
	if err = protect(target); err != nil {
		return record, err
	}
	hash := sha256.New()
	record.Size, err = io.Copy(io.MultiWriter(out, hash), &contextReader{ctx, in})
	if err != nil {
		return record, err
	}
	if err = out.Sync(); err != nil {
		return record, err
	}
	if err = out.Close(); err != nil {
		return record, err
	}
	record.SHA256 = hex.EncodeToString(hash.Sum(nil))
	return record, syncDir(filepath.Dir(target))
}
func hashFile(ctx context.Context, path string) (fileRecord, error) {
	in, err := os.Open(path)
	if err != nil {
		return fileRecord{}, err
	}
	defer in.Close()
	h := sha256.New()
	n, err := io.Copy(h, &contextReader{ctx, in})
	return fileRecord{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func writeJSON(path string, value any, protect func(string) error) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".migration-")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err = protect(temp.Name()); err == nil {
		_, err = temp.Write(append(raw, '\n'))
	}
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return err
	}
	// Rename cannot replace an existing destination on Windows. Only a journal
	// with an identical backup may already exist, and it need not be rewritten.
	if existing, readErr := os.ReadFile(path); readErr == nil && strings.TrimSpace(string(existing)) == strings.TrimSpace(string(raw)) {
		return nil
	}
	if err = replaceFile(temp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func protectPrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return os.Chmod(path, 0700)
	}
	return os.Chmod(path, 0600)
}
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
