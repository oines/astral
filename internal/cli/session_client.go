package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func sessionClient() *http.Client {
	return &http.Client{Timeout: 4 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func webJSON(ctx context.Context, method, endpoint, token string, input, output any) (string, error) {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return "", err
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return "", err
	}
	r.Header.Set("User-Agent", "Astral CLI")
	if input != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.AddCookie(&http.Cookie{Name: "astral_session", Value: token})
	}
	response, err := sessionClient().Do(r)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	if output != nil {
		if err = json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(output); err != nil {
			return "", err
		}
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == "astral_session" && cookie.Value != "" {
			return cookie.Value, nil
		}
	}
	return "", nil
}
func loginJSON(ctx context.Context, server, path string, input, output any) (string, error) {
	return webJSON(ctx, "POST", strings.TrimRight(server, "/")+path, "", input, output)
}
