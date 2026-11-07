package computers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral-core/standardtools/local"
)

func fileWriteFixture(t *testing.T) (*Daemon, standardtools.FileSystem, string) {
	t.Helper()
	directory := t.TempDir()
	backend, err := local.New(local.Options{CWD: directory})
	if err != nil {
		t.Fatal(err)
	}
	fs, err := backend.FileSystem(t.Context(), core.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{Root: t.TempDir(), fileWriteSlots: make(chan struct{}, 8)}
	return d, fs, filepath.Join(directory, "synthetic-target.txt")
}

func assertFileWriteOutcome(t *testing.T, err error, want core.Outcome) {
	t.Helper()
	var tool *core.ToolError
	if !errors.As(err, &tool) || tool.Outcome != want {
		t.Fatalf("file write outcome: got=%v want=%s", err, want)
	}
}

func assertFileWriteContents(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != want {
		t.Fatalf("file contents: got=%q want=%q error=%v", raw, want, err)
	}
}

func TestDaemonFileWriteDoesNotOpenTargetBeforeCompleteUpload(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new-file"
		if existing {
			name = "existing-file"
		}
		t.Run(name, func(t *testing.T) {
			d, fs, path := fileWriteFixture(t)
			if existing {
				if err := os.WriteFile(path, []byte("original content"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			input, upload := io.Pipe()
			t.Cleanup(func() { _ = upload.Close(); _ = input.Close() })
			done, prefixRead := make(chan error, 1), make(chan error, 1)
			go func() { done <- d.writeFile(t.Context(), fs, path, input) }()
			go func() { _, err := io.WriteString(upload, "complete new content"); prefixRead <- err }()
			select {
			case err := <-prefixRead:
				if err != nil {
					t.Fatal(err)
				}
			case err := <-done:
				t.Fatalf("upload returned before EOF: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("upload prefix was not received")
			}
			if existing {
				assertFileWriteContents(t, path, "original content")
			} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("incomplete upload created a target: %v", err)
			}
			select {
			case err := <-done:
				t.Fatalf("upload committed without EOF: %v", err)
			default:
			}
			if err := upload.Close(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("complete upload did not settle")
			}
			assertFileWriteContents(t, path, "complete new content")
		})
	}
}

type failedFileUpload struct{ err error }

func (r failedFileUpload) Read([]byte) (int, error) { return 0, r.err }

type cancellingFileUpload struct {
	reader *strings.Reader
	cancel context.CancelFunc
}

func (r *cancellingFileUpload) Read(data []byte) (int, error) {
	n, err := r.reader.Read(data)
	if err == io.EOF {
		r.cancel()
	}
	return n, err
}

func TestDaemonFileWriteInterruptedUploadKeepsOriginal(t *testing.T) {
	for _, kind := range []string{"broken-stream", "cancelled-at-eof", "staging-unavailable"} {
		t.Run(kind, func(t *testing.T) {
			d, fs, path := fileWriteFixture(t)
			if err := os.WriteFile(path, []byte("original content"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var input io.Reader
			switch kind {
			case "broken-stream":
				input = io.MultiReader(strings.NewReader("uncommitted prefix"), failedFileUpload{io.ErrUnexpectedEOF})
			case "cancelled-at-eof":
				input = &cancellingFileUpload{reader: strings.NewReader("uncommitted prefix"), cancel: cancel}
			case "staging-unavailable":
				d.Root = filepath.Join(d.Root, "missing-staging-directory")
				input = io.LimitReader(repeatedFileUpload{}, fileWriteMemoryLimit+1)
			}
			err := d.writeFile(ctx, fs, path, input)
			assertFileWriteOutcome(t, err, core.OutcomeNotExecuted)
			assertFileWriteContents(t, path, "original content")
		})
	}
}

type repeatedFileUpload struct{}

func (repeatedFileUpload) Read(data []byte) (int, error) {
	for i := range data {
		data[i] = 's'
	}
	return len(data), nil
}

func TestDaemonFileWriteLimitKeepsOriginalAndRemovesStaging(t *testing.T) {
	d, fs, path := fileWriteFixture(t)
	if err := os.WriteFile(path, []byte("original content"), 0600); err != nil {
		t.Fatal(err)
	}
	err := d.writeFile(t.Context(), fs, path, io.LimitReader(repeatedFileUpload{}, fileWriteSizeLimit+1))
	assertFileWriteOutcome(t, err, core.OutcomeNotExecuted)
	assertFileWriteContents(t, path, "original content")
	files, err := os.ReadDir(d.Root)
	if err != nil || len(files) != 0 {
		t.Fatalf("rejected upload retained staging files: count=%d error=%v", len(files), err)
	}
}

func TestFileWriteStagingSpillsPrivatelyAndCleansUp(t *testing.T) {
	directory := t.TempDir()
	payload := strings.Repeat("synthetic payload\n", 32)
	for _, memoryLimit := range []int{16, len(payload)} {
		staged, err := receiveFileWrite(t.Context(), directory, strings.NewReader(payload), memoryLimit, len(payload))
		if err != nil {
			t.Fatal(err)
		}
		if memoryLimit < len(payload) {
			if staged.file == nil || len(staged.memory) != 0 {
				t.Fatal("upload did not release its memory payload after spilling")
			}
			info, err := staged.file.Stat()
			if err != nil || goruntime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
				t.Fatalf("staging is not private: info=%v error=%v", info, err)
			}
		} else if staged.file != nil || cap(staged.memory) > memoryLimit {
			t.Fatal("bounded in-memory upload unexpectedly spilled or over-allocated its backing buffer")
		}
		reader, err := staged.reader()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if err != nil || string(got) != payload {
			t.Fatalf("staging changed upload: %q %v", got, err)
		}
		if err := staged.close(); err != nil {
			t.Fatal(err)
		}
		files, err := os.ReadDir(directory)
		if err != nil || len(files) != 0 {
			t.Fatalf("completed upload retained staging files: count=%d error=%v", len(files), err)
		}
	}
}

func TestDaemonFileWritePreservesInodeLinksAndPermissions(t *testing.T) {
	for _, kind := range []string{"regular", "hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			d, fs, original := fileWriteFixture(t)
			if err := os.WriteFile(original, []byte("original content"), 0640); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(original)
			if err != nil {
				t.Fatal(err)
			}
			path := original
			if kind != "regular" {
				path = filepath.Join(filepath.Dir(original), "synthetic-link.txt")
				link := os.Link
				if kind == "symlink" {
					link = os.Symlink
				}
				if err := link(original, path); err != nil {
					t.Skipf("filesystem cannot create %s: %v", kind, err)
				}
			}
			if err := d.writeFile(t.Context(), fs, path, strings.NewReader("complete replacement")); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(original)
			if err != nil || !os.SameFile(before, after) || before.Mode().Perm() != after.Mode().Perm() {
				t.Fatalf("in-place identity or permissions changed: before=%v after=%v error=%v", before, after, err)
			}
			assertFileWriteContents(t, original, "complete replacement")
			assertFileWriteContents(t, path, "complete replacement")
			if kind == "symlink" {
				link, err := os.Lstat(path)
				if err != nil || link.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("write replaced the symlink: %v %v", link, err)
				}
			}
		})
	}
}

type countedFileUpload struct {
	io.Reader
	reads atomic.Int32
}

func (r *countedFileUpload) Read(data []byte) (int, error) {
	r.reads.Add(1)
	return r.Reader.Read(data)
}

func TestDaemonFileWriteCapacityWaitsWithoutReadingAndCanCancel(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "slot-released", true: "cancelled"}[cancelled], func(t *testing.T) {
			d, fs, path := fileWriteFixture(t)
			d.fileWriteSlots = make(chan struct{}, 1)
			d.fileWriteSlots <- struct{}{}
			if err := os.WriteFile(path, []byte("original content"), 0600); err != nil {
				t.Fatal(err)
			}
			input := &countedFileUpload{Reader: strings.NewReader("complete replacement")}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- d.writeFile(ctx, fs, path, input) }()
			select {
			case err := <-done:
				t.Fatalf("capacity returned a failure instead of waiting: %v", err)
			case <-time.After(25 * time.Millisecond):
			}
			if input.reads.Load() != 0 {
				t.Fatal("queued upload consumed its request body")
			}
			assertFileWriteContents(t, path, "original content")
			if cancelled {
				cancel()
			} else {
				<-d.fileWriteSlots
			}
			select {
			case err := <-done:
				if cancelled {
					assertFileWriteOutcome(t, err, core.OutcomeNotExecuted)
					if input.reads.Load() != 0 {
						t.Fatal("cancelled queued upload consumed its body")
					}
					assertFileWriteContents(t, path, "original content")
				} else if err != nil {
					t.Fatal(err)
				} else {
					assertFileWriteContents(t, path, "complete replacement")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("queued upload did not settle after cancellation or slot release")
			}
		})
	}
}

func TestFileWriteCommitIOFailureRemainsUnknown(t *testing.T) {
	_, _, path := fileWriteFixture(t)
	if err := os.WriteFile(path, []byte("original content"), 0600); err != nil {
		t.Fatal(err)
	}
	input := io.MultiReader(bytes.NewBufferString("partial local content"), failedFileUpload{errors.New("synthetic local staging read failure")})
	err := commitFileWrite(t.Context(), path, input)
	assertFileWriteOutcome(t, err, core.OutcomeUnknown)
	assertFileWriteContents(t, path, "partial local content")
}
