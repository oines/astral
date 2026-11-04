package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/transport"
)

func (s *Service) computerFileRoutes() {
	const path = "/v1/computers/{id}/files/content"
	s.describeContent(httpcontract.Operation{
		Method: "GET", Path: path, ID: "agentComputerFileContent", Tag: "Agent Gateway", CommandAuth: true,
		ContentType: "application/octet-stream",
		Description: "Stream a regular file from an explicitly selected Computer currently allowed for this Bot. Paths use the target Computer's OS; relative paths use its AgentHome. No directory allowlist, file-size cap or total transfer timeout is imposed. Does not switch Computers, create a file reference, snapshot the file or save a Server copy. Byte ranges seek on the source Computer.",
		Query:       []httpcontract.Parameter{{Name: "path", Required: true, Description: "Source file path on the selected Computer, not the calling Computer."}},
	})
	s.mux.HandleFunc("GET "+path, s.computerFileContent)
}

func (s *Service) computerFileContent(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 || len(query["path"]) != 1 || query.Get("path") == "" || strings.ContainsRune(query.Get("path"), 0) {
		httpcontract.Failure(w, r, domain.Invalid("provide exactly one source path query parameter"))
		return
	}
	source := binding(r)
	target, err := s.Harness.Hub.Bind(r.Context(), source.AgentID, source.SessionID, r.PathValue("id"), "")
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	call := computers.Call{Binding: target, Path: query.Get("path")}
	var selected computers.FileInfo
	var offset, length int64
	var partial bool
	if strings.TrimSpace(r.Header.Get("Range")) != "" {
		if err := s.Harness.Hub.JSON(r.Context(), "file.stat", call, &selected); err != nil {
			httpcontract.Failure(w, r, computerFileError(err))
			return
		}
		offset, length, partial, err = contentRange(r.Header.Get("Range"), selected.Size)
		if err != nil {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", selected.Size))
			httpcontract.JSON(w, http.StatusRequestedRangeNotSatisfiable, httpcontract.Error{Code: "invalid_range", Message: err.Error()})
			return
		}
		call.Path, call.Offset, call.Length = selected.Path, offset, &length
	}
	metadata, stream, err := s.Harness.Hub.Call(r.Context(), "file.read", call, nil)
	if err != nil {
		httpcontract.Failure(w, r, computerFileError(err))
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = stream.Close()
		}
	}()
	var info computers.FileInfo
	if err := json.Unmarshal(metadata, &info); err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	if partial && (info.Size != selected.Size || !info.ModTime.Equal(selected.ModTime)) {
		httpcontract.Failure(w, r, domain.Conflict("source file changed while selecting its byte range"))
		return
	}
	if !partial {
		length = info.Size
	}
	contentHeaders(w.Header(), info.Name, info.MediaType)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.Header().Set("Last-Modified", info.ModTime.UTC().Format(http.TimeFormat))
	status := http.StatusOK
	if partial {
		status = http.StatusPartialContent
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, info.Size))
	}
	// The RPC already seeks and bounds a range at its source. Keep reading its
	// final status, including a source mutation or disconnect after the bytes.
	// No whole-file buffer, size limit, local copy or transfer deadline is used.
	w.WriteHeader(status)
	w.(*responseWriter).stream = newContentBody(r.Context(), stream)
	keep = true
}

func computerFileError(err error) error {
	var remote *transport.Error
	if errors.As(err, &remote) {
		switch remote.Code {
		case "invalid":
			return domain.Invalid(remote.Message)
		case "conflict":
			return domain.Conflict(remote.Message)
		}
	}
	return err
}
