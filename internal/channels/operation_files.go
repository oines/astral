package channels

import (
	"context"
	"strings"

	"github.com/oines/astral/internal/domain"
)

// Platform operations read explicit sources from the calling Computer, just as
// ordinary sends do. They never reinterpret a path on the adapter's host.
func (s *Service) operationFiles(ctx context.Context, b domain.ExecutionBinding, files map[string]string) (map[string]Download, error) {
	uploads := make(map[string]Download, len(files))
	for field, location := range files {
		if strings.TrimSpace(location) == "" || strings.Contains(location, "://") && !strings.HasPrefix(location, "astral-file://") {
			closeOperationFiles(uploads)
			return nil, domain.Invalid("files must contain Computer paths or astral-file:// references")
		}
		path, uri := location, ""
		if strings.HasPrefix(location, "astral-file://") {
			path, uri = "", location
		}
		info, body, err := s.Harness.Files.OpenSource(ctx, b, path, uri)
		if err != nil {
			closeOperationFiles(uploads)
			return nil, err
		}
		uploads[field] = Download{Body: body, Size: info.Size, Name: info.Name, MediaType: info.MediaType}
	}
	return uploads, nil
}

func closeOperationFiles(files map[string]Download) {
	for _, file := range files {
		if file.Body != nil {
			_ = file.Body.Close()
		}
	}
}
