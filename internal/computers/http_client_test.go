package computers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerClientSupportsHTTPAndRejectsRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { t.Error("followed Server redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	client := NewHTTPClient()
	defer client.CloseIdleConnections()
	request, _ := http.NewRequest("GET", server.URL, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("followed redirect: %d", response.StatusCode)
	}
}

func TestServerClientRetainsSystemTLSVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	client := NewHTTPClient()
	defer client.CloseIdleConnections()
	if response, err := client.Get(server.URL); err == nil {
		response.Body.Close()
		t.Fatal("accepted an untrusted HTTPS certificate")
	}
}
