package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	if len(args) == 0 || (args[0] != "setup" && args[0] != "reset" && args[0] != "status") {
		return fmt.Errorf("usage: astral auth setup|reset|status [--data DIRECTORY] [--username USER] [--password-file FILE]")
	}
	f := flags("auth "+args[0], stderr)
	data := f.String("data", "", "Server data directory (automatically find the installed Server)")
	username := f.String("username", "", "Web administrator username")
	passwordFile := f.String("password-file", "", "read the new password from a private file instead of prompting")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected authentication argument")
	}
	directory, err := resolveServerData(ctx, *data, stderr, args[0] == "setup")
	if err != nil {
		return err
	}
	if args[0] == "status" {
		if *username != "" || *passwordFile != "" {
			return fmt.Errorf("auth status does not accept account credentials")
		}
		status, err := platform.AdministratorStatus(ctx, directory)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(status)
	}
	if *username == "" {
		*username = os.Getenv("ASTRAL_ADMIN_USERNAME")
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
	password := ""
	if *passwordFile == "" {
		*passwordFile = os.Getenv("ASTRAL_ADMIN_PASSWORD_FILE")
		password = os.Getenv("ASTRAL_ADMIN_PASSWORD")
	}
	if *passwordFile != "" {
		if password != "" {
			return fmt.Errorf("set only one of ASTRAL_ADMIN_PASSWORD or ASTRAL_ADMIN_PASSWORD_FILE")
		}
		raw, err := os.ReadFile(*passwordFile)
		if err != nil {
			return fmt.Errorf("read administrator password file: %w", err)
		}
		if len(raw) > 4096 {
			return fmt.Errorf("administrator password file is too large")
		}
		password = strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	} else if password == "" {
		password, err = hiddenInput("New password: ", stderr)
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
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = platform.ConfigureAdministrator(bounded, directory, *username, password, args[0] == "reset"); err != nil {
		return err
	}
	fmt.Fprintln(out, "Web administrator configured. All previous Web login sessions are invalid.")
	return nil
}
