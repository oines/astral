package platform

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/daemonservice"
	"github.com/oines/astral/internal/releasefeed"
)

const containerUpdateRequest = "/usr/local/lib/astral/update-request.json"

type containerUpdate struct {
	Binary       releasefeed.Binary `json:"binary"`
	Path         string             `json:"path"`
	PreviousBoot string             `json:"previous_boot"`
}

func configureDaemonUpdate(d *computers.Daemon) {
	supervised := d.Hello.Container != nil && os.Getenv("ASTRAL_CONTAINER_SUPERVISOR") == "1"
	if !supervised {
		if d.Hello.Container != nil {
			return
		}
		if _, err := os.Stat(filepath.Join(d.Root, "service", "spec.json")); err != nil {
			return
		}
	}
	d.Hello.Capabilities = append(d.Hello.Capabilities, computers.DaemonUpdateCapability)
	d.Update = func(ctx context.Context, binary releasefeed.Binary) error {
		if err := binary.Validate(runtime.GOOS, runtime.GOARCH); err != nil {
			return err
		}
		parent := filepath.Join(d.Root, "service", "releases", binary.Version+"-"+binary.SHA256[:12])
		if supervised {
			parent = "/usr/local/bin"
		}
		if err := os.MkdirAll(parent, 0700); err != nil {
			return err
		}
		target := filepath.Join(parent, "astral")
		if runtime.GOOS == "windows" {
			target += ".exe"
		}
		if supervised {
			target = filepath.Join(parent, ".astral-release-"+binary.SHA256)
		}
		if err := binary.Download(ctx, target); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if supervised {
			return WriteJSON(containerUpdateRequest, containerUpdate{Binary: binary, Path: target, PreviousBoot: d.Hello.BootID})
		}
		manager, err := daemonservice.New(d.Root)
		if err != nil {
			return err
		}
		state, err := manager.Status(ctx)
		if err != nil {
			return err
		}
		if state.State != "running" {
			return fmt.Errorf("managed Daemon service is not running; use the update command")
		}
		return manager.StartUpdate(ctx, target)
	}
}
