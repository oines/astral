package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockReleasesWithOwnerHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.lock")
	first, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if second, err := Lock(path); err == nil {
		second.Close()
		t.Fatal("a second owner acquired the same lock")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := Lock(path)
	if err != nil {
		t.Fatalf("lock was not released when the owner closed it: %v", err)
	}
	third.Close()
}

func TestWritePrivateReplacesCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "credentials.json")
	if err := WritePrivate(path, []byte("old credential")); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(path, []byte("new credential")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "new credential" {
		t.Fatalf("credential replacement failed: %q %v", raw, err)
	}
	assertPrivate(t, path)
	assertPrivate(t, filepath.Dir(path))
}
