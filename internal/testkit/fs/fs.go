// Package fs supplies filesystem fixtures that work with ordinary user rights.
package fs

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// SymlinkDir exercises directory-link traversal on both platforms. Windows
// without Developer Mode can create junctions without elevation, even when
// creating symbolic links requires a privilege the user does not have.
func SymlinkDir(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err == nil {
		return
	}
	if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	output, junctionErr := exec.Command("cmd.exe", "/d", "/s", "/c", "mklink", "/J", link, target).CombinedOutput()
	if junctionErr != nil {
		t.Fatalf("create directory link %s -> %s: symlink=%v; junction=%v: %s", link, target, err, junctionErr, output)
	}
}
