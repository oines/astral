package serverupdate

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

type desktop struct {
	endpoint, token string
	client          *http.Client
}

// NewDesktop accepts only the endpoint and per-process secret received over the
// private parent pipe. No environment, browser input or saved file selects it.
func NewDesktop(endpoint, token string) (Controller, error) {
	parsed, err := url.Parse(endpoint)
	key, keyErr := hex.DecodeString(token)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" || keyErr != nil || len(key) != 32 {
		return nil, fmt.Errorf("invalid private desktop update configuration")
	}
	if _, _, err = net.SplitHostPort(parsed.Host); err != nil {
		return nil, fmt.Errorf("invalid private desktop update configuration")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid private desktop update configuration")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &desktop{endpoint: "http://" + parsed.Host, token: token, client: &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (d *desktop) State(ctx context.Context) (State, error) { return d.call(ctx, "GET", "/state") }
func (d *desktop) Check(ctx context.Context) (State, error) { return d.call(ctx, "POST", "/check") }
func (d *desktop) Apply(ctx context.Context) (State, error) { return d.call(ctx, "POST", "/apply") }
func (d *desktop) call(ctx context.Context, method, path string) (State, error) {
	var state State
	if err := d.exchange(ctx, method, path, nil, &state); err != nil {
		return state, err
	}
	if state.Manager != "desktop" || state.CurrentVersion == "" || !slices.Contains([]string{"idle", "checking", "current", "available", "downloading", "installing", "error", "unavailable"}, state.Phase) {
		return State{}, domain.Offline("Desktop update control did not return a valid state.")
	}
	return state, nil
}

func (d *desktop) CacheInventory(ctx context.Context) ([]CacheItem, error) {
	var result struct {
		Items []CacheItem `json:"items"`
	}
	if err := d.exchange(ctx, "GET", "/storage/cache", nil, &result); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	if result.Items == nil || len(result.Items) > 512 {
		return nil, domain.Offline("Desktop cleanup inventory is invalid.")
	}
	for _, item := range result.Items {
		if !validCacheToken(item.Token) || seen[item.Token] || item.Kind != "update_cache" || item.Bytes < 0 || item.Name == "" || len(item.Name) > 255 || strings.ContainsAny(item.Name, "/\\\x00") {
			return nil, domain.Offline("Desktop cleanup inventory is invalid.")
		}
		seen[item.Token] = true
	}
	return result.Items, nil
}

func validCacheToken(token string) bool {
	if !strings.HasPrefix(token, "desktop:") || len(token) != len("desktop:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(token, "desktop:"))
	return err == nil && token == strings.ToLower(token)
}

func (d *desktop) CleanCache(ctx context.Context, operationID, token string) (int64, error) {
	if strings.TrimSpace(operationID) == "" || len(operationID) > 128 || !validCacheToken(token) {
		return 0, domain.Invalid("A stable cleanup operation and Desktop cache token are required.")
	}
	var result struct {
		RemovedBytes *int64 `json:"removed_bytes"`
	}
	if err := d.exchange(ctx, "POST", "/storage/cache/clean", struct {
		OperationID string `json:"operation_id"`
		Token       string `json:"token"`
	}{operationID, token}, &result); err != nil {
		return 0, err
	}
	if result.RemovedBytes == nil || *result.RemovedBytes < 0 {
		return 0, domain.Offline("Desktop cleanup did not return a valid outcome.")
	}
	return *result.RemovedBytes, nil
}

func (d *desktop) exchange(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, d.endpoint+path, body)
	if err != nil {
		return domain.Offline("Desktop update control is unavailable.")
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := d.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return domain.Offline("Desktop update control is unavailable.")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return domain.Conflict("Desktop update files changed or are required by an update; refresh the inventory before retrying.")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil || len(raw) > 128<<10 || response.StatusCode != http.StatusOK {
		return domain.Offline("Desktop update control did not return a valid response.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(output); err != nil || decoder.Decode(new(any)) != io.EOF {
		return domain.Offline("Desktop update control did not return a valid response.")
	}
	return nil
}
