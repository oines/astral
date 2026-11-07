package computers

import (
	"container/heap"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

const FileListCapability = "file.list"

const (
	DefaultDirectoryLimit = 200
	MaxDirectoryLimit     = 500
)

type DirectoryEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	IsDir      bool      `json:"is_dir"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

type DirectoryPage struct {
	ComputerID string           `json:"computer_id"`
	Path       string           `json:"path"`
	Parent     string           `json:"parent"`
	Entries    []DirectoryEntry `json:"entries"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// ListDirectory does not change the Session's selected Computer or CWD. Both
// dispatch and delivery use the same authorized, boot-pinned connection.
func (h *Hub) ListDirectory(ctx context.Context, b domain.ExecutionBinding, path, cursor string, limit int) (DirectoryPage, error) {
	if limit == 0 {
		limit = DefaultDirectoryLimit
	}
	if limit < 1 || limit > MaxDirectoryLimit {
		return DirectoryPage{}, domain.Invalid("directory limit must be between 1 and 500")
	}
	peer, err := h.Peer(ctx, b)
	if err != nil {
		return DirectoryPage{}, err
	}
	h.mu.RLock()
	active := h.live[b.ComputerID]
	supported := active != nil && active.peer == peer && slices.Contains(active.hello.Capabilities, FileListCapability)
	system := ""
	if active != nil {
		system = active.hello.OS
	}
	h.mu.RUnlock()
	if !supported {
		return DirectoryPage{}, &domain.Error{Status: http.StatusNotImplemented, Code: "unsupported", Message: "This Computer's daemon does not support directory browsing. Update its daemon to browse files."}
	}
	var page DirectoryPage
	if err = peer.JSON(ctx, FileListCapability, Call{Binding: b, Path: path, Cursor: cursor, Limit: limit}, &page); err != nil {
		return DirectoryPage{}, directoryCallError(err)
	}
	if err = h.Validate(ctx, b); err != nil {
		return DirectoryPage{}, err
	}
	if page.ComputerID != b.ComputerID || !absoluteTargetPath(system, page.Path) || len(page.Entries) > limit {
		return DirectoryPage{}, domain.Invalid("directory listing returned invalid source metadata")
	}
	return page, nil
}

func directoryCallError(err error) error {
	var remote *transport.Error
	if !errors.As(err, &remote) {
		return err
	}
	status := 0
	switch remote.Code {
	case "invalid":
		status = http.StatusBadRequest
	case "not_found":
		status = http.StatusNotFound
	case "forbidden":
		status = http.StatusForbidden
	case "offline":
		status = http.StatusServiceUnavailable
	case "unsupported":
		status = http.StatusNotImplemented
	}
	if status != 0 {
		return &domain.Error{Status: status, Code: remote.Code, Message: remote.Message}
	}
	return err
}

type directoryCursor struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
}

func directoryEntryCompare(a, b DirectoryEntry) int {
	if a.IsDir != b.IsDir {
		if a.IsDir {
			return -1
		}
		return 1
	}
	return strings.Compare(a.Name, b.Name)
}

// A max heap keeps only the next page plus one lookahead entry. A large
// directory is scanned in fixed chunks rather than materialized in memory.
type directoryEntries []DirectoryEntry

func (h directoryEntries) Len() int           { return len(h) }
func (h directoryEntries) Less(i, j int) bool { return directoryEntryCompare(h[i], h[j]) > 0 }
func (h directoryEntries) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *directoryEntries) Push(v any)        { *h = append(*h, v.(DirectoryEntry)) }
func (h *directoryEntries) Pop() any {
	last := len(*h) - 1
	v := (*h)[last]
	*h = (*h)[:last]
	return v
}

func listDirectory(ctx context.Context, fs standardtools.FileSystem, call Call) (DirectoryPage, error) {
	page := DirectoryPage{ComputerID: call.Binding.ComputerID, Entries: []DirectoryEntry{}}
	if strings.ContainsRune(call.Path, 0) || call.Limit < 0 || call.Limit > MaxDirectoryLimit {
		return page, domain.Invalid("provide a valid directory path and a limit between 1 and 500")
	}
	limit := call.Limit
	if limit == 0 {
		limit = DefaultDirectoryLimit
	}
	path, err := fs.ResolvePath(ctx, call.Path, true)
	if err != nil {
		return page, directoryFilesystemError(err, call.Path)
	}
	info, err := os.Stat(path.Path)
	if err != nil {
		return page, directoryFilesystemError(err, path.Path)
	}
	page.Path = path.Path
	if !info.IsDir() {
		page.Path = filepath.Dir(page.Path)
	}
	page.Parent = filepath.Dir(page.Path)
	if page.Parent == page.Path {
		page.Parent = ""
	}
	var after *DirectoryEntry
	if call.Cursor != "" {
		if len(call.Cursor) > 16<<10 {
			return page, domain.Invalid("directory cursor is invalid for this path")
		}
		var cursor directoryCursor
		raw, decodeErr := base64.RawURLEncoding.DecodeString(call.Cursor)
		if decodeErr != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Path != page.Path || cursor.Name == "" || cursor.Name == "." || cursor.Name == ".." || filepath.Base(cursor.Name) != cursor.Name {
			return page, domain.Invalid("directory cursor is invalid for this path")
		}
		after = &DirectoryEntry{Name: cursor.Name, IsDir: cursor.IsDir}
	}
	dir, err := os.Open(page.Path)
	if err != nil {
		return page, directoryFilesystemError(err, page.Path)
	}
	defer dir.Close()
	next := directoryEntries{}
	for {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return page, err
			}
			entryPath := filepath.Join(page.Path, entry.Name())
			info, statErr := entry.Info()
			if errors.Is(statErr, os.ErrNotExist) {
				continue // The live directory may change while it is being read.
			}
			if statErr != nil {
				return page, directoryFilesystemError(statErr, entryPath)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				if target, targetErr := os.Stat(entryPath); targetErr == nil {
					info = target
				}
			}
			item := DirectoryEntry{Name: entry.Name(), Path: entryPath, IsDir: info.IsDir(), Size: info.Size(), ModifiedAt: info.ModTime()}
			if after != nil && directoryEntryCompare(item, *after) <= 0 {
				continue
			}
			if next.Len() < limit+1 {
				heap.Push(&next, item)
			} else if directoryEntryCompare(item, next[0]) < 0 {
				next[0] = item
				heap.Fix(&next, 0)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return page, directoryFilesystemError(readErr, page.Path)
		}
	}
	slices.SortFunc(next, directoryEntryCompare)
	if len(next) > limit {
		next = next[:limit]
		last := next[len(next)-1]
		raw, _ := json.Marshal(directoryCursor{Path: page.Path, Name: last.Name, IsDir: last.IsDir})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	page.Entries = append(page.Entries, next...)
	return page, nil
}

func directoryFilesystemError(err error, path string) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return domain.Missing("Directory", path)
	case errors.Is(err, os.ErrPermission):
		return domain.Forbidden("The Computer's operating-system account cannot read this directory.")
	default:
		return err
	}
}
