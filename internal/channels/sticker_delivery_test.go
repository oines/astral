package channels

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

type stickerImageAdapter struct {
	Adapter
	data      []byte
	expected  string
	fail      bool
	downloads atomic.Int64
}

func (a *stickerImageAdapter) Download(_ context.Context, attachment domain.Attachment) (Download, error) {
	a.downloads.Add(1)
	if attachment.ExternalID != a.expected {
		return Download{}, errors.New("downloaded a sticker original instead of its thumbnail")
	}
	if a.fail {
		return Download{}, errors.New("preview is unavailable")
	}
	return Download{Body: io.NopCloser(bytes.NewReader(a.data)), Size: int64(len(a.data)), Name: attachment.Name}, nil
}

func TestStickerDeliveryUsesImagesWithoutSavingOriginalFiles(t *testing.T) {
	// A self-generated transparent 2x2 lossless WebP. No external image tool is
	// required by the test or the running Server.
	webp, err := base64.StdEncoding.DecodeString("UklGRhwAAABXRUJQVlA4TA8AAAAvAUAAEAcQ/Y8CBiKi/wEA")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, format, mime, expected string
		thumbnail, fail, corrupt     bool
		images                       int
	}{
		{name: "static-original", format: "static", mime: "image/webp", expected: "original", thumbnail: true, images: 1},
		{name: "animated-thumbnail", format: "animated", mime: "application/x-tgsticker", expected: "thumbnail", thumbnail: true, images: 1},
		{name: "video-thumbnail", format: "video", mime: "video/webm", expected: "thumbnail", thumbnail: true, images: 1},
		{name: "animated-no-thumbnail", format: "animated", mime: "application/x-tgsticker"},
		{name: "unavailable-thumbnail", format: "video", mime: "video/webm", expected: "thumbnail", thumbnail: true, fail: true},
		{name: "corrupt-thumbnail", format: "animated", mime: "application/x-tgsticker", expected: "thumbnail", thumbnail: true, corrupt: true},
		{name: "unavailable-static-original", format: "static", mime: "image/webp", expected: "original", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bot := newBot(t)
			p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("received sticker") })
			s := New(p.Store, p.Harness)
			source := &stickerImageAdapter{data: webp, expected: test.expected, fail: test.fail}
			if test.corrupt {
				source.data = []byte("invalid image")
			}
			s.Registry["telegram.bot"] = func(c domain.Connection, token string, client *http.Client) (Adapter, error) {
				base, err := NewTelegram(c, token, client)
				source.Adapter = base
				return source, err
			}
			account := bot.connection(t, s, p.Agent.ID)
			binding := createBinding(t, p, s, account, "-100", "")
			metadata := &domain.StickerMetadata{Type: "regular", Format: test.format, Emoji: "🙂", SetName: "test_stickers", Width: 2, Height: 2}
			attachments := []domain.Attachment{{Ref: "10:0", ExternalID: "original", Kind: "sticker", MediaType: test.mime, Name: "sticker", Sticker: metadata}}
			if test.thumbnail {
				metadata.ThumbnailRef = "10:1"
				attachments = append(attachments, domain.Attachment{Ref: "10:1", ExternalID: "thumbnail", Kind: "image", Name: "thumbnail"})
			}
			event := domain.Observation{SchemaVersion: 1, ID: "sticker-event", MessageID: "10", ConnectionID: account.ID, ConversationID: binding.ConversationID, Trigger: "mention", Attachments: attachments}
			if _, err := p.Store.Archive(p.Context, event); err != nil {
				t.Fatal(err)
			}
			if err := s.deliver(p.Context, binding, event); err != nil {
				t.Fatal("a sticker preview blocked the received message:", err)
			}
			p.Idle(t, binding.SessionID)
			view, err := p.Harness.InspectContext(p.Context, binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, message := range view.Request.Messages {
				if message.ContextKind != "im.observation" {
					continue
				}
				found = true
				var meta messages.Metadata
				if json.Unmarshal(message.Metadata, &meta) != nil || len(meta.Attachments) != len(attachments) || len(meta.Files) != 0 || meta.Attachments[0].Sticker == nil {
					t.Fatal("sticker metadata was lost or reception created a Computer file")
				}
				if !strings.Contains(meta.DisplayText, "sticker") && !strings.Contains(meta.DisplayText, "Sticker") {
					t.Fatal("sticker-only message has a blank display label")
				}
				images := 0
				for _, part := range message.Content {
					if part.Type == "image" {
						images++
						expectedRef := "10:0"
						if test.format != "static" {
							expectedRef = "10:1"
						}
						if len(part.Data) == 0 || part.URL != messages.AttachmentDownloadPath(binding.ConversationID, event.MessageID, expectedRef) {
							t.Fatalf("image pixels or source lost: data=%d url=%q", len(part.Data), part.URL)
						}
					}
				}
				if images != test.images {
					t.Fatalf("native image count=%d, expected=%d", images, test.images)
				}
				if test.images == 0 && !strings.Contains(string(encoded(message)), "Sticker preview unavailable") {
					t.Fatal("missing preview was not explained to the model")
				}
			}
			if !found || p.Requests.Load() != 1 {
				t.Fatal("sticker was not delivered exactly once")
			}
			files, err := list[domain.FileRef](p.Context, p.Store, "files")
			if err != nil || len(files) != 0 {
				t.Fatal("sticker reception automatically registered files")
			}
			if err := filepath.WalkDir(p.Daemons[0].Root, func(path string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() && entry.Name() != "identity.json" {
					t.Errorf("sticker reception wrote a Computer file: %s", path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// A committed delivery must replay its saved pixels and metadata;
			// repeating routing after Telegram becomes unavailable cannot refetch.
			before := source.downloads.Load()
			source.fail = true
			if err := s.deliver(p.Context, binding, event); err != nil || source.downloads.Load() != before {
				t.Fatal("committed delivery fetched its source again:", err)
			}
			if test.name == "static-original" {
				source.fail = false
				caller, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Daemons[0].Credentials.ComputerID, "")
				if err != nil {
					t.Fatal(err)
				}
				destination := filepath.Join(caller.CWD, "explicitly-downloaded-sticker.webp")
				ref, err := s.DownloadAttachment(p.Context, caller, binding.ConversationID, event.ID, "10:0", destination)
				if err != nil || ref.ComputerID != caller.ComputerID || ref.Path != destination {
					t.Fatalf("explicit attachment API download failed: %+v %v", ref, err)
				}
				data, err := os.ReadFile(destination)
				if err != nil || !bytes.Equal(data, webp) {
					t.Fatal("explicit download did not preserve the original sticker bytes:", err)
				}
				files, err := list[domain.FileRef](p.Context, p.Store, "files")
				if err != nil || len(files) != 1 {
					t.Fatal("explicit download did not register exactly one Computer file")
				}
			}
		})
	}
}
