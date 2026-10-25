package computers

import "net/http"

// NewHTTPClient retains system certificate verification for HTTPS reverse
// proxies and refuses redirects so enrollment credentials keep their target.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Transport:     http.DefaultTransport.(*http.Transport).Clone(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
