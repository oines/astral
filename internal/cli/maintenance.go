package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/platform"
)

func maintenance(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "vacuum" {
		return maintenanceVacuum(ctx, args[1:], out, stderr)
	}
	if len(args) > 0 && args[0] == "with-data-lock" {
		return maintenanceWithDataLock(ctx, args[1:], out, stderr)
	}
	if len(args) == 0 || args[0] != "weixin-wire-ownership" {
		return fmt.Errorf("usage: astral maintenance vacuum --data DIRECTORY | with-data-lock --data DIRECTORY --computers-dir DIRECTORY -- COMMAND [ARGS...] | weixin-wire-ownership --data DIRECTORY [--purge-unowned]")
	}
	f := flags("maintenance weixin-wire-ownership", stderr)
	directory := f.String("data", "", "existing Server data directory; stop Desktop or Server first")
	purge := f.Bool("purge-unowned", false, "delete all remaining anonymous legacy Weixin protocol caches; other accounts may need a new incoming message before sending")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*directory) == "" || f.NArg() != 0 {
		return fmt.Errorf("--data DIRECTORY is required; no positional arguments are accepted")
	}
	data, err := filepath.Abs(*directory)
	if err != nil {
		return err
	}
	for _, name := range []string{"astral.db", "master.key"} {
		info, err := os.Stat(filepath.Join(data, name))
		if err != nil {
			return fmt.Errorf("existing Server data directory required: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s must be a regular file", name)
		}
	}
	lock, err := platform.Lock(filepath.Join(data, "server.lock"))
	if err != nil {
		return fmt.Errorf("stop Astral Desktop or Server before offline maintenance: %w", err)
	}
	defer lock.Close()
	store, err := persistence.Open(data)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := channels.RepairWeixinWireOwnership(ctx, store, *purge)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(out).Encode(result); err != nil {
		return err
	}
	if result.Unowned > 0 {
		return fmt.Errorf("%d anonymous legacy Weixin protocol caches remain; --purge-unowned explicitly clears these caches across all accounts without deleting conversation history", result.Unowned)
	}
	return nil
}
