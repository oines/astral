package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/transport"
)

func (a *API) fileRoutes() {
	sessionParameter := httpcontract.Parameter{Name: "session_id", Required: true, Description: "Session whose Bot owns or is allowed to access this file."}
	route[httpcontract.Empty, domain.FileRef](a, "GET", "/files/{id}", "getFileReference", "Files", func(r *http.Request, _ httpcontract.Empty) (domain.FileRef, error) {
		session, err := a.fileBrowserSession(r, r.URL.Query().Get("session_id"))
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
		session, err := a.fileBrowserSession(r, r.URL.Query().Get("session_id"))
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
		b, err := a.fileBrowserBinding(r, session, r.PathValue("id"))
		if err != nil {
			return computers.DirectoryPage{}, err
		}
		return a.Config.Harness.Hub.ListDirectory(r.Context(), b, path, r.URL.Query().Get("cursor"), limit)
	}, sessionParameter,
		httpcontract.Parameter{Name: "path", Description: "Directory or file path on the selected Computer. Relative paths use that Computer's Bot home. An omitted path uses the Session CWD on its current Computer, otherwise the Bot home."},
		httpcontract.Parameter{Name: "cursor", Description: "Opaque continuation cursor from this directory's previous page."},
		httpcontract.Parameter{Name: "limit", Type: "integer", Description: "Page size, from 1 to 500; defaults to 200."})

	const content = "/api/v1/sessions/{id}/computers/{computer}/files/content"
	a.Contract.DescribeContent(httpcontract.Operation{
		Method: "GET", Path: content, ID: "sessionComputerFileContent", Tag: "Files", Auth: true, ContentType: "application/octet-stream",
		Description: "Read a regular file on a Computer allowed for this Session. Relative paths use the Bot home on that Computer.",
		Query:       []httpcontract.Parameter{{Name: "path", Required: true, Description: "Path on the source Computer."}, {Name: "download", Type: "boolean", Description: "Set to true or 1 to download the file."}},
	})
	a.mux.HandleFunc("GET "+content, a.computerFileContent)
}

func (a *API) fileBrowserSession(r *http.Request, id string) (domain.Session, error) {
	var session domain.Session
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

func (a *API) fileBrowserBinding(r *http.Request, session domain.Session, computerID string) (domain.ExecutionBinding, error) {
	// Browsing must still work if the task's CWD was moved or deleted. Resolve
	// explicit paths independently; the omitted-path default is handled above.
	return a.Config.Harness.Hub.Bind(r.Context(), session.AgentID, session.ID, computerID, "")
}

func (a *API) computerFileContent(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" || strings.ContainsRune(path, 0) {
		httpcontract.Failure(w, r, domain.Invalid("a source file path is required"))
		return
	}
	session, err := a.fileBrowserSession(r, r.PathValue("id"))
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	binding, err := a.fileBrowserBinding(r, session, r.PathValue("computer"))
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	a.streamComputerFile(w, r, computers.Call{Binding: binding, Path: path}, "")
}

func fileBrowserError(err error) error {
	var remote *transport.Error
	if errors.As(err, &remote) {
		status := map[string]int{"invalid": 400, "forbidden": 403, "not_found": 404, "file_unavailable": 404, "conflict": 409, "offline": 503}[remote.Code]
		if status != 0 {
			return &domain.Error{Status: status, Code: remote.Code, Message: remote.Message}
		}
	}
	return err
}
