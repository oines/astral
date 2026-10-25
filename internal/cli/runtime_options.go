package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/platform"
)

func serveRuntimeOptions(config *platform.Config, f *flag.FlagSet, proxies []string, username, passwordFile string, lookup func(string) (string, bool)) error {
	explicit := map[string]bool{}
	f.Visit(func(option *flag.Flag) { explicit[option.Name] = true })
	for _, item := range []struct {
		flag, env string
		target    *string
	}{
		{"data", "ASTRAL_SERVER_DATA_DIR", &config.DataDirectory},
		{"computers-dir", "ASTRAL_COMPUTER_DATA_DIR", &config.ComputerDirectory},
		{"web-dir", "ASTRAL_WEB_DIR", &config.WebDirectory},
		{"distribution-dir", "ASTRAL_DISTRIBUTION_DIR", &config.DistributionDirectory},
	} {
		if !explicit[item.flag] {
			if value, set := lookup(item.env); set {
				*item.target = value
			}
		}
	}
	if !explicit["no-local"] {
		if value, set := lookup("ASTRAL_NO_LOCAL"); set {
			parsed, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("ASTRAL_NO_LOCAL must be true or false")
			}
			config.NoLocal = parsed
		}
	}
	if explicit["trusted-proxy"] {
		config.TrustedProxies = append([]string{}, proxies...)
	} else if raw, set := lookup("ASTRAL_TRUSTED_PROXIES"); set {
		if err := json.Unmarshal([]byte(raw), &config.TrustedProxies); err != nil || config.TrustedProxies == nil {
			return fmt.Errorf("ASTRAL_TRUSTED_PROXIES must be a JSON array of IP addresses or CIDRs")
		}
	}
	for _, value := range config.TrustedProxies {
		if _, err := netip.ParsePrefix(value); err != nil {
			if _, err = netip.ParseAddr(value); err != nil {
				return fmt.Errorf("trusted proxies must be IP addresses or CIDRs")
			}
		}
	}
	if !explicit["web-session-ttl"] {
		if raw, set := lookup("ASTRAL_WEB_SESSION_TTL"); set {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return fmt.Errorf("ASTRAL_WEB_SESSION_TTL must be a positive duration, for example 720h")
			}
			config.SessionLifetime = value
		}
	}
	if explicit["web-session-ttl"] && config.SessionLifetime <= 0 {
		return fmt.Errorf("--web-session-ttl must be positive")
	}
	if !explicit["initial-admin-username"] {
		username, _ = lookup("ASTRAL_ADMIN_USERNAME")
	}
	var password string
	if !explicit["initial-admin-password-file"] {
		passwordFile, _ = lookup("ASTRAL_ADMIN_PASSWORD_FILE")
		password, _ = lookup("ASTRAL_ADMIN_PASSWORD")
		if passwordFile != "" && password != "" {
			return fmt.Errorf("set only one of ASTRAL_ADMIN_PASSWORD or ASTRAL_ADMIN_PASSWORD_FILE")
		}
	}
	if passwordFile != "" {
		raw, err := os.ReadFile(passwordFile)
		if err != nil {
			return fmt.Errorf("read initial administrator password file: %w", err)
		}
		if len(raw) > 4096 {
			return fmt.Errorf("initial administrator password file is too large")
		}
		password = strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r")
	}
	if (username == "") != (password == "") {
		return fmt.Errorf("initial administrator username and password must both be provided")
	}
	config.InitialAdminUsername, config.InitialAdminPassword = username, password
	return nil
}
