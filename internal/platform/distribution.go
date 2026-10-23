package platform

import (
	"crypto/sha256"
	_ "embed"
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
	if (system != "linux" && system != "darwin" && system != "windows") || (arch != "amd64" && arch != "arm64") {
		return "", domain.Invalid("unsupported distribution target")
	}
	name := "astral-" + system + "-" + arch
	if system == "windows" {
		name += ".exe"
	}
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
		for _, system := range []string{"darwin", "linux", "windows"} {
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
					suffix := ""
					if system == "windows" {
						suffix = ".exe"
					}
					_, _ = fmt.Fprintf(w, "%x  astral-%s-%s%s\n", hash.Sum(nil), system, arch, suffix)
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
	arch := parts[2]
	if parts[1] == "windows" {
		if !strings.HasSuffix(arch, ".exe") {
			http.NotFound(w, r)
			return
		}
		arch = strings.TrimSuffix(arch, ".exe")
	}
	path, err := s.binary(parts[1], arch)
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, path)
}

func (s *Server) powerShellInstaller(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, powerShellInstallerScript)
}
func (s *Server) installer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, installerScript)
}

//go:embed installers/install.sh
var installerScript string

//go:embed installers/install.ps1
var powerShellInstallerScript string
