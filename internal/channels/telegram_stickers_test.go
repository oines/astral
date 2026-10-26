package channels

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func telegramStickerTestUpdate(animated, video bool, sender int64, thumbnail bool) string {
	preview := ""
	if thumbnail {
		preview = `,"thumbnail":{"file_id":"thumbnail-file","file_unique_id":"thumbnail-unique","width":128,"height":64,"file_size":17}`
	}
	return fmt.Sprintf(`{"update_id":23,"message":{"message_id":42,"date":1792900000,"from":{"id":%d,"first_name":"Ada"},"chat":{"id":7,"type":"private"},"sticker":{"file_id":"original-file","file_unique_id":"sticker-unique","type":"custom_emoji","width":512,"height":256,"is_animated":%t,"is_video":%t,"emoji":"🙂","set_name":"AstralStickers","custom_emoji_id":"custom-emoji","file_size":12345%s}}}`, sender, animated, video, preview)
}

type telegramStickerTestFile struct {
	path, mediaType, body string
}

func telegramStickerTestAdapter(t *testing.T, update string, files map[string]telegramStickerTestFile) Adapter {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/botsticker-test/getUpdates":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, update)
		case "/botsticker-test/getFile":
			var request struct {
				ID string `json:"file_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			file, ok := files[request.ID]
			if !ok {
				t.Errorf("unexpected downloaded file ID %q", request.ID)
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"file_path": file.path, "file_size": len(file.body)}})
		default:
			for _, file := range files {
				if r.URL.Path == "/file/botsticker-test/"+file.path {
					w.Header().Set("Content-Type", file.mediaType)
					_, _ = io.WriteString(w, file.body)
					return
				}
			}
			t.Errorf("unexpected Telegram path %q", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	adapter, err := NewTelegram(domain.Connection{Meta: domain.Meta{ID: "account"}, AccountID: "999", Config: encoded(TelegramConfig{BaseURL: server.URL})}, "sticker-test", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestTelegramStickerPollPreservesOriginalThumbnailAndReplayIdentity(t *testing.T) {
	for _, test := range []struct {
		format, extension, mediaType string
		animated, video              bool
	}{
		{format: "static", extension: ".webp", mediaType: "image/webp"},
		{format: "animated", extension: ".tgs", mediaType: "application/x-tgsticker", animated: true},
		{format: "video", extension: ".webm", mediaType: "video/webm", video: true},
	} {
		t.Run(test.format, func(t *testing.T) {
			adapter := telegramStickerTestAdapter(t, telegramStickerTestUpdate(test.animated, test.video, 7, true), map[string]telegramStickerTestFile{
				"original-file":  {path: "stickers/original" + test.extension, mediaType: "application/octet-stream", body: "original " + test.format},
				"thumbnail-file": {path: "thumbnails/preview.jpg", mediaType: "application/octet-stream", body: "thumbnail payload"},
			})
			batch, err := adapter.Poll(t.Context(), "")
			if err != nil || len(batch.Events) != 1 || batch.NextCursor != "24" {
				t.Fatalf("sticker polling failed: %#v, %v", batch, err)
			}
			event := batch.Events[0].Observation
			if event.Text != "" || event.Trigger != "private" || event.Self || len(event.Attachments) != 2 {
				t.Fatalf("sticker event lost its private trigger or media: %#v", event)
			}
			original, thumbnail := event.Attachments[0], event.Attachments[1]
			metadata := &domain.StickerMetadata{Type: "custom_emoji", Format: test.format, Emoji: "🙂", SetName: "AstralStickers", UniqueID: "sticker-unique", CustomEmojiID: "custom-emoji", Width: 512, Height: 256, ThumbnailRef: "42:1"}
			if original.Kind != "sticker" || original.Ref != "42:0" || original.ExternalID != "original-file" || original.Name != "sticker-42"+test.extension || original.MediaType != test.mediaType || original.Size != 12345 || !reflect.DeepEqual(original.Sticker, metadata) {
				t.Fatalf("original sticker identity or metadata lost: %#v, metadata %#v", original, original.Sticker)
			}
			if thumbnail.Kind != "image" || thumbnail.Ref != metadata.ThumbnailRef || thumbnail.ExternalID != "thumbnail-file" || thumbnail.MediaType != "" || thumbnail.Sticker != nil || thumbnail.Size != 17 {
				t.Fatalf("thumbnail is not an independent image with unknown MIME: %#v", thumbnail)
			}
			for _, downloadTest := range []struct {
				attachment domain.Attachment
				mediaType  string
				body       string
			}{{original, test.mediaType, "original " + test.format}, {thumbnail, "image/jpeg", "thumbnail payload"}} {
				download, err := adapter.Download(t.Context(), downloadTest.attachment)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(download.Body)
				closeErr := download.Body.Close()
				if readErr != nil || closeErr != nil || string(body) != downloadTest.body || download.MediaType != downloadTest.mediaType || download.Name != downloadTest.attachment.Name || download.Size != int64(len(body)) {
					t.Fatalf("wrong original/thumbnail download: %#v body=%q read=%v close=%v", download, body, readErr, closeErr)
				}
			}
			replayed, err := adapter.Poll(t.Context(), "")
			if err != nil || len(replayed.Events) != 1 || event.ID != replayed.Events[0].Observation.ID || !reflect.DeepEqual(event.Attachments, replayed.Events[0].Observation.Attachments) {
				t.Fatalf("replayed update changed sticker references: %#v, %v", replayed, err)
			}
			store, err := persistence.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if inserted, err := store.Archive(t.Context(), event); err != nil || !inserted {
				t.Fatalf("sticker was not archived: inserted=%t error=%v", inserted, err)
			}
			if inserted, err := store.Archive(t.Context(), replayed.Events[0].Observation); err != nil || inserted {
				t.Fatalf("replayed sticker duplicated its observation: inserted=%t error=%v", inserted, err)
			}
			stored, err := store.Observation(t.Context(), event.ID)
			if err != nil || !reflect.DeepEqual(stored.Attachments, event.Attachments) {
				t.Fatalf("sticker metadata did not survive storage: %#v, %v", stored, err)
			}
		})
	}
}

func TestTelegramStickerSelfTriggerAndOptionalThumbnail(t *testing.T) {
	for _, sender := range []int64{7, 999} {
		t.Run(fmt.Sprint(sender), func(t *testing.T) {
			adapter := telegramStickerTestAdapter(t, telegramStickerTestUpdate(false, false, sender, false), nil)
			batch, err := adapter.Poll(t.Context(), "")
			if err != nil || len(batch.Events) != 1 {
				t.Fatalf("sticker polling failed: %#v, %v", batch, err)
			}
			event := batch.Events[0].Observation
			self := sender == 999
			if event.Self != self || self && event.Trigger != "" || !self && event.Trigger != "private" {
				t.Fatalf("incorrect self/private wake-up behavior: %#v", event)
			}
			if len(event.Attachments) != 1 || event.Attachments[0].Sticker == nil || event.Attachments[0].Sticker.ThumbnailRef != "" {
				t.Fatalf("missing thumbnail invented an attachment/reference: %#v", event.Attachments)
			}
		})
	}
}

func TestTelegramReplyToStickerHasReadablePreview(t *testing.T) {
	for _, test := range []struct{ emoji, want string }{{"🙂", "[Sticker 🙂]"}, {"", "[Sticker]"}} {
		t.Run(test.want, func(t *testing.T) {
			raw := fmt.Sprintf(`{"update_id":24,"message":{"message_id":43,"date":1792900001,"text":"reply","from":{"id":7,"first_name":"Ada"},"chat":{"id":-100,"type":"supergroup"},"reply_to_message":{"message_id":42,"from":{"id":999,"first_name":"Astral"},"sticker":{"file_id":"original-file","is_animated":true,"emoji":%q}}}}`, test.emoji)
			adapter := telegramStickerTestAdapter(t, raw, nil)
			batch, err := adapter.Poll(t.Context(), "")
			if err != nil || len(batch.Events) != 1 {
				t.Fatalf("reply polling failed: %#v, %v", batch, err)
			}
			event := batch.Events[0].Observation
			if event.ReplyPreview != test.want || event.ReplyID != "42" || event.ReplySender != "Astral" || event.Trigger != "reply" || len(event.Attachments) != 0 {
				t.Fatalf("reply lost sticker summary or attached the quoted original: %#v", event)
			}
		})
	}
}

func TestTelegramStickerThumbnailDownloadLearnsMIMEWithoutGuessing(t *testing.T) {
	for _, test := range []struct{ name, path, responseType, want string }{
		{"JPEG path", "thumbnails/preview.jpeg", "application/octet-stream", "image/jpeg"},
		{"WebP path", "thumbnails/preview.WEBP", "application/octet-stream", "image/webp"},
		{"JPEG response", "thumbnails/preview", "image/jpeg; arbitrary=parameter", "image/jpeg"},
		{"WebP response", "thumbnails/preview", "image/webp", "image/webp"},
		{"unknown format", "thumbnails/preview", "application/octet-stream", ""},
		{"unexpected image format", "thumbnails/preview", "image/png", ""},
		{"conflicting image formats", "thumbnails/preview.jpg", "image/webp", ""},
		{"unexpected format with JPEG path", "thumbnails/preview.jpg", "image/png", ""},
		{"nonimage response with WebP path", "thumbnails/preview.webp", "text/html", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := telegramStickerTestAdapter(t, telegramStickerTestUpdate(false, false, 7, true), map[string]telegramStickerTestFile{
				"thumbnail-file": {path: test.path, mediaType: test.responseType, body: "image bytes"},
			})
			download, err := adapter.Download(t.Context(), domain.Attachment{ExternalID: "thumbnail-file", Kind: "image", Name: "sticker-thumbnail-42"})
			if err != nil {
				t.Fatal(err)
			}
			defer download.Body.Close()
			body, err := io.ReadAll(download.Body)
			if err != nil || string(body) != "image bytes" || download.MediaType != test.want || strings.Contains(download.Name, ".jpg") {
				t.Fatalf("thumbnail MIME was guessed or lost: %#v body=%q error=%v", download, body, err)
			}
		})
	}
}
