package computers

import "net/http"

// NewHTTPClient retains system certificate verification for HTTPS reverse
// proxies and refuses redirects so the pinned tunnel keeps its exact target.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport:     http.DefaultTransport.(*http.Transport).Clone(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
