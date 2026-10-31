package datamigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReclaimSpaceIncludesLargestRecallAndSharedTemporaryVolume(t *testing.T) {
	data := t.TempDir()
	temporary := t.TempDir()
	t.Setenv("SQLITE_TMPDIR", temporary)
	for name, size := range map[string]int{"core.db": 10, "astral.db": 20, "recall.db": 100, "recall.db-wal": 50} {
		if err := os.WriteFile(filepath.Join(data, name), make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 2*(100+50) is needed in each directory. On a shared volume it is
	// 600 + 64 MiB, rather than the 300 + 64 MiB visible in the data alone.
	err := reclaimSpace(data, func(string) (diskSpace, error) { return diskSpace{"shared", (64 << 20) + 500}, nil })
	if err == nil || !strings.Contains(err.Error(), "SQLite reclamation temporary work") {
		t.Fatalf("shared temporary reservation omitted: %v", err)
	}
	err = reclaimSpace(data, func(path string) (diskSpace, error) {
		if path == sqliteTempDirectory() {
			return diskSpace{"temp", 299}, nil
		}
		return diskSpace{"data", 1 << 30}, nil
	})
	if err == nil {
		t.Fatal("insufficient independent temporary volume accepted")
	}
}
