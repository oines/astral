package channels

import (
	"context"

	core "github.com/oines/astral-core"
)

const observationImageCompressAboveBytes = 30_000 // Core counts the base64 payload.

// processObservationImage prepares IM pixels once, before the observation is
// admitted into Core. Request construction, Probe and compaction reuse the
// frozen preview; original attachments keep their independent download path.
func processObservationImage(ctx context.Context, data []byte) (core.ToolResult, error) {
	return core.ProcessImage(ctx, data, core.ImageOptions{
		CompressAboveBytes: observationImageCompressAboveBytes, Quality: 30,
	})
}
