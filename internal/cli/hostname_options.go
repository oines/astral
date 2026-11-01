package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"strconv"

	"github.com/oines/astral/internal/platform"
)

func serveHostnameOptions(config *platform.Config, flags *flag.FlagSet, restrict bool, hostnames []string, lookup func(string) (string, bool)) error {
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "restrict-hostnames":
			config.NetworkCLI.RestrictHostnames = &restrict
		case "allowed-hostname":
			config.NetworkCLI.AllowedHostnames = &hostnames
		}
	})
	if config.NetworkCLI.RestrictHostnames == nil {
		if raw, set := lookup("ASTRAL_RESTRICT_HOSTNAMES"); set {
			value, err := strconv.ParseBool(raw)
			if err != nil {
				return fmt.Errorf("ASTRAL_RESTRICT_HOSTNAMES must be true or false")
			}
			config.NetworkEnvironment.RestrictHostnames = &value
		}
	}
	if config.NetworkCLI.AllowedHostnames == nil {
		if raw, set := lookup("ASTRAL_ALLOWED_HOSTNAMES"); set {
			var values []string
			if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
				return fmt.Errorf("ASTRAL_ALLOWED_HOSTNAMES must be a JSON array of hostnames or IP addresses")
			}
			config.NetworkEnvironment.AllowedHostnames = &values
		}
	}
	return nil
}
