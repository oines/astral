package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/oines/astral/internal/datamigrate"
	"github.com/oines/astral/internal/platform"
)

func migrate(ctx context.Context, args []string, out, stderr io.Writer) error {
	restore := len(args) > 0 && args[0] == "restore"
	recover := len(args) > 0 && args[0] == "recover"
	if restore || recover {
		args = args[1:]
	}
	f := flags("migrate", stderr)
	data := f.String("data", "", "existing Server data directory; stop Desktop/Server first")
	status := f.Bool("status", false, "inspect migration status without changing data")
	backup := f.String("backup", "", "verified snapshot directory for migrate restore")
	latest := f.Bool("latest", false, "restore the snapshot from the most recent upgrade")
	if err := f.Parse(args); err != nil {
		return err
	}
	invalidRestore := restore && (*status || (*backup != "") == *latest)
	invalidOptions := !restore && (*backup != "" || *latest) || recover && *status
	if strings.TrimSpace(*data) == "" || f.NArg() != 0 || invalidRestore || invalidOptions {
		return fmt.Errorf("usage: astral migrate --data DIRECTORY [--status] | astral migrate restore --data DIRECTORY (--backup SNAPSHOT | --latest) | astral migrate recover --data DIRECTORY")
	}
	directory, err := maintenanceDirectory(*data)
	if err != nil {
		return err
	}
	lock, err := platform.Lock(filepath.Join(directory, "server.lock"))
	if err != nil {
		return fmt.Errorf("stop Astral Desktop/Server before migration: %w", err)
	}
	defer lock.Close()
	if recover {
		return datamigrate.Recover(ctx, directory, platform.ProtectPrivate)
	}
	if restore {
		if *latest {
			*backup, err = datamigrate.LastBackup(directory)
			if err != nil {
				return err
			}
		}
		return datamigrate.Restore(ctx, directory, *backup, platform.ProtectPrivate)
	}
	if *status {
		plan, err := datamigrate.Inspect(ctx, directory)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(plan)
	}
	upgrade, err := datamigrate.Prepare(ctx, directory, Version, platform.ProtectPrivate)
	if err != nil {
		return err
	}
	if err = upgrade.Commit(); err != nil {
		return err
	}
	if upgrade != nil {
		fmt.Fprintln(out, "Data migration completed. Backup:", upgrade.Backup)
	} else {
		fmt.Fprintln(out, "Data is current; no migration required.")
	}
	return nil
}
