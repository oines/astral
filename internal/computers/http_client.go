package computers

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
)

// NewHTTPClient uses ordinary platform trust unless a local container relay's
// certificate authority was explicitly delivered with its enrollment file.
func NewHTTPClient(serverCA string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if serverCA != "" {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(serverCA)) {
			return nil, fmt.Errorf("invalid Computer Server certificate authority")
		}
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
		// This explicit local relay is reached directly, not through a user's
		// network proxy. Its certificate remains verified against the pin.
		transport.Proxy = nil
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
