package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/transport"
)

func (a *API) fileRoutes() {
	sessionParameter := httpcontract.Parameter{Name: "session_id", Required: true, Description: "Session whose Bot owns or is allowed to access this file."}
	route[httpcontract.Empty, domain.FileRef](a, "GET", "/files/{id}", "getFileReference", "Files", func(r *http.Request, _ httpcontract.Empty) (domain.FileRef, error) {
		session, err := a.fileBrowserSession(r)
		if err != nil {
			return domain.FileRef{}, err
		}
		var ref domain.FileRef
		if err = a.Config.Harness.Store.Get(r.Context(), "files", r.PathValue("id"), &ref); err != nil {
			return ref, err
		}
		if ref.AgentID != session.AgentID {
			return domain.FileRef{}, domain.Forbidden("file reference belongs to another Bot")
		}
		// Metadata belongs to history. Reading it does not contact the source
		// Computer or require that a historical Computer is still available.
		return ref, nil
	}, sessionParameter)

	route[httpcontract.Empty, computers.DirectoryPage](a, "GET", "/computers/{id}/files/directory", "listComputerDirectory", "Files", func(r *http.Request, _ httpcontract.Empty) (computers.DirectoryPage, error) {
		session, err := a.fileBrowserSession(r)
		if err != nil {
			return computers.DirectoryPage{}, err
		}
		limit := computers.DefaultDirectoryLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > computers.MaxDirectoryLimit {
				return computers.DirectoryPage{}, domain.Invalid("directory limit must be between 1 and 500")
			}
		}
		path := r.URL.Query().Get("path")
		if path == "" && session.ComputerID == r.PathValue("id") {
			path = session.CWD
		}
		if strings.ContainsRune(path, 0) {
			return computers.DirectoryPage{}, domain.Invalid("directory path is invalid")
		}
		b, err := a.fileBrowserBinding(r, session)
		if err != nil {
			return computers.DirectoryPage{}, err
		}
		return a.Config.Harness.Hub.ListDirectory(r.Context(), b, path, r.URL.Query().Get("cursor"), limit)
	}, sessionParameter,
		httpcontract.Parameter{Name: "path", Description: "Directory or file path on the selected Computer. Relative paths use that Computer's Bot home. An omitted path uses the Session CWD on its current Computer, otherwise the Bot home."},
		httpcontract.Parameter{Name: "cursor", Description: "Opaque continuation cursor from this directory's previous page."},
		httpcontract.Parameter{Name: "limit", Type: "integer", Description: "Page size, from 1 to 500; defaults to 200."})

	route[httpcontract.Empty, gateway.ReferenceResponse](a, "POST", "/computers/{id}/files/reference", "referenceComputerFile", "Files", func(r *http.Request, _ httpcontract.Empty) (gateway.ReferenceResponse, error) {
		session, err := a.fileBrowserSession(r)
		if err != nil {
			return gateway.ReferenceResponse{}, err
		}
		path := r.URL.Query().Get("path")
		if path == "" || strings.ContainsRune(path, 0) {
			return gateway.ReferenceResponse{}, domain.Invalid("a source file path is required")
		}
		b, err := a.fileBrowserBinding(r, session)
		if err != nil {
			return gateway.ReferenceResponse{}, err
		}
		ref, err := a.Config.Harness.Files.Reference(r.Context(), b, path, false)
		if err != nil {
			return gateway.ReferenceResponse{}, fileBrowserError(err)
		}
		return gateway.ReferenceResponse{File: ref, URI: filestream.URL(ref)}, nil
	}, sessionParameter, httpcontract.Parameter{Name: "path", Required: true, Description: "Ordinary file path on the selected Computer. Creates a live reference, without a snapshot or Server copy."})
}

func (a *API) fileBrowserSession(r *http.Request) (domain.Session, error) {
	var session domain.Session
	id := r.URL.Query().Get("session_id")
	if id == "" {
		return session, domain.Invalid("Session is required for Computer file access")
	}
	if err := a.Config.Harness.Store.Get(r.Context(), "sessions", id, &session); err != nil {
		return session, err
	}
	if session.Deletion != nil {
		return domain.Session{}, domain.Missing("Session", id)
	}
	return session, nil
}

func (a *API) fileBrowserBinding(r *http.Request, session domain.Session) (domain.ExecutionBinding, error) {
	// Browsing must still work if the task's CWD was moved or deleted. Resolve
	// explicit paths independently; the omitted-path default is handled above.
	return a.Config.Harness.Hub.Bind(r.Context(), session.AgentID, session.ID, r.PathValue("id"), "")
}

func fileBrowserError(err error) error {
	var remote *transport.Error
	if errors.As(err, &remote) {
		status := map[string]int{"invalid": 400, "forbidden": 403, "not_found": 404, "conflict": 409, "offline": 503}[remote.Code]
		if status != 0 {
			return &domain.Error{Status: status, Code: remote.Code, Message: remote.Message}
		}
	}
	return err
}
