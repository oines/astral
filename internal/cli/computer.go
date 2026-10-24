package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/oines/astral/internal/daemonservice"
	"github.com/oines/astral/internal/platform"
)

func computerService(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: astral computer service install|start|stop|restart|status|logs|uninstall --root DIRECTORY")
	}
	f := flags("computer service "+args[0], stderr)
	root := f.String("root", "", "enrolled Daemon data directory")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if *root == "" {
		return fmt.Errorf("--root is required; use the data directory shown in Computer details")
	}
	manager, err := daemonservice.New(*root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "install":
		executable, e := os.Executable()
		if e != nil {
			return e
		}
		absolute, e := filepath.Abs(*root)
		if e != nil {
			return e
		}
		err = manager.Install(ctx, daemonservice.Spec{Executable: executable, Credentials: filepath.Join(absolute, "credentials.json"), EnvironmentPATH: os.Getenv("PATH")})
	case "start":
		err = manager.Start(ctx)
	case "stop":
		err = manager.Stop(ctx)
	case "restart":
		err = manager.Restart(ctx)
	case "uninstall":
		err = manager.Uninstall(ctx)
	case "status":
	case "logs":
		file, e := os.Open(filepath.Join(*root, "daemon.log"))
		if e != nil {
			return e
		}
		defer file.Close()
		info, e := file.Stat()
		if e != nil {
			return e
		}
		if info.Size() > 64<<10 {
			if _, e = file.Seek(-(64 << 10), io.SeekEnd); e != nil {
				return e
			}
		}
		_, e = io.Copy(out, file)
		return e
	default:
		return fmt.Errorf("unknown Daemon service operation %q", args[0])
	}
	if err != nil {
		return err
	}
	state, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(state)
}

func computerUpdate(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flags("computer update", stderr)
	root := f.String("root", "", "existing Daemon data directory")
	server := f.String("server", "", "Server supplying this release")
	container := f.String("container", "", "existing Astral container identity or name")
	f.Bool("update", false, "installer update operation")
	if err := f.Parse(args); err != nil {
		return err
	}
	return platform.UpdateComputer(ctx, platform.UpdateOptions{Root: *root, ServerURL: *server, Container: *container, Version: Version}, func(message string) { fmt.Fprintln(out, message) })
}
