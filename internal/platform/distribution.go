package platform

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

func (s *Server) binary(system, arch string) (string, error) {
	if (system != "linux" && system != "darwin") || (arch != "amd64" && arch != "arm64") {
		return "", domain.Invalid("unsupported distribution target")
	}
	name := "astral-" + system + "-" + arch
	if s.Config.DistributionDirectory != "" {
		path := filepath.Join(s.Config.DistributionDirectory, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	if system == runtime.GOOS && arch == runtime.GOARCH && s.Config.Executable != "" {
		if info, err := os.Stat(s.Config.Executable); err == nil && info.Mode().IsRegular() {
			return s.Config.Executable, nil
		}
	}
	return "", domain.Missing("release binary", name+"; run pnpm build:distribution and set --distribution-dir dist/bin")
}
func (s *Server) downloadBinary(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "checksums.txt" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for _, system := range []string{"darwin", "linux"} {
			for _, arch := range []string{"arm64", "amd64"} {
				path, err := s.binary(system, arch)
				if err != nil {
					continue
				}
				file, err := os.Open(path)
				if err != nil {
					continue
				}
				hash := sha256.New()
				_, err = io.Copy(hash, file)
				file.Close()
				if err == nil {
					_, _ = fmt.Fprintf(w, "%x  astral-%s-%s\n", hash.Sum(nil), system, arch)
				}
			}
		}
		return
	}
	parts := strings.Split(name, "-")
	if len(parts) != 3 || parts[0] != "astral" {
		http.NotFound(w, r)
		return
	}
	path, err := s.binary(parts[1], parts[2])
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}
func (s *Server) installer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, installerScript)
}

const installerScript = `#!/bin/sh
set -eu
server=
for arg in "$@"; do
  if [ "$arg" = "--server" ]; then next_server=1; continue; fi
  if [ "${next_server:-0}" = 1 ]; then server=$arg; next_server=0; fi
done
if [ -z "$server" ]; then echo "Missing Astral Server URL." >&2; exit 2; fi
case "$(uname -s)" in Darwin) target_os=darwin ;; Linux) target_os=linux ;; *) echo "Astral supports macOS and Linux." >&2; exit 2 ;; esac
case "$(uname -m)" in arm64|aarch64) target_arch=arm64 ;; x86_64|amd64) target_arch=amd64 ;; *) echo "Unsupported CPU architecture." >&2; exit 2 ;; esac
install_base="${XDG_DATA_HOME:-$HOME/.local/share}/astral/bin"
mkdir -p "$install_base"
chmod 700 "$install_base"
temporary="$(mktemp -d "$install_base/install.XXXXXX")"
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
binary_name="astral-$target_os-$target_arch"
curl --fail --location --silent --show-error "$server/downloads/$binary_name" -o "$temporary/$binary_name"
curl --fail --location --silent --show-error "$server/downloads/checksums.txt" -o "$temporary/checksums.txt"
expected="$(awk -v file="$binary_name" '$2 == file { print $1 }' "$temporary/checksums.txt")"
if command -v sha256sum >/dev/null 2>&1; then actual="$(sha256sum "$temporary/$binary_name" | awk '{print $1}')"; else actual="$(shasum -a 256 "$temporary/$binary_name" | awk '{print $1}')"; fi
if [ -z "$expected" ] || [ "$actual" != "$expected" ]; then echo "Astral binary checksum did not match." >&2; exit 1; fi
chmod 700 "$temporary/$binary_name"
mv "$temporary/$binary_name" "$install_base/astral"
"$install_base/astral" computer connect "$@"
`
