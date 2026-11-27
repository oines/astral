package acp

import (
	"encoding/json"
	"strings"
	"testing"

	core "github.com/oines/astral-core"
)

func TestOutboundImageUsesExistingInlineData(t *testing.T) {
	tests := []struct {
		name string
		part core.Content
		mime string
		data string
	}{
		{
			name: "frozen JSON",
			part: core.Content{Type: "image", MediaType: "image/png", Data: json.RawMessage(`"/w=="`)},
			mime: "image/png",
			data: "/w==",
		},
		{
			name: "legacy escaped JSON",
			part: core.Content{Type: "image", MediaType: "image/png", Data: json.RawMessage(`"\/w=="`)},
			mime: "image/png",
			data: "/w==",
		},
		{
			name: "MCP data URL",
			part: core.Content{Type: "image", MediaType: "image/webp", URL: "data:image/webp;base64,AAEC"},
			mime: "image/webp",
			data: "AAEC",
		},
		{
			name: "media type inferred from inline URL",
			part: core.Content{Type: "image", URL: "data:image/x&test;base64,YQ==", Data: json.RawMessage(`{"provider":"typed content"}`)},
			mime: "image/x&test",
			data: "YQ==",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			image, length, ok := outboundImage(test.part)
			if !ok {
				t.Fatal("valid inline image was rejected")
			}
			raw, err := json.Marshal(image)
			if err != nil || len(raw) != length {
				t.Fatalf("encoded length %d, reported %d, err %v", len(raw), length, err)
			}
			var block map[string]string
			if err := json.Unmarshal(raw, &block); err != nil || len(block) != 3 || block["type"] != "image" || block["mimeType"] != test.mime || block["data"] != test.data {
				t.Fatalf("outbound image %s, err %v", raw, err)
			}
		})
	}
}

func TestOutboundImageRejectsInvalidOrExternalParts(t *testing.T) {
	for _, tc := range []struct {
		name string
		part core.Content
	}{
		{name: "not image", part: core.Content{Type: "text", MediaType: "image/png", Data: json.RawMessage(`"AA=="`)}},
		{name: "empty", part: core.Content{Type: "image", MediaType: "image/png", Data: json.RawMessage(`""`)}},
		{name: "JSON object", part: core.Content{Type: "image", MediaType: "image/png", Data: json.RawMessage(`{"data":"AA=="}`)}},
		{name: "missing MIME type", part: core.Content{Type: "image", Data: json.RawMessage(`"AA=="`)}},
		{name: "wrong MIME type", part: core.Content{Type: "image", MediaType: "text/html", Data: json.RawMessage(`"AA=="`)}},
		{name: "malformed MIME type", part: core.Content{Type: "image", MediaType: "image/@#foo", Data: json.RawMessage(`"AA=="`)}},
		{name: "base64 padding", part: core.Content{Type: "image", MediaType: "image/png", Data: json.RawMessage(`"A===" `)}},
		{name: "embedded whitespace", part: core.Content{Type: "image", MediaType: "image/png", Data: json.RawMessage(`"AAA\nAYl9"`)}},
		{name: "external source", part: core.Content{Type: "image", URL: "https://example.test/test.png"}},
		{name: "unencoded data URL", part: core.Content{Type: "image", URL: "data:image/png,AA=="}},
		{name: "invalid data URL base64", part: core.Content{Type: "image", URL: "data:image/png;base64,A@=="}},
		{name: "conflicting MIME types", part: core.Content{Type: "image", MediaType: "image/jpeg", URL: "data:image/png;base64,AA=="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, size, ok := outboundImage(tc.part); ok || size != 0 {
				t.Fatalf("invalid image accepted with size %d", size)
			}
		})
	}
}

func TestOutboundImageLimitsEncodedSizeWithoutCopyingFrozenData(t *testing.T) {
	const mime = "image/png"
	const baseSize = len(`{"type":"image","mimeType":"image/png","data":""}`)
	budget := (maxOutboundImageBytes - baseSize) / 4 * 4
	frozen := json.RawMessage(`"` + strings.Repeat("A", budget) + `"`)
	image, size, ok := outboundImage(core.Content{Type: "image", MediaType: mime, Data: frozen})
	if !ok || size != budget+baseSize {
		t.Fatalf("size %d for valid %d-byte image, ok %v", size, budget, ok)
	}
	data, ok := image.Data.(json.RawMessage)
	if !ok || &data[0] != &frozen[0] {
		t.Fatal("frozen image data was copied")
	}

	tail := strings.Repeat("A", budget+4)
	for _, part := range []core.Content{
		{Type: "image", MediaType: mime, Data: json.RawMessage(`"` + tail + `"`)},
		{Type: "image", URL: "data:image/png;base64," + tail},
	} {
		if _, size, ok := outboundImage(part); ok || size != 0 {
			t.Fatalf("oversized image accepted with size %d", size)
		}
	}
}
