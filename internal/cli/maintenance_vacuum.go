package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/oines/astral-core/store/sqlite"
	"github.com/oines/astral/internal/datamigrate"
	"github.com/oines/astral/internal/platform"
)

func maintenanceVacuum(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flags("maintenance vacuum", stderr)
	directory := f.String("data", "", "existing Server data directory; stop Desktop/Server and retain a backup first")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *directory == "" || f.NArg() != 0 {
		return fmt.Errorf("usage: astral maintenance vacuum --data DIRECTORY")
	}
	data, err := maintenanceDirectory(*directory)
	if err != nil {
		return err
	}
	lock, err := platform.Lock(filepath.Join(data, "server.lock"))
	if err != nil {
		return fmt.Errorf("stop Astral Desktop/Server before vacuum: %w", err)
	}
	defer lock.Close()
	if _, err = os.Stat(filepath.Join(data, "migration-state.json")); err == nil {
		return fmt.Errorf("recover the interrupted data migration before vacuum")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = datamigrate.VacuumSpace(data); err != nil {
		return err
	}
	path := filepath.Join(data, "core.db")
	before, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err = sqlite.Vacuum(ctx, path); err != nil {
		return err
	}
	after, err := os.Stat(path)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Core database vacuum completed: %d -> %d bytes. Upgrade snapshots are retained separately.\n", before.Size(), after.Size())
	return err
}
