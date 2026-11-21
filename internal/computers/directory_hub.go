//go:build !daemon

package computers

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

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
