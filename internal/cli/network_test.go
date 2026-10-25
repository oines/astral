package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/oines/astral/internal/platform"
)

func TestServeNetworkOverridesPreservePresence(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"absent", nil, nil},
		{"environment", nil, map[string]string{"ASTRAL_SERVER_LISTEN": "0.0.0.0:4545", "ASTRAL_PUBLIC_URL": "http://host:4545", "ASTRAL_ALLOW_REMOTE_MANAGEMENT": "false"}},
		{"cli", []string{"--listen=127.0.0.1:0", "--public-url=", "--allow-remote-management=false"}, map[string]string{"ASTRAL_SERVER_LISTEN": "invalid", "ASTRAL_PUBLIC_URL": "invalid", "ASTRAL_ALLOW_REMOTE_MANAGEMENT": "invalid"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := flags("serve", io.Discard)
			listen := f.String("listen", "", "")
			public := f.String("public-url", "", "")
			allow := f.Bool("allow-remote-management", false, "")
			if err := f.Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			var config platform.Config
			err := serveNetworkOptions(&config, f, *listen, *public, *allow, func(key string) (string, bool) { v, ok := tt.env[key]; return v, ok })
			if err != nil {
				t.Fatal(err)
			}
			switch tt.name {
			case "absent":
				if config.NetworkCLI.Listen != nil || config.NetworkCLI.PublicURL != nil || config.NetworkCLI.AllowRemoteManagement != nil || config.NetworkEnvironment.AllowRemoteManagement != nil {
					t.Fatal("absent settings became overrides")
				}
			case "environment":
				if config.NetworkEnvironment.AllowRemoteManagement == nil || *config.NetworkEnvironment.AllowRemoteManagement || config.NetworkEnvironment.PublicURL == nil || *config.NetworkEnvironment.PublicURL != tt.env["ASTRAL_PUBLIC_URL"] {
					t.Fatal("environment values or explicit false were lost")
				}
			case "cli":
				if config.NetworkCLI.AllowRemoteManagement == nil || *config.NetworkCLI.AllowRemoteManagement || config.NetworkCLI.PublicURL == nil || *config.NetworkCLI.PublicURL != "" || config.NetworkEnvironment.Listen != nil {
					t.Fatal("CLI did not override environment, including false and empty address")
				}
			}
		})
	}
}

func TestServeRejectsInvalidNetworkBoolean(t *testing.T) {
	f := flags("serve", io.Discard)
	var config platform.Config
	if err := serveNetworkOptions(&config, f, "", "", false, func(key string) (string, bool) { return "sometimes", key == "ASTRAL_ALLOW_REMOTE_MANAGEMENT" }); err == nil {
		t.Fatal("invalid environment boolean was silently accepted")
	}
}

func TestServeRejectsPositionalsBeforeOpeningData(t *testing.T) {
	err := serve(context.Background(), []string{"--allow-remote-management", "false", "--data", t.TempDir()}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unexpected serve argument") {
		t.Fatalf("bare false must not enable remote management or ignore following flags: %v", err)
	}
}
