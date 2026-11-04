package gateway

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/httpcontract"
)

// describeContent adds the stream-specific HTTP contract without widening the
// general JSON operation or forwarding arbitrary caller headers.
func (s *Service) describeContent(operation httpcontract.Operation) {
	s.Contract.Describe(operation)
	view := s.Contract.Paths[operation.Path].(map[string]any)["get"].(map[string]any)
	parameters, _ := view["parameters"].([]any)
	view["parameters"] = append(parameters, map[string]any{
		"name": "Range", "in": "header", "required": false,
		"description": "One bytes range (start-end, start-, or -suffix). Sources with unknown size return the complete content with HTTP 200.",
		"schema":      map[string]any{"type": "string"},
	})
	responses := view["responses"].(map[string]any)
	responses["206"] = map[string]any{
		"description": "Partial Content",
		"content":     map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}},
		"headers": map[string]any{
			"Accept-Ranges":  map[string]any{"schema": map[string]any{"type": "string", "const": "bytes"}},
			"Content-Range":  map[string]any{"schema": map[string]any{"type": "string"}},
			"Content-Length": map[string]any{"schema": map[string]any{"type": "integer", "minimum": 0}},
		},
	}
	responses["416"] = map[string]any{
		"description": "Range Not Satisfiable",
		"content":     responses["default"].(map[string]any)["content"],
		"headers":     map[string]any{"Content-Range": map[string]any{"schema": map[string]any{"type": "string"}}},
	}
}

// serveContent keeps the platform body streaming. A range skips its prefix on
// that same authorized source; it does not create a cached Computer file.
func serveContent(w http.ResponseWriter, r *http.Request, download channels.Download) {
	if download.Body == nil {
		httpcontract.Failure(w, r, fmt.Errorf("attachment content is unavailable"))
		return
	}
	body := newContentBody(r.Context(), download.Body)
	keep := false
	defer func() {
		if !keep {
			_ = body.Close()
		}
	}()
	status := http.StatusOK
	if download.Size > 0 {
		offset, length, partial, err := contentRange(r.Header.Get("Range"), download.Size)
		w.Header().Set("Accept-Ranges", "bytes")
		if err != nil {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", download.Size))
			httpcontract.JSON(w, http.StatusRequestedRangeNotSatisfiable, httpcontract.Error{Code: "invalid_range", Message: err.Error()})
			return
		}
		if offset > 0 {
			if _, err := io.CopyN(io.Discard, download.Body, offset); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				httpcontract.Failure(w, r, fmt.Errorf("read attachment range prefix: %w", err))
				return
			}
		}
		if partial && offset+length < download.Size {
			body.Reader = io.LimitReader(download.Body, length)
		}
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		if partial {
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+length-1, download.Size))
		}
	}
	contentHeaders(w.Header(), download.Name, download.MediaType)
	w.WriteHeader(status)
	w.(*responseWriter).stream = body
	keep = true
}

func contentHeaders(header http.Header, name, mediaType string) {
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	header.Set("Content-Type", mediaType)
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	header.Set("Cache-Control", "private, no-store")
	header.Set("X-Content-Type-Options", "nosniff")
}

type contentBody struct {
	io.Reader
	source   io.ReadCloser
	stop     func() bool
	once     sync.Once
	closeErr error
}

func newContentBody(ctx context.Context, source io.ReadCloser) *contentBody {
	body := &contentBody{Reader: source, source: source}
	body.stop = context.AfterFunc(ctx, body.closeSource)
	return body
}

func (b *contentBody) Close() error {
	if b.stop != nil {
		b.stop()
	}
	b.closeSource()
	return b.closeErr
}

func (b *contentBody) closeSource() {
	b.once.Do(func() {
		b.closeErr = b.source.Close()
	})
}

func contentRange(raw string, size int64) (offset, length int64, partial bool, err error) {
	length = size
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	invalid := func() (int64, int64, bool, error) {
		return 0, 0, true, fmt.Errorf("provide one satisfiable bytes range")
	}
	if !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		return invalid()
	}
	start, end, ok := strings.Cut(strings.TrimPrefix(raw, "bytes="), "-")
	if !ok {
		return invalid()
	}
	parse := func(value string) (int64, error) {
		if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return 0, fmt.Errorf("invalid byte offset")
		}
		return strconv.ParseInt(value, 10, 64)
	}
	if start == "" {
		suffix, parseErr := parse(end)
		if parseErr != nil || suffix <= 0 || size <= 0 {
			return invalid()
		}
		length = min(size, suffix)
		return size - length, length, true, nil
	}
	offset, err = parse(start)
	if err != nil || offset >= size {
		return invalid()
	}
	last := size - 1
	if end != "" {
		last, err = parse(end)
		if err != nil || last < offset {
			return invalid()
		}
		last = min(last, size-1)
	}
	return offset, last - offset + 1, true, nil
}
