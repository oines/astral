package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oines/astral/internal/platform"
	"golang.org/x/term"
)

func hiddenInput(prompt string, stderr io.Writer) (string, error) {
	fmt.Fprint(stderr, prompt)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("a terminal is required for hidden input; use ASTRAL_ADMIN_USERNAME and ASTRAL_ADMIN_PASSWORD_FILE for unattended initialization")
	}
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(stderr)
	return string(raw), err
}
func administrator(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "setup" && args[0] != "reset") {
		return fmt.Errorf("usage: astral auth setup|reset [--data DIRECTORY] [--username USER]")
	}
	root, err := platform.DefaultRoot()
	if err != nil {
		return err
	}
	f := flags("auth "+args[0], stderr)
	defaultData := filepath.Join(root, "server")
	if value := os.Getenv("ASTRAL_SERVER_DATA_DIR"); value != "" {
		defaultData = value
	}
	data := f.String("data", defaultData, "Server data directory")
	username := f.String("username", "", "Web administrator username")
	if err = f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected authentication argument")
	}
	if *username == "" {
		fmt.Fprint(stderr, "Administrator username: ")
		value, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
		if readErr != nil {
			return readErr
		}
		*username = strings.TrimSpace(value)
	}
	if args[0] == "reset" {
		fmt.Fprintln(stderr, "This resets Web login, removes TOTP/Passkeys, and signs out every browser. Desktop, Bots and computers are retained.")
	}
	password, err := hiddenInput("New password: ", stderr)
	if err != nil {
		return err
	}
	confirm, err := hiddenInput("Confirm password: ", stderr)
	if err != nil {
		return err
	}
	if password != confirm {
		return fmt.Errorf("passwords do not match")
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = platform.ConfigureAdministrator(bounded, *data, *username, password, args[0] == "reset"); err != nil {
		return err
	}
	fmt.Fprintln(out, "Web administrator configured. All previous Web login sessions are invalid.")
	return nil
}
