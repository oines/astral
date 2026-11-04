package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/testkit"
)

type contentAdapter struct {
	attachmentAdapter
	size int64
	open func() io.ReadCloser
}

func (a contentAdapter) Download(context.Context, domain.Attachment) (channels.Download, error) {
	return channels.Download{Body: a.open(), Name: "original.bin", Size: a.size}, nil
}

type contentFixture struct {
	p              *testkit.Platform
	api            *Service
	binding        domain.ExecutionBinding
	client         *http.Client
	conversationID string
	eventID        string
}

func newContentFixture(t *testing.T, size int64, open func() io.ReadCloser) contentFixture {
	t.Helper()
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	channel := channels.New(p.Store, p.Harness)
	channel.Registry["fixture"] = func(domain.Connection, string, *http.Client) (channels.Adapter, error) {
		return contentAdapter{attachmentAdapter: attachmentAdapter{payload: []byte("fixture")}, size: size, open: open}, nil
	}
	account := domain.Connection{Name: "content", AgentID: p.Agent.ID, Adapter: "fixture", AccessMode: "all", Enabled: true}
	token := "content-fixture-only"
	if err := channel.SaveConnection(p.Context, &account, &token); err != nil {
		t.Fatal(err)
	}
	conversation := domain.Conversation{Meta: domain.Meta{ID: domain.ID("conversation")}, ConnectionID: account.ID, ExternalID: "42", Kind: "group"}
	if err := p.Store.Save(p.Context, "conversations", &conversation.Meta, &conversation); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{SchemaVersion: 1, ID: domain.ID("event"), ExternalEventID: "content-message", MessageID: "content-message", ConnectionID: account.ID,
		ConversationID: conversation.ID, Kind: "message.created", ReceivedAt: time.Now(), SentAt: time.Now(),
		Attachments: []domain.Attachment{{Ref: "original:0", ExternalID: "fixture", Name: "original.bin", MediaType: "application/octet-stream", Size: size}}}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	api := New(p.Harness, channel)
	p.Hub.Gateway = api.Handle
	binding, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	return contentFixture{p: p, api: api, binding: binding, client: testkit.ComputerAPI(t, p, binding), conversationID: conversation.ID, eventID: event.ID}
}

func (f contentFixture) contentURL() string {
	return f.binding.APIURL + "/v1/conversations/" + f.conversationID + "/messages/content-message/attachments/original:0/content"
}

func TestAttachmentContentRangesThroughRealGateway(t *testing.T) {
	payload := []byte("0123456789")
	f := newContentFixture(t, int64(len(payload)), func() io.ReadCloser { return io.NopCloser(bytes.NewReader(payload)) })
	for _, test := range []struct {
		rangeHeader, want, contentRange string
		status                          int
	}{
		{"", "0123456789", "", 200},
		{"bytes=2-5", "2345", "bytes 2-5/10", 206},
		{"bytes=7-", "789", "bytes 7-9/10", 206},
		{"bytes=-3", "789", "bytes 7-9/10", 206},
		{"bytes=8-100", "89", "bytes 8-9/10", 206},
		{"bytes=-100", "0123456789", "bytes 0-9/10", 206},
		{"bytes=10-", "", "bytes */10", 416},
		{"bytes=4-2", "", "bytes */10", 416},
		{"bytes=-0", "", "bytes */10", 416},
		{"bytes=0-1,4-5", "", "bytes */10", 416},
		{"items=1-2", "", "bytes */10", 416},
		{"bytes=+1-2", "", "bytes */10", 416},
		{"bytes=0-9223372036854775808", "", "bytes */10", 416},
	} {
		t.Run(test.rangeHeader, func(t *testing.T) {
			request, err := http.NewRequest("GET", f.contentURL(), nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Range", test.rangeHeader)
			response, err := f.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != test.status || response.Header.Get("Content-Range") != test.contentRange || response.Header.Get("Accept-Ranges") != "bytes" {
				t.Fatalf("HTTP %d headers=%v body=%q error=%v", response.StatusCode, response.Header, body, err)
			}
			if test.status == 416 {
				var problem httpcontract.Error
				if json.Unmarshal(body, &problem) != nil || problem.Code != "invalid_range" {
					t.Fatalf("range error lost its JSON contract: %s", body)
				}
			} else if string(body) != test.want || response.ContentLength != int64(len(test.want)) {
				t.Fatalf("wrong range bytes=%q length=%d", body, response.ContentLength)
			}
		})
	}
}

func TestContentUnknownSizeIgnoresRangeWithoutAdvertisingIt(t *testing.T) {
	f := newContentFixture(t, 0, func() io.ReadCloser { return io.NopCloser(strings.NewReader("unknown-size content")) })
	request, _ := http.NewRequest("GET", f.contentURL(), nil)
	request.Header.Set("Range", "bytes=4-8")
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(body) != "unknown-size content" || response.Header.Get("Accept-Ranges") != "" || response.Header.Get("Content-Range") != "" {
		t.Fatalf("unknown-size source claimed range support: status=%d headers=%v body=%q err=%v", response.StatusCode, response.Header, body, err)
	}
}

func TestContentShortSourceAbortsHTTPResponse(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 512<<10)
	f := newContentFixture(t, int64(2*len(payload)), func() io.ReadCloser { return io.NopCloser(bytes.NewReader(payload)) })
	response, err := f.client.Get(f.contentURL())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if response.StatusCode != 200 || err == nil || len(body) != len(payload) {
		t.Fatalf("short stream appeared complete: status=%d bytes=%d err=%v", response.StatusCode, len(body), err)
	}
}

type finalContentError struct{ io.Reader }

func (b finalContentError) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		err = fmt.Errorf("source finalization failed")
	}
	return n, err
}

func TestDownloadAttachmentFailureKeepsNonSuccessHTTPStatus(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 512<<10)
	f := newContentFixture(t, int64(2*len(payload)), func() io.ReadCloser {
		return io.NopCloser(finalContentError{Reader: bytes.NewReader(payload)})
	})
	request := testkit.JSON(DownloadRequest{ConversationID: f.conversationID, EventID: f.eventID, AttachmentRef: "original:0", Path: f.p.Daemons[0].Hello.Home + "/failed-download.bin"})
	response, err := f.client.Post(f.binding.APIURL+"/v1/attachments/download", "application/json", bytes.NewReader(request))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var problem httpcontract.Error
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil || response.StatusCode < 400 || problem.Code == "" {
		t.Fatalf("failed download reported HTTP success: status=%d problem=%+v decode=%v", response.StatusCode, problem, err)
	}
}

type blockingContent struct {
	started chan struct{}
	done    chan struct{}
	reads   sync.Once
	closes  atomic.Int32
}

func (b *blockingContent) Read([]byte) (int, error) {
	b.reads.Do(func() { close(b.started) })
	<-b.done
	return 0, io.ErrClosedPipe
}
func (b *blockingContent) Close() error {
	if b.closes.Add(1) == 1 {
		close(b.done)
	}
	return nil
}

func TestContentCancellationClosesSourceDuringRangePrefix(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest("GET", "/content", nil).WithContext(ctx)
	request.Header.Set("Range", "bytes=8-9")
	source := &blockingContent{started: make(chan struct{}), done: make(chan struct{})}
	response := &responseWriter{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		serveContent(response, request, channels.Download{Body: source, Size: 10})
		close(done)
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("range prefix did not start reading")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not unblock range prefix")
	}
	if source.closes.Load() != 1 || response.stream != nil || response.code < 400 {
		t.Fatalf("cancelled range retained its source: closes=%d status=%d stream=%v", source.closes.Load(), response.code, response.stream)
	}
}

func TestContentRangeDoesNotReadPastSelectedBytesAndCloses(t *testing.T) {
	request := httptest.NewRequest("GET", "/content", nil)
	request.Header.Set("Range", "bytes=2-3")
	source := &countedContent{Reader: strings.NewReader("0123456789")}
	response := &responseWriter{header: http.Header{}}
	serveContent(response, request, channels.Download{Body: source, Size: 10})
	body, err := io.ReadAll(response.stream)
	if err != nil || string(body) != "23" || source.bytes != 4 {
		t.Fatalf("range consumed unrelated suffix bytes: body=%q consumed=%d err=%v", body, source.bytes, err)
	}
	if err := response.stream.Close(); err != nil || source.closes != 1 {
		t.Fatalf("range did not close source: closes=%d err=%v", source.closes, err)
	}
}

type countedContent struct {
	io.Reader
	bytes, closes int
}

func (b *countedContent) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.bytes += n
	return n, err
}
func (b *countedContent) Close() error { b.closes++; return nil }

func TestAllContentRoutesDeclareRangeContract(t *testing.T) {
	api := New(nil, nil)
	contentRoutes := 0
	for path, raw := range api.Contract.Paths {
		if !strings.HasSuffix(path, "/content") {
			continue
		}
		contentRoutes++
		operation := raw.(map[string]any)["get"].(map[string]any)
		hasRange := false
		for _, parameter := range operation["parameters"].([]any) {
			p := parameter.(map[string]any)
			hasRange = hasRange || p["name"] == "Range" && p["in"] == "header"
		}
		responses := operation["responses"].(map[string]any)
		if !hasRange || responses["200"] == nil || responses["206"] == nil || responses["416"] == nil {
			t.Fatalf("content range contract incomplete: %s %+v", path, operation)
		}
	}
	if contentRoutes != 7 {
		t.Fatalf("unexpected content route coverage: %d", contentRoutes)
	}
}
