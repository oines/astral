package computers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	skillSyncInterval       = time.Minute
	skillTransferTimeout    = 2 * time.Minute
)

type skillCall struct {
	Binding domain.ExecutionBinding `json:"binding"`
	Name    string                  `json:"name,omitempty"`
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
	for _, version := range manifest.Removed {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.applyShared(ctx, version, nil); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", version.Name, err))
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
		archive, revision, err := d.skills.export(ctx, call.Name)
		if err != nil {
			return err
		}
		if err := out.Headers(revision); err != nil {
			return err
		}
		_, err = out.Write(archive)
		return err
	}
	return domain.Missing("daemon method", request.Method)
}
