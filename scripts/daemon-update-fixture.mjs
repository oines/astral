import assert from 'node:assert/strict'

// Route only releasefeed's HTTP client in the disposable Go source copy.
// Production URLs, redirect policy, timeouts and all other clients are retained.
export function bindDaemonReleaseFixture(source, certificate, address) {
  const anchor = 'return &http.Client{Timeout:'
  assert.equal(source.split(anchor).length - 1, 1, 'Expected one release HTTP client constructor')
  assert.equal(source.split('import (').length - 1, 1, 'Expected one releasefeed import block')
  return source
    .replace('import (', 'import (\n"crypto/tls"\n"crypto/x509"\n"net"')
    .replace(anchor, () => `roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(${JSON.stringify(certificate)})) { panic("invalid fixture CA") }
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "127.0.0.1"},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, ${JSON.stringify(address)})
		},
	}
	return &http.Client{Transport: transport, Timeout:`)
}
