package acp

import (
	"bytes"
	"encoding/json"
	"mime"
	"strings"

	core "github.com/oines/astral-core"
)

const maxOutboundImageBytes = maxFrame - (128 << 10)

type imageContent struct {
	Type     string `json:"type"`
	MimeType string `json:"mimeType"`
	// Both representations encode as a JSON string. Reuse the frozen
	// RawMessage or the data URL's string slice until the frame is written.
	Data any `json:"data"`
}

func outboundImage(part core.Content) (imageContent, int, bool) {
	if part.Type != "image" {
		return imageContent{}, 0, false
	}
	if part.MediaType != "" && len(part.Data) <= maxOutboundImageBytes {
		if raw := bytes.TrimSpace(part.Data); len(raw) > 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
			b64 := raw[1 : len(raw)-1]
			if validImageBase64(b64) {
				return makeOutboundImage(part.MediaType, json.RawMessage(raw), len(b64))
			}
			// Legacy JSON may escape a base64 character such as '/'.
			if bytes.IndexByte(b64, '\\') >= 0 {
				var decodedString string
				if json.Unmarshal(raw, &decodedString) == nil && validImageBase64(decodedString) {
					return makeOutboundImage(part.MediaType, decodedString, len(decodedString))
				}
			}
		}
	}
	if len(part.URL) > maxOutboundImageBytes {
		return imageContent{}, 0, false
	}
	dataURL, ok := strings.CutPrefix(part.URL, "data:")
	if !ok {
		return imageContent{}, 0, false
	}
	header, b64, ok := strings.Cut(dataURL, ",")
	if !ok {
		return imageContent{}, 0, false
	}
	mediaType, ok := strings.CutSuffix(header, ";base64")
	if !ok || part.MediaType != "" && !strings.EqualFold(part.MediaType, mediaType) || !validImageBase64(b64) {
		return imageContent{}, 0, false
	}
	return makeOutboundImage(mediaType, b64, len(b64))
}

func makeOutboundImage(rawMediaType string, data any, encodedBytes int) (imageContent, int, bool) {
	const overhead = len(`{"type":"image","mimeType":"","data":""}`)
	if len(rawMediaType) > maxOutboundImageBytes-encodedBytes-overhead {
		return imageContent{}, 0, false
	}
	mediaType, params, err := mime.ParseMediaType(rawMediaType)
	if err != nil || !strings.HasPrefix(mediaType, "image/") || len(params) != 0 {
		return imageContent{}, 0, false
	}
	mediaJSON, _ := json.Marshal(mediaType)
	wireBytes := overhead + len(mediaJSON) - 2 + encodedBytes
	if wireBytes > maxOutboundImageBytes {
		return imageContent{}, 0, false
	}
	return imageContent{Type: "image", MimeType: mediaType, Data: data}, wireBytes, true
}

// Validate the standard alphabet and padding without decoding or copying the
// payload. The Core and MCP formats both carry standard padded base64.
func validImageBase64[T ~string | ~[]byte](data T) bool {
	end := len(data)
	if end == 0 || end%4 != 0 {
		return false
	}
	for range 2 {
		if data[end-1] != '=' {
			break
		}
		end--
	}
	for i := 0; i < end; i++ {
		c := data[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/') {
			return false
		}
	}
	return true
}
