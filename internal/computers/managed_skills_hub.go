//go:build !daemon

package computers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/skills"
	"github.com/oines/astral/internal/transport"
)

// NotifySkills queues a hint for each participating Daemon after a change.
// Each connection coalesces hints and also catches up by fetching its manifest.
func (h *Hub) NotifySkills() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, active := range h.live {
		signalSkillsChanged(active.skillChanges)
	}
}

func (c *connection) sendSkillNotifications() {
	for {
		select {
		case <-c.peer.Done():
			return
		case <-c.skillChanges:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = c.peer.JSON(ctx, skillChangedMethod, struct{}{}, nil)
			cancel()
		}
	}
}

func (h *Hub) serveSkillSync(ctx context.Context, id string, active *connection, request transport.Request, out *transport.Responder) error {
	if h.Skills == nil || active.skillChanges == nil {
		return domain.Missing("daemon method", request.Method)
	}
	h.mu.RLock()
	current := h.live[id] == active
	h.mu.RUnlock()
	if !current {
		return domain.Offline("the original computer connection is unavailable")
	}
	if request.Method == skillManifestMethod {
		manifest, err := h.Skills.Manifest(ctx)
		if err != nil {
			return err
		}
		return out.JSON(manifest)
	}
	var selector skills.Selector
	if err := json.Unmarshal(request.Params, &selector); err != nil {
		return domain.Invalid("invalid skill download selector")
	}
	version, archive, err := h.Skills.Download(ctx, selector)
	if err != nil {
		return err
	}
	defer archive.Close()
	if err := out.Headers(version); err != nil {
		return err
	}
	_, err = io.Copy(out, archive)
	return err
}

func (h *Hub) skillPeer(ctx context.Context, binding domain.ExecutionBinding) (*transport.Peer, error) {
	peer, err := h.Peer(ctx, binding)
	if err != nil {
		return nil, err
	}
	h.mu.RLock()
	active := h.live[binding.ComputerID]
	current := active != nil && active.peer == peer
	supported := current && slices.Contains(active.hello.Capabilities, ManagedSkillsCapability)
	h.mu.RUnlock()
	if !current {
		return nil, domain.Offline("the original computer connection is unavailable")
	}
	if !supported {
		return nil, &domain.Error{Status: http.StatusNotImplemented, Code: "unsupported", Message: "Update this Computer's Daemon to use managed Skills."}
	}
	return peer, nil
}

func (h *Hub) validateSkillPeer(ctx context.Context, binding domain.ExecutionBinding, original *transport.Peer) error {
	peer, err := h.skillPeer(ctx, binding)
	if err != nil {
		return err
	}
	if peer != original {
		return domain.Offline("the original computer connection is unavailable")
	}
	return nil
}

func skillCallError(err error) error {
	var remote *transport.Error
	if errors.As(err, &remote) && remote.Code == "conflict" {
		return domain.Conflict(remote.Message)
	}
	return directoryCallError(err)
}

func (h *Hub) SkillsStatus(ctx context.Context, binding domain.ExecutionBinding) (LocalSkillsStatus, error) {
	peer, err := h.skillPeer(ctx, binding)
	if err != nil {
		return LocalSkillsStatus{}, err
	}
	var status LocalSkillsStatus
	if err = peer.JSON(ctx, skillStatusMethod, skillCall{Binding: binding}, &status); err != nil {
		return LocalSkillsStatus{}, skillCallError(err)
	}
	return status, h.validateSkillPeer(ctx, binding, peer)
}

func (h *Hub) ExportSkill(ctx context.Context, binding domain.ExecutionBinding, name string) ([]byte, int64, error) {
	peer, err := h.skillPeer(ctx, binding)
	if err != nil {
		return nil, 0, err
	}
	meta, body, err := peer.Call(ctx, skillExportMethod, skillCall{Binding: binding, Name: name}, nil)
	if err != nil {
		return nil, 0, skillCallError(err)
	}
	defer body.Close()
	var revision *int64
	if err := json.Unmarshal(meta, &revision); err != nil || revision == nil || *revision < 0 {
		return nil, 0, &domain.Error{Status: http.StatusNotImplemented, Code: "unsupported", Message: "Update this Computer's Daemon to publish Shared Skills."}
	}
	archive, err := readSkillArchive(body)
	if err != nil {
		return nil, 0, skillCallError(err)
	}
	return archive, *revision, h.validateSkillPeer(ctx, binding, peer)
}
