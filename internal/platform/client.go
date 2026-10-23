package platform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/oines/astral/internal/modelbridge"
)

type ConnectOptions struct {
	ServerURL    string `json:"server_url"`
	EnrollmentID string `json:"enrollment_id"`
	Token        string `json:"token"`
	Root         string `json:"root,omitempty"`
	Foreground   bool   `json:"foreground,omitempty"`
	Container    bool   `json:"container,omitempty"`
	ServerCA     string `json:"server_ca,omitempty"`
}

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)

func HTTPJSON(ctx context.Context, method, url, token string, input, output any) error {
	return httpJSONWithCA(ctx, method, url, token, input, output, "")
}
func httpJSONWithCA(ctx context.Context, method, url, token string, input, output any, serverCA string) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client, err := computers.NewHTTPClient(serverCA)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("astral server could not be reached: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var problem struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &problem) == nil && problem.Message != "" {
			return fmt.Errorf("astral returned HTTP %d: %s", response.StatusCode, problem.Message)
		}
		return fmt.Errorf("astral returned HTTP %d", response.StatusCode)
	}
	if output != nil {
		return json.Unmarshal(raw, output)
	}
	return nil
}
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
	if err := modelbridge.ValidateURL(options.ServerURL); err != nil {
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
		if credentials.ServerURL != options.ServerURL {
			return domain.Conflict("this Daemon directory belongs to a different Server")
		}
	} else {
		d, createErr := computers.NewDaemon(root, computers.Credentials{ServerURL: options.ServerURL, ServerCA: options.ServerCA})
		if createErr != nil {
			return createErr
		}
		registerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = httpJSONWithCA(registerCtx, "POST", strings.TrimRight(options.ServerURL, "/")+"/internal/enrollments/"+options.EnrollmentID, "", computers.EnrollmentRequest{Token: options.Token, Hello: d.Hello}, &credentials, options.ServerCA)
		cancel()
		if err != nil {
			return err
		}
		// Preserve the usable address supplied by the installer (including an
		// intentional local/reverse-proxy route) rather than reinterpreting it.
		credentials.ServerURL = options.ServerURL
		credentials.ServerCA = options.ServerCA
		if err = WriteJSON(credentialsPath, credentials); err != nil {
			return err
		}
		if report != nil {
			report("Computer registered: " + credentials.ComputerID)
		}
	}
	if options.Foreground {
		return RunDaemon(ctx, root, credentialsPath, report)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	manager, err := daemonservice.New(root)
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
	}
	if err = manager.Install(ctx, daemonservice.Spec{Executable: executable, Credentials: credentialsPath, EnvironmentPATH: os.Getenv("PATH")}); err != nil {
		return fmt.Errorf("Computer registered, but its current-user service could not be installed: %w", err)
	}
	if err = manager.Start(ctx); err != nil {
		return fmt.Errorf("Daemon service installed but could not start: %w", err)
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
			return fmt.Errorf("Daemon service was installed but did not become running and online; inspect %s", filepath.Join(root, "daemon.log"))
		case <-timer.C:
		}
	}
}
func computerStatus(ctx context.Context, c computers.Credentials) (domain.ComputerStatus, error) {
	var status domain.ComputerStatus
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	err := httpJSONWithCA(bounded, "GET", strings.TrimRight(c.ServerURL, "/")+"/internal/computers/"+c.ComputerID+"/status", c.Token, nil, &status, c.ServerCA)
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
		if err = runDocker(ctx, report, "start", existing); err != nil {
			return err
		}
		if report != nil {
			report("Existing container started: " + existing + ". Its program, environment, and enrollment were retained.")
		}
		return nil
	}
	if installation.ID == "" {
		if err = HTTPJSON(ctx, "POST", strings.TrimRight(options.ServerURL, "/")+"/internal/enrollments/"+options.EnrollmentID+"/prepare", options.Token, nil, nil); err != nil {
			return fmt.Errorf("claim container preparation before building its environment: %w", err)
		}
	}
	binary := filepath.Join(options.Root, "astral-linux-"+probe.Arch)
	if runtime.GOOS == "linux" && runtime.GOARCH == probe.Arch {
		binary, err = os.Executable()
	} else {
		err = downloadVerifiedBinary(ctx, options.ServerURL, "astral-linux-"+probe.Arch, binary)
	}
	if err != nil {
		return err
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
		// Reusing an enrolled container must keep its durable route and CA even
		// after the temporary enrollment has expired.
		inside.ServerURL, inside.ServerCA = previous.ServerURL, previous.ServerCA
	} else if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return previousErr
	}
	if probe.Desktop && (runtime.GOOS == "windows" || runtime.GOOS == "darwin") {
		if parsed, parseErr := url.Parse(options.ServerURL); previousErr != nil && parseErr == nil && isLoopbackHost(parsed.Hostname()) {
			var access containerAccessResponse
			hostname, _ := os.Hostname()
			err = HTTPJSON(ctx, "POST", strings.TrimRight(options.ServerURL, "/")+"/internal/enrollments/"+options.EnrollmentID+"/container-access", options.Token, map[string]string{"hostname": hostname}, &access)
			if err != nil {
				return fmt.Errorf("prepare local container connection: %w", err)
			}
			inside.ServerURL, inside.ServerCA = access.ServerURL, access.ServerCA
		}
	}
	if err = WriteJSON(enrollmentPath, inside); err != nil {
		return err
	}
	id, err := LaunchContainer(ctx, DockerOptions{Identity: identity, Binary: binary, EnrollmentFile: enrollmentPath, Runtime: &probe}, func(phase, line string) {
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

func downloadVerifiedBinary(ctx context.Context, serverURL, name, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(file string) (*http.Response, error) {
		request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(serverURL, "/")+"/downloads/"+file, nil)
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
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && parts[1] == name {
			expected = parts[0]
		}
	}
	if len(raw) > 8192 || len(expected) != 64 {
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
