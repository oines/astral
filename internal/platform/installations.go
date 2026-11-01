package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/oines/astral/internal/computers"
)

// ComputerInstallation contains only user-visible installation identity. Its
// credentials stay in the existing private directory and are never copied into
// the discovery index.
type ComputerInstallation struct {
	Root           string `json:"root"`
	ComputerID     string `json:"computer_id,omitempty"`
	ServerURL      string `json:"server_url,omitempty"`
	Container      string `json:"container,omitempty"`
	DockerEndpoint string `json:"docker_endpoint,omitempty"`
	DockerContext  string `json:"docker_context,omitempty"`
	Managed        bool   `json:"managed"`
}

func rememberComputerInstallation(directory string) error {
	root, err := DefaultRoot()
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	key := sha256.Sum256([]byte(absolute))
	return WriteJSON(filepath.Join(root, "installations", "computers", hex.EncodeToString(key[:12])+".json"), ComputerInstallation{Root: absolute})
}

// InstalledComputers reads known installer directories and the current user's
// small custom-installation index. It never searches arbitrary disks, contacts
// a Server, or treats the Server's embedded local Daemon as an independent
// service that can be upgraded or restarted by this command.
func InstalledComputers(root string) ([]ComputerInstallation, error) {
	known, err := filepath.Glob(filepath.Join(root, "computers", "*", "*"))
	if err != nil {
		return nil, err
	}
	index, err := filepath.Glob(filepath.Join(root, "installations", "computers", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range index {
		var installation ComputerInstallation
		if err := ReadJSON(path, &installation); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if filepath.IsAbs(installation.Root) {
			known = append(known, installation.Root)
		}
	}
	result := []ComputerInstallation{}
	seen := map[string]bool{}
	for _, directory := range known {
		absolute, err := filepath.EvalSymlinks(directory)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		var container ContainerInstallation
		if ReadJSON(filepath.Join(absolute, "container.json"), &container) == nil && container.Name != "" && container.Identity != "" {
			result = append(result, ComputerInstallation{Root: absolute, Container: container.Name, DockerEndpoint: container.Endpoint, DockerContext: container.Context, Managed: true})
			continue
		}
		var credentials computers.Credentials
		if ReadJSON(filepath.Join(absolute, "credentials.json"), &credentials) != nil || credentials.ComputerID == "" {
			continue
		}
		_, serviceErr := os.Stat(filepath.Join(absolute, "service", "spec.json"))
		if serviceErr != nil {
			continue
		}
		result = append(result, ComputerInstallation{Root: absolute, ComputerID: credentials.ComputerID, ServerURL: credentials.ServerURL, Managed: true})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Root < result[j].Root })
	return result, nil
}
