package gateway

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"sync"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/httpcontract"
)

func (s *Service) describeContent(operation httpcontract.Operation) {
	s.Contract.DescribeContent(operation)
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
		offset, length, partial, err := httpcontract.ParseRange(r.Header.Get("Range"), download.Size)
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
