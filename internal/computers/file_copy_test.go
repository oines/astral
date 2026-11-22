package computers

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/oines/astral/internal/transport"
)

func TestFileCopyWaitsForSourceStatus(t *testing.T) {
	const contents = "complete copy"
	size := int64(len(contents))
	broken := errors.New("source changed after sending its data")
	for _, status := range []error{nil, broken, transport.ErrDisconnected} {
		name := "success"
		if status != nil {
			name = status.Error()
		}
		t.Run(name, func(t *testing.T) {
			_, fs, path := fileWriteFixture(t)
			if err := os.WriteFile(path, []byte("original"), 0640); err != nil {
				t.Fatal(err)
			}
			input, writer := io.Pipe()
			defer input.Close()
			defer writer.Close()
			done := make(chan error, 1)
			var info FileInfo
			go func() {
				defer input.Close()
				var err error
				info, err = receiveCopiedFile(t.Context(), fs, filepath.Base(path), &size, input)
				done <- err
			}()
			if _, err := io.WriteString(writer, contents); err != nil {
				t.Fatal(err)
			}
			assertFileWriteContents(t, path, "original")
			if err := writer.CloseWithError(status); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, status) {
				t.Fatalf("copy returned %v, want %v", err, status)
			}
			if status == nil {
				assertFileWriteContents(t, path, contents)
				if info.Path != path || info.Size != size || !strings.HasPrefix(info.MediaType, "text/plain") {
					t.Fatalf("incorrect result: %+v", info)
				}
				st, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if runtime.GOOS != "windows" && st.Mode().Perm() != 0640 {
					t.Fatalf("copy changed target permissions to %v", st.Mode().Perm())
				}
			} else {
				assertFileWriteContents(t, path, "original")
			}
			files, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(files) != 1 {
				t.Fatalf("staging files remained: %v, %v", files, err)
			}
		})
	}
}

func TestFileCopyRejectsInvalidLengthAndDestination(t *testing.T) {
	_, fs, path := fileWriteFixture(t)
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, length := range []int64{1, 3} {
		if _, err := receiveCopiedFile(t.Context(), fs, path, &length, strings.NewReader("ab")); err == nil {
			t.Fatalf("copied two bytes with declared length %d", length)
		}
		assertFileWriteContents(t, path, "original")
	}
	size := int64(2)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	input := &cancellingFileUpload{reader: strings.NewReader("ab"), cancel: cancel}
	if _, err := receiveCopiedFile(ctx, fs, path, &size, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled copy returned %v", err)
	}
	assertFileWriteContents(t, path, "original")
	if _, err := receiveCopiedFile(t.Context(), fs, filepath.Dir(path), &size, strings.NewReader("ab")); err == nil {
		t.Fatal("accepted a directory destination")
	}
	if runtime.GOOS != "windows" {
		link := path + ".symlink"
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if _, err := receiveCopiedFile(t.Context(), fs, link, &size, strings.NewReader("ab")); err == nil {
			t.Fatal("accepted a symlink destination")
		}
		if _, err := os.Readlink(link); err != nil {
			t.Fatal(err)
		}
		assertFileWriteContents(t, path, "original")
	}
}
