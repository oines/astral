package computers

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGeneratedComputerCommandRoutesBootstrapAndPreservesEnrollmentArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell bootstrap; Windows uses the PowerShell generator")
	}
	for _, proxy := range []string{"", "https://gh-proxy.org/", "https://proxy.example/prefix"} {
		t.Run(proxy, func(t *testing.T) {
			directory := t.TempDir()
			urlLog := filepath.Join(directory, "download-url")
			argsLog := filepath.Join(directory, "installer-arguments")
			proxyLog := filepath.Join(directory, "installer-proxy")
			fixture := `#!/bin/sh
for value in "$@"; do
  case "$value" in https://*) printf '%s' "$value" > "$ASTRAL_FIXTURE_URL" ;; esac
done
cat <<'INSTALLER'
#!/bin/sh
printf '%s\n' "$@" > "$ASTRAL_FIXTURE_ARGS"
printf '%s' "${ASTRAL_GITHUB_PROXY:-}" > "$ASTRAL_FIXTURE_PROXY"
INSTALLER
`
			if err := os.WriteFile(filepath.Join(directory, "curl"), []byte(fixture), 0700); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("sh", "-c", ShellInstallerCommand("--server 'http://private-server:4545' --token 'temporary token'"))
			command.Env = append(os.Environ(), "PATH="+directory+string(os.PathListSeparator)+os.Getenv("PATH"), "ASTRAL_GITHUB_PROXY="+proxy, "ASTRAL_FIXTURE_URL="+urlLog, "ASTRAL_FIXTURE_ARGS="+argsLog, "ASTRAL_FIXTURE_PROXY="+proxyLog)
			if raw, err := command.CombinedOutput(); err != nil {
				t.Fatalf("bootstrap failed: %v %s", err, raw)
			}
			prefix := proxy
			if prefix != "" {
				prefix = strings.TrimRight(prefix, "/") + "/"
			}
			if raw, err := os.ReadFile(urlLog); err != nil || string(raw) != prefix+"https://github.com/oines/astral/releases/latest/download/install-computer.sh" {
				t.Fatalf("bootstrap did not use selected public release source: %s %v", raw, err)
			}
			if raw, err := os.ReadFile(argsLog); err != nil || string(raw) != "--server\nhttp://private-server:4545\n--token\ntemporary token\n" {
				t.Fatalf("proxy selection rewrote private enrollment arguments: %s %v", raw, err)
			}
			if raw, err := os.ReadFile(proxyLog); err != nil || string(raw) != proxy {
				t.Fatalf("installer did not inherit the explicit proxy choice: %s %v", raw, err)
			}
		})
	}
}
