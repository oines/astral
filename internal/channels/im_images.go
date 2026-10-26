package channels

import (
	"bytes"
	"context"
	"image"
	"math"

	core "github.com/oines/astral-core"
)

const observationImageMaxPixels = 75_000

// processObservationImage prepares IM pixels once, before the observation is
// admitted into Core. Request construction, Probe and compaction reuse the
// frozen preview; original attachments keep their independent download path.
func processObservationImage(ctx context.Context, data []byte) (core.ToolResult, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 {
		// Let Core report its usual image decode failure to the admission path.
		return core.ProcessImage(ctx, data, core.ImageOptions{})
	}
	longest := max(config.Width, config.Height)
	pixels := float64(config.Width) * float64(config.Height)
	if pixels > observationImageMaxPixels {
		longest = max(1, int(math.Floor(float64(longest)*math.Sqrt(observationImageMaxPixels/pixels))))
		// A one-pixel short edge cannot shrink further. Cap the other edge as
		// well so even extremely narrow images respect the total pixel budget.
		longest = min(longest, observationImageMaxPixels)
	}
	// The same bound for both axes limits the longest edge after EXIF rotation.
	// Core applies orientation, preserves transparency, never upscales, and
	// retains its existing decode-memory and encoded-size limits.
	return core.ProcessImage(ctx, data, core.ImageOptions{MaxWidth: longest, MaxHeight: longest})
}
