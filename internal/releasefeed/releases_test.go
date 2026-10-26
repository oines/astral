package releasefeed

import (
	"context"
	"crypto/sha256"
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

func TestLatestPinsTagAndChecksBytes(t *testing.T) {
	data := "official release bytes"
	hash := sha256.Sum256([]byte(data))
	corrupt := false
	latestTag := "v0.1.8"
	latestReads := 0
	c := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" {
			t.Fatal("non-HTTPS release request")
		}
		var body string
		switch r.URL.String() {
		case "https://api.github.com/repos/oines/astral/releases/latest":
			latestReads++
			body = fmt.Sprintf(`{"tag_name":%q,"prerelease":false}`, latestTag)
		case "https://github.com/oines/astral/releases/download/v0.1.8/checksums.txt":
			body = fmt.Sprintf("%x  astral-linux-arm64\n", hash)
		case "https://github.com/oines/astral/releases/download/v0.1.8/astral-linux-arm64":
			body = data
			if corrupt {
				body = "tampered"
			}
		default:
			t.Fatalf("download did not use immutable official tag: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	b, err := latest(context.Background(), "linux", "arm64", c)
	if err != nil {
		t.Fatal(err)
	}
	// Another release is published after resolution but before bytes transfer.
	// Download must retain the resolved version and checksum, not follow latest.
	latestTag = "v0.1.9"
	if err = b.verify(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	forged := b
	forged.SHA256 = strings.Repeat("b", 64)
	if err = forged.verify(context.Background(), c); err == nil {
		t.Fatal("local handoff replaced the official checksum")
	}
	target := filepath.Join(t.TempDir(), "astral")
	if err = b.download(context.Background(), target, c); err != nil {
		t.Fatal(err)
	}
	if b.Version != "0.1.8" || latestReads != 1 {
		t.Fatal("download changed the pinned version after a new latest release")
	}
	corrupt = true
	if err = b.download(context.Background(), target, c); err == nil {
		t.Fatal("accepted corrupted binary")
	}
	raw, _ := os.ReadFile(target)
	if string(raw) != data {
		t.Fatal("failed download replaced working executable")
	}
	wrong := b
	wrong.Name = "astral-windows-amd64.exe"
	if wrong.Validate("linux", "arm64") == nil {
		t.Fatal("accepted wrong target")
	}
	wrong = b
	wrong.Version = "0.1.8/../latest"
	if wrong.Validate("linux", "arm64") == nil {
		t.Fatal("accepted tag traversal")
	}
}

func TestVersionComparison(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"0.1.9", "0.1.10", -1}, {"v0.2.0", "0.1.99", 1}, {"0.1.0", "0.1.0", 0}} {
		got, err := Compare(c.a, c.b)
		if err != nil || got != c.want {
			t.Fatalf("%s %s: %d %v", c.a, c.b, got, err)
		}
	}
	if _, err := Compare("dev", "0.1.0"); err == nil {
		t.Fatal("dev is not a release version")
	}
}
