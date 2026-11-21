//go:build daemon

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/oines/astral/internal/platform"
)

func Run(ctx context.Context, args []string, out, stderr io.Writer) int {
	platform.DaemonVersion = Version
	err := run(ctx, args, out, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		daemonUsage(out)
		return nil
	}
	switch args[0] {
	case "computer":
		return computer(ctx, args[1:], out, stderr)
	case "version", "--version", "-v":
		return daemonVersion(args[1:], out)
	case "help", "-h", "--help":
		daemonUsage(out)
		return nil
	default:
		return fmt.Errorf("unknown command %q (this is the standalone astral-daemon executable; use `astral-daemon computer ...` or `astral-daemon version`)", args[0])
	}
}

func daemonVersion(args []string, out io.Writer) error {
	if len(args) == 0 {
		_, err := fmt.Fprintln(out, Version)
		return err
	}
	if len(args) == 1 && args[0] == "--json" {
		return json.NewEncoder(out).Encode(struct {
			Version string `json:"version"`
		}{Version: Version})
	}
	return fmt.Errorf("astral-daemon version: unknown argument; usage: astral-daemon version [--json]")
}

func daemonUsage(out io.Writer) {
	fmt.Fprint(out, `Astral Daemon CLI

Commands:
  astral-daemon computer connect --server URL --server-pin PIN --enrollment ID --token TOKEN [--container] [--foreground] [--temporary]
  astral-daemon computer daemon --root DIR --credentials FILE [--log FILE]
  astral-daemon computer service install|start|stop|restart|status|logs|uninstall [--root DIR]
  astral-daemon computer status [--root DIR]
  astral-daemon computer update [--root DIR | --container NAME]
  astral-daemon version [--json]
`)
}
