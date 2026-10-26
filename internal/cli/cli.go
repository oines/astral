// Package cli delegates owner operations to the same HTTP API used by Web and
// Desktop. Service/Daemon startup and explicit offline maintenance run locally.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/platform"
	"github.com/oines/astral/internal/serviceurl"
)

var Version = "dev"

func Run(ctx context.Context, args []string, out, stderr io.Writer) int {
	platform.DaemonVersion = Version
	err := run(ctx, args, out, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
func flags(name string, stderr io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	return f
}
func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return nil
	}
	switch args[0] {
	case "help", "--help", "-h":
		usage(out)
		return nil
	case "version", "--version":
		return version(args[1:], out)
	case "serve":
		return serve(ctx, args[1:], out, stderr)
	case "computer":
		return computer(ctx, args[1:], out, stderr)
	case "maintenance":
		return maintenance(ctx, args[1:], out, stderr)
	case "connect":
		return connectOwner(ctx, args[1:], out, stderr)
	case "doctor":
		return ownerRequest(ctx, []string{"GET", "/api/v1/bootstrap"}, out, stderr)
	case "request":
		return ownerRequest(ctx, args[1:], out, stderr)
	case "resources":
		if len(args) < 2 {
			return fmt.Errorf("usage: astral resources agents|models|providers|computers|connections|sessions|search-providers")
		}
		switch args[1] {
		case "agents", "models", "providers", "computers", "connections", "sessions", "search-providers":
		default:
			return fmt.Errorf("unknown resource collection")
		}
		return ownerRequest(ctx, append([]string{"GET", "/api/v1/" + args[1]}, args[2:]...), out, stderr)
	case "session", "sessions":
		return session(ctx, args[1:], out, stderr)
	case "auth":
		return administrator(ctx, args[1:], out, stderr)
	default:
		return fmt.Errorf("unknown command %q; use astral help", args[0])
	}
}
func usage(out io.Writer) {
	fmt.Fprint(out, `Astral — persistent agents on your Computers

  astral serve [--listen 0.0.0.0:4545] [--public-url http://…] [--allow-remote-management]
  astral computer connect --server URL --enrollment ID --token TOKEN [--container]
  astral computer service status|logs|start|stop|restart|uninstall --root DIRECTORY
  astral computer update --root DIRECTORY
  astral computer update --container CONTAINER_NAME
  astral connect --server URL --username USER
  astral resources agents|models|providers|computers|connections|sessions
  astral sessions new --agent ID [--title TITLE]
  astral sessions send ID --text TEXT [--mode follow_up|steer]
  astral sessions stop|continue|history|export ID
  astral request METHOD /api/v1/PATH [--body-file FILE]
  astral doctor
  astral auth setup|reset [--data DIRECTORY]
  astral maintenance weixin-wire-ownership --data DIRECTORY [--purge-unowned]
  astral maintenance with-data-lock --data DIRECTORY --computers-dir DIRECTORY -- COMMAND [ARGS...]
  astral version [--json | --data-formats]

The Server serves HTTP. Use your own reverse proxy for HTTPS when needed.
Remote management is disabled by default; Computer connections remain available.
Network settings: explicit CLI > environment > saved settings > discovery/defaults.
Web login uses a username, password and any configured second factor.
Desktop authenticates only through its private startup channel.
Computer registration tokens are temporary; normal Daemon credentials are saved locally.
`)
}
func serve(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flags("serve", stderr)
	var config platform.Config
	listen := f.String("listen", "", "HTTP listen address (default 0.0.0.0:4545)")
	var publicURLs repeatedStrings
	f.Var(&publicURLs, "public-url", "HTTP/HTTPS access address; repeat for multiple addresses, empty for automatic")
	remoteManagement := f.Bool("allow-remote-management", false, "allow remote Web UI and management API access (account login still required)")
	f.StringVar(&config.DataDirectory, "data", "", "Server database directory")
	f.StringVar(&config.ComputerDirectory, "computers-dir", "", "local Daemon data directory (separate from Server data)")
	f.StringVar(&config.WebDirectory, "web-dir", "", "shared Web UI build directory")
	f.StringVar(&config.DistributionDirectory, "distribution-dir", "", "release binaries directory")
	f.BoolVar(&config.NoLocal, "no-local", false, "do not start the local Daemon automatically")
	var trustedProxies repeatedStrings
	f.Var(&trustedProxies, "trusted-proxy", "trusted reverse proxy IP/CIDR; repeat, empty disables proxy trust")
	f.DurationVar(&config.SessionLifetime, "web-session-ttl", 0, "browser login lifetime (default 720h)")
	adminUsername := f.String("initial-admin-username", "", "initialize the first Web administrator only")
	adminPasswordFile := f.String("initial-admin-password-file", "", "file containing initial administrator password")
	ready := f.String("ready-file", "", "write private startup information for the desktop shell")
	parentPID := f.Int("parent-pid", 0, "stop this managed Server if its desktop parent exits")
	managedStdin := f.Bool("managed-stdin", false, "stop this managed Server when its desktop input pipe closes")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected serve argument %q; use --allow-remote-management=false for an explicit false value", f.Arg(0))
	}
	if err := serveNetworkOptions(&config, f, *listen, []string(publicURLs), *remoteManagement, os.LookupEnv); err != nil {
		return err
	}
	if err := serveRuntimeOptions(&config, f, []string(trustedProxies), *adminUsername, *adminPasswordFile, os.LookupEnv); err != nil {
		return err
	}
	var managedInput io.Reader
	if *managedStdin {
		if *parentPID <= 0 || *parentPID != os.Getppid() {
			return fmt.Errorf("desktop parent process does not match")
		}
		reader := bufio.NewReaderSize(os.Stdin, 4096)
		type handshakeResult struct {
			token string
			err   error
		}
		result := make(chan handshakeResult, 1)
		go func() {
			raw, err := reader.ReadSlice('\n')
			var hello struct {
				DesktopToken string `json:"desktop_token"`
			}
			if err == nil {
				err = json.Unmarshal(raw, &hello)
			}
			if err == nil && len(hello.DesktopToken) != 64 {
				err = fmt.Errorf("invalid private desktop handshake")
			}
			result <- handshakeResult{hello.DesktopToken, err}
		}()
		select {
		case value := <-result:
			if value.err != nil {
				return value.err
			}
			config.DesktopToken = value.token
		case <-time.After(5 * time.Second):
			return fmt.Errorf("private desktop handshake timed out")
		case <-ctx.Done():
			return ctx.Err()
		}
		managedInput = reader
	}
	if *parentPID != 0 || *managedStdin {
		var input io.Reader
		if *managedStdin {
			input = managedInput
		}
		managed, cancel, err := platform.ManagedContext(ctx, *parentPID, input)
		if err != nil {
			return err
		}
		defer cancel()
		ctx = managed
	}
	if config.WebDirectory == "" {
		if stat, err := os.Stat("apps/web/dist/index.html"); err == nil && stat.Mode().IsRegular() {
			config.WebDirectory = "apps/web/dist"
		}
	}
	if config.DistributionDirectory == "" {
		if stat, err := os.Stat("dist/bin"); err == nil && stat.IsDir() {
			config.DistributionDirectory = "dist/bin"
		}
	}
	config.Version = Version
	s, err := platform.New(config)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if closeErr := s.Close(shutdown); closeErr != nil {
			fmt.Fprintln(stderr, "Shutdown:", closeErr)
		}
	}()
	if err = s.Start(); err != nil {
		return err
	}
	if *ready != "" {
		if err = platform.WriteJSON(*ready, map[string]any{"url": s.URL, "pid": os.Getpid(), "data_directory": s.Config.DataDirectory}); err != nil {
			return err
		}
		defer os.Remove(*ready)
	}
	fmt.Fprintln(out, "Astral is ready:", s.URL)
	fmt.Fprintln(out, "Set up Web login with: astral auth setup --data", s.Config.DataDirectory)
	fmt.Fprintln(out, "Computer data:", s.Config.ComputerDirectory)
	<-ctx.Done()
	return nil
}

// Keep absence distinct from an explicit empty address or false switch. The
// platform resolves these inputs above saved preferences without overwriting
// the user's lower-priority configuration.
type repeatedStrings []string

func (v *repeatedStrings) String() string { raw, _ := json.Marshal([]string(*v)); return string(raw) }
func (v *repeatedStrings) Set(value string) error {
	if value == "" {
		*v = []string{}
	} else {
		*v = append(*v, value)
	}
	return nil
}
func serveNetworkOptions(config *platform.Config, f *flag.FlagSet, listen string, publicURLs []string, allowRemote bool, lookup func(string) (string, bool)) error {
	f.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "listen":
			config.NetworkCLI.Listen = &listen
		case "public-url":
			config.NetworkCLI.PublicURLs = &publicURLs
		case "allow-remote-management":
			config.NetworkCLI.AllowRemoteManagement = &allowRemote
		}
	})
	if config.NetworkCLI.Listen == nil {
		if value, set := lookup("ASTRAL_SERVER_LISTEN"); set {
			config.NetworkEnvironment.Listen = &value
		}
	}
	if config.NetworkCLI.PublicURLs == nil {
		if value, set := lookup("ASTRAL_PUBLIC_URLS"); set {
			var values []string
			if err := json.Unmarshal([]byte(value), &values); err != nil || values == nil {
				return fmt.Errorf("ASTRAL_PUBLIC_URLS must be a JSON array of URLs")
			}
			config.NetworkEnvironment.PublicURLs = &values
		}
	}
	if config.NetworkCLI.AllowRemoteManagement == nil {
		if value, set := lookup("ASTRAL_ALLOW_REMOTE_MANAGEMENT"); set {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("ASTRAL_ALLOW_REMOTE_MANAGEMENT must be true or false")
			}
			config.NetworkEnvironment.AllowRemoteManagement = &parsed
		}
	}
	return nil
}

func computer(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: astral computer connect|daemon|service|update")
	}
	switch args[0] {
	case "status":
		return computerStatus(ctx, args[1:], out, stderr)
	case "connect":
		f := flags("computer connect", stderr)
		var options platform.ConnectOptions
		f.StringVar(&options.ServerURL, "server", "", "Astral Server URL")
		f.StringVar(&options.ServerPin, "server-pin", "", "Server SPKI SHA256 fingerprint")
		f.StringVar(&options.EnrollmentID, "enrollment", "", "temporary enrollment identity")
		f.StringVar(&options.Token, "token", "", "temporary enrollment token")
		f.StringVar(&options.Root, "root", "", "Daemon's persistent directory")
		f.BoolVar(&options.Container, "container", false, "create a Computer in the local container runtime")
		f.BoolVar(&options.Foreground, "foreground", false, "keep the Daemon in the foreground")
		enrollmentFile := f.String("enrollment-file", "", "private enrollment configuration file")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *enrollmentFile != "" {
			var saved platform.ConnectOptions
			if err := platform.ReadJSON(*enrollmentFile, &saved); err != nil {
				return err
			}
			saved.Root, saved.Foreground, saved.Container = options.Root, options.Foreground, options.Container
			options = saved
		}
		return platform.Connect(ctx, options, func(message string) { fmt.Fprintln(out, message) })
	case "daemon":
		f := flags("computer daemon", stderr)
		root := f.String("root", "", "Daemon data directory")
		credentials := f.String("credentials", "", "private credentials file")
		logPath := f.String("log", "", "append Daemon diagnostics to this file")
		parentPID := f.Int("parent-pid", 0, "stop this managed Daemon when its service launcher exits")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || *credentials == "" {
			return fmt.Errorf("--root and --credentials are required")
		}
		if *parentPID != 0 {
			managed, cancel, err := platform.ManagedContext(ctx, *parentPID, nil)
			if err != nil {
				return err
			}
			defer cancel()
			ctx = managed
		}
		return platform.RunDaemonLogged(ctx, *root, *credentials, *logPath, func(message string) { fmt.Fprintln(stderr, message) })
	case "service":
		return computerService(ctx, args[1:], out, stderr)
	case "update":
		return computerUpdate(ctx, args[1:], out, stderr)
	default:
		return fmt.Errorf("unknown Computer command")
	}
}

type ownerConfig struct {
	Server      string `json:"server"`
	SessionFile string `json:"session_file"`
}

func currentOwner() (ownerConfig, error) {
	root, err := platform.DefaultRoot()
	if err != nil {
		return ownerConfig{}, err
	}
	var config ownerConfig
	if err = platform.ReadJSON(filepath.Join(root, "client.json"), &config); err != nil {
		return config, fmt.Errorf("sign in first with astral connect --server URL --username USER: %w", err)
	}
	if config.Server == "" || config.SessionFile == "" {
		return config, fmt.Errorf("no Web login session is configured; use astral connect")
	}
	return config, nil
}
func connectOwner(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flags("connect", stderr)
	server := f.String("server", "", "Server URL")
	username := f.String("username", "", "administrator username")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected connect argument")
	}
	if err := serviceurl.Validate(*server); err != nil {
		return err
	}
	if *username == "" {
		fmt.Fprint(stderr, "Username: ")
		raw, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		*username = strings.TrimSpace(raw)
	}
	password, err := hiddenInput("Password: ", stderr)
	if err != nil {
		return err
	}
	var result struct {
		Authenticated bool     `json:"authenticated"`
		ChallengeID   string   `json:"challenge_id"`
		Methods       []string `json:"methods"`
	}
	token, err := loginJSON(ctx, *server, "/auth/login", map[string]string{"username": *username, "password": password}, &result)
	if err != nil {
		return err
	}
	if !result.Authenticated {
		code, err := hiddenInput("Authenticator or recovery code: ", stderr)
		if err != nil {
			return err
		}
		code = strings.TrimSpace(code)
		endpoint := "/auth/login/recovery"
		if len(code) == 6 {
			if _, err := strconv.Atoi(code); err == nil {
				endpoint = "/auth/login/totp"
			}
		}
		token, err = loginJSON(ctx, *server, endpoint, map[string]string{"challenge_id": result.ChallengeID, "code": code}, &result)
		if err != nil {
			return err
		}
	}
	if !result.Authenticated || token == "" {
		return fmt.Errorf("login did not complete; use a configured authenticator or recovery code")
	}
	root, err := platform.DefaultRoot()
	if err != nil {
		return err
	}
	sessionFile := filepath.Join(root, "client.session")
	if err = platform.WritePrivate(sessionFile, []byte(token+"\n")); err != nil {
		return err
	}
	if err = platform.WriteJSON(filepath.Join(root, "client.json"), ownerConfig{Server: *server, SessionFile: sessionFile}); err != nil {
		return err
	}
	fmt.Fprintln(out, "Signed in to", *server)
	return nil
}
func ownerRequest(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: astral request METHOD /api/v1/PATH [--body-file FILE]")
	}
	method, path := strings.ToUpper(args[0]), args[1]
	if !strings.HasPrefix(path, "/api/v1/") || strings.HasPrefix(path, "//") {
		return fmt.Errorf("owner requests must use /api/v1/ paths")
	}
	f := flags("request", stderr)
	server := f.String("server", "", "override Server URL")
	sessionFile := f.String("session-file", "", "private Web session credential file")
	bodyFile := f.String("body-file", "", "JSON request body file")
	bodyText := f.String("body", "", "JSON request body")
	if err := f.Parse(args[2:]); err != nil {
		return err
	}
	config, configErr := currentOwner()
	if *server != "" && config.Server != "" && strings.TrimRight(*server, "/") != strings.TrimRight(config.Server, "/") && *sessionFile == "" {
		return fmt.Errorf("sign in to the new Server with astral connect, or explicitly provide its --session-file")
	}
	if *server != "" {
		config.Server = *server
	}
	if *sessionFile != "" {
		config.SessionFile = *sessionFile
	}
	if config.Server == "" || config.SessionFile == "" {
		if configErr != nil {
			return configErr
		}
		return fmt.Errorf("an Astral Server and Web session file are required; sign in with astral connect")
	}
	if err := serviceurl.Validate(config.Server); err != nil {
		return err
	}
	token, err := os.ReadFile(config.SessionFile)
	if err != nil {
		return err
	}
	var input any
	raw := []byte(*bodyText)
	if *bodyFile != "" {
		if len(raw) > 0 {
			return fmt.Errorf("use --body or --body-file, not both")
		}
		raw, err = os.ReadFile(*bodyFile)
		if err != nil {
			return err
		}
	}
	if len(raw) > 0 {
		if !json.Valid(raw) {
			return fmt.Errorf("request body is not JSON")
		}
		input = json.RawMessage(raw)
	}
	var output json.RawMessage
	bounded, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	nextToken, err := webJSON(bounded, method, strings.TrimRight(config.Server, "/")+path, strings.TrimSpace(string(token)), input, &output)
	if err != nil {
		return err
	}
	if nextToken != "" && nextToken != strings.TrimSpace(string(token)) {
		if err = platform.WritePrivate(config.SessionFile, []byte(nextToken+"\n")); err != nil {
			return fmt.Errorf("the refreshed Web login could not be stored; sign in again with astral connect: %w", err)
		}
	}
	var pretty any
	if json.Unmarshal(output, &pretty) == nil {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(pretty)
	}
	_, err = out.Write(output)
	return err
}
func session(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: astral sessions new|send|stop|continue|history|export")
	}
	switch args[0] {
	case "new":
		f := flags("sessions new", stderr)
		agent := f.String("agent", "", "Agent ID")
		title := f.String("title", "New session", "title")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *agent == "" {
			return fmt.Errorf("--agent is required")
		}
		raw, _ := json.Marshal(map[string]string{"agent_id": *agent, "title": *title})
		return ownerRequest(ctx, []string{"POST", "/api/v1/sessions", "--body", string(raw)}, out, stderr)
	case "send":
		if len(args) < 2 {
			return fmt.Errorf("session ID is required")
		}
		f := flags("sessions send", stderr)
		text := f.String("text", "", "prompt text")
		mode := f.String("mode", "follow_up", "follow_up or steer")
		id := f.String("id", domain.ID("input"), "stable input ID for retries")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		raw, _ := json.Marshal(map[string]string{"id": *id, "text": *text, "mode": *mode})
		return ownerRequest(ctx, []string{"POST", "/api/v1/sessions/" + url.PathEscape(args[1]) + "/inputs", "--body", string(raw)}, out, stderr)
	case "stop", "continue", "history":
		if len(args) < 2 {
			return fmt.Errorf("session ID is required")
		}
		method := "POST"
		if args[0] == "history" {
			method = "GET"
		}
		return ownerRequest(ctx, append([]string{method, "/api/v1/sessions/" + url.PathEscape(args[1]) + "/" + args[0]}, args[2:]...), out, stderr)
	case "export":
		if len(args) < 2 {
			return fmt.Errorf("session ID is required")
		}
		f := flags("sessions export", stderr)
		output := f.String("output", "", "write JSONL to a new file instead of stdout")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		config, err := currentOwner()
		if err != nil {
			return err
		}
		if err = serviceurl.Validate(config.Server); err != nil {
			return err
		}
		token, err := os.ReadFile(config.SessionFile)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(config.Server, "/")+"/api/v1/sessions/"+url.PathEscape(args[1])+"/rollout", nil)
		if err != nil {
			return err
		}
		request.AddCookie(&http.Cookie{Name: auth.SessionCookieName(request.URL.Scheme == "https"), Value: strings.TrimSpace(string(token))})
		response, err := sessionClient().Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return fmt.Errorf("rollout export failed: HTTP %d", response.StatusCode)
		}
		writer := out
		if *output != "" {
			file, openErr := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if openErr != nil {
				return openErr
			}
			defer file.Close()
			writer = file
		}
		_, err = io.CopyBuffer(writer, response.Body, make([]byte, 32<<10))
		return err
	case "fork":
		if len(args) < 2 {
			return fmt.Errorf("session ID is required")
		}
		f := flags("sessions fork", stderr)
		entry := f.String("entry", "", "committed history Entry ID")
		title := f.String("title", "Fork", "new Session title")
		id := f.String("id", domain.ID("session"), "stable new Session ID")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if *entry == "" {
			return fmt.Errorf("--entry is required")
		}
		raw, _ := json.Marshal(map[string]string{"id": *id, "entry_id": *entry, "title": *title})
		return ownerRequest(ctx, []string{"POST", "/api/v1/sessions/" + url.PathEscape(args[1]) + "/fork", "--body", string(raw)}, out, stderr)
	default:
		return fmt.Errorf("unknown Session command %q", args[0])
	}
}
