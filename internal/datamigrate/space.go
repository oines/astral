package datamigrate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
)

type diskSpace struct {
	volume    string
	available uint64
}
type spaceNeed struct {
	path    string
	bytes   uint64
	purpose string
}

func checkSpace(needs []spaceNeed, probe func(string) (diskSpace, error)) error {
	type total struct {
		available, required uint64
		path, purpose       string
	}
	volumes := map[string]total{}
	for _, need := range needs {
		if need.bytes == 0 {
			continue
		}
		path := need.path
		for {
			_, err := os.Stat(path)
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			parent := filepath.Dir(path)
			if parent == path {
				return err
			}
			path = parent
		}
		space, err := probe(path)
		if err != nil {
			return fmt.Errorf("check free space at %s: %w", path, err)
		}
		value, ok := volumes[space.volume]
		if !ok {
			value = total{available: space.available, path: path}
		} else {
			value.available = min(value.available, space.available)
		}
		if need.bytes > math.MaxUint64-value.required {
			return fmt.Errorf("migration space budget overflow")
		}
		value.required += need.bytes
		if value.purpose != "" {
			value.purpose += ", "
		}
		value.purpose += need.purpose
		volumes[space.volume] = value
	}
	for _, value := range volumes {
		if value.available < value.required {
			return fmt.Errorf("insufficient free space at %s for %s: need at least %d bytes, available %d; free space or move the complete stopped Server directory before retrying", value.path, value.purpose, value.required, value.available)
		}
	}
	return nil
}

func fileBytes(path string) (uint64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() || info.Size() < 0 {
		return 0, fmt.Errorf("not a regular data file: %s", path)
	}
	return uint64(info.Size()), nil
}

// Inspection can copy an uncheckpointed database and its WAL into TMPDIR.
// Check before calling either backend's inspector, not after the copy fails.
func inspectionSpace(data string) error {
	var temporary uint64
	for _, name := range []string{"astral.db", "core.db"} {
		wal, err := fileBytes(filepath.Join(data, name+"-wal"))
		if err != nil {
			return err
		}
		if wal == 0 {
			continue
		}
		db, err := fileBytes(filepath.Join(data, name))
		if err != nil {
			return err
		}
		temporary = max(temporary, db+wal)
	}
	if temporary == 0 {
		return nil
	}
	return checkSpace([]spaceNeed{{os.TempDir(), temporary + (64 << 20), "read-only WAL inspection"}}, availableSpace)
}

// Reserve conservative headroom independently of the expected deduplication
// ratio. This is a preflight, not a quota: other processes can consume space.
func migrationSpace(ctx context.Context, data string) error {
	var tree, databases uint64
	err := filepath.WalkDir(data, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(data, path)
		if err != nil {
			return err
		}
		if excluded(rel) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration data contains a symlink: %s", rel)
		}
		if entry.IsDir() {
			return nil
		}
		n, err := fileBytes(path)
		if err != nil {
			return err
		}
		if n > math.MaxUint64/16-tree {
			return fmt.Errorf("migration data too large")
		}
		tree += n
		for _, name := range []string{"astral.db", "core.db", "astral.db-wal", "core.db-wal", "astral.db-journal", "core.db-journal"} {
			if rel == name {
				databases += n
				break
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return checkSpace([]spaceNeed{
		{data, 4*databases + tree + (64 << 20), "database conversion, journal and recovery reserve"},
		{filepath.Join(filepath.Dir(data), "upgrade-backups"), tree, "complete upgrade snapshot"},
		{sqliteTempDirectory(), 2 * databases, "SQLite temporary work"},
	}, availableSpace)
}

func VacuumSpace(data string) error {
	if err := inspectionSpace(data); err != nil {
		return err
	}
	n, err := fileBytes(filepath.Join(data, "core.db"))
	if err != nil {
		return err
	}
	return checkSpace([]spaceNeed{{data, 2*n + (64 << 20), "database vacuum"}, {sqliteTempDirectory(), 2 * n, "SQLite vacuum temporary work"}}, availableSpace)
}

func sqliteTempDirectory() string {
	if runtime.GOOS != "windows" {
		if dir := os.Getenv("SQLITE_TMPDIR"); dir != "" {
			return dir
		}
	}
	return os.TempDir()
}
