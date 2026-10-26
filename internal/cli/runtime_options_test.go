package cli

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/oines/astral/internal/platform"
)

func TestRuntimeEnvironmentAndExplicitCLIHaveIndependentPrecedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(file, []byte("fixture-password-with-newline\n"), 0600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"ASTRAL_SERVER_DATA_DIR": "/env/server", "ASTRAL_COMPUTER_DATA_DIR": "/env/computers", "ASTRAL_NO_LOCAL": "true", "ASTRAL_TRUSTED_PROXIES": `["10.0.0.0/24"]`, "ASTRAL_WEB_SESSION_TTL": "24h", "ASTRAL_ADMIN_USERNAME": "fixture-admin", "ASTRAL_ADMIN_PASSWORD_FILE": file}
	lookup := func(key string) (string, bool) { v, ok := env[key]; return v, ok }
	var config platform.Config
	f := flags("serve", io.Discard)
	f.StringVar(&config.DataDirectory, "data", "", "")
	if err := f.Parse([]string{"--data=/explicit/server"}); err != nil {
		t.Fatal(err)
	}
	if err := serveRuntimeOptions(&config, f, nil, "", "", lookup); err != nil {
		t.Fatal(err)
	}
	if config.DataDirectory != "/explicit/server" || config.ComputerDirectory != "/env/computers" || !config.NoLocal || config.SessionLifetime != 24*time.Hour || !reflect.DeepEqual(config.TrustedProxies, []string{"10.0.0.0/24"}) || config.InitialAdminUsername != "fixture-admin" || config.InitialAdminPassword != "fixture-password-with-newline" {
		t.Fatalf("runtime configuration lost precedence or secret-file handling: %+v", config)
	}
}

func TestRuntimeSecretsAndTrustConfigurationFailClosed(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"two password sources": {"ASTRAL_ADMIN_USERNAME": "a", "ASTRAL_ADMIN_PASSWORD": "password", "ASTRAL_ADMIN_PASSWORD_FILE": "/file"},
		"partial account":      {"ASTRAL_ADMIN_USERNAME": "a"},
		"invalid proxies":      {"ASTRAL_TRUSTED_PROXIES": `["*.example.com"]`},
		"null proxies":         {"ASTRAL_TRUSTED_PROXIES": "null"},
		"zero lifetime":        {"ASTRAL_WEB_SESSION_TTL": "0"},
	} {
		t.Run(name, func(t *testing.T) {
			var config platform.Config
			err := serveRuntimeOptions(&config, flags("serve", io.Discard), nil, "", "", func(k string) (string, bool) { v, ok := env[k]; return v, ok })
			if err == nil {
				t.Fatal("invalid runtime configuration was accepted")
			}
		})
	}
}

func TestMultipleCLIAddressesReplaceEnvironmentAsAWhole(t *testing.T) {
	f := flags("serve", io.Discard)
	var urls repeatedStrings
	f.Var(&urls, "public-url", "")
	if err := f.Parse([]string{"--public-url=http://one:4545", "--public-url=https://two"}); err != nil {
		t.Fatal(err)
	}
	var config platform.Config
	if err := serveNetworkOptions(&config, f, "", urls, false, func(k string) (string, bool) { return "invalid ignored lower priority", k == "ASTRAL_PUBLIC_URLS" }); err != nil {
		t.Fatal(err)
	}
	if config.NetworkCLI.PublicURLs == nil || !reflect.DeepEqual(*config.NetworkCLI.PublicURLs, []string{"http://one:4545", "https://two"}) || config.NetworkEnvironment.PublicURLs != nil {
		t.Fatal("address sources were merged or ordering lost")
	}
}
