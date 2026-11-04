package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestFixtureProviderOnlyAllowsTruncatedBodiesDuringCanceledShutdown(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name    string
		body    string
		ctx     context.Context
		closing bool
		allowed bool
	}{
		{name: "ordinary empty body", body: "", ctx: context.Background()},
		{name: "ordinary truncated JSON", body: `{"messages":`, ctx: context.Background()},
		{name: "closing but request still live", body: `{"messages":`, ctx: context.Background(), closing: true},
		{name: "canceled outside fixture shutdown", body: `{"messages":`, ctx: canceled},
		{name: "canceled shutdown empty body", body: "", ctx: canceled, closing: true, allowed: true},
		{name: "canceled shutdown truncated body", body: `{"messages":`, ctx: canceled, closing: true, allowed: true},
		{name: "malformed JSON even during canceled shutdown", body: `{"messages":!}`, ctx: canceled, closing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request map[string]any
			err := json.NewDecoder(strings.NewReader(test.body)).Decode(&request)
			if err == nil {
				t.Fatal("fixture must exercise a real JSON decoding failure")
			}
			got := fixtureProviderRequestError(test.ctx, test.closing, err)
			if (got == nil) != test.allowed {
				t.Fatalf("decode error %v: allowed=%v, want %v", err, got == nil, test.allowed)
			}
			if got != nil && got != err {
				t.Fatalf("normal request error was changed: got %v, want %v", got, err)
			}
		})
	}
}
