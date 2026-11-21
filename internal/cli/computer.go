package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/daemonservice"
	"github.com/oines/astral/internal/githubrelease"
	"github.com/oines/astral/internal/platform"
)

var Version = "dev"

func flags(name string, stderr io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	return f
}

func computer(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: astral computer connect|daemon|service|update")
	}
	switch args[0] {
	case "status":
		return computerStatus(ctx, args[1:], out, stderr)
	case "probe":
		return computerProbe(ctx, args[1:], stderr)
	case "connect":
		f := flags("computer connect", stderr)
		var options platform.ConnectOptions
		f.StringVar(&options.ServerURL, "server", "", "Astral Server URL")
		f.StringVar(&options.ServerPin, "server-pin", "", "Server SPKI SHA256 fingerprint")
		f.StringVar(&options.EnrollmentID, "enrollment", "", "temporary enrollment identity")
		f.StringVar(&options.Token, "token", "", "temporary enrollment token")
		f.StringVar(&options.Root, "root", "", "Daemon's persistent directory")
		f.BoolVar(&options.Container, "container", false, "create a Computer in the local container runtime")
		f.BoolVar(&options.Foreground, "foreground", false, "keep the Daemon in the foreground")
		f.BoolVar(&options.Temporary, "temporary", false, "connect temporarily without installing a service or saving credentials")
		enrollmentFile := f.String("enrollment-file", "", "private enrollment configuration file")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *enrollmentFile != "" {
			var saved platform.ConnectOptions
			if err := platform.ReadJSON(*enrollmentFile, &saved); err != nil {
				return err
			}
			saved.Root, saved.Foreground, saved.Container, saved.Temporary = options.Root, options.Foreground, options.Container, options.Temporary
			options = saved
		}
		return platform.Connect(ctx, options, func(message string) { fmt.Fprintln(out, message) })
	case "daemon":
		f := flags("computer daemon", stderr)
		root := f.String("root", "", "Daemon data directory")
		credentials := f.String("credentials", "", "private credentials file")
		logPath := f.String("log", "", "append Daemon diagnostics to this file")
		parentPID := f.Int("parent-pid", 0, "stop this managed Daemon when its service launcher exits")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *credentials == "" {
			return fmt.Errorf("--root and --credentials are required")
		}
		if *parentPID != 0 {
			managed, cancel, err := platform.ManagedContext(ctx, *parentPID, nil)
			if err != nil {
				return err
			}
			defer cancel()
			ctx = managed
		}
		return platform.RunDaemonLogged(ctx, *root, *credentials, *logPath, func(message string) { fmt.Fprintln(stderr, message) })
	case "service":
		return computerService(ctx, args[1:], out, stderr)
	case "update":
		return computerUpdate(ctx, args[1:], out, stderr)
	case "supervise":
		return computerSupervise(ctx, args[1:], out, stderr)
	default:
		return fmt.Errorf("unknown Computer command")
	}
}

// computerProbe is an internal, credential-free container route check. The
// caller selects the first address that reaches the intended pinned Server.
func computerProbe(ctx context.Context, args []string, stderr io.Writer) error {
	f := flags("computer probe", stderr)
	server := f.String("server", "", "Astral Server URL")
	pin := f.String("server-pin", "", "Server SPKI SHA256 fingerprint")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected computer probe argument")
	}
	if *server == "" || *pin == "" {
		return fmt.Errorf("--server and --server-pin are required")
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return computers.ProbeServer(bounded, *server, *pin)
}

func computerService(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: astral computer service install|start|stop|restart|status|logs|uninstall --root DIRECTORY")
	}
	f := flags("computer service "+args[0], stderr)
	root := f.String("root", "", "enrolled Daemon data directory (automatically find a single installation)")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected computer service argument")
	}
	if *root == "" {
		installation, err := resolveComputerInstallation(ctx, false, stderr)
		if err != nil {
			return err
		}
		*root = installation.Root
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
		proxy, e := githubrelease.Proxy()
		if e != nil {
			return e
		}
		err = manager.Install(ctx, daemonservice.Spec{Executable: executable, Credentials: filepath.Join(absolute, "credentials.json"), EnvironmentPATH: os.Getenv("PATH"), GitHubProxy: &proxy})
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
	container := f.String("container", "", "existing Astral container identity or name")
	f.Bool("update", false, "installer update operation")
	managed := f.Bool("managed-helper", false, "one-shot current-user update job")
	var downloadSource githubrelease.ProxyOverride
	f.Func("github-proxy", "HTTPS GitHub download prefix (empty for direct downloads)", func(raw string) error {
		prefix, err := githubrelease.NormalizeProxy(raw)
		if err == nil {
			downloadSource = githubrelease.ProxyOverride{Set: true, Prefix: prefix}
		}
		return err
	})
	containerEndpoint := ""
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected computer update argument")
	}
	if *root == "" && *container == "" {
		installation, err := resolveComputerInstallation(ctx, true, stderr)
		if err != nil {
			return err
		}
		if installation.Container != "" {
			*container = installation.Container
			containerEndpoint = installation.DockerEndpoint
		} else {
			*root = installation.Root
		}
	}
	if *managed {
		manager, err := daemonservice.New(*root)
		if err != nil {
			return err
		}
		defer manager.FinishUpdate(context.Background())
		file, err := os.OpenFile(filepath.Join(*root, "update.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer file.Close()
		out = io.MultiWriter(out, file)
	}
	err := platform.UpdateComputer(ctx, platform.UpdateOptions{Root: *root, Container: *container, Version: Version, ContainerEndpoint: containerEndpoint, DownloadSource: downloadSource, ManagedHelper: *managed}, func(message string) { fmt.Fprintln(out, message) })
	if err != nil && *managed {
		fmt.Fprintln(out, "Daemon update failed:", err)
	}
	return err
}

func computerSupervise(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flags("computer supervise", stderr)
	root := f.String("root", "/var/lib/astral", "container Daemon data directory")
	enrollment := f.String("enrollment-file", "/run/astral-enrollment.json", "container enrollment file")
	if err := f.Parse(args); err != nil {
		return err
	}
	return platform.SuperviseContainer(ctx, *root, *enrollment, out, stderr)
}
