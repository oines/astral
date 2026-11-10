package computers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/skills"
	"github.com/oines/astral/internal/transport"
)

const (
	ManagedSkillsCapability = "skills.v1"
	skillManifestMethod     = "skills.manifest"
	skillDownloadMethod     = "skills.download"
	skillChangedMethod      = "skills.changed"
	skillStatusMethod       = "skills.status"
	skillExportMethod       = "skills.export"
	skillAcknowledgeMethod  = "skills.acknowledge"
	skillSyncInterval       = time.Minute
	skillTransferTimeout    = 2 * time.Minute
)

type skillCall struct {
	Binding domain.ExecutionBinding `json:"binding"`
	Name    string                  `json:"name,omitempty"`
	DraftID string                  `json:"draft_id,omitempty"`
	Version skills.Version          `json:"version,omitempty"`
}

func readSkillArchive(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, int64(skills.MaxArchiveBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > skills.MaxArchiveBytes {
		return nil, domain.Invalid("skill archive exceeds its size limit")
	}
	return data, nil
}

func signalSkillsChanged(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// NotifySkills queues a hint for each participating Daemon after a publication.
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

func (m *managedSkills) syncLoop(ctx context.Context, generation *daemonConnection) {
	poll := time.NewTicker(skillSyncInterval)
	defer poll.Stop()
	for ctx.Err() == nil {
		m.setSyncError(m.sync(ctx, generation.peer))
		select {
		case <-ctx.Done():
			return
		case <-generation.peer.Done():
			return
		case <-generation.skillChanges:
		case <-poll.C:
		}
	}
}

func (m *managedSkills) sync(ctx context.Context, peer *transport.Peer) error {
	if _, err := m.snapshot(); err != nil {
		return err
	}
	manifestCtx, cancel := context.WithTimeout(ctx, skillTransferTimeout)
	var manifest skills.Manifest
	err := peer.JSON(manifestCtx, skillManifestMethod, struct{}{}, &manifest)
	cancel()
	if err != nil {
		return err
	}
	if err = m.directory(m.sharedRoot(), true); err != nil {
		return err
	}
	var errs []error
	if err = m.applyBuiltin(ctx, manifest.Builtin, func() ([]byte, error) {
		return downloadManagedSkill(ctx, peer, skills.Builtin, manifest.Builtin)
	}); err != nil {
		errs = append(errs, fmt.Errorf("sync built-in skills: %w", err))
	}
	for _, version := range manifest.Shared {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err = m.applyShared(ctx, version, func() ([]byte, error) {
			return downloadManagedSkill(ctx, peer, skills.Shared, version)
		}); err != nil {
			errs = append(errs, fmt.Errorf("sync %s: %w", version.Name, err))
		}
	}
	return errors.Join(errs...)
}

func downloadManagedSkill(ctx context.Context, peer *transport.Peer, kind string, expected skills.Version) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, skillTransferTimeout)
	defer cancel()
	meta, body, err := peer.Call(ctx, skillDownloadMethod, skills.Selector{Kind: kind, Name: expected.Name, Revision: expected.Revision, Digest: expected.Digest}, nil)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var version skills.Version
	if err := json.Unmarshal(meta, &version); err != nil || version != expected {
		return nil, domain.Invalid("skill download returned a different version than requested")
	}
	return readSkillArchive(body)
}

func (d *Daemon) handleManagedSkills(ctx context.Context, request transport.Request, out *transport.Responder) error {
	if d.skills == nil {
		return domain.Missing("daemon method", request.Method)
	}
	var call skillCall
	if err := json.Unmarshal(request.Params, &call); err != nil {
		return domain.Invalid("invalid managed skills request")
	}
	if err := d.binding(call.Binding); err != nil {
		return err
	}
	d.mu.Lock()
	gateway := d.gateways[call.Binding.GrantID]
	d.mu.Unlock()
	if gateway == nil || gateway.binding != call.Binding {
		return domain.Forbidden("execution environment was not prepared")
	}
	switch request.Method {
	case skillStatusMethod:
		status, err := d.skills.status(ctx)
		if err != nil {
			return err
		}
		return out.JSON(status)
	case skillExportMethod:
		archive, err := d.skills.export(ctx, call.Name, call.DraftID)
		if err != nil {
			return err
		}
		_, err = out.Write(archive)
		return err
	case skillAcknowledgeMethod:
		if err := d.skills.acknowledge(ctx, call.Name, call.DraftID, call.Version); err != nil {
			return err
		}
		return out.JSON(struct{}{})
	}
	return domain.Missing("daemon method", request.Method)
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

func (h *Hub) ExportSkill(ctx context.Context, binding domain.ExecutionBinding, name, draftID string) ([]byte, error) {
	peer, err := h.skillPeer(ctx, binding)
	if err != nil {
		return nil, err
	}
	_, body, err := peer.Call(ctx, skillExportMethod, skillCall{Binding: binding, Name: name, DraftID: draftID}, nil)
	if err != nil {
		return nil, skillCallError(err)
	}
	defer body.Close()
	archive, err := readSkillArchive(body)
	if err != nil {
		return nil, skillCallError(err)
	}
	return archive, h.validateSkillPeer(ctx, binding, peer)
}

func (h *Hub) AcknowledgeSkillPublish(ctx context.Context, binding domain.ExecutionBinding, name, draftID string, version skills.Version) error {
	peer, err := h.skillPeer(ctx, binding)
	if err != nil {
		return err
	}
	if err = peer.JSON(ctx, skillAcknowledgeMethod, skillCall{Binding: binding, Name: name, DraftID: draftID, Version: version}, nil); err != nil {
		return skillCallError(err)
	}
	return h.validateSkillPeer(ctx, binding, peer)
}
