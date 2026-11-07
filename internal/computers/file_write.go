package computers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
)

const (
	fileWriteMemoryLimit = 256 << 10
	fileWriteSizeLimit   = 32 << 20
)

// writeFile holds one of the Daemon's eight slots through reception, local
// commit and staging cleanup. Waiting callers do not consume their bodies;
// the transport's existing bounded credits provide upload backpressure.
// At most 8*32 MiB is staged. Retained payload buffers are capped at 256 KiB
// each; 32 KiB read buffers and transient buffer growth are additional costs.
func (d *Daemon) writeFile(ctx context.Context, fs standardtools.FileSystem, name string, input io.Reader) (err error) {
	if err := ctx.Err(); err != nil {
		return toolError(err, core.OutcomeNotExecuted)
	}
	select {
	case d.fileWriteSlots <- struct{}{}:
		defer func() { <-d.fileWriteSlots }()
	case <-ctx.Done():
		return toolError(ctx.Err(), core.OutcomeNotExecuted)
	}
	path, err := fs.ResolvePath(ctx, name, false)
	if err != nil {
		return err
	}
	staged, err := receiveFileWrite(ctx, d.Root, input, fileWriteMemoryLimit, fileWriteSizeLimit)
	if err != nil {
		return toolError(fmt.Errorf("file upload was not committed: %w", err), core.OutcomeNotExecuted)
	}
	defer func() {
		if cleanupErr := staged.close(); cleanupErr != nil && err == nil {
			err = toolError(fmt.Errorf("file was written but upload cleanup failed: %w", cleanupErr), core.OutcomeUnknown)
		}
	}()
	reader, err := staged.reader()
	if err != nil {
		return toolError(err, core.OutcomeNotExecuted)
	}
	return commitFileWrite(ctx, path.Path, reader)
}

type stagedFileWrite struct {
	memory []byte
	file   *os.File
}

// Only a normal EOF (the transport's input_end) completes reception. A broken
// connection, cancellation, over-limit payload or staging I/O error leaves the
// destination unopened. Staging is private, temporary and never replayed.
func receiveFileWrite(ctx context.Context, directory string, input io.Reader, memoryLimit, sizeLimit int) (_ *stagedFileWrite, err error) {
	if input == nil {
		return nil, errors.New("file upload has no input stream")
	}
	staged := &stagedFileWrite{}
	defer func() {
		if err != nil {
			_ = staged.close()
		}
	}()
	buffer := make([]byte, 32<<10)
	total, emptyReads := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := input.Read(buffer)
		if n < 0 || n > len(buffer) {
			return nil, errors.New("file upload returned an invalid read count")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if n > sizeLimit-total {
			return nil, errors.New("file exceeds the 32 MiB tool write limit; use a shell command for larger files")
		}
		if n > 0 {
			total += n
			emptyReads = 0
			if staged.file == nil && len(staged.memory)+n <= memoryLimit {
				// Bound backing capacity as well as length, including short reads.
				if needed := len(staged.memory) + n; needed > cap(staged.memory) {
					capacity := min(memoryLimit, max(needed, max(32<<10, 2*cap(staged.memory))))
					grown := make([]byte, len(staged.memory), capacity)
					copy(grown, staged.memory)
					staged.memory = grown
				}
				staged.memory = append(staged.memory, buffer[:n]...)
			} else {
				if staged.file == nil {
					staged.file, err = os.CreateTemp(directory, ".astral-file-write-*")
					if err != nil {
						return nil, err
					}
					if _, err = staged.file.Write(staged.memory); err != nil {
						return nil, err
					}
					staged.memory = nil
				}
				if _, err = staged.file.Write(buffer[:n]); err != nil {
					return nil, err
				}
			}
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return nil, io.ErrNoProgress
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return nil, readErr
			}
			return staged, nil
		}
	}
}

func (s *stagedFileWrite) reader() (io.Reader, error) {
	if s.file == nil {
		return bytes.NewReader(s.memory), nil
	}
	_, err := s.file.Seek(0, io.SeekStart)
	return s.file, err
}

func (s *stagedFileWrite) close() error {
	s.memory = nil
	if s.file == nil {
		return nil
	}
	file := s.file
	s.file = nil
	closeErr := file.Close()
	removeErr := os.Remove(file.Name())
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	return errors.Join(closeErr, removeErr)
}

func commitFileWrite(ctx context.Context, path string, input io.Reader) error {
	if err := ctx.Err(); err != nil {
		return toolError(err, core.OutcomeNotExecuted)
	}
	// Keep Core's existing in-place semantics: preserve the inode, symlinks,
	// hard links and existing mode rather than replacing the target by rename.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if err != nil {
		return toolError(err, core.OutcomeNotExecuted)
	}
	// All network input is already local. Once the target is opened, finish
	// and settle this bounded local write even if the caller disconnects.
	_, writeErr := copyContext(context.WithoutCancel(ctx), file, input)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return toolError(fmt.Errorf("file may be partially written: %w", err), core.OutcomeUnknown)
	}
	return nil
}
