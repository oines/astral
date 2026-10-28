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
	request, err := http.NewRequestWithContext(ctx, method, d.endpoint+path, nil)
	if err != nil {
		return state, domain.Offline("Desktop update control is unavailable.")
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	response, err := d.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return state, ctx.Err()
		}
		return state, domain.Offline("Desktop update control is unavailable.")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil || len(raw) > 128<<10 || response.StatusCode != http.StatusOK {
		return state, domain.Offline("Desktop update control did not return a valid state.")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err = decoder.Decode(&state); err != nil || decoder.Decode(new(any)) != io.EOF || state.Manager != "desktop" || state.CurrentVersion == "" || !slices.Contains([]string{"idle", "checking", "current", "available", "downloading", "installing", "error", "unavailable"}, state.Phase) {
		return State{}, domain.Offline("Desktop update control did not return a valid state.")
	}
	return state, nil
}
