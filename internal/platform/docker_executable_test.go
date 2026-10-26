package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestDockerExecutablePreservesPATHSelection(t *testing.T) {
	selected := filepath.Join(t.TempDir(), "custom-docker")
	var calls []string
	got, err := resolveDockerExecutable(func(name string) (string, error) {
		calls = append(calls, name)
		return selected, nil
	}, []string{filepath.Join(t.TempDir(), "other-docker")})
	if err != nil || got != selected || !reflect.DeepEqual(calls, []string{"docker"}) {
		t.Fatalf("PATH selection changed: %q, %v, lookups %v", got, err, calls)
	}
}

func TestDockerExecutableFallsBackWhenGUIPATHOmitsCLI(t *testing.T) {
	root := t.TempDir()
	cliDirectory := filepath.Join(root, "runtime", "bin")
	if err := os.MkdirAll(cliDirectory, 0755); err != nil {
		t.Fatal(err)
	}
	name := "docker"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	candidate := filepath.Join(cliDirectory, name)
	if err := os.WriteFile(candidate, []byte("test executable; not executed"), 0755); err != nil {
		t.Fatal(err)
	}
	// The GUI's PATH does not contain the runtime installation directory.
	t.Setenv("PATH", filepath.Join(root, "system-bin"))
	got, err := resolveDockerExecutable(exec.LookPath, []string{filepath.Join(root, "missing", name), candidate})
	if err != nil || got != candidate {
		t.Fatalf("installed Docker was not found outside GUI PATH: %q, %v", got, err)
	}
}

func TestDockerExecutableDoesNotOverridePATHLookupFailure(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "relative executable", err: &exec.Error{Name: "docker", Err: exec.ErrDot}},
		{name: "permission denied", err: &exec.Error{Name: "docker", Err: os.ErrPermission}},
		{name: "ErrDot remains authoritative when wrapped with NotFound", err: errors.Join(exec.ErrDot, exec.ErrNotFound)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			got, err := resolveDockerExecutable(func(name string) (string, error) {
				calls = append(calls, name)
				return "docker", test.err
			}, []string{filepath.Join(t.TempDir(), "docker")})
			if got != "docker" || err != test.err || !reflect.DeepEqual(calls, []string{"docker"}) {
				t.Fatalf("unsafe fallback after PATH error: %q, %v, lookups %v", got, err, calls)
			}
		})
	}
}

func TestDockerExecutablePreservesOriginalNotFoundWhenCandidatesUnavailable(t *testing.T) {
	original := &exec.Error{Name: "docker", Err: exec.ErrNotFound}
	candidate := filepath.Join(t.TempDir(), "docker")
	var calls []string
	got, err := resolveDockerExecutable(func(name string) (string, error) {
		calls = append(calls, name)
		if name == "docker" {
			return "", original
		}
		return "", &os.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
	}, []string{candidate})
	if got != "" || err != original || !errors.Is(err, exec.ErrNotFound) || !reflect.DeepEqual(calls, []string{"docker", candidate}) {
		t.Fatalf("original lookup error was lost: %q, %v, lookups %v", got, err, calls)
	}
}

func TestDockerExecutableRejectsRelativeLookupResultsAndCandidates(t *testing.T) {
	t.Run("PATH result", func(t *testing.T) {
		_, err := resolveDockerExecutable(func(string) (string, error) { return "bin/docker", nil }, nil)
		if !errors.Is(err, exec.ErrDot) {
			t.Fatalf("relative PATH result accepted: %v", err)
		}
	})
	t.Run("fallback", func(t *testing.T) {
		original := &exec.Error{Name: "docker", Err: exec.ErrNotFound}
		absolute := filepath.Join(t.TempDir(), "docker")
		var calls []string
		_, err := resolveDockerExecutable(func(name string) (string, error) {
			calls = append(calls, name)
			if name == "docker" {
				return "", original
			}
			return "bin/docker", nil
		}, []string{"./docker", "bin/docker", absolute})
		if err != original || !reflect.DeepEqual(calls, []string{"docker", absolute}) {
			t.Fatalf("relative fallback accepted: %v, lookups %v", err, calls)
		}
	})
}

func TestDockerExecutablePreservesMulticallSymlinkName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("OrbStack's multicall symlink is a macOS installation layout")
	}
	root := t.TempDir()
	target := filepath.Join(root, "docker-tools")
	if err := os.WriteFile(target, []byte("test executable; not executed"), 0755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "docker")
	if err := os.Symlink("docker-tools", alias); err != nil {
		t.Fatal(err)
	}
	got, err := resolveDockerExecutable(func(name string) (string, error) {
		if name == "docker" {
			return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
		}
		return exec.LookPath(name)
	}, []string{alias})
	if err != nil || got != alias || got == target {
		t.Fatalf("multicall symlink name changed: %q, %v", got, err)
	}
}

func TestDockerExecutableCandidatesCoverStandardInstallations(t *testing.T) {
	for _, test := range []struct {
		goos, home string
		env        map[string]string
		want       []string
	}{
		{
			goos: "darwin", home: "/Users/example",
			want: []string{
				"/usr/local/bin/docker", "/opt/homebrew/bin/docker",
				"/Users/example/.orbstack/bin/docker", "/Users/example/.docker/bin/docker",
				"/Applications/OrbStack.app/Contents/MacOS/xbin/docker", "/Applications/Docker.app/Contents/Resources/bin/docker",
				"/Users/example/Applications/OrbStack.app/Contents/MacOS/xbin/docker", "/Users/example/Applications/Docker.app/Contents/Resources/bin/docker",
			},
		},
		{
			goos: "windows", home: `C:\Users\example`,
			env: map[string]string{"ProgramW6432": `C:\Program Files`, "ProgramFiles": `c:/program files/`, "LOCALAPPDATA": `C:\Users\example\AppData\Local`},
			want: []string{
				`C:\Program Files\Docker\Docker\resources\bin\docker.exe`,
				`C:\Users\example\AppData\Local\Programs\DockerDesktop\resources\bin\docker.exe`,
			},
		},
		{
			goos: "linux", home: "/home/example",
			want: []string{"/usr/local/bin/docker", "/usr/bin/docker", "/bin/docker", "/snap/bin/docker", "/home/example/.docker/bin/docker", "/home/example/.local/bin/docker"},
		},
	} {
		t.Run(test.goos, func(t *testing.T) {
			got := dockerExecutableCandidates(test.goos, test.home, func(name string) string { return test.env[name] })
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("standard installation candidates differ:\ngot  %q\nwant %q", got, test.want)
			}
		})
	}
}

func TestDockerExecutableCandidatesRejectRelativeEnvironmentDirectories(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		got := dockerExecutableCandidates(goos, "relative-home", func(string) string { return "relative-install" })
		for _, candidate := range got {
			if strings.Contains(candidate, "relative-") || (goos != "windows" && !strings.HasPrefix(candidate, "/")) {
				t.Fatalf("%s candidate uses current directory: %q", goos, candidate)
			}
		}
		if goos == "windows" && len(got) != 0 {
			t.Fatalf("Windows relative installation paths accepted: %q", got)
		}
	}
	for _, root := range []string{`C:relative`, `\root-relative`, "", `C:/..`, `\\server`} {
		if got := dockerExecutableCandidate("windows", root, "docker.exe"); got != "" {
			t.Fatalf("incomplete Windows absolute root %q accepted: %q", root, got)
		}
	}
	if got := dockerExecutableCandidate("windows", `\\server\share`, "Docker", "docker.exe"); got != `\\server\share\Docker\docker.exe` {
		t.Fatalf("complete Windows UNC root rejected: %q", got)
	}
}
