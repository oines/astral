package serverupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func digest(raw []byte) string                                        { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func officialFixture(t *testing.T, change func(map[string]any)) (*http.Client, *[]string) {
	t.Helper()
	installer := []byte("#!/bin/bash\nexit 0\n")
	manifest := map[string]any{"schema_version": 1, "purpose": "release", "public_release_ready": true, "version": "0.1.2", "tag": "v0.1.2", "assets": []any{map[string]any{"name": "install-server.sh", "size": len(installer), "sha256": digest(installer)}}}
	if change != nil {
		change(manifest)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	base := "https://github.com/oines/astral/releases/download/v0.1.2"
	bodies := map[string]string{"https://api.github.com/repos/oines/astral/releases/latest": `{"tag_name":"v0.1.2","draft":false,"prerelease":false}`, base + "/release-manifest.json": string(raw), base + "/SHA256SUMS": fmt.Sprintf("%s  install-server.sh\n%s  release-manifest.json\n", digest(installer), digest(raw)), base + "/install-server.sh": string(installer)}
	calls := []string{}
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.String())
		body, ok := bodies[r.URL.String()]
		if !ok {
			t.Fatalf("unexpected official update request: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	return client, &calls
}
func TestOfficialMetadataIsPinnedAndChecksNeverFetchInstaller(t *testing.T) {
	client, calls := officialFixture(t, nil)
	target, err := resolveLatest(context.Background(), client)
	if err != nil || target.Version != "0.1.2" {
		t.Fatalf("resolve failed: %+v %v", target, err)
	}
	if len(*calls) != 3 { // Latest, immutable sums, immutable manifest only.
		t.Fatalf("unexpected metadata requests: %v", *calls)
	}
	output := filepath.Join(t.TempDir(), "installer")
	if err = fetchInstallerWithClient(context.Background(), target, output, client); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 5 || strings.Contains(strings.Join((*calls)[3:], "\n"), "/latest") {
		t.Fatal("runner re-resolved Latest instead of its immutable selected release")
	}
	raw, err := os.ReadFile(output)
	if err != nil || digest(raw) != target.InstallerSHA {
		t.Fatal("downloaded installer was not verified")
	}
}
func TestCandidateAndMalformedInstallerInventoryAreRejected(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(m map[string]any) { m["public_release_ready"] = false },
		func(m map[string]any) { m["purpose"] = "unsigned-candidate" },
		func(m map[string]any) { m["tag"] = "v0.1.3" },
		func(m map[string]any) { m["assets"] = []any{} },
		func(m map[string]any) { a := m["assets"].([]any); m["assets"] = append(a, a[0]) },
		func(m map[string]any) { m["assets"].([]any)[0].(map[string]any)["sha256"] = strings.Repeat("0", 64) },
	} {
		client, _ := officialFixture(t, change)
		if _, err := resolveLatest(context.Background(), client); err == nil {
			t.Fatal("invalid official installer metadata accepted")
		}
	}
}
func TestDescriptorCannotSelectArbitraryVersionURLOrInstallerHash(t *testing.T) {
	for _, target := range []releaseTarget{{Version: "https://evil.example/installer"}, {Version: "v0.1.2"}, {Version: "0.1.2", InstallerSHA: strings.Repeat("0", 64)}} {
		client, calls := officialFixture(t, nil)
		err := fetchInstallerWithClient(context.Background(), target, filepath.Join(t.TempDir(), "installer"), client)
		if err == nil {
			t.Fatal("invalid descriptor downloaded installer")
		}
		for _, call := range *calls {
			if strings.HasSuffix(call, "/install-server.sh") {
				t.Fatal("unverified descriptor reached executable download")
			}
		}
	}
	for _, raw := range []string{"", strings.Repeat("a", 64) + "  install-server.sh\n" + strings.Repeat("a", 64) + "  install-server.sh", strings.Repeat("A", 64) + "  install-server.sh"} {
		if _, err := checksum([]byte(raw), "install-server.sh"); err == nil {
			t.Fatal("bad checksum accepted")
		}
	}
}
