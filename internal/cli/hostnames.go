package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/oines/astral/internal/platform"
)

func configuration(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "hostnames" {
		return fmt.Errorf("usage: astral config hostnames [show|disable] [--data DIRECTORY] [--json]")
	}
	operation := "show"
	args = args[1:]
	if len(args) > 0 && (args[0] == "show" || args[0] == "disable") {
		operation, args = args[0], args[1:]
	}
	f := flags("config hostnames", stderr)
	data := f.String("data", "", "Server data directory (automatically find the installed Server)")
	structured := f.Bool("json", false, "print effective and saved hostname settings as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("usage: astral config hostnames [show|disable] [--data DIRECTORY] [--json]")
	}
	directory, err := resolveServerData(ctx, *data, stderr, false)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	settings, err := platform.LocalHostnameSettings(bounded, directory, operation == "disable")
	if err != nil {
		return err
	}
	if *structured {
		return json.NewEncoder(out).Encode(struct {
			DataDirectory     string `json:"data_directory"`
			RestrictHostnames any    `json:"restrict_hostnames"`
			AllowedHostnames  any    `json:"allowed_hostnames"`
		}{directory, settings.RestrictHostnames, settings.AllowedHostnames})
	}
	fmt.Fprintln(out, "Server data:", directory)
	state := "disabled"
	if settings.RestrictHostnames.Value {
		state = "enabled"
	}
	fmt.Fprintf(out, "Hostname restriction: %s (%s)\n", state, settings.RestrictHostnames.Source)
	if !settings.RestrictHostnames.Editable {
		fmt.Fprintln(out, "Change --restrict-hostnames or ASTRAL_RESTRICT_HOSTNAMES in the Server startup configuration to remove the override.")
	}
	if len(settings.AllowedHostnames.Value) > 0 {
		fmt.Fprintln(out, "Allowed hostnames:")
		for _, host := range settings.AllowedHostnames.Value {
			fmt.Fprintln(out, "  "+host)
		}
	}
	if operation == "disable" {
		fmt.Fprintln(out, "Hostname restriction disabled in Server settings. Web sign-in is still required.")
	}
	return nil
}
