package platform

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDistributionDownloadChecksBytesBeforeReplacingExecutable(t *testing.T) {
	const name = "astral-linux-arm64"
	binary := []byte("release binary")
	digest := sha256.Sum256(binary)
	corrupt := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/checksums.txt":
			fmt.Fprintf(w, "%x  %s\n", digest, name)
		case "/" + name:
			if corrupt {
				w.Write([]byte("interrupted or tampered"))
			} else {
				w.Write(binary)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), name)
	if err := downloadReleaseBinary(context.Background(), server.URL, name, target, server.Client()); err != nil {
		t.Fatal(err)
	}
	corrupt = true
	if err := downloadReleaseBinary(context.Background(), server.URL, name, target, server.Client()); err == nil {
		t.Fatal("accepted mismatched distribution")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != string(binary) {
		t.Fatalf("corrupt download replaced the working executable: %q, %v", data, err)
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
		t.Fatalf("unexpected executable permissions: %v", err)
	}
}
