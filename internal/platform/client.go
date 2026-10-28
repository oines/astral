package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/daemonservice"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/securetransport"
	"github.com/oines/astral/internal/serviceurl"
)

type ConnectOptions struct {
	ServerURL    string `json:"server_url"`
	ServerPin    string `json:"server_pin"`
	EnrollmentID string `json:"enrollment_id"`
	Token        string `json:"token"`
	Root         string `json:"root,omitempty"`
	Foreground   bool   `json:"foreground,omitempty"`
	Container    bool   `json:"container,omitempty"`
}

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

func connectRoot(options ConnectOptions) (string, error) {
	if options.Root != "" {
		return filepath.Abs(options.Root)
	}
	base, err := DefaultRoot()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(options.ServerURL))
	return filepath.Join(base, "computers", hex.EncodeToString(hash[:6]), options.EnrollmentID), nil
}
func Connect(ctx context.Context, options ConnectOptions, report func(string)) error {
	if err := serviceurl.Validate(options.ServerURL); err != nil {
		return err
	}
	if _, err := securetransport.ClientConfig(options.ServerPin); err != nil {
		return err
	}
	if !identityPattern.MatchString(options.EnrollmentID) {
		return domain.Invalid("invalid enrollment identity")
	}
	root, err := connectRoot(options)
	if err != nil {
		return err
	}
	options.Root = root
	if options.Container {
		return connectContainer(ctx, options, report)
	}
	credentialsPath := filepath.Join(root, "credentials.json")
	var credentials computers.Credentials
	readErr := ReadJSON(credentialsPath, &credentials)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if credentials.ComputerID != "" {
		if credentials.ServerURL != options.ServerURL || credentials.ServerPin != options.ServerPin {
			return domain.Conflict("this Daemon directory belongs to a different Server")
		}
	}
	var manager *daemonservice.Manager
	if !options.Foreground {
		manager, err = daemonservice.New(root)
		if err != nil {
			return err
		}
		if err = manager.Prepare(ctx, report); err != nil {
			return fmt.Errorf("prepare current-user Daemon service before pairing: %w", err)
		}
	}
	if credentials.ComputerID == "" {
		d, createErr := computers.NewDaemon(root, computers.Credentials{ServerURL: options.ServerURL, ServerPin: options.ServerPin})
		if createErr != nil {
			return createErr
		}
		registerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		credentials, err = computers.RegisterRemote(registerCtx, options.ServerURL, options.ServerPin, options.EnrollmentID, options.Token, d.Hello)
		cancel()
		if err != nil {
			return err
		}
		// Preserve the usable address supplied by the installer (including an
		// intentional local/reverse-proxy route) rather than reinterpreting it.
		credentials.ServerURL = options.ServerURL
		if err = WriteJSON(credentialsPath, credentials); err != nil {
			return err
		}
		if report != nil {
			report("Computer registered: " + credentials.ComputerID)
		}
	}
	if options.Foreground {
		// A container's foreground entry point always has a parent that can
		// replace/restart the Daemon without exiting or rebuilding the container.
		if runtime.GOOS == "linux" && os.Getenv("ASTRAL_CONTAINER_INFO") != "" && os.Getenv("ASTRAL_CONTAINER_SUPERVISOR") != "1" {
			output := &lineOutput{emit: report}
			return SuperviseContainer(ctx, root, "", output, output)
		}
		return RunDaemon(ctx, root, credentialsPath, report)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	state, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	if state.State == "not-installed" {
		lock, lockErr := Lock(filepath.Join(root, "daemon.lock"))
		if lockErr != nil {
			return fmt.Errorf("an unmanaged Daemon is already using this directory; stop it before installing its current-user service: %w", lockErr)
		}
		_ = lock.Close()
	}
	if report != nil {
		report("Installing " + state.DisplayName + " as a current-user service (" + state.Backend + ").")
		report("Daemon program: " + executable)
		report("Daemon data: " + root)
		report("Daemon log: " + state.LogPath)
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
		prefix := ""
		if runtime.GOOS == "windows" {
			quote = func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
			prefix = "& "
		}
		report("Service status: " + prefix + quote(executable) + " computer service status --root " + quote(root))
	}
	if err = manager.Install(ctx, daemonservice.Spec{Executable: executable, Credentials: credentialsPath, EnvironmentPATH: os.Getenv("PATH")}); err != nil {
		return fmt.Errorf("computer registered, but its current-user service could not be installed: %w", err)
	}
	if err = manager.Start(ctx); err != nil {
		return fmt.Errorf("daemon service installed but could not start: %w", err)
	}
	wait, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	timer := time.NewTicker(200 * time.Millisecond)
	defer timer.Stop()
	for {
		service, serviceErr := manager.Status(wait)
		if status, statusErr := computerStatus(wait, credentials); serviceErr == nil && service.State == "running" && statusErr == nil && status.Online {
			if report != nil {
				report("Computer connected: " + status.Name)
			}
			return nil
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("daemon service was installed but did not become running and online; inspect %s", filepath.Join(root, "daemon.log"))
		case <-timer.C:
		}
	}
}
func computerStatus(ctx context.Context, c computers.Credentials) (domain.ComputerStatus, error) {
	var status domain.ComputerStatus
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	status, err := computers.RemoteStatus(bounded, c)
	return status, err
}
func RunDaemon(ctx context.Context, root, credentialsPath string, report func(string)) error {
	lock, err := Lock(filepath.Join(root, "daemon.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	var credentials computers.Credentials
	if err = ReadJSON(credentialsPath, &credentials); err != nil {
		return err
	}
	d, err := computers.NewDaemon(root, credentials)
	if err != nil {
		return err
	}
	d.Hello.Version = DaemonVersion
	configureDaemonUpdate(d)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = d.Close(closeCtx)
	}()
	err = d.Run(ctx, func(err error) {
		if err != nil && report != nil && ctx.Err() == nil {
			report("Daemon connection interrupted: " + err.Error())
		}
	})
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// RunDaemonLogged keeps service diagnostics in the enrolled user's own data
// directory. It does not redirect process-wide stdout or stderr.
func RunDaemonLogged(ctx context.Context, root, credentialsPath, logPath string, report func(string)) error {
	if logPath == "" {
		return RunDaemon(ctx, root, credentialsPath, report)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	emit := func(message string) {
		_, _ = fmt.Fprintf(file, "%s %s\n", time.Now().UTC().Format(time.RFC3339), message)
		if report != nil {
			report(message)
		}
	}
	emit("Astral Daemon starting")
	err = RunDaemon(ctx, root, credentialsPath, emit)
	if err != nil {
		emit("Astral Daemon stopped: " + err.Error())
	} else {
		emit("Astral Daemon stopped")
	}
	return err
}

// ContainerIdentity is stable for retries of the same enrollment; it never
// depends on a downloaded program version.
func ContainerIdentity(options ConnectOptions) string {
	sum := sha256.Sum256([]byte(options.ServerURL + "\x00" + options.EnrollmentID))
	return hex.EncodeToString(sum[:12])
}
func connectContainer(ctx context.Context, options ConnectOptions, report func(string)) error {
	probe, err := ProbeDocker(ctx)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(options.Root, 0700); err != nil {
		return err
	}
	identity := ContainerIdentity(options)
	ctx = dockerContext(ctx, probe)
	var installation ContainerInstallation
	if readErr := ReadJSON(filepath.Join(options.Root, "container.json"), &installation); readErr == nil {
		if installation.Identity != identity {
			return domain.Conflict("this directory belongs to another container enrollment")
		}
		if installation.Endpoint != "" && installation.Endpoint != probe.Endpoint {
			return domain.Conflict("this container belongs to another local Docker endpoint; select its original context")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if existing, findErr := findExistingContainer(ctx, identity); findErr != nil {
		return findErr
	} else if existing != "" {
		created, stateErr := containerAwaitingFirstStart(ctx, existing)
		if stateErr != nil {
			return stateErr
		}
		if created {
			binary, binaryErr := containerBinary(ctx, options.Root, probe.Arch)
			if binaryErr != nil {
				return binaryErr
			}
			if err = injectContainerDaemon(ctx, existing, binary); err != nil {
				return err
			}
		}
		if err = runDocker(ctx, report, "start", existing); err != nil {
			return err
		}
		if report != nil {
			report("Existing container started: " + existing + ". Its program, environment, and enrollment were retained.")
		}
		return nil
	}
	var preparation struct {
		LocalContainerURL string `json:"local_container_url"`
	}
	if installation.ID == "" {
		hostname, _ := os.Hostname()
		if preparation.LocalContainerURL, err = computers.PrepareRemoteContainer(ctx, options.ServerURL, options.ServerPin, options.EnrollmentID, options.Token, hostname); err != nil {
			return fmt.Errorf("claim container preparation before downloading its environment: %w", err)
		}
	}
	enrollmentPath := filepath.Join(options.Root, "container-enrollment.json")
	inside := options
	inside.Container = false
	inside.Root = ""
	inside.Foreground = false
	var existing ContainerInstallation
	if readErr := ReadJSON(filepath.Join(options.Root, "container.json"), &existing); readErr == nil && existing.Identity != identity {
		return domain.Conflict("this directory belongs to another container enrollment")
	} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	var previous ConnectOptions
	previousErr := ReadJSON(enrollmentPath, &previous)
	if previousErr == nil && previous.EnrollmentID == options.EnrollmentID {
		// Reusing an enrolled container keeps the durable route after the
		// temporary enrollment has expired.
		inside.ServerURL = previous.ServerURL
	} else if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return previousErr
	}
	binary, err := containerBinary(ctx, options.Root, probe.Arch)
	if err != nil {
		return err
	}
	if err = WriteJSON(enrollmentPath, inside); err != nil {
		return err
	}
	id, err := LaunchContainer(ctx, DockerOptions{Identity: identity, Binary: binary, EnrollmentFile: enrollmentPath, Runtime: &probe, ServerURLs: []string{preparation.LocalContainerURL}}, func(phase, line string) {
		if report != nil {
			report(phase + ": " + line)
		}
	})
	if err != nil {
		return err
	}
	if report != nil {
		report("Container started: " + id + ". Astral will show it online after its Daemon connects.")
	}
	return nil
}

func containerBinary(ctx context.Context, root, arch string) (string, error) {
	if runtime.GOOS == "linux" && runtime.GOARCH == arch {
		return os.Executable()
	}
	binary := filepath.Join(root, "astral-linux-"+arch)
	return binary, downloadVerifiedBinary(ctx, "astral-linux-"+arch, binary)
}

const officialReleaseBase = "https://github.com/oines/astral/releases/latest/download"

func downloadVerifiedBinary(ctx context.Context, name, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) >= 8 || request.URL.Scheme != "https" {
			return errors.New("official release redirect is not trusted")
		}
		switch request.URL.Hostname() {
		case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		default:
			return errors.New("official release redirect has an unexpected host")
		}
	}}
	defer client.CloseIdleConnections()
	return downloadReleaseBinary(ctx, officialReleaseBase, name, target, client)
}

func downloadReleaseBinary(ctx context.Context, base, name, target string, client *http.Client) error {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" {
		return errors.New("official executable downloads require HTTPS")
	}
	get := func(file string) (*http.Response, error) {
		request, err := http.NewRequestWithContext(ctx, "GET", base+"/"+file, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("daemon download failed: %w", err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("daemon distribution is unavailable (HTTP %d)", response.StatusCode)
		}
		return response, nil
	}
	checksums, err := get("checksums.txt")
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(checksums.Body, 8193))
	checksums.Body.Close()
	if err != nil {
		return err
	}
	expected := ""
	matches := 0
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == name {
			expected = parts[0]
			matches++
		}
	}
	if len(raw) > 8192 || len(expected) != 64 || matches != 1 {
		return fmt.Errorf("daemon distribution has no valid checksum")
	}
	response, err := get(name)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	file, err := os.CreateTemp(filepath.Dir(target), ".astral-download-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0700); err != nil {
		file.Close()
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, (256<<20)+1))
	if err = errors.Join(copyErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if size > 256<<20 || hex.EncodeToString(hash.Sum(nil)) != expected {
		return fmt.Errorf("daemon binary checksum did not match")
	}
	return os.Rename(file.Name(), target)
}
