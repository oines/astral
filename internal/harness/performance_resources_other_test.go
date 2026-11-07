//go:build !darwin && !linux

package harness

import "testing"

// Resource measurements are opt-in and supported by the Unix test binaries.
// Other platforms keep the ordinary fixture lifecycle unchanged.
func performanceTrackFixture(_ *testing.T, _ *fixture) {}
