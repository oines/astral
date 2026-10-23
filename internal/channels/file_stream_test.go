package channels

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type measuredFile struct {
	left    int64
	maxRead int
	calls   atomic.Int64
}

func (r *measuredFile) Read(p []byte) (int, error) {
	r.calls.Add(1)
	r.maxRead = max(r.maxRead, len(p))
	if r.left == 0 {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.left))
	clear(p[:n])
	r.left -= int64(n)
	return n, nil
}
func (*measuredFile) Close() error { return nil }

type largeImageAdapter struct {
	Adapter
	source *measuredFile
}

func (a largeImageAdapter) Download(context.Context, domain.Attachment) (Download, error) {
	return Download{Body: a.source, Size: 64 << 20, Name: "large.png"}, nil
}

func TestLargeImageKeepsOriginalReferenceWithoutBlockingModelInput(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
		body := string(encoded(request["messages"]))
		if !strings.Contains(body, "Image preview omitted") || !strings.Contains(body, "download-url") {
			t.Error("oversized image lost its preview explanation or original file reference")
		}
		return testkit.Text("received attachment metadata")
	})
	s := New(p.Store, p.Harness)
	source := &measuredFile{left: 64 << 20}
	s.Registry["telegram.bot"] = func(c domain.Connection, token string, client *http.Client) (Adapter, error) {
		base, err := NewTelegram(c, token, client)
		return largeImageAdapter{Adapter: base, source: source}, err
	}
	account := bot.connection(t, s, p.Agent.ID)
	b := createBinding(t, p, s, account, "-100", "")
	event := domain.Observation{SchemaVersion: 1, ID: "large-image", MessageID: "10", ConnectionID: account.ID, ConversationID: b.ConversationID, Trigger: "mention", Attachments: []domain.Attachment{{Ref: "large:0", Kind: "image", MediaType: "image/png", Name: "large.png", Size: 64 << 20}}}
	if err := s.deliver(p.Context, b, event); err != nil {
		t.Fatal("large image blocked the incoming message:", err)
	}
	p.Idle(t, b.SessionID)
	if source.left != (64<<20)-(20<<20)-1 || p.Requests.Load() != 1 {
		t.Fatal("model preparation read the complete original or did not continue")
	}
}

func TestBase64UploadIsLazyAndHasNoFileSizeCeiling(t *testing.T) {
	const size = 64 << 20
	source := &measuredFile{left: size}
	placeholder := filePlaceholder()
	body := streamFileJSON(map[string]string{"file_data": placeholder}, placeholder, source, size)
	defer body.Close()
	// A producer cannot consume file bytes until the consumer starts reading
	// its metadata prefix, so even a slow platform bounds memory by the pipe.
	if source.calls.Load() != 0 {
		t.Fatal("file was read before upload consumed its metadata")
	}
	n, err := io.Copy(io.Discard, body)
	if err != nil || n != body.length || source.left != 0 || source.maxRead > 64<<10 {
		t.Fatalf("file was buffered, truncated or capped: n=%d length=%d left=%d maxRead=%d err=%v", n, body.length, source.left, source.maxRead, err)
	}
}

func TestWeixinStreamingCipherMatchesProtocolAndReportsCorruption(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 16)
	for _, size := range []int{0, 1, 15, 16, 17, 65537} {
		plain := bytes.Repeat([]byte("a"), size)
		var encrypted, decrypted bytes.Buffer
		if err := weixinEncryptStream(&encrypted, bytes.NewReader(plain), key); err != nil {
			t.Fatal(err)
		}
		expected, _ := weixinEncrypt(plain, key)
		if !bytes.Equal(expected, encrypted.Bytes()) {
			t.Fatalf("cipher mismatch at %d", size)
		}
		if err := weixinDecryptStream(&decrypted, bytes.NewReader(encrypted.Bytes()), key); err != nil || !bytes.Equal(plain, decrypted.Bytes()) {
			t.Fatalf("stream round trip at %d: %v", size, err)
		}
	}
	if err := weixinDecryptStream(io.Discard, strings.NewReader("truncated"), key); err == nil {
		t.Fatal("truncated ciphertext was accepted")
	}
}

func TestNapCatBase64DownloadStreamsBeforeResponseCompletes(t *testing.T) {
	release := make(chan struct{})
	encodedFile := base64.StdEncoding.EncodeToString([]byte("downloaded"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"base64":"`+encodedFile[:4])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, encodedFile[4:]+`"},"retcode":0,"status":"ok"}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	file, err := napcatTestAdapter(t, server).Download(ctx, domain.Attachment{ExternalID: `{"file":"opaque"}`, Name: "文件.txt", Size: 1 << 40})
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	defer file.Body.Close()
	data, err := io.ReadAll(file.Body)
	if err != nil || string(data) != "downloaded" {
		t.Fatalf("streamed file: %q %v", data, err)
	}
}

func TestNapCatLateResponseFailureDoesNotBecomeSuccessfulDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"base64":"ZGF0YQ=="},"status":"failed","retcode":200}`)
	}))
	defer server.Close()
	file, err := napcatTestAdapter(t, server).Download(t.Context(), domain.Attachment{ExternalID: `{"file":"opaque"}`})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Body.Close()
	if _, err = io.Copy(io.Discard, file.Body); err == nil {
		t.Fatal("late failure was silently accepted")
	}
}
