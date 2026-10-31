package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type essenceFixture struct {
	p       *testkit.Platform
	s       *Service
	account domain.Connection
	chat    domain.Conversation
	b       domain.ExecutionBinding
	server  *httptest.Server
	entries []map[string]any
	listed  atomic.Int64
	media   atomic.Int64
	onList  func()
	onMedia func()
	first   []byte
	second  []byte
}

func newEssenceFixture(t *testing.T) *essenceFixture {
	t.Helper()
	f := &essenceFixture{p: testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })}
	var first, second bytes.Buffer
	if err := png.Encode(&first, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&second, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	f.first, f.second = first.Bytes(), second.Bytes()
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_essence_msg_list":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["group_id"] != "456" {
				t.Errorf("essence lookup escaped the authorized group: %+v %v", body, err)
			}
			f.listed.Add(1)
			if f.onList != nil {
				f.onList()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "retcode": 0, "data": f.entries})
		case "/first", "/second":
			f.media.Add(1)
			if r.URL.Query().Get("signature") != "hidden" {
				t.Error("selected image lost its private provider URL")
			}
			if f.onMedia != nil {
				f.onMedia()
			}
			payload, mediaType := f.first, "image/png"
			if r.URL.Path == "/second" {
				payload, mediaType = f.second, "image/gif"
			}
			w.Header().Set("Content-Type", mediaType)
			_, _ = w.Write(payload)
		default:
			t.Errorf("essence access must not call get_msg, get_image or arbitrary resources: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	f.s = New(f.p.Store, f.p.Harness)
	f.account = domain.Connection{Meta: domain.Meta{ID: "essence-account"}, AgentID: f.p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all",
		Config: encoded(NapCatConfig{BaseURL: f.server.URL, WebSocketURL: strings.Replace(f.server.URL, "http:", "ws:", 1) + "/ws"})}
	f.chat = domain.Conversation{Meta: domain.Meta{ID: "essence-chat"}, ConnectionID: f.account.ID, ExternalID: "group:456", Kind: "group"}
	if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
		t.Fatal(err)
	}
	if err := f.p.Store.Save(f.p.Context, "conversations", &f.chat.Meta, &f.chat); err != nil {
		t.Fatal(err)
	}
	var err error
	f.b, err = f.p.Hub.Bind(f.p.Context, f.p.Agent.ID, f.p.Session.ID, f.p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	f.entries = []map[string]any{{"message_id": 100, "msg_seq": 10, "msg_random": 20, "content": []any{
		map[string]any{"type": "text", "data": map[string]any{"text": "before"}},
		map[string]any{"type": "image", "data": map[string]any{"file": "", "url": f.server.URL + "/first?signature=hidden"}},
		map[string]any{"type": "text", "data": map[string]any{"text": "between"}},
		map[string]any{"type": "image", "data": map[string]any{"file": "", "url": f.server.URL + "/second?signature=hidden"}},
	}}}
	return f
}

func TestNapCatEssenceResourcesKeepOrderedUnarchivedPicturesAndStableCoordinates(t *testing.T) {
	f := newEssenceFixture(t)
	resource, err := f.s.NapCatEssenceParts(f.p.Context, f.b, f.chat.ID, NapCatEssenceSource{MessageID: "100"})
	if err != nil || len(resource.Parts) != 4 || resource.Parts[0].Text != "before" || resource.Parts[1].Attachment == nil || resource.Parts[2].Text != "between" || resource.Parts[3].Attachment == nil || f.media.Load() != 0 {
		t.Fatalf("essence metadata lost ordered text/pictures or downloaded eagerly: %+v %v", resource, err)
	}
	if resource.Source.MsgSeq != "10" || resource.Source.MsgRandom != "20" {
		t.Fatalf("missing stable original coordinates: %+v", resource.Source)
	}
	// The original message becomes available, replacing NapCat's synthetic ID.
	f.entries[0]["message_id"] = -200
	download, err := f.s.NapCatEssenceContent(f.p.Context, f.b, f.chat.ID, resource.Source, "p1")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(download.Body)
	_ = download.Body.Close()
	if err != nil || !bytes.Equal(body, f.first) || download.MediaType != "image/png" || !strings.HasSuffix(download.Name, ".png") {
		t.Fatalf("could not read selected URL-only old essence image: %q %v", body, err)
	}
	destination := filepath.Join(f.p.Directory, "selected.gif")
	ref, err := f.s.DownloadNapCatEssencePart(f.p.Context, f.b, f.chat.ID, resource.Source, "p3", destination)
	if err != nil || ref.ComputerID != f.b.ComputerID || ref.Path != destination || ref.MediaType != "image/gif" {
		t.Fatalf("explicit download lost the original Computer: %+v %v", ref, err)
	}
	if data, err := os.ReadFile(destination); err != nil || !bytes.Equal(data, f.second) {
		t.Fatalf("download did not save exactly the selected image: %q %v", data, err)
	}
	for _, id := range []string{"100", "-200"} {
		if _, err := f.p.Store.Message(f.p.Context, f.chat.ID, id); err == nil {
			t.Fatal("resource read imported an essence into ordinary IM history")
		}
	}
	if f.media.Load() != 2 || f.p.Requests.Load() != 0 {
		t.Fatal("resource inspection downloaded extra media or ran the Agent")
	}
	f.entries = nil
	if _, err := f.s.NapCatEssenceContent(f.p.Context, f.b, f.chat.ID, resource.Source, "p1"); err == nil || f.media.Load() != 2 {
		t.Fatal("removed essence was served through a stale media grant")
	}
}

func TestNapCatEssenceRejectsInvalidAndAmbiguousSources(t *testing.T) {
	f := newEssenceFixture(t)
	for _, source := range []NapCatEssenceSource{{MessageID: "0"}, {MessageID: "https://example.com"}, {MessageID: "100", MsgSeq: "10"}, {MessageID: "100", MsgSeq: "10", MsgRandom: "+20"}} {
		if _, err := f.s.NapCatEssenceParts(f.p.Context, f.b, f.chat.ID, source); err == nil {
			t.Fatalf("invalid essence identity was accepted: %+v", source)
		}
	}
	if f.listed.Load() != 0 {
		t.Fatal("invalid source reached the platform")
	}
	if _, err := f.s.DownloadNapCatEssencePart(f.p.Context, f.b, f.chat.ID, NapCatEssenceSource{MessageID: "100"}, "p1", ""); err == nil || f.listed.Load() != 0 {
		t.Fatal("download accepted an implicit destination")
	}
	duplicate := map[string]any{"message_id": 100, "msg_seq": 11, "msg_random": 21, "content": []any{}}
	f.entries = append(f.entries, duplicate)
	if _, err := f.s.NapCatEssenceParts(f.p.Context, f.b, f.chat.ID, NapCatEssenceSource{MessageID: "100"}); err == nil {
		t.Fatal("ambiguous platform short ID selected an arbitrary original")
	}
	resource, err := f.s.NapCatEssenceParts(f.p.Context, f.b, f.chat.ID, NapCatEssenceSource{MessageID: "100", MsgSeq: "10", MsgRandom: "20"})
	if err != nil || len(resource.Parts) != 4 {
		t.Fatalf("stable coordinates did not disambiguate original: %+v %v", resource, err)
	}
	for _, ref := range []string{"../p1", "p01", "p1.m0.p0", "p999"} {
		if _, err := f.s.NapCatEssenceContent(f.p.Context, f.b, f.chat.ID, resource.Source, ref); err == nil {
			t.Errorf("invalid or non-media path was accepted: %s", ref)
		}
	}
	if f.media.Load() != 0 {
		t.Fatal("invalid selection acquired media")
	}
}

func TestNapCatEssenceRechecksAccountAndChatAuthorization(t *testing.T) {
	for _, stage := range []string{"before", "listing", "media"} {
		t.Run(stage, func(t *testing.T) {
			f := newEssenceFixture(t)
			revoke := func() {
				f.account.AccessMode = "specified"
				if err := f.p.Store.Save(f.p.Context, "connections", &f.account.Meta, &f.account); err != nil {
					t.Error(err)
				}
			}
			switch stage {
			case "before":
				revoke()
			case "listing":
				f.onList = revoke
			case "media":
				f.onMedia = revoke
			}
			if _, err := f.s.NapCatEssenceContent(f.p.Context, f.b, f.chat.ID, NapCatEssenceSource{MessageID: "100"}, "p1"); err == nil {
				t.Fatal("revoked group resource remained readable")
			}
			if stage != "media" && f.media.Load() != 0 || stage == "before" && f.listed.Load() != 0 {
				t.Fatal("authorization failure still fetched protected resources")
			}
		})
	}
}

func TestNapCatEssenceInlineForwardUsesOriginalGroupWithoutGetMsg(t *testing.T) {
	f := newEssenceFixture(t)
	f.entries[0]["content"] = []any{map[string]any{"type": "forward", "data": map[string]any{"content": []any{
		map[string]any{"group_id": "untrusted-quoted-group", "sender": map[string]any{"nickname": "Quoted"}, "message": []any{
			map[string]any{"type": "text", "data": map[string]any{"text": "quoted"}},
			map[string]any{"type": "image", "data": map[string]any{"file": "", "url": f.server.URL + "/first?signature=hidden"}},
		}},
	}}}}
	source, part, err := f.s.NapCatEssencePart(f.p.Context, f.b, f.chat.ID, NapCatEssenceSource{MessageID: "100"}, "p0.m0.p1")
	if err != nil || part.Attachment == nil || part.Ref != "p0.m0.p1" {
		t.Fatalf("inline quoted image was not readable: %+v %v", part, err)
	}
	download, err := f.s.NapCatEssenceContent(f.p.Context, f.b, f.chat.ID, source, part.Ref)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(download.Body)
	_ = download.Body.Close()
	if err != nil || !bytes.Equal(data, f.first) {
		t.Fatalf("nested content lost original: %s %v", data, err)
	}
	f.entries[0]["content"] = []any{map[string]any{"type": "forward", "data": map[string]any{"id": "untrusted-remote-forward"}}}
	if _, err := f.s.NapCatEssenceContent(f.p.Context, f.b, f.chat.ID, source, "p0.m0.p0"); err == nil {
		t.Fatal("opaque forward became an independent source grant")
	}
	if f.listed.Load() != 3 {
		t.Fatalf("unexpected source proof count: %d", f.listed.Load())
	}
}

func TestNapCatEssenceMediaDigestRejectsOldPartLinkAfterNativeShapeChanges(t *testing.T) {
	f := newEssenceFixture(t)
	content := f.entries[0]["content"].([]any)
	// The official fallback omits non-text/image segments.
	f.entries[0]["content"] = []any{content[0], content[1], content[3]}
	resource, err := f.s.NapCatEssenceParts(f.p.Context, f.b, f.chat.ID, NapCatEssenceSource{MessageID: "100"})
	if err != nil {
		t.Fatal(err)
	}
	source := resource.Source
	source.MediaDigest = NapCatEssenceMediaDigest(*resource.Parts[2].Attachment)
	// Native parsing restores reply before text/A/B. The old p2 now names A.
	f.entries[0]["message_id"] = -200
	f.entries[0]["content"] = []any{map[string]any{"type": "reply", "data": map[string]any{"id": "50"}}, content[0], content[1], content[3]}
	_, err = f.s.NapCatEssenceContent(f.p.Context, f.b, f.chat.ID, source, "p2")
	var conflict *domain.Error
	if !errors.As(err, &conflict) || conflict.Status != 409 || f.media.Load() != 0 {
		t.Fatalf("stale B link did not reject newly indexed A: %v", err)
	}
	fresh, err := f.s.NapCatEssenceParts(f.p.Context, f.b, f.chat.ID, source)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Source.MediaDigest = NapCatEssenceMediaDigest(*fresh.Parts[3].Attachment)
	download, err := f.s.NapCatEssenceContent(f.p.Context, f.b, f.chat.ID, fresh.Source, "p3")
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(download.Body)
	_ = download.Body.Close()
	if err != nil || !bytes.Equal(data, f.second) || download.MediaType != "image/gif" || !strings.HasSuffix(download.Name, ".gif") {
		t.Fatalf("refresh did not recover the selected original GIF: type=%s name=%s err=%v", download.MediaType, download.Name, err)
	}
}

func TestNapCatEssenceDownloadsUseValidatedHTTPMediaType(t *testing.T) {
	f := newEssenceFixture(t)
	for _, sample := range []struct{ header, expected string }{
		{"image/png; charset=binary", "image/png"},
		{"image/gif", "image/gif"},
		{"image/png; invalid", ""},
	} {
		t.Run(sample.header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", sample.header)
				_, _ = w.Write(f.first)
			}))
			defer server.Close()
			download, err := qqDownloadURL(f.p.Context, qqHTTPClient(nil), server.URL, domain.Attachment{Kind: "image", Name: "guess.jpg"})
			if err != nil {
				t.Fatal(err)
			}
			defer download.Body.Close()
			if download.MediaType != sample.expected {
				t.Fatalf("unexpected normalized HTTP media type: %q, expected %q", download.MediaType, sample.expected)
			}
		})
	}
}
