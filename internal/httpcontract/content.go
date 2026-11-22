package httpcontract

import (
	"fmt"
	"strconv"
	"strings"
)

// DescribeContent documents a binary stream with single-byte-range support.
func (c *Contract) DescribeContent(operation Operation) {
	c.Describe(operation)
	view := c.Paths[operation.Path].(map[string]any)["get"].(map[string]any)
	parameters, _ := view["parameters"].([]any)
	view["parameters"] = append(parameters, map[string]any{
		"name": "Range", "in": "header", "required": false,
		"description": "One bytes range (start-end, start-, or -suffix). Sources with unknown size return the complete content with HTTP 200.",
		"schema":      map[string]any{"type": "string"},
	})
	responses := view["responses"].(map[string]any)
	responses["206"] = map[string]any{
		"description": "Partial Content",
		"content":     map[string]any{"application/octet-stream": map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}},
		"headers": map[string]any{
			"Accept-Ranges":  map[string]any{"schema": map[string]any{"type": "string", "const": "bytes"}},
			"Content-Range":  map[string]any{"schema": map[string]any{"type": "string"}},
			"Content-Length": map[string]any{"schema": map[string]any{"type": "integer", "minimum": 0}},
		},
	}
	responses["416"] = map[string]any{
		"description": "Range Not Satisfiable",
		"content":     responses["default"].(map[string]any)["content"],
		"headers":     map[string]any{"Content-Range": map[string]any{"schema": map[string]any{"type": "string"}}},
	}
}

// ParseRange selects one optional HTTP bytes range of a file of known size.
func ParseRange(raw string, size int64) (offset, length int64, partial bool, err error) {
	length = size
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	invalid := func() (int64, int64, bool, error) {
		return 0, 0, true, fmt.Errorf("provide one satisfiable bytes range")
	}
	if !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		return invalid()
	}
	start, end, ok := strings.Cut(strings.TrimPrefix(raw, "bytes="), "-")
	if !ok {
		return invalid()
	}
	parse := func(value string) (int64, error) {
		if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return 0, fmt.Errorf("invalid byte offset")
		}
		return strconv.ParseInt(value, 10, 64)
	}
	if start == "" {
		suffix, parseErr := parse(end)
		if parseErr != nil || suffix <= 0 || size <= 0 {
			return invalid()
		}
		length = min(size, suffix)
		return size - length, length, true, nil
	}
	offset, err = parse(start)
	if err != nil || offset >= size {
		return invalid()
	}
	last := size - 1
	if end != "" {
		last, err = parse(end)
		if err != nil || last < offset {
			return invalid()
		}
		last = min(last, size-1)
	}
	return offset, last - offset + 1, true, nil
}
