//go:build integration

package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installerReleaseFixture exercises the production installer against the exact
// native binary built by this checkout. Only the disposable script is rewritten:
// production installers retain their fixed official HTTPS release origin.
func installerReleaseFixture(t *testing.T, directory, distribution string) (script, certificate string) {
	t.Helper()
	name := "astral-" + runtime.GOOS + "-" + runtime.GOARCH
	installer := "install.sh"
	if runtime.GOOS == "windows" {
		name += ".exe"
		installer = "install.ps1"
	}
	binary := filepath.Join(distribution, name)
	file, err := os.Open(binary)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	manifest := hex.EncodeToString(hash.Sum(nil)) + "  " + name + "\n"
	release := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + name:
			http.ServeFile(w, r, binary)
		case "/checksums.txt":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(release.Close)
	certificate = filepath.Join(directory, "release-fixture.pem")
	if err = os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: release.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("installers", installer))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	replace := func(old, next string) {
		t.Helper()
		if strings.Count(source, old) != 1 {
			t.Fatalf("installer fixture expected exactly one %q", old)
		}
		source = strings.Replace(source, old, next, 1)
	}
	replace("https://github.com/oines/astral/releases/latest/download", release.URL)
	if runtime.GOOS == "windows" {
		replace("@('github.com', 'release-assets.githubusercontent.com', 'objects.githubusercontent.com')", "@('127.0.0.1')")
		// HttpClient completes TLS callbacks on worker threads without a
		// PowerShell runspace. A static C# delegate trusts only this fixture's
		// exact certificate; no global 'accept any certificate' override exists.
		trust := fmt.Sprintf(`
Add-Type -TypeDefinition @'
using System;
using System.Net;
using System.Net.Security;
using System.Reflection;
using System.Security.Cryptography.X509Certificates;
public static class AstralInstallerFixtureTLS {
  public static bool Validate(object sender, X509Certificate certificate, X509Chain chain, SslPolicyErrors errors) {
    return certificate != null && Convert.ToBase64String(certificate.GetRawCertData()) == "%s";
  }
  public static void Configure(object handler) {
    PropertyInfo property = handler.GetType().GetProperty("ServerCertificateCustomValidationCallback");
    if (property != null) {
      MethodInfo method = typeof(AstralInstallerFixtureTLS).GetMethod("Validate", BindingFlags.Public | BindingFlags.Static);
      property.SetValue(handler, Delegate.CreateDelegate(property.PropertyType, method), null);
    } else {
      ServicePointManager.ServerCertificateValidationCallback = Validate;
    }
  }
}
'@
`, base64.StdEncoding.EncodeToString(release.Certificate().Raw))
		replace("Add-Type -AssemblyName System.Net.Http", "Add-Type -AssemblyName System.Net.Http\n"+trust)
		replace("$handler.AllowAutoRedirect = $false", "$handler.AllowAutoRedirect = $false\n  [AstralInstallerFixtureTLS]::Configure($handler)")
	}
	script = filepath.Join(directory, installer)
	if err = os.WriteFile(script, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	return script, certificate
}
