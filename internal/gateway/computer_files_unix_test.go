//go:build unix

package gateway

import (
	"net/http"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestComputerFileCopyRejectsFIFOWithoutWriter(t *testing.T) {
	f := newComputerFileFixture(t)
	path := filepath.Join(f.source.AgentHome, "pipe")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	f.copyFile(t, CopyComputerFileRequest{SourcePath: path, TargetPath: "result"}, http.StatusBadRequest)
}
