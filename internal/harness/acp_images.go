package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
)

const (
	maxACPImagePixels = 10_000_000
	maxACPImageBase64 = 1 << 20
	acpImageTimeout   = 15 * time.Second
)

type ACPImage struct {
	MediaType string
	Data      []byte
}

func acpImageInputDigest(input InputRequest, images []ACPImage) string {
	hashes := make([][sha256.Size]byte, len(images))
	for i, image := range images {
		hashes[i] = sha256.Sum256(image.Data)
	}
	return digest(struct {
		Input  InputRequest
		Images [][sha256.Size]byte
	}{input, hashes})
}

func (r *runtime) uploadACPImages(ctx context.Context, images []ACPImage) ([]domain.FileRef, map[string]core.Content, error) {
	s := r.service
	var session domain.Session
	if err := s.Store.Get(ctx, "sessions", r.id, &session); err != nil {
		return nil, nil, err
	}
	if session.ComputerID == "" || s.Hub == nil {
		return nil, nil, domain.Invalid("Select a Computer for this Session before sending inline images.")
	}
	ctx, cancel := context.WithTimeout(ctx, acpImageTimeout)
	defer cancel()
	prepared := make([]core.Content, len(images))
	var preparedBytes int
	for i, image := range images {
		content, err := filestream.PrepareImage(ctx, image.Data, core.ImageOptions{
			MaxBytes: maxACPImageBase64, MaxPixels: maxACPImagePixels,
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, domain.Invalid("Inline image could not be decoded within the supported size and dimensions.")
		}
		preparedBytes += len(content.Data)
		if preparedBytes > maxACPImageBase64 {
			return nil, nil, domain.Invalid("The prepared inline images exceed the 1 MiB limit.")
		}
		prepared[i] = content
	}
	binding, err := s.Hub.Bind(ctx, r.agentID, r.id, session.ComputerID, "")
	if err != nil {
		return nil, nil, err
	}
	files := make([]domain.FileRef, 0, len(images))
	frozen := make(map[string]core.Content, len(images))
	for i, image := range images {
		file, err := s.Files.Upload(ctx, binding, acpImageFilename(image.MediaType), bytes.NewReader(image.Data))
		if err != nil {
			return nil, nil, err
		}
		if !strings.HasPrefix(file.MediaType, "image/") || file.Version == "" || file.Size != int64(len(image.Data)) {
			return nil, nil, domain.Invalid("Inline image requires a verified Computer attachment.")
		}
		files = append(files, file)
		content := prepared[i]
		content.URL = filestream.URL(file)
		frozen[content.URL] = content
	}
	return files, frozen, nil
}

func acpImageFilename(mediaType string) string {
	switch mediaType {
	case "image/bmp":
		return "image.bmp"
	case "image/gif":
		return "image.gif"
	case "image/jpeg":
		return "image.jpg"
	case "image/png":
		return "image.png"
	case "image/webp":
		return "image.webp"
	default:
		return "image"
	}
}
