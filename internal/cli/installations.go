package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/platform"
	"golang.org/x/term"
)

type installationDirectories struct {
	configRoot string
	dataHome   string
}
type installationDirectoriesKey struct{}

func commandDirectories(ctx context.Context) (installationDirectories, error) {
	if directories, ok := ctx.Value(installationDirectoriesKey{}).(installationDirectories); ok {
		return directories, nil
	}
	root, err := platform.DefaultRoot()
	if err != nil {
		return installationDirectories{}, err
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return installationDirectories{}, err
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return installationDirectories{configRoot: root, dataHome: dataHome}, nil
}

func existingServerData(directory string) bool {
	for _, name := range []string{"astral.db", "control.json", "network.json"} {
		if info, err := os.Stat(filepath.Join(directory, name)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func installedServerData(root string) string {
	var marker struct {
		Schema        int    `json:"schema"`
		Root          string `json:"root"`
		DataDirectory string `json:"data_directory"`
		UID           int    `json:"uid"`
		Service       string `json:"service"`
	}
	if !filepath.IsAbs(root) || platform.ReadJSON(filepath.Join(root, "installation.json"), &marker) != nil || marker.Schema != 1 || filepath.Clean(marker.Root) != filepath.Clean(root) || filepath.Clean(marker.DataDirectory) != filepath.Join(root, "data", "server") || marker.Service != "astral-server.service" || (os.Getuid() >= 0 && marker.UID != os.Getuid()) {
		return ""
	}
	if existingServerData(marker.DataDirectory) {
		return marker.DataDirectory
	}
	return ""
}

func chooseInstallation(label, flag string, values []string, input io.Reader, output io.Writer, interactive bool) (int, error) {
	if len(values) == 1 {
		return 0, nil
	}
	if len(values) == 0 {
		return 0, fmt.Errorf("no installed %s was found; specify %s explicitly", label, flag)
	}
	if !interactive {
		return 0, fmt.Errorf("multiple %s installations were found; specify %s explicitly:\n  %s", label, flag, strings.Join(values, "\n  "))
	}
	fmt.Fprintln(output, "Select the "+label+" installation:")
	for index, value := range values {
		fmt.Fprintf(output, "  %d. %s\n", index+1, value)
	}
	fmt.Fprint(output, "Number: ")
	raw, err := bufio.NewReader(input).ReadString('\n')
	if err != nil {
		return 0, err
	}
	choice, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || choice < 1 || choice > len(values) {
		return 0, fmt.Errorf("select a number from 1 to %d", len(values))
	}
	return choice - 1, nil
}

func resolveServerData(ctx context.Context, explicit string, output io.Writer, create bool) (string, error) {
	if explicit != "" {
		return filepath.Abs(explicit)
	}
	if directory := os.Getenv("ASTRAL_SERVER_DATA_DIR"); directory != "" {
		return filepath.Abs(directory)
	}
	if root := os.Getenv("ASTRAL_INSTALLATION_ROOT"); root != "" {
		if directory := installedServerData(root); directory != "" {
			return directory, nil
		}
		return "", fmt.Errorf("ASTRAL_INSTALLATION_ROOT does not identify an existing Astral Server installation")
	}
	// A stable installer command may resolve all the way into a versioned
	// distribution directory. Find that installation rather than guessing a
	// second default data directory.
	if executable, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(executable); err == nil {
			for directory, count := filepath.Dir(resolved), 0; count < 8; directory, count = filepath.Dir(directory), count+1 {
				if data := installedServerData(directory); data != "" {
					return data, nil
				}
				if parent := filepath.Dir(directory); parent == directory {
					break
				}
			}
		}
	}
	directories, err := commandDirectories(ctx)
	if err != nil {
		return "", err
	}
	root := directories.configRoot
	values := []string{}
	seen := map[string]bool{}
	add := func(directory string) {
		absolute, err := filepath.Abs(directory)
		if err == nil && !seen[absolute] && existingServerData(absolute) {
			seen[absolute] = true
			values = append(values, absolute)
		}
	}
	if data := installedServerData(filepath.Join(directories.dataHome, "astral-server")); data != "" {
		add(data)
	}
	// The managed user service also identifies a non-default installer root.
	if raw, err := os.ReadFile(filepath.Join(filepath.Dir(root), "systemd", "user", "astral-server.service")); err == nil {
		first, _, _ := strings.Cut(string(raw), "\n")
		if installationRoot, ok := strings.CutPrefix(first, "# Astral Server installer root: "); ok {
			if data := installedServerData(installationRoot); data != "" {
				add(data)
			}
		}
	}
	add(filepath.Join(root, "server"))
	add(filepath.Join(root, "schema-3-core-11", "server"))
	if desktopRoot := os.Getenv("ASTRAL_DESKTOP_DATA_DIR"); desktopRoot != "" {
		add(filepath.Join(desktopRoot, "server"))
	}
	if len(values) == 0 && create {
		return filepath.Join(root, "server"), nil
	}
	choice, err := chooseInstallation("Server", "--data DIRECTORY", values, os.Stdin, output, term.IsTerminal(int(os.Stdin.Fd())))
	if err != nil {
		return "", err
	}
	return values[choice], nil
}

func resolveComputerInstallation(ctx context.Context, includeContainers bool, output io.Writer) (platform.ComputerInstallation, error) {
	directories, err := commandDirectories(ctx)
	if err != nil {
		return platform.ComputerInstallation{}, err
	}
	installations, err := platform.InstalledComputers(directories.configRoot)
	if err != nil {
		return platform.ComputerInstallation{}, err
	}
	filtered := installations[:0]
	labels := []string{}
	for _, installation := range installations {
		if installation.Container != "" && !includeContainers {
			continue
		}
		filtered = append(filtered, installation)
		label := installation.Root
		if installation.Container != "" {
			label = installation.Container + " — " + installation.Root
			if installation.DockerContext != "" {
				label = installation.Container + " (" + installation.DockerContext + ") — " + installation.Root
			}
		} else if installation.ServerURL != "" {
			label = installation.ServerURL + " — " + installation.Root
		}
		labels = append(labels, label)
	}
	flag := "--root DIRECTORY"
	if includeContainers {
		flag += " or --container NAME"
	}
	choice, err := chooseInstallation("Computer", flag, labels, os.Stdin, output, term.IsTerminal(int(os.Stdin.Fd())))
	if err != nil {
		return platform.ComputerInstallation{}, err
	}
	return filtered[choice], nil
}
