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

type stickerImageSource struct {
	data      []byte
	expected  string
	fail      atomic.Bool
	downloads atomic.Int64
}

type stickerImageAdapter struct {
	Adapter
	*stickerImageSource
}

func (s *stickerImageSource) adapter(c domain.Connection, token string, client *http.Client) (Adapter, error) {
	base, err := NewTelegram(c, token, client)
	if err != nil {
		return nil, err
	}
	return &stickerImageAdapter{base, s}, nil
}

func (a *stickerImageAdapter) Download(_ context.Context, attachment domain.Attachment) (Download, error) {
	a.downloads.Add(1)
	if attachment.ExternalID != a.expected {
		return Download{}, errors.New("downloaded a sticker original instead of its thumbnail")
	}
	if a.fail.Load() {
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
			requests := make(chan map[string]any, 2)
			p := testkit.New(t, 1, func(_ context.Context, _ int, request map[string]any) any {
				requests <- request
				return testkit.Text("received sticker")
			})
			s := New(p.Store, p.Harness)
			source := &stickerImageSource{data: webp, expected: test.expected}
			source.fail.Store(test.fail)
			if test.corrupt {
				source.data = []byte("invalid image")
			}
			s.Registry["telegram.bot"] = source.adapter
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
			awaitDirectReminderRequests(t, p, binding.SessionID, "sticker mention", 2)
			p.Idle(t, binding.SessionID)
			<-requests // The original direct-trigger request is followed by one reminder.
			activeDeliveryAssertOneReplyReminder(t, <-requests)
			view, err := p.Harness.InspectContext(p.Context, binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			observations := 0
			for _, message := range view.Request.Messages {
				if message.ContextKind != "im.observation" {
					continue
				}
				observations++
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
			if observations != 1 || p.Requests.Load() != 2 {
				t.Fatalf("sticker or reminder was lost or duplicated: observations=%d requests=%d", observations, p.Requests.Load())
			}
			files, err := list[domain.FileRef](p.Context, p.Store, "files")
			if err != nil || len(files) != 0 {
				t.Fatal("sticker reception automatically registered files")
			}
			if err := filepath.WalkDir(p.Daemons[0].Root, func(path string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() && entry.Name() != "identity.json" && entry.Name() != "local-api.json" {
					t.Errorf("sticker reception wrote a Computer file: %s", path)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// A committed delivery must replay its saved pixels and metadata;
			// repeating routing after Telegram becomes unavailable cannot refetch.
			before := source.downloads.Load()
			source.fail.Store(true)
			if err := s.deliver(p.Context, binding, event); err != nil || source.downloads.Load() != before {
				t.Fatal("committed delivery fetched its source again:", err)
			}
			if test.name == "static-original" {
				source.fail.Store(false)
				caller, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Daemons[0].Credentials.ComputerID, "")
				if err != nil {
					t.Fatal(err)
				}
				download, _, err := s.AttachmentContent(p.Context, caller, binding.ConversationID, event.MessageID, "10:0")
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(download.Body)
				_ = download.Body.Close()
				if err != nil || !bytes.Equal(data, webp) {
					t.Fatal("explicit download did not preserve the original sticker bytes:", err)
				}
				files, err := list[domain.FileRef](p.Context, p.Store, "files")
				if err != nil || len(files) != 0 {
					t.Fatal("reading original bytes created a file reference")
				}
			}
			// A replay must not re-register the direct trigger or schedule another
			// reminder after the first one has been consumed and reconciled.
			testkit.Await(t, p.Context, func() bool {
				var watch map[string]any
				return domain.IsMissing(p.Store.Maintenance(p.Context, "im_reply_watch", binding.SessionID, &watch))
			})
			p.Idle(t, binding.SessionID)
			if p.Requests.Load() != 2 {
				t.Fatal("sticker replay or completed reminder started another model request")
			}
		})
	}
}
