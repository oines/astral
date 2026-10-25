package httpapi

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/persistence"
)

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (r *deadlineRecorder) SetReadDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

type unreadBody struct{}

func (unreadBody) Read([]byte) (int, error) { panic("rejected request body was read") }
func (unreadBody) Close() error             { return nil }

type failedBody struct{ err error }

func (b failedBody) Read([]byte) (int, error) { return 0, b.err }
func (failedBody) Close() error               { return nil }

type waitingBody struct {
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *waitingBody) Read([]byte) (int, error) {
	b.once.Do(func() { b.started <- struct{}{} })
	<-b.release
	return 0, io.EOF
}
func (*waitingBody) Close() error { return nil }
func limitedAuthAPI(t *testing.T) *API {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	service, err := auth.New(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Setup(context.Background(), "owner", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	return New(Config{Auth: service})
}
func TestPublicAuthRejectsOversizedFixedAndChunkedBodies(t *testing.T) {
	api := limitedAuthAPI(t)
	for _, chunked := range []bool{false, true} {
		request := httptest.NewRequest("POST", "http://localhost/auth/login", nil)
		request.RemoteAddr = "192.0.2.1:12345"
		request.ContentLength = publicAuthBodyLimit + 1
		request.Body = unreadBody{}
		if chunked {
			request.ContentLength = -1
			request.TransferEncoding = []string{"chunked"}
			request.Body = io.NopCloser(strings.NewReader(strings.Repeat("a", int(publicAuthBodyLimit)+1)))
		}
		response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		api.AuthHandler().ServeHTTP(response, request)
		if response.Code != 413 || response.Header().Get("Connection") != "close" {
			t.Fatalf("chunked=%v oversized request returned %d %s", chunked, response.Code, response.Body.String())
		}
		if len(response.deadlines) < 2 || response.deadlines[len(response.deadlines)-1].IsZero() {
			t.Fatal("rejected unread body regained unlimited connection deadline")
		}
	}
}
func TestPublicAuthReadDeadlineIsScopedAndClearedAfterCompleteBody(t *testing.T) {
	api := limitedAuthAPI(t)
	request := httptest.NewRequest("POST", "http://localhost/auth/login", strings.NewReader(`{"username":"owner","password":"correct-horse-battery"}`))
	response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	before := time.Now()
	api.AuthHandler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if len(response.deadlines) != 2 || response.deadlines[0].Sub(before) < 9*time.Second || response.deadlines[0].Sub(before) > 11*time.Second || !response.deadlines[1].IsZero() {
		t.Fatalf("read deadline not bounded/cleared: %+v", response.deadlines)
	}
	get := httptest.NewRequest("GET", "http://localhost/auth/status", nil)
	plain := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	api.AuthHandler().ServeHTTP(plain, get)
	if plain.Code != 200 || len(plain.deadlines) != 0 {
		t.Fatal("body-free status request received a connection deadline")
	}
}
func TestPublicAuthReadTimeoutClosesConnection(t *testing.T) {
	api := limitedAuthAPI(t)
	request := httptest.NewRequest("POST", "http://localhost/auth/login", nil)
	request.ContentLength = -1
	request.Body = failedBody{err: os.ErrDeadlineExceeded}
	response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	api.AuthHandler().ServeHTTP(response, request)
	if response.Code != 408 || response.Header().Get("Connection") != "close" {
		t.Fatalf("body timeout => %d %s", response.Code, response.Body.String())
	}
	if response.deadlines[len(response.deadlines)-1].IsZero() {
		t.Fatal("timed-out body can keep connection alive")
	}
}
func TestPublicAuthEarlyRejectionDoesNotDrainUnboundedBody(t *testing.T) {
	api := limitedAuthAPI(t)
	for _, method := range []string{"POST", "GET", "DELETE"} {
		request := httptest.NewRequest(method, "http://localhost/auth/session", nil)
		request.Body = unreadBody{}
		request.ContentLength = -1
		if method == "POST" {
			request.Header.Set("Origin", "https://evil.example")
		}
		response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		api.AuthHandler().ServeHTTP(response, request)
		if (response.Code != 400 && response.Code != 403) || response.Header().Get("Connection") != "close" {
			t.Fatalf("method %s did not close rejected body: %d", method, response.Code)
		}
		if len(response.deadlines) < 2 || response.deadlines[len(response.deadlines)-1].IsZero() {
			t.Fatal("early rejected body lost deadline")
		}
	}
}
func TestPublicAuthSlowBodiesHavePerPeerAndGlobalAdmissionLimits(t *testing.T) {
	api := limitedAuthAPI(t)
	started := make(chan struct{}, 64)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	var workers sync.WaitGroup
	start := func(peer string) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			request := httptest.NewRequest("POST", "http://localhost/auth/login", nil)
			request.RemoteAddr = peer
			request.ContentLength = -1
			request.Body = &waitingBody{started: started, release: release}
			api.AuthHandler().ServeHTTP(httptest.NewRecorder(), request)
		}()
	}
	await := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("authentication body reader was not admitted")
		}
	}
	for i := 0; i < 8; i++ {
		start("192.0.2.1:" + strconv.Itoa(1000+i))
		await()
	}
	reject := func(peer string) {
		t.Helper()
		request := httptest.NewRequest("POST", "http://localhost/auth/login", nil)
		request.RemoteAddr = peer
		request.ContentLength = -1
		request.Body = unreadBody{}
		response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		api.AuthHandler().ServeHTTP(response, request)
		if response.Code != 429 || response.Header().Get("Connection") != "close" {
			t.Fatalf("reader budget exceeded by %s: %d", peer, response.Code)
		}
	}
	reject("192.0.2.1:9999")
	for peer := 2; peer <= 8; peer++ {
		for i := 0; i < 8; i++ {
			start("192.0.2." + strconv.Itoa(peer) + ":" + strconv.Itoa(1000+i))
			await()
		}
	}
	reject("192.0.2.9:1000")
	unblock()
	workers.Wait()
	request := httptest.NewRequest("POST", "http://localhost/auth/login", strings.NewReader(`{"username":"owner","password":"correct-horse-battery"}`))
	request.RemoteAddr = "192.0.2.1:1001"
	response := httptest.NewRecorder()
	api.AuthHandler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("released body slots were not reusable: %d %s", response.Code, response.Body.String())
	}
}
