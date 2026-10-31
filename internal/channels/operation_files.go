package channels

import (
	"context"
	"strings"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
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
		if !strings.HasPrefix(location, "astral-file://") {
			ref, err := s.Harness.Files.Reference(ctx, b, location, true)
			if err != nil {
				closeOperationFiles(uploads)
				return nil, err
			}
			location = filestream.URL(ref)
		}
		ref, _, body, err := s.Harness.Files.Read(ctx, b.AgentID, b.SessionID, location, 0, nil)
		if err != nil {
			closeOperationFiles(uploads)
			return nil, err
		}
		uploads[field] = Download{Body: body, Size: ref.Size, Name: ref.DisplayName, MediaType: ref.MediaType}
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
