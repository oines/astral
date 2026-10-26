package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const testDesktopToken = "1111111111111111111111111111111111111111111111111111111111111111"

func newAuthenticatedTestServer(config Config) (*Server, error) {
	config.DesktopToken = testDesktopToken
	return New(config)
}
func desktopJSON(ctx context.Context, method, endpoint, token string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", "Desktop "+token)
	r.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(response.Body)
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, raw)
	}
	if output != nil {
		return json.NewDecoder(response.Body).Decode(output)
	}
	return nil
}
