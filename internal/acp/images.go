package acp

import (
	"encoding/base64"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/harness"
)

// The WebSocket frame limit also bounds the total encoded and decoded bytes.
// Cap the count separately so a frame of tiny images cannot cause many uploads.
const maxPromptImages = 4

func decodePromptImages(blocks []contentBlock) ([]harness.ACPImage, *rpcError) {
	var images []harness.ACPImage
	for _, block := range blocks {
		data, err := base64.StdEncoding.Strict().DecodeString(block.Data)
		if err != nil || len(data) == 0 {
			return nil, invalidParams("Inline image data must be valid base64.")
		}
		mediaType := core.ImageMediaType(data)
		if mediaType == "" || !strings.EqualFold(mediaType, block.MimeType) {
			return nil, invalidParams("Inline image data must match a supported raster MIME type.")
		}
		images = append(images, harness.ACPImage{MediaType: mediaType, Data: data})
	}
	return images, nil
}
