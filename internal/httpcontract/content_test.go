package httpcontract

import (
	"math"
	"testing"
)

func TestRangeUsesDecimalDigitsAndSupportsInt64Sizes(t *testing.T) {
	for _, raw := range []string{"bytes=+1-2", "bytes=1-+2", "bytes=-+2", "bytes=１-2", "bytes=0-9223372036854775808"} {
		if _, _, _, err := ParseRange(raw, math.MaxInt64); err == nil {
			t.Errorf("accepted invalid range %q", raw)
		}
	}
	for _, raw := range []string{"bytes=9223372036854775806-", "bytes=-1", "bytes=9223372036854775806-9223372036854775807"} {
		offset, length, partial, err := ParseRange(raw, math.MaxInt64)
		if err != nil || offset != math.MaxInt64-1 || length != 1 || !partial {
			t.Errorf("last byte of large file %q: offset=%d length=%d partial=%v err=%v", raw, offset, length, partial, err)
		}
	}
}
