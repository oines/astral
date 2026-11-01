package platform

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/domain"
)

// Docker checks use the same process identity and CLI environment as actual
// container operations. In particular, a terminal's sudo access says nothing
// about the background Server's access to the selected Docker socket.
func dockerProcessUser() string {
	if uid := os.Geteuid(); uid >= 0 {
		return dockerAccountForUID(uid)
	}
	if account, err := user.Current(); err == nil {
		if account.Username != "" {
			return account.Username
		}
		if account.Uid != "" {
			return "UID " + account.Uid
		}
	}
	return "unknown account"
}

func dockerAccountForUID(uid int) string {
	id := strconv.Itoa(uid)
	if runtime.GOOS == "linux" {
		// In static Linux builds, even LookupId can return Current's cached
		// USER fallback when this UID has no passwd entry. Read the actual
		// account record or report the effective UID without guessing a name.
		if passwd, err := os.ReadFile("/etc/passwd"); err == nil {
			for _, line := range strings.Split(string(passwd), "\n") {
				fields := strings.Split(line, ":")
				if len(fields) >= 3 && fields[0] != "" && fields[2] == id {
					return fields[0]
				}
			}
		}
	} else if account, err := user.LookupId(id); err == nil && account.Uid == id && account.Username != "" {
		return account.Username
	}
	return "UID " + id
}

type dockerProbeError struct {
	status string
	user   string
	cause  error
}

func (e *dockerProbeError) Error() string {
	message := "Docker could not be checked."
	switch e.status {
	case "missing_cli":
		message = "Install Docker Engine before creating a Computer container."
		switch runtime.GOOS {
		case "darwin":
			message = "Install OrbStack or Docker Desktop before creating a Computer container."
		case "windows":
			message = "Install Docker Desktop in Linux-container mode before creating a Computer container."
		}
	case "permission_denied":
		message = fmt.Sprintf("Astral is running as account %q, which cannot access Docker. Configure Docker access for this account, then check again.", e.user)
	case "not_running":
		message = "The selected Docker engine is not running or cannot be reached. Start it, then check again."
	}
	return message + " " + e.cause.Error()
}

func (e *dockerProbeError) Unwrap() error {
	return errors.Join(domain.Offline(e.Error()), e.cause)
}

func dockerProbeFailure(err error) error {
	status := "error"
	text := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, exec.ErrNotFound):
		status = "missing_cli"
	case errors.Is(err, fs.ErrPermission) || strings.Contains(text, "permission denied") || strings.Contains(text, "access is denied") || strings.Contains(text, "operation not permitted"):
		status = "permission_denied"
	case strings.Contains(text, "cannot connect to the docker daemon") || strings.Contains(text, "is the docker daemon running") || strings.Contains(text, "docker daemon is not running") || strings.Contains(text, "connection refused") || (strings.Contains(text, "dial unix") && strings.Contains(text, "no such file or directory")) || (strings.Contains(text, "pipe/") && strings.Contains(text, "the system cannot find the file specified")):
		status = "not_running"
	}
	return &dockerProbeError{status: status, user: dockerProcessUser(), cause: err}
}
