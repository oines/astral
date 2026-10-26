package platform

import (
	"errors"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// GUI launches may omit the directories where Docker Desktop and OrbStack
// install their CLI. Keep an explicit PATH selection authoritative and only
// inspect standard locations when PATH has no Docker executable at all.
func dockerExecutable() (string, error) {
	home, _ := os.UserHomeDir()
	return resolveDockerExecutable(exec.LookPath, dockerExecutableCandidates(runtime.GOOS, home, os.Getenv))
}

func resolveDockerExecutable(lookPath func(string) (string, error), candidates []string) (string, error) {
	executable, lookupErr := lookPath("docker")
	if lookupErr == nil {
		if !filepath.IsAbs(executable) {
			return "", &exec.Error{Name: "docker", Err: exec.ErrDot}
		}
		return executable, nil
	}
	if !errors.Is(lookupErr, exec.ErrNotFound) || errors.Is(lookupErr, exec.ErrDot) {
		return executable, lookupErr
	}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			continue
		}
		found, err := lookPath(candidate)
		if err == nil && filepath.IsAbs(found) {
			// In particular, do not resolve OrbStack's docker symlink: its
			// multicall binary uses the executable name to select the CLI.
			return found, nil
		}
	}
	return executable, lookupErr
}

func dockerExecutableCandidates(goos, home string, getenv func(string) string) []string {
	var candidates []string
	seen := make(map[string]bool)
	add := func(root string, parts ...string) {
		candidate := dockerExecutableCandidate(goos, root, parts...)
		if candidate == "" {
			return
		}
		key := candidate
		if goos == "windows" {
			key = strings.ToLower(key)
		}
		if !seen[key] {
			seen[key] = true
			candidates = append(candidates, candidate)
		}
	}
	switch goos {
	case "darwin":
		add("/usr/local/bin", "docker")
		add("/opt/homebrew/bin", "docker")
		add(home, ".orbstack", "bin", "docker")
		add(home, ".docker", "bin", "docker")
		for _, applications := range []string{"/Applications", dockerExecutableCandidate(goos, home, "Applications")} {
			add(applications, "OrbStack.app", "Contents", "MacOS", "xbin", "docker")
			add(applications, "Docker.app", "Contents", "Resources", "bin", "docker")
		}
	case "windows":
		add(getenv("ProgramW6432"), "Docker", "Docker", "resources", "bin", "docker.exe")
		add(getenv("ProgramFiles"), "Docker", "Docker", "resources", "bin", "docker.exe")
		add(getenv("LOCALAPPDATA"), "Programs", "DockerDesktop", "resources", "bin", "docker.exe")
		add(home, "AppData", "Local", "Programs", "DockerDesktop", "resources", "bin", "docker.exe")
	case "linux":
		for _, directory := range []string{"/usr/local/bin", "/usr/bin", "/bin", "/snap/bin"} {
			add(directory, "docker")
		}
		add(home, ".docker", "bin", "docker")
		add(home, ".local", "bin", "docker")
	}
	return candidates
}

// Build candidates for the target OS independently of the build host, so the
// Windows installation paths can also be verified by tests on macOS and Linux.
func dockerExecutableCandidate(goos, root string, parts ...string) string {
	if goos != "windows" {
		if !path.IsAbs(root) {
			return ""
		}
		return path.Join(append([]string{root}, parts...)...)
	}
	root = strings.ReplaceAll(root, `\`, "/")
	drive := len(root) >= 3 && ((root[0] >= 'A' && root[0] <= 'Z') || (root[0] >= 'a' && root[0] <= 'z')) && root[1:3] == ":/"
	unc := strings.HasPrefix(root, "//")
	if !drive && !unc {
		return ""
	}
	joined := path.Join(append([]string{root}, parts...)...)
	if drive && (len(joined) < 3 || joined[1:3] != ":/") {
		return ""
	}
	if unc {
		// path.Join normalizes a UNC prefix to one slash. Restore it only
		// for complete server/share paths, never for a root-relative path.
		volume := strings.Split(strings.TrimPrefix(path.Clean(root), "/"), "/")
		if len(volume) < 2 || volume[0] == "" || volume[1] == "" {
			return ""
		}
		joined = "/" + joined
	}
	return strings.ReplaceAll(joined, "/", `\`)
}
