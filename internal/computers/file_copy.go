package computers

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
)

// receiveCopiedFile commits the new directory entry only after file.read has
// finished successfully. Its final status can arrive after the last data byte.
func receiveCopiedFile(ctx context.Context, fs standardtools.FileSystem, name string, length *int64, input io.Reader) (_ FileInfo, err error) {
	if name == "" || length == nil || *length < 0 || input == nil {
		return FileInfo{}, domain.Invalid("file copy requires a target path, source size and stream")
	}
	path, err := fs.ResolvePath(ctx, name, false)
	if err != nil {
		return FileInfo{}, err
	}
	file, err := os.CreateTemp(path.Parent, ".astral-file-copy-*")
	if err != nil {
		return FileInfo{}, err
	}
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(file.Name())
		}
	}()
	size, err := copyContext(ctx, file, io.LimitReader(input, *length))
	if err != nil {
		return FileInfo{}, err
	}
	if size != *length {
		return FileInfo{}, io.ErrUnexpectedEOF
	}
	var extra [1]byte
	if _, err := io.ReadFull(input, extra[:]); err != io.EOF {
		if err == nil {
			err = domain.Conflict("source exceeded its declared size")
		}
		return FileInfo{}, err
	}
	existing, err := os.Lstat(path.Path)
	switch {
	case err == nil:
		if !existing.Mode().IsRegular() {
			return FileInfo{}, domain.Invalid("copy target must be a regular file")
		}
		if err := file.Chmod(existing.Mode().Perm()); err != nil {
			return FileInfo{}, err
		}
	case !errors.Is(err, os.ErrNotExist):
		return FileInfo{}, err
	}
	if err := file.Sync(); err != nil {
		return FileInfo{}, err
	}
	if err := file.Close(); err != nil {
		return FileInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return FileInfo{}, err
	}
	if err := os.Rename(file.Name(), path.Path); err != nil {
		return FileInfo{}, err
	}
	return FileInfo{Path: path.Path, Name: filepath.Base(path.Path), Size: size, MediaType: fileMediaType(path.Path)}, nil
}
