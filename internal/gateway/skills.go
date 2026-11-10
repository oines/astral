package gateway

import (
	"net/http"
	"os"
	"strconv"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/skills"
)

type ManagedSkillsView struct {
	Manifest skills.Manifest             `json:"manifest"`
	Local    computers.LocalSkillsStatus `json:"local"`
}

type SkillPublishInput struct {
	BaseRevision *int64 `json:"base_revision"`
	Digest       string `json:"digest"`
	DraftID      string `json:"draft_id"`
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
	register[SkillPublishInput, skills.Version](s, "POST", "/v1/skills/shared/{name}/publish", "agentPublishSharedSkill", s.publishSharedSkill)

	const archivePath = "/v1/skills/shared/{name}/revisions/{revision}/content"
	s.describeContent(httpcontract.Operation{Method: "GET", Path: archivePath, ID: "agentSharedSkillRevision", Tag: "Agent Gateway", CommandAuth: true, ContentType: "application/octet-stream"})
	s.mux.HandleFunc("GET "+archivePath, func(w http.ResponseWriter, r *http.Request) {
		service := s.Harness.Hub.Skills
		if service == nil {
			httpcontract.Failure(w, r, domain.Missing("capability", "skills"))
			return
		}
		revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
		if err != nil || revision < 1 {
			httpcontract.Failure(w, r, domain.Invalid("revision must be a positive integer"))
			return
		}
		version, body, err := service.Download(r.Context(), skills.Selector{Kind: skills.Shared, Name: r.PathValue("name"), Revision: revision})
		if err != nil {
			httpcontract.Failure(w, r, err)
			return
		}
		download := channels.Download{Body: body, Name: version.Name + "-r" + strconv.FormatInt(revision, 10) + ".tar"}
		if file, ok := body.(*os.File); ok {
			info, err := file.Stat()
			if err != nil {
				_ = body.Close()
				httpcontract.Failure(w, r, err)
				return
			}
			download.Size = info.Size()
		}
		serveContent(w, r, download)
	})
}

func (s *Service) publishSharedSkill(r *http.Request, input SkillPublishInput) (skills.Version, error) {
	hub, ctx, b := s.Harness.Hub, r.Context(), binding(r)
	service := hub.Skills
	if service == nil {
		return skills.Version{}, domain.Missing("capability", "skills")
	}
	if input.BaseRevision == nil || input.DraftID == "" {
		return skills.Version{}, domain.Invalid("base_revision and draft_id are required; read the local draft with GET /v1/skills")
	}
	request := skills.PublishRequest{
		Name: r.PathValue("name"), BaseRevision: *input.BaseRevision, Digest: input.Digest,
		DraftID: input.DraftID, ID: r.Header.Get("Idempotency-Key"),
	}
	version, found, err := service.Receipt(ctx, b, request)
	if err != nil {
		return skills.Version{}, err
	}
	if !found {
		data, err := hub.ExportSkill(ctx, b, request.Name, request.DraftID)
		if err != nil {
			return skills.Version{}, err
		}
		version, err = service.Publish(ctx, b, request, data)
		if err != nil {
			return skills.Version{}, err
		}
	}
	// The published revision is already durable. If this Computer disconnects
	// here, retrying the same intent can acknowledge the original draft again.
	_ = hub.AcknowledgeSkillPublish(ctx, b, request.Name, request.DraftID, version)
	hub.NotifySkills()
	return version, nil
}
