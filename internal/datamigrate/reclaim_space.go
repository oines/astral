package datamigrate

import (
	"fmt"
	"math"
	"path/filepath"
)

// ReclaimSpace checks the largest sequential Core/Host/Recall vacuum, including
// its WAL, on both the database and SQLite temporary volumes. checkSpace adds
// both reservations when those directories share a filesystem.
func ReclaimSpace(data string) error { return reclaimSpace(data, availableSpace) }

func reclaimSpace(data string, probe func(string) (diskSpace, error)) error {
	var largest uint64
	for _, name := range []string{"core.db", "astral.db", "recall.db"} {
		var size uint64
		for _, suffix := range []string{"", "-wal"} {
			n, err := fileBytes(filepath.Join(data, name+suffix))
			if err != nil {
				return err
			}
			if n > math.MaxUint64-size {
				return fmt.Errorf("reclamation space budget overflow")
			}
			size += n
		}
		largest = max(largest, size)
	}
	if largest > (math.MaxUint64-(64<<20))/4 {
		return fmt.Errorf("reclamation space budget overflow")
	}
	return checkSpace([]spaceNeed{{data, 2*largest + (64 << 20), "database space reclamation"}, {sqliteTempDirectory(), 2 * largest, "SQLite reclamation temporary work"}}, probe)
}
