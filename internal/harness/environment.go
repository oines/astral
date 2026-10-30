package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

const environmentKind = "astral.environment"
const environmentSnapshotHeading = "Current Astral environment snapshot:"
const modelContextKind = "astral.model"

type environmentMetadata struct {
	Version      int                          `json:"version"`
	Fingerprint  string                       `json:"fingerprint"`
	CompactionID string                       `json:"compaction_id,omitempty"`
	Model        modelbridge.ModelDescription `json:"model"`
	ComputerID   string                       `json:"computer_id,omitempty"`
	BootID       string                       `json:"boot_id,omitempty"`
	SkillHashes  map[string]string            `json:"skill_hashes,omitempty"`
}

type modelContextMetadata struct {
	Version int                          `json:"version"`
	Model   modelbridge.ModelDescription `json:"model"`
}

type inspectContextKey struct{}

// InitializeSession is an explicit creation/binding operation. Reopening or
// inspecting history does not call it.
func (s *Service) InitializeSession(ctx context.Context, id string) error {
	r, err := s.open(ctx, id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var record domain.Session
	if err = s.Store.Get(ctx, "sessions", id, &record); err != nil {
		return err
	}
	if record.ComputerID != "" && record.CWD == "" {
		var computer domain.Computer
		if err = s.Store.Get(ctx, "computers", record.ComputerID, &computer); err == nil && s.Hub.Status(computer).Online && !computer.Revoked {
			if _, err = r.currentBinding(ctx); err != nil {
				return err
			}
		}
	}
	return r.refreshEnvironment(ctx)
}

// collectContext returns passive changes at Core's request boundary. Model-only
// changes do not replace the Computer environment. After compaction, the full
// snapshot carries the effective model even if a prior notice was summarized.
func (r *runtime) collectContext(ctx context.Context, boundary core.ContextBoundary) ([]core.Message, error) {
	r.environmentMu.Lock()
	defer r.environmentMu.Unlock()
	snapshot, err := r.collectEnvironment(ctx)
	if err != nil {
		return nil, err
	}
	model, err := r.service.Models.DescribeModel(ctx, boundary.Context.Request.ModelID)
	if err != nil {
		return nil, err
	}
	latest, previousModel := environmentState(boundary.Context.Request.Messages)
	metadata := environmentMetadata{Version: 3, Fingerprint: digest(encode(snapshot)), CompactionID: boundary.CompactionID, Model: model, SkillHashes: map[string]string{}}
	if computer, ok := snapshot["current_computer"].(map[string]any); ok {
		metadata.ComputerID, _ = computer["id"].(string)
		metadata.BootID, _ = computer["boot_id"].(string)
	}
	for _, skill := range snapshot["discovery"].(computers.EnvironmentDiscovery).Skills {
		metadata.SkillHashes[skill.Path] = skill.SHA256
	}
	if metadata.CompactionID == "" {
		metadata.CompactionID = latest.CompactionID
	}
	if latest.Version == metadata.Version && latest.Fingerprint == metadata.Fingerprint && latest.CompactionID == metadata.CompactionID {
		return modelChange(previousModel, model), nil
	}
	visible := modelEnvironment(snapshot, latest, metadata)
	visible["current_model"] = model
	message := core.Message{Role: core.RoleUser, ContextKind: environmentKind, Metadata: encode(metadata), Content: core.Text(environmentSnapshotHeading + "\n" + string(encode(visible)))}
	return []core.Message{message}, nil
}

func environmentState(messages []core.Message) (environmentMetadata, modelbridge.ModelDescription) {
	var environment environmentMetadata
	var model modelbridge.ModelDescription
	for _, message := range messages {
		switch message.ContextKind {
		case environmentKind:
			var candidate environmentMetadata
			if json.Unmarshal(message.Metadata, &candidate) == nil && candidate.Version >= 1 && candidate.Version <= 3 && candidate.Fingerprint != "" {
				environment, model = candidate, candidate.Model
			}
		case modelContextKind:
			var candidate modelContextMetadata
			if json.Unmarshal(message.Metadata, &candidate) == nil && candidate.Version == 1 {
				model = candidate.Model
			}
		}
	}
	return environment, model
}

// The scanner and execution binding retain their full internal data. Only this
// projection becomes model text; fingerprints are computed before projecting.
func modelEnvironment(snapshot map[string]any, previous, current environmentMetadata) map[string]any {
	visible := maps.Clone(snapshot)
	delete(visible, "search_provider_id")
	visible["web_search"] = map[string]bool{"configured": snapshot["search_provider_id"] != ""}
	if computer, ok := snapshot["current_computer"].(map[string]any); ok {
		computer = maps.Clone(computer)
		delete(computer, "boot_id")
		visible["current_computer"] = computer
	}
	allowed := []map[string]any{}
	for _, computer := range snapshot["allowed_computers"].([]map[string]any) {
		computer = maps.Clone(computer)
		delete(computer, "kind")
		delete(computer, "arch")
		allowed = append(allowed, computer)
	}
	visible["allowed_computers"] = allowed
	discovery := snapshot["discovery"].(computers.EnvironmentDiscovery)
	view := map[string]any{"status": discovery.Status}
	if discovery.Status == "ok" || discovery.Status == "partial" {
		skills := []map[string]string{}
		for _, skill := range discovery.Skills {
			skills = append(skills, map[string]string{"name": skill.Name, "description": skill.Description, "path": skill.Path, "scope": skill.Scope})
		}
		view["skills"] = skills
		guidance := map[string]string{"path": discovery.AgentsMD.Path}
		if discovery.AgentsMD.Status == "ok" {
			guidance["content"] = discovery.AgentsMD.Content
		} else {
			guidance["status"] = discovery.AgentsMD.Status
		}
		view["agents_md"] = guidance
		roots := []computers.DiscoveryRoot{}
		for _, root := range discovery.SkillRoots {
			if root.Status != "ok" {
				roots = append(roots, root)
			}
		}
		if len(roots) > 0 {
			view["skill_roots"] = roots
		}
	}
	if len(discovery.Issues) > 0 {
		issues := []map[string]string{}
		for _, issue := range discovery.Issues {
			value := map[string]string{"message": issue.Message}
			if issue.Path != "" {
				value["path"] = issue.Path
			}
			issues = append(issues, value)
		}
		view["issues"] = issues
	}
	visible["discovery"] = view
	var updates []string
	if current.ComputerID != "" && previous.ComputerID == current.ComputerID {
		if previous.BootID != "" && current.BootID != "" && previous.BootID != current.BootID {
			updates = append(updates, "The selected Computer restarted; earlier process handles may no longer be available.")
		}
		// Preserve a useful signal when only the Skill body changed. Hash values
		// are durable Host metadata, not something the model needs to compare.
		for _, skill := range discovery.Skills {
			if hash := previous.SkillHashes[skill.Path]; hash != "" && hash != skill.SHA256 {
				updates = append(updates, "Skill content changed; read this SKILL.md again before using it: "+skill.Path)
			}
		}
	}
	if len(updates) > 0 {
		visible["updates"] = updates
	}
	return visible
}

func modelChange(previous, current modelbridge.ModelDescription) []core.Message {
	if previous == current {
		return nil
	}
	notice := "Host model configuration updated."
	if previous.ID == "" {
		notice = "Host current model information."
	} else if previous.ID != current.ID || previous.Name != current.Name {
		notice = fmt.Sprintf("Host model switched from %s (%s) to %s (%s).", previous.Name, previous.ID, current.Name, current.ID)
	}
	return []core.Message{{Role: core.RoleUser, ContextKind: modelContextKind,
		Metadata: encode(modelContextMetadata{Version: 1, Model: current}),
		Content:  core.Text(notice + "\nCurrent model (context_window is the token limit; vision describes image input):\n" + string(encode(current)))}}
}

// Idle selections are persisted with InjectContext without waking the model.
// During a run, CollectContext inserts the same notice at the first request
// boundary using that request's model. A separate queued notice could race the
// boundary and duplicate, or describe a selection already superseded there.

func (r *runtime) refreshEnvironment(ctx context.Context) error {
	view, err := r.core.Load().InspectContext(context.WithValue(ctx, inspectContextKey{}, true))
	if err != nil {
		return err
	}
	batch, err := r.collectContext(ctx, core.ContextBoundary{SessionID: r.id, Reason: "environment_changed", Context: view})
	if err != nil || len(batch) == 0 {
		return err
	}
	_, err = r.core.Load().InjectContext(ctx, core.Input{ID: domain.ID("context"), Contexts: batch})
	return err
}

func (r *runtime) collectEnvironment(ctx context.Context) (map[string]any, error) {
	s := r.service
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", r.id, &record); err != nil {
		return nil, err
	}
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", record.AgentID, &agent); err != nil {
		return nil, err
	}
	allowed := []map[string]any{}
	var currentComputer map[string]any
	var localAPI map[string]string
	discovery := computers.UnavailableDiscovery("not_selected", "Select a Computer to discover its skills and workspace guidance.")
	for _, id := range agent.AllowedComputers {
		var computer domain.Computer
		if err := s.Store.Get(ctx, "computers", id, &computer); err != nil {
			allowed = append(allowed, map[string]any{"id": id, "status": "unavailable"})
			continue
		}
		status := s.Hub.Status(computer)
		value := map[string]any{"id": id, "name": computer.Name, "kind": computer.Kind, "os": status.OS, "arch": status.Arch, "online": status.Online, "revoked": computer.Revoked}
		allowed = append(allowed, value)
		if id == record.ComputerID {
			currentComputer = map[string]any{"id": id, "name": computer.Name, "kind": computer.Kind, "os": status.OS, "arch": status.Arch, "online": status.Online, "revoked": computer.Revoked, "cwd": record.CWD, "shell": status.Shell}
			discovery = computers.UnavailableDiscovery("unavailable", "The selected Computer is offline or access was revoked.")
			if status.Online && !computer.Revoked {
				scanCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				binding, bindErr := s.Hub.Bind(scanCtx, r.agentID, r.id, record.ComputerID, record.CWD)
				if bindErr == nil {
					currentComputer["cwd"], currentComputer["agent_home"], currentComputer["shell"], currentComputer["boot_id"] = binding.CWD, binding.AgentHome, binding.Shell, binding.BootID
					localAPI = map[string]string{"base_url": binding.APIURL, "capabilities_url": binding.APIURL + "/v1/capabilities"}
					discovery, bindErr = s.Hub.Discover(scanCtx, binding)
				}
				cancel()
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if bindErr != nil {
					discovery = computers.UnavailableDiscovery("unavailable", bindErr.Error())
				}
			}
		}
	}
	if record.ComputerID != "" && currentComputer == nil {
		currentComputer = map[string]any{"id": record.ComputerID, "cwd": record.CWD, "status": "unavailable_or_access_revoked"}
		discovery = computers.UnavailableDiscovery("unavailable", "The selected Computer is unavailable or no longer authorized.")
	}
	sort.Slice(allowed, func(i, j int) bool { return allowed[i]["id"].(string) < allowed[j]["id"].(string) })
	zone, err := s.Timezone.View(ctx)
	if err != nil {
		return nil, err
	}
	info := map[string]any{"current_computer": currentComputer, "allowed_computers": allowed, "search_provider_id": agent.SearchProviderID,
		"discovery": discovery,
		"local_api": localAPI, "timezone": zone.Name}
	return info, nil
}

func (r *runtime) rememberBinding(ctx context.Context, binding domain.ExecutionBinding) error {
	if err := r.service.Store.Update(ctx, func(tx *persistence.Tx) error {
		var record domain.Session
		if err := tx.Get(ctx, "sessions", r.id, &record); err != nil {
			return err
		}
		if record.ComputerID != binding.ComputerID || record.CWD != "" && record.CWD != binding.CWD {
			return domain.Conflict("Session environment changed while binding its Computer")
		}
		if record.CWD != "" {
			return nil
		}
		record.CWD = binding.CWD
		return tx.Save(ctx, "sessions", &record.Meta, &record)
	}); err != nil {
		return err
	}
	return nil
}
