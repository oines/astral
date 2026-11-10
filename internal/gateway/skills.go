package gateway

import (
	"net/http"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/skills"
)

type ManagedSkillsView struct {
	Manifest skills.Manifest             `json:"manifest"`
	Local    computers.LocalSkillsStatus `json:"local"`
}

func (s *Service) skillRoutes() {
	register[httpcontract.Empty, ManagedSkillsView](s, "GET", "/v1/skills", "agentManagedSkills", func(r *http.Request, _ httpcontract.Empty) (ManagedSkillsView, error) {
		var view ManagedSkillsView
		hub, ctx, b := s.Harness.Hub, r.Context(), binding(r)
		if hub.Skills == nil {
			return view, domain.Missing("capability", "skills")
		}
		var err error
		if view.Manifest, err = hub.Skills.Manifest(ctx); err != nil {
			return view, err
		}
		if view.Local, err = hub.SkillsStatus(ctx, b); err != nil {
			return view, err
		}
		return view, nil
	})
	register[httpcontract.Empty, skills.Version](s, "POST", "/v1/skills/shared/{name}/publish", "agentPublishSharedSkill", s.publishSharedSkill)
	register[httpcontract.Empty, skills.Version](s, "DELETE", "/v1/skills/shared/{name}", "agentDeleteSharedSkill", s.deleteSharedSkill)
}

func (s *Service) publishSharedSkill(r *http.Request, _ httpcontract.Empty) (skills.Version, error) {
	hub, ctx, b := s.Harness.Hub, r.Context(), binding(r)
	service := hub.Skills
	if service == nil {
		return skills.Version{}, domain.Missing("capability", "skills")
	}
	name, id := r.PathValue("name"), r.Header.Get("Idempotency-Key")
	version, err := service.Publish(ctx, b, name, id, func() ([]byte, int64, error) {
		return hub.ExportSkill(ctx, b, name)
	})
	if err != nil {
		return skills.Version{}, err
	}
	hub.NotifySkills()
	return version, nil
}

func (s *Service) deleteSharedSkill(r *http.Request, _ httpcontract.Empty) (skills.Version, error) {
	hub := s.Harness.Hub
	if hub.Skills == nil {
		return skills.Version{}, domain.Missing("capability", "skills")
	}
	version, err := hub.Skills.Delete(r.Context(), binding(r), r.PathValue("name"), r.Header.Get("Idempotency-Key"))
	if err != nil {
		return skills.Version{}, err
	}
	hub.NotifySkills()
	return version, nil
}
