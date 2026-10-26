package platform

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/oines/astral/internal/domain"
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
