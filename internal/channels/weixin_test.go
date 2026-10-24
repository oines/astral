package channels

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/persistence"
)

func weixinTestAdapter(t *testing.T, endpoint string, store *persistence.Store) *weixinAdapter {
	t.Helper()
	adapter, err := NewWeixin(domain.Connection{Meta: domain.Meta{ID: "weixin-account"}, Config: encoded(WeixinConfig{BaseURL: endpoint, CDNBaseURL: endpoint + "/c2c"})},
		string(encoded(WeixinCredential{BotToken: "secret-login-token", BotID: "bot-id", UserID: "scanner-id", BaseURL: endpoint})), nil)
	if err != nil {
		t.Fatal(err)
	}
	w := adapter.(*weixinAdapter)
	w.setStore(store)
	return w
}

func TestWeixinLoginValidation(t *testing.T) {
	w := weixinTestAdapter(t, weixinDefaultURL, nil)
	w.client = &http.Client{Transport: weixinRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Error("a QR-issued identity must not depend on another API request")
		return nil, fmt.Errorf("unexpected API request")
	})}
	identity, err := w.Validate(t.Context())
	if err != nil || identity.ID != "bot-id" || len(identity.Diagnostics) == 0 {
		t.Fatalf("identity = %#v, %v", identity, err)
	}
	caps := w.Capabilities()
	if caps.Platform != "weixin" || caps.Adapter != "weixin.ilink" || !caps.FileSending || caps.EnumerateConversations || caps.ReplyReferences || caps.MessageEdits || caps.DeletionEvents || caps.DeliveryReceipts || caps.CaptionLimit != 0 || strings.Join(caps.ConversationKinds, ",") != "private" {
		t.Fatalf("incorrect capabilities: %#v", caps)
	}
	for _, secret := range []string{"raw-token", `{}`, `{"bot_token":"x","user_id":"scanner"}`, `{"bot_token":"x\r\ny","bot_id":"id","user_id":"u"}`} {
		if _, err := NewWeixin(domain.Connection{}, secret, nil); err == nil {
			t.Errorf("accepted invalid grant %q", secret)
		}
	}
	invalid := domain.Connection{Config: json.RawMessage(`{"bot_id":"spoofed"}`)}
	if _, err := NewWeixin(invalid, string(encoded(w.credential)), nil); err == nil {
		t.Fatal("editable config must not contain account identity")
	}
}

func TestWeixinAPIErrorsHaveSafeOwnerDiagnostics(t *testing.T) {
	for _, code := range []int{-1, -14} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(rw, `{"ret":%d,"errmsg":"secret-login-token private-context-token"}`, code)
			}))
			defer server.Close()
			err := weixinRequest(t.Context(), server.Client(), server.URL, "", http.MethodGet, "/status", nil, nil)
			var remote *weixinAPIError
			if !errors.As(err, &remote) || remote.Code != code {
				t.Fatalf("lost the platform error code: %v", err)
			}
			response := httptest.NewRecorder()
			httpcontract.Failure(response, httptest.NewRequest(http.MethodPost, "/api/v1/weixin/logins/test/poll", nil), err)
			var problem httpcontract.Error
			if json.Unmarshal(response.Body.Bytes(), &problem) != nil || response.Code != http.StatusServiceUnavailable || problem.Message != remote.Error() || problem.Code == "operation_failed" {
				t.Fatalf("platform failure became an opaque server error: %d %s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret-login-token") || strings.Contains(response.Body.String(), "private-context-token") {
				t.Fatal("owner diagnostics exposed remote credential text")
			}
		})
	}
}

func TestWeixinQRPollCompatibility(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		err         error
		bodyTimeout bool
		want        string
	}{
		{name: "transport timeout", err: context.DeadlineExceeded, want: "wait"},
		{name: "body timeout", bodyTimeout: true, want: "wait"},
		{name: "legacy scanned", body: `{"status":"scaned"}`, want: "scaned"},
		{name: "scanned", body: `{"status":"scanned"}`, want: "scaned"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: weixinRoundTripper(func(*http.Request) (*http.Response, error) {
				if test.err != nil {
					return nil, test.err
				}
				var reader io.Reader = strings.NewReader(test.body)
				if test.bodyTimeout {
					reader = iotest.ErrReader(context.DeadlineExceeded)
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(reader)}, nil
			})}
			qr := WeixinQR{Qrcode: "fixture-qr", BaseURL: weixinDefaultURL}
			result, err := WeixinPollQR(t.Context(), qr, "", client)
			if err != nil || result.Status != test.want {
				t.Fatalf("poll = %#v, %v; want %s", result, err, test.want)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := WeixinPollQR(ctx, qr, "", client); !errors.Is(err, context.Canceled) {
				t.Fatalf("caller cancellation became a waiting authorization: %v", err)
			}
		})
	}
}

func TestWeixinPollingPersistsContextAndMediaAcrossAdapters(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	key := bytes.Repeat([]byte{7}, 16)
	plain := []byte("encrypted Weixin attachment\x00\x01")
	ciphertext, err := weixinEncrypt(plain, key)
	if err != nil {
		t.Fatal(err)
	}
	checksum := md5.Sum(plain)
	var polls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getupdates":
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if _, exists := body["sync_buf"]; exists {
				t.Error("must not send deprecated sync_buf")
			}
			if polls.Add(1) == 1 {
				if string(body["get_updates_buf"]) != `"persisted-start"` {
					t.Errorf("did not resume caller cursor: %s", body["get_updates_buf"])
				}
				_, _ = fmt.Fprintf(rw, `{"ret":0,"get_updates_buf":"next-opaque-buffer","msgs":[
{"message_id":18446744073709551614,"seq":1,"from_user_id":"peer-id","to_user_id":"bot-id","message_type":1,"message_state":2,"create_time_ms":1770000000123,"context_token":"secret-peer-context","item_list":[
{"type":1,"text_item":{"text":"hello"},"ref_msg":{"svr_id":"18446744073709551613","title":"quoted text"}},
{"type":4,"file_item":{"file_name":"../../report.txt","len":"%d","md5":"%s","media":{"encrypt_query_param":"download+opaque&param","aes_key":"%s"}}},
{"type":3,"voice_item":{"text":"voice transcript","encode_type":6,"media":{"encrypt_query_param":"voice","aes_key":"%s"}}}]},
{"message_id":2,"from_user_id":"group-sender","group_id":"group-id","message_type":1,"context_token":"ignored-group"},
{"message_id":3,"from_user_id":"peer-id","message_type":2,"item_list":[{"type":1,"text_item":{"text":"bot echo"}}]},
{"message_id":4,"from_user_id":"peer-id","message_type":1,"message_state":1,"item_list":[{"type":1,"text_item":{"text":"unfinished"}}]}
]}`, len(plain), hex.EncodeToString(checksum[:]), base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key))), base64.StdEncoding.EncodeToString(key))
			} else {
				if string(body["get_updates_buf"]) != `"next-opaque-buffer"` {
					t.Errorf("changed cursor: %s", body["get_updates_buf"])
				}
				_, _ = io.WriteString(rw, `{"ret":0,"get_updates_buf":"","sync_buf":"deprecated-ignored","msgs":[]}`)
			}
		case "/c2c/download":
			if r.URL.Query().Get("encrypted_query_param") != "download+opaque&param" || r.Header.Get("Authorization") != "" {
				t.Error("invalid CDN query or leaked bot token")
			}
			_, _ = rw.Write(ciphertext)
		case "/ilink/bot/sendmessage":
			var body struct {
				Message weixinMessage `json:"msg"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Message.ContextToken != "secret-peer-context" || body.Message.To != "peer-id" || len(body.Message.Items) != 1 {
				t.Errorf("send did not use persisted peer context: %#v", body.Message)
			}
			_, _ = io.WriteString(rw, `{"ret":0,"message_id":18446744073709551615}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(rw, r)
		}
	}))
	defer server.Close()
	w := weixinTestAdapter(t, server.URL, store)
	batch, err := w.Poll(t.Context(), "persisted-start")
	if err != nil || len(batch.Events) != 1 || batch.NextCursor != "next-opaque-buffer" {
		t.Fatalf("batch = %#v, err = %v", batch, err)
	}
	event := batch.Events[0]
	observation := event.Observation
	if observation.MessageID != "18446744073709551614" || observation.Trigger != "private" || observation.Text != "hello\nvoice transcript" || observation.ReplyID != "18446744073709551613" || observation.ReplyPreview != "quoted text" || observation.SentAt.UnixMilli() != 1770000000123 || len(observation.Attachments) != 2 {
		t.Fatalf("bad normalized observation: %#v", observation)
	}
	file := observation.Attachments[0]
	if file.Name != "report.txt" || file.Size != int64(len(plain)) || strings.Contains(string(encoded(observation)), "secret-peer-context") || strings.Contains(string(encoded(observation)), "download+opaque") {
		t.Fatalf("attachment metadata leaked capability secrets: %#v", file)
	}
	if observation.Attachments[1].MediaType != "audio/silk" || !strings.HasSuffix(observation.Attachments[1].Name, ".silk") {
		t.Fatal("voice codec was misrepresented")
	}
	// A fresh factory instance is used for actual API sends and downloads. It
	// must recover contexts and encryption keys from the existing Store.
	restarted := weixinTestAdapter(t, server.URL, store)
	conversation, err := restarted.ResolveConversation(t.Context(), "peer-id", "")
	if err != nil || conversation.ID != event.Conversation.ID {
		t.Fatalf("resolve after restart = %#v, %v", conversation, err)
	}
	if _, err := restarted.ResolveConversation(t.Context(), "unknown-peer", ""); err == nil {
		t.Fatal("must not invent reachability for an unknown Weixin user")
	}
	if _, err := restarted.ResolveConversation(t.Context(), "peer-id", "topic"); err == nil {
		t.Fatal("Topics are unsupported")
	}
	download, err := restarted.Download(t.Context(), file)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(download.Body)
	_ = download.Body.Close()
	if err != nil || !bytes.Equal(data, plain) || download.Size != int64(len(plain)) {
		t.Fatalf("download = %q, %v", data, err)
	}
	receipt, err := restarted.Send(t.Context(), Send{Target: conversation, Text: "reply"})
	if err != nil || len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "18446744073709551615" {
		t.Fatalf("send = %#v, %v", receipt, err)
	}
	batch, err = restarted.Poll(t.Context(), batch.NextCursor)
	if err != nil || batch.NextCursor != "next-opaque-buffer" {
		t.Fatalf("empty cursor must preserve checkpoint: %#v, %v", batch, err)
	}
	other := weixinTestAdapter(t, server.URL, store)
	other.connection.ID = "another-account"
	if _, err := other.ResolveConversation(t.Context(), "peer-id", ""); err == nil {
		t.Fatal("conversation context crossed account boundary")
	}
	if _, err := other.Download(t.Context(), file); err == nil {
		t.Fatal("media keys crossed account boundary")
	}
	for _, kind := range []string{"weixin_wire", "connections"} {
		records, err := store.List(t.Context(), kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if bytes.Contains(record.Data, []byte("secret-peer-context")) || bytes.Contains(record.Data, []byte("download+opaque")) || bytes.Contains(record.Data, []byte(hex.EncodeToString(key))) {
				t.Fatal("wire secrets persisted in public document JSON")
			}
		}
	}
}

func TestWeixinSendAcceptanceAndUnknownOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, response string
		status         int
		unknown        bool
		accepted       bool
		retryable      bool
	}{
		{name: "client receipt", response: `{"ret":0}`, status: 200, accepted: true},
		{name: "server receipt", response: `{"ret":0,"message_id":"123"}`, status: 200, accepted: true},
		{name: "missing acceptance", response: `{}`, status: 200, unknown: true},
		{name: "invalid response", response: `{"ret":`, status: 200, unknown: true},
		{name: "transport response", response: `secret-login-token`, status: 502, unknown: true},
		{name: "expired", response: `{"ret":-14,"errmsg":"secret-login-token"}`, status: 200},
		{name: "known rejection", response: `{"ret":-1,"errmsg":"secret-login-token"}`, status: 200, retryable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				rw.WriteHeader(test.status)
				_, _ = io.WriteString(rw, test.response)
			}))
			defer server.Close()
			w := weixinTestAdapter(t, server.URL, nil)
			_ = w.putWire(t.Context(), "context", "peer", "private-context")
			receipt, err := w.Send(t.Context(), Send{Target: w.conversation("peer"), Text: "one message"})
			if attempts.Load() != 1 {
				t.Fatalf("send retried %d times", attempts.Load())
			}
			if test.accepted {
				if err != nil || len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] == "" {
					t.Fatalf("acceptance = %#v, %v", receipt, err)
				}
				if test.name == "client receipt" && !strings.HasPrefix(receipt.MessageIDs[0], "client:astral_weixin_") {
					t.Fatal("client receipt must be clearly distinguished from server ID")
				}
			} else {
				var sendErr *SendError
				if !errors.As(err, &sendErr) || sendErr.Unknown != test.unknown || sendErr.Retryable != test.retryable {
					t.Fatalf("wrong send outcome: %#v / %v", sendErr, err)
				}
				if strings.Contains(err.Error(), "secret-login-token") {
					t.Fatal("platform diagnostics leaked credentials")
				}
			}
		})
	}
}

func TestWeixinUploadEncryptsBeforeSingleMessageSend(t *testing.T) {
	for _, test := range []struct {
		mime      string
		mediaType int
		itemType  int
	}{
		{"text/plain", 3, 4}, {"image/png", 1, 2}, {"video/mp4", 2, 5}, {"audio/mpeg", 3, 4},
	} {
		t.Run(test.mime, func(t *testing.T) {
			plain := []byte("file bytes needing PKCS7 padding")
			var key []byte
			var sends, uploads atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/ilink/bot/getuploadurl":
					var body struct {
						Type int    `json:"media_type"`
						Key  string `json:"aeskey"`
						MD5  string `json:"rawfilemd5"`
						Size int    `json:"rawsize"`
						Enc  int    `json:"filesize"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					key, _ = hex.DecodeString(body.Key)
					digest := md5.Sum(plain)
					if body.Type != test.mediaType || len(key) != 16 || body.MD5 != hex.EncodeToString(digest[:]) || body.Size != len(plain) || body.Enc != (len(plain)/16+1)*16 {
						t.Errorf("incorrect upload metadata: %#v", body)
					}
					_, _ = io.WriteString(rw, `{"upload_param":"upload+parameter&value"}`)
				case "/c2c/upload":
					uploads.Add(1)
					if r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != "application/octet-stream" || r.URL.Query().Get("encrypted_query_param") != "upload+parameter&value" || r.URL.Query().Get("filekey") == "" {
						t.Error("incorrect CDN upload request or leaked token")
					}
					data, _ := io.ReadAll(r.Body)
					decoded, err := weixinDecrypt(data, key)
					if err != nil || !bytes.Equal(decoded, plain) {
						t.Errorf("CDN encryption mismatch: %q, %v", decoded, err)
					}
					rw.Header().Set("x-encrypted-param", "download-reference")
					_, _ = io.WriteString(rw, "")
				case "/ilink/bot/sendmessage":
					sends.Add(1)
					var body struct {
						Message weixinMessage `json:"msg"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if len(body.Message.Items) != 1 || body.Message.Items[0].Type != test.itemType || body.Message.ContextToken != "context-for-peer" {
						t.Errorf("incorrect media send: %#v", body.Message)
					}
					item := body.Message.Items[0]
					var media *weixinMedia
					switch test.itemType {
					case 2:
						media = item.Image.Media
					case 5:
						media = item.Video.Media
					default:
						media = item.File.Media
						if item.File.Name != "report.txt" || string(item.File.Size) != fmt.Sprint(len(plain)) {
							t.Error("incorrect filename or plaintext length")
						}
					}
					decodedKey, err := weixinDecodeKey(media.AESKey, "")
					if err != nil || !bytes.Equal(decodedKey, key) || media.Query != "download-reference" || media.EncryptType != 1 {
						t.Error("incorrect outgoing CDN key/reference")
					}
					_, _ = io.WriteString(rw, `{"ret":0,"message_id":42}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			w := weixinTestAdapter(t, server.URL, nil)
			_ = w.putWire(t.Context(), "context", "peer", "context-for-peer")
			receipt, err := w.Send(t.Context(), Send{Target: w.conversation("peer"), File: io.NopCloser(bytes.NewReader(plain)), OpenFile: func(context.Context) (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(plain)), nil }, FileName: "../../report.txt", MediaType: test.mime, Size: int64(len(plain))})
			if err != nil || len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "42" || sends.Load() != 1 || uploads.Load() != 1 {
				t.Fatalf("media receipt = %#v, %v, sends=%d uploads=%d", receipt, err, sends.Load(), uploads.Load())
			}
		})
	}
}

type weixinRoundTripper func(*http.Request) (*http.Response, error)

func (f weixinRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWeixinQRLoginHeadersVerificationAndTrustedRedirect(t *testing.T) {
	var phase atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("iLink-App-Id") != "bot" {
			t.Error("QR flow must not use bearer credentials")
		}
		if r.URL.Path == "/ilink/bot/get_bot_qrcode" {
			if r.Method != http.MethodPost || r.URL.Query().Get("bot_type") != "3" || r.Header.Get("AuthorizationType") != "ilink_bot_token" {
				t.Error("incorrect QR start request")
			}
			var body map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			if string(body["local_token_list"]) != "[]" || body["base_info"] != nil {
				t.Errorf("incorrect QR request body: %#v", body)
			}
			_, _ = io.WriteString(rw, `{"qrcode":"qr+opaque&code","qrcode_img_content":"https://weixin.qq.com/login?code=content"}`)
			return
		}
		if r.Method != http.MethodGet || r.URL.Query().Get("qrcode") != "qr+opaque&code" || r.Header.Get("AuthorizationType") != "" || r.Header.Get("X-WECHAT-UIN") != "" {
			t.Error("incorrect QR status request")
		}
		switch phase.Add(1) {
		case 1:
			_, _ = io.WriteString(rw, `{"status":"need_verifycode"}`)
		case 2:
			if r.URL.Query().Get("verify_code") != "123456" {
				t.Error("verification code did not reach status request")
			}
			_, _ = io.WriteString(rw, `{"status":"scaned_but_redirect","redirect_host":"ilinkai2.weixin.qq.com"}`)
		case 3:
			_, _ = io.WriteString(rw, `{"status":"confirmed","bot_token":"qr-secret-token","ilink_bot_id":"qr-bot-id","ilink_user_id":"qr-user-id","baseurl":"https://ilinkai2.weixin.qq.com/"}`)
		default:
			_, _ = io.WriteString(rw, `{"status":"scaned_but_redirect","redirect_host":"weixin.qq.com.evil.example"}`)
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client := &http.Client{Transport: weixinRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "ilinkai.weixin.qq.com" && r.URL.Host != "ilinkai2.weixin.qq.com" {
			t.Errorf("untrusted outgoing host: %s", r.URL.Host)
		}
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(copy)
	})}
	qr, err := WeixinStartQR(t.Context(), client)
	if err != nil || qr.Qrcode != "qr+opaque&code" || qr.BaseURL != weixinDefaultURL || qr.QrcodeURL == "" {
		t.Fatalf("QR = %#v, %v", qr, err)
	}
	status, err := WeixinPollQR(t.Context(), qr, "", client)
	if err != nil || status.Status != "need_verifycode" {
		t.Fatalf("verify state = %#v, %v", status, err)
	}
	status, err = WeixinPollQR(t.Context(), qr, "123456", client)
	if err != nil || status.RedirectHost != "ilinkai2.weixin.qq.com" {
		t.Fatalf("redirect = %#v, %v", status, err)
	}
	qr.BaseURL = "https://" + status.RedirectHost
	status, err = WeixinPollQR(t.Context(), qr, "", client)
	if err != nil || status.Credential == nil || status.Credential.BotToken != "qr-secret-token" || status.Credential.BotID != "qr-bot-id" || status.Credential.UserID != "qr-user-id" || status.Credential.BaseURL != qr.BaseURL {
		t.Fatalf("confirmation = %#v, %v", status, err)
	}
	if strings.Contains(string(encoded(status)), "qr-secret-token") {
		t.Fatal("QR status JSON exposed login grant")
	}
	if _, err = WeixinPollQR(t.Context(), qr, "", client); err == nil {
		t.Fatal("accepted untrusted QR redirect host")
	}
	for _, host := range []string{"http://ilinkai.weixin.qq.com", "https://ilinkai.weixin.qq.com.evil.example", "https://ilinkai.weixin.qq.com:1234", "https://user@ilinkai.weixin.qq.com", "https://ilinkai.weixin.qq.com/path", "https://ilinkai.weixin.qq.com?query=1"} {
		if weixinTrustedAPIURL(host) {
			t.Errorf("trusted invalid API URL %q", host)
		}
	}
}

func TestWeixinEncryptionAndValidationBoundaries(t *testing.T) {
	key := bytes.Repeat([]byte{3}, 16)
	for _, size := range []int{0, 1, 15, 16, 17, 31, 32, 1000} {
		plain := bytes.Repeat([]byte{byte(size)}, size)
		ciphertext, err := weixinEncrypt(plain, key)
		if err != nil || len(ciphertext) != (size/16+1)*16 {
			t.Fatalf("encryption size %d: %v", size, err)
		}
		decrypted, err := weixinDecrypt(ciphertext, key)
		if err != nil || !bytes.Equal(decrypted, plain) {
			t.Fatalf("roundtrip size %d: %v", size, err)
		}
	}
	for _, encodedKey := range []string{base64.StdEncoding.EncodeToString(key), base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(key)))} {
		decoded, err := weixinDecodeKey(encodedKey, "")
		if err != nil || !bytes.Equal(decoded, key) {
			t.Fatalf("key decoder = %x, %v", decoded, err)
		}
	}
	if _, err := weixinDecodeKey("garbage", hex.EncodeToString(key)); err != nil {
		t.Fatal("image hex key must take priority")
	}
	for _, encodedKey := range []string{"garbage", base64.StdEncoding.EncodeToString([]byte("short")), base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'z'}, 32))} {
		if _, err := weixinDecodeKey(encodedKey, ""); err == nil {
			t.Fatal("accepted invalid key")
		}
	}
	for _, data := range [][]byte{nil, {1}, bytes.Repeat([]byte{2}, 16)} {
		if _, err := weixinDecrypt(data, key); err == nil {
			t.Fatal("accepted invalid ciphertext or padding")
		}
	}
	w := weixinTestAdapter(t, "http://127.0.0.1:1", nil)
	for _, endpoint := range []string{"file:///etc/passwd", "http://remote.example/file", "https://evil.example/download", "https://user@novac2c.cdn.weixin.qq.com/file"} {
		if _, err := w.mediaURL(endpoint, "download", nil); err == nil {
			t.Errorf("accepted unsafe CDN URL %q", endpoint)
		}
	}
	text := strings.Repeat("你好🙂", 4000)
	chunks, err := w.SplitText(text)
	if err != nil || strings.Join(chunks, "") != text || len(chunks) != 3 {
		t.Fatalf("split lost text: chunks=%d err=%v", len(chunks), err)
	}
	for _, chunk := range chunks {
		if len([]rune(chunk)) > 4000 {
			t.Fatal("chunk exceeds text limit")
		}
	}
	_ = w.putWire(t.Context(), "context", "peer", "peer-context")
	for _, send := range []Send{
		{Target: w.conversation("peer"), Text: "quote", ReplyTo: "1"},
		{Target: domain.Conversation{ExternalID: "peer", Kind: "group"}, Text: "group"},
		{Target: w.conversation("unknown"), Text: "unsolicited"},
		{Target: w.conversation("peer"), File: io.NopCloser(strings.NewReader("file")), Text: "caption"},
	} {
		_, err := w.Send(context.Background(), send)
		var sendErr *SendError
		if !errors.As(err, &sendErr) || sendErr.Unknown || sendErr.Retryable {
			t.Fatalf("invalid send reached network: %#v %v", sendErr, err)
		}
	}
}
