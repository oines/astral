package channels

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

type avatarTransport func(*http.Request) (*http.Response, error)

func (f avatarTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type avatarFixture struct {
	p               *testkit.Platform
	s               *Service
	b               domain.ExecutionBinding
	chat            domain.Conversation
	account         domain.Connection
	data            []byte
	status          int
	length          int64
	mediaType       string
	member          string
	media, members  int
	onMember, onGet func()
	lastURL         string
}

func newAvatarFixture(t *testing.T) *avatarFixture {
	t.Helper()
	f := &avatarFixture{p: testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") }), status: http.StatusOK,
		mediaType: "image/jpeg", member: `{"group_id":456,"user_id":123}`}
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	f.data, f.length = data.Bytes(), int64(data.Len())
	f.s = New(f.p.Store, f.p.Harness)
	f.account = domain.Connection{Meta: domain.Meta{ID: "avatar-account"}, AgentID: f.p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all",
		Config: encoded(NapCatConfig{BaseURL: "https://napcat.example.test", WebSocketURL: "wss://napcat.example.test/ws"})}
	if err := f.p.Store.Update(f.p.Context, func(tx *persistence.Tx) error {
		var err error
		f.account.CredentialID, err = tx.PutSecret(f.p.Context, "avatar-account-token")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.chat = domain.Conversation{Meta: domain.Meta{ID: "avatar-chat"}, ConnectionID: f.account.ID, ExternalID: "group:456", Kind: "group"}
	if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Store.Save(f.p.Context, "conversations", &f.chat.Meta, &f.chat); err != nil {
		t.Fatal(err)
	}
	var err error
	f.b, err = f.p.Hub.Bind(f.p.Context, f.p.Agent.ID, f.p.Session.ID, f.p.Daemons[0].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	f.s.Client = &http.Client{Transport: avatarTransport(func(r *http.Request) (*http.Response, error) {
		body, size, status, contentType := []byte(nil), int64(-1), http.StatusOK, "application/json"
		if r.URL.Host == "napcat.example.test" && r.URL.Path == "/get_group_member_info" {
			if r.Header.Get("Authorization") != "Bearer avatar-account-token" {
				t.Error("member proof lost NapCat account authentication")
			}
			var params map[string]any
			if err := json.NewDecoder(r.Body).Decode(&params); err != nil || params["group_id"] != "456" || params["user_id"] != "123" || params["no_cache"] != true {
				t.Errorf("member proof escaped selected group: %+v %v", params, err)
			}
			f.members++
			if f.onMember != nil {
				f.onMember()
			}
			body = []byte(`{"status":"ok","retcode":0,"data":` + f.member + `}`)
		} else if (r.URL.Host == "p.qlogo.cn" || r.URL.Host == "q1.qlogo.cn") && r.URL.Scheme == "https" && r.Method == http.MethodGet {
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Error("avatar acquisition leaked account credentials")
			}
			f.media++
			f.lastURL = r.URL.String()
			if f.onGet != nil {
				f.onGet()
			}
			body, size, status, contentType = f.data, f.length, f.status, f.mediaType
		} else {
			t.Errorf("avatar read contacted an unexpected URL: %s", r.URL)
		}
		return &http.Response{StatusCode: status, ContentLength: size, Header: http.Header{"Content-Type": {contentType}, "Location": {"https://unexpected.example/private"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
	})}
	return f
}

func (f *avatarFixture) revoke(t *testing.T) {
	t.Helper()
	f.account.AccessMode = "specified"
	if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
		t.Fatal(err)
	}
}

func TestNapCatAvatarsSelectScopedIdentityAndPreserveActualImage(t *testing.T) {
	for _, test := range []struct{ name, chat, user, url, filename string }{
		{"group", "group:456", "", "https://p.qlogo.cn/gh/456/456/0/", "qq-group-456-avatar.png"},
		{"member", "group:456", "123", "https://q1.qlogo.cn/g?b=qq&nk=123&s=100", "qq-user-123-avatar.png"},
		{"private peer", "private:123", "", "https://q1.qlogo.cn/g?b=qq&nk=123&s=100", "qq-user-123-avatar.png"},
		{"explicit peer", "private:123", "123", "https://q1.qlogo.cn/g?b=qq&nk=123&s=100", "qq-user-123-avatar.png"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newAvatarFixture(t)
			f.chat.ExternalID = test.chat
			if strings.HasPrefix(test.chat, "private:") {
				f.chat.Kind = "private"
			}
			if err := f.p.Store.Save(f.p.Context, "conversations", &f.chat.Meta, &f.chat); err != nil {
				t.Fatal(err)
			}
			download, err := f.s.NapCatAvatarContent(f.p.Context, f.b, f.chat.ID, test.user)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(download.Body)
			_ = download.Body.Close()
			if err != nil || !bytes.Equal(body, f.data) || download.Name != test.filename || download.MediaType != "image/png" || download.Size != int64(len(f.data)) || f.lastURL != test.url {
				t.Fatalf("avatar lost identity, actual type or bytes: %+v %s %v", download, f.lastURL, err)
			}
			if (test.name == "member") != (f.members == 1) || f.p.Requests.Load() != 0 {
				t.Fatal("avatar bypassed membership proof or woke the Agent")
			}
			if messages, err := f.p.Store.Observations(f.p.Context, f.chat.ID, 0, 10); err != nil || len(messages) != 0 {
				t.Fatal("avatar read imported an IM observation:", err)
			}
		})
	}
}

func TestNapCatAvatarsPreserveSupportedImageFormats(t *testing.T) {
	for _, format := range []string{"jpeg", "png", "gif", "webp"} {
		t.Run(format, func(t *testing.T) {
			f := newAvatarFixture(t)
			var data bytes.Buffer
			suffix := "." + format
			switch format {
			case "jpeg":
				if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
					t.Fatal(err)
				}
				suffix = ".jpg"
			case "png":
				data.Write(f.data)
			case "gif":
				if err := gif.Encode(&data, image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White}), nil); err != nil {
					t.Fatal(err)
				}
			case "webp":
				// A one-pixel lossless WebP fixture.
				decoded, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")
				if err != nil {
					t.Fatal(err)
				}
				data.Write(decoded)
			}
			f.data, f.length, f.mediaType = data.Bytes(), int64(data.Len()), "application/octet-stream"
			download, err := f.s.NapCatAvatarContent(f.p.Context, f.b, f.chat.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			defer download.Body.Close()
			body, err := io.ReadAll(download.Body)
			if err != nil || !bytes.Equal(body, f.data) || download.MediaType != "image/"+format || !strings.HasSuffix(download.Name, suffix) {
				t.Fatalf("avatar format or bytes changed: %+v %v", download, err)
			}
		})
	}
}

func TestNapCatAvatarsRejectOutOfScopeAndRevokedSourcesBeforeWriting(t *testing.T) {
	for _, scenario := range []string{"unlisted", "private stranger", "nonmember", "membership revocation", "image revocation", "account revision", "wrong adapter", "invalid user", "invalid group"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAvatarFixture(t)
			user := "123"
			switch scenario {
			case "unlisted":
				f.revoke(t)
			case "private stranger":
				f.chat.ExternalID, f.chat.Kind = "private:456", "private"
			case "nonmember":
				f.member = `{"group_id":789,"user_id":123}`
			case "membership revocation":
				f.onMember = func() { f.revoke(t) }
			case "image revocation":
				f.onGet = func() { f.revoke(t) }
			case "account revision":
				f.onGet = func() {
					f.account.Name = "changed"
					if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
						t.Fatal(err)
					}
				}
			case "wrong adapter":
				f.account.Adapter = "qq.bot"
				if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
					t.Fatal(err)
				}
			case "invalid user":
				user = "123&nk=456"
			case "invalid group":
				f.chat.ExternalID = "group:000"
			}
			if err := f.p.Store.Save(f.p.Context, "conversations", &f.chat.Meta, &f.chat); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.p.Directory, "must-not-exist.png")
			if _, err := f.s.DownloadNapCatAvatar(f.p.Context, f.b, f.chat.ID, user, path); err == nil {
				t.Fatal("out-of-scope avatar was downloaded")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("rejected avatar wrote a Computer file")
			}
			if scenario != "image revocation" && scenario != "account revision" && f.media != 0 {
				t.Fatal("unauthorized source contacted avatar service")
			}
		})
	}
}

func TestNapCatAvatarsRejectUnusableImagesAndDownloadsUseCallingComputer(t *testing.T) {
	for _, scenario := range []string{"redirect", "HTML", "interrupted", "oversized", "oversized unknown", "missing path", "revoked computer"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAvatarFixture(t)
			path := filepath.Join(f.p.Directory, "avatar.png")
			switch scenario {
			case "redirect":
				f.status = http.StatusFound
			case "HTML":
				f.data, f.length = []byte("<html>not an image</html>"), -1
			case "interrupted":
				f.length++
			case "oversized":
				f.length = napcatAvatarLimit + 1
			case "oversized unknown":
				f.data, f.length = bytes.Repeat([]byte{'x'}, int(napcatAvatarLimit+1)), -1
			case "missing path":
				path = ""
			case "revoked computer":
				f.p.Agent.DefaultComputerID = f.p.Daemons[1].Credentials.ComputerID
				f.p.Agent.AllowedComputers = []string{f.p.Agent.DefaultComputerID}
				if err := f.p.Harness.SaveAgent(f.p.Context, &f.p.Agent); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.s.DownloadNapCatAvatar(f.p.Context, f.b, f.chat.ID, "", path); err == nil {
				t.Fatal("invalid avatar download succeeded")
			}
			if path != "" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("failed acquisition wrote a file")
				}
			}
		})
	}
	f := newAvatarFixture(t)
	var session domain.Session
	if err := f.p.Store.Get(f.p.Context, "sessions", f.p.Session.ID, &session); err != nil {
		t.Fatal(err)
	}
	session.ComputerID = f.p.Daemons[1].Credentials.ComputerID
	if err := f.p.Store.Save(f.p.Context, "sessions", &session.Meta, &session); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.p.Directory, "avatar-wrong-extension.jpg")
	ref, err := f.s.DownloadNapCatAvatar(f.p.Context, f.b, f.chat.ID, "", path)
	if err != nil || ref.ComputerID != f.b.ComputerID || ref.Path != path || ref.MediaType != "image/png" {
		t.Fatalf("download lost original Computer binding or actual MIME: %+v %v", ref, err)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, f.data) {
		t.Fatalf("download changed image bytes: %v", err)
	}
}

func TestNapCatAvatarAcquisitionFailurePreservesExistingDestination(t *testing.T) {
	f := newAvatarFixture(t)
	f.data, f.length = []byte("<html>avatar service failed</html>"), -1
	path := filepath.Join(f.p.Directory, "keep.png")
	const previous = "existing file contents"
	if err := os.WriteFile(path, []byte(previous), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DownloadNapCatAvatar(f.p.Context, f.b, f.chat.ID, "", path); err == nil {
		t.Fatal("non-image service response was saved")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != previous {
		t.Fatal("failed avatar acquisition replaced an existing Computer file:", err)
	}
}
