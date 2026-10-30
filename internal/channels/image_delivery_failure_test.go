package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

type inboxImageAdapter struct {
	Adapter
	download  func(context.Context, domain.Attachment) (Download, error)
	downloads *atomic.Int64
}

func (a *inboxImageAdapter) Download(ctx context.Context, attachment domain.Attachment) (Download, error) {
	a.downloads.Add(1)
	return a.download(ctx, attachment)
}

type inboxImageBody struct {
	io.Reader
	readErr, closeErr error
}

func (b *inboxImageBody) Read(data []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.Reader.Read(data)
}

func (b *inboxImageBody) Close() error { return b.closeErr }

func imageFailureChat(t *testing.T, p *testkit.Platform, s *Service, source *inboxImageAdapter) (domain.Connection, domain.ConversationBinding) {
	t.Helper()
	bot := newBot(t)
	source.downloads = &atomic.Int64{}
	s.Registry["telegram.bot"] = func(c domain.Connection, token string, client *http.Client) (Adapter, error) {
		base, err := NewTelegram(c, token, client)
		return &inboxImageAdapter{Adapter: base, download: source.download, downloads: source.downloads}, err
	}
	account := bot.connection(t, s, p.Agent.ID)
	return account, createBinding(t, p, s, account, "-100", "")
}

func TestFailedImagePreviewDoesNotBlockLaterIMTrigger(t *testing.T) {
	// These are shared inbox admission failures, independent of the platform
	// that supplied the image. Image documents must take the same soft path.
	for _, scenario := range []struct {
		name, kind string
		download   func(context.Context, domain.Attachment) (Download, error)
	}{
		{"download", "image", func(context.Context, domain.Attachment) (Download, error) {
			return Download{}, errors.New("https://provider.invalid/private-token?secret=do-not-expose")
		}},
		{"read", "image", func(context.Context, domain.Attachment) (Download, error) {
			return Download{Body: &inboxImageBody{readErr: errors.New("truncated provider response")}}, nil
		}},
		{"close", "image", func(context.Context, domain.Attachment) (Download, error) {
			return Download{Body: &inboxImageBody{Reader: strings.NewReader("data"), closeErr: errors.New("provider rejected its late response")}}, nil
		}},
		{"decode", "image", func(context.Context, domain.Attachment) (Download, error) {
			return Download{Body: io.NopCloser(strings.NewReader("invalid image data"))}, nil
		}},
		{"image-document", "file", func(context.Context, domain.Attachment) (Download, error) {
			return Download{}, errors.New("image document no longer exists")
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			requests := make(chan map[string]any, 2)
			p := testkit.New(t, 0, func(_ context.Context, _ int, request map[string]any) any {
				requests <- request
				return testkit.Text("I received the later mention and the earlier image metadata.")
			})
			s := New(p.Store, p.Harness)
			source := &inboxImageAdapter{download: scenario.download}
			account, binding := imageFailureChat(t, p, s, source)
			image := domain.Observation{SchemaVersion: 1, ID: "failed-image", MessageID: "10", Kind: "message.created", ConnectionID: account.ID, ConversationID: binding.ConversationID,
				Sender: domain.Sender{ID: "7", Name: "Ada"}, Text: "caption before the mention", ReplyID: "9", ReplySender: "Grace", ReplyPreview: "original reply preview",
				ReplyQuote:  &domain.TextQuote{Text: "selected source fragment", Position: 2, Manual: true},
				Attachments: []domain.Attachment{{Ref: "10:0", ExternalID: "opaque-file", Kind: scenario.kind, MediaType: "image/png", Name: "received.png"}},
				SentAt:      time.Now(), ReceivedAt: time.Now()}
			if _, err := p.Store.Archive(p.Context, image); err != nil {
				t.Fatal(err)
			}
			later, through := activeDeliveryArchive(t, p, binding, "later-mention", "mention")
			startChannels(t, s, p.Context)
			activeDeliveryRouted(t, p, later.ID)
			testkit.Await(t, p.Context, func() bool { return p.Requests.Load() == 1 })
			p.Idle(t, binding.SessionID)
			request := <-requests
			text := activeDeliveryText(request)
			for _, fragment := range []string{image.Text, image.ReplyPreview, image.ReplyQuote.Text, later.Text, "Image preview unavailable", "download-url", `trigger="mention"`} {
				if !strings.Contains(text, fragment) {
					t.Errorf("model input lost %q after the image failure: %s", fragment, text)
				}
			}
			if strings.Contains(text, "provider.invalid") || strings.Contains(text, "do-not-expose") || strings.Contains(text, "truncated provider response") {
				t.Fatal("model input exposed the raw source error")
			}
			var position cursor
			testkit.Await(t, p.Context, func() bool {
				return p.Store.Get(p.Context, "inbox_cursors", binding.ID, &position) == nil && position.Sequence >= through
			})
			view, err := p.Harness.InspectContext(p.Context, binding.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, message := range view.Request.Messages {
				if message.ContextKind != "im.observation" {
					continue
				}
				var meta messages.Metadata
				if err := json.Unmarshal(message.Metadata, &meta); err != nil {
					t.Fatal(err)
				}
				if meta.EventID != image.ID {
					continue
				}
				found = true
				if len(meta.Attachments) != 1 || meta.Attachments[0] != image.Attachments[0] || len(meta.Files) != 0 {
					t.Fatal("failed preview changed the received attachment metadata or saved a Computer file")
				}
				for _, part := range message.Content {
					if part.Type == "image" {
						t.Fatal("failed image created an invalid native image")
					}
				}
			}
			if !found || source.downloads.Load() != 1 || p.Requests.Load() != 1 {
				t.Fatalf("image was lost, retried or duplicated: found=%v downloads=%d requests=%d", found, source.downloads.Load(), p.Requests.Load())
			}
			if err := s.deliver(p.Context, binding, image); err != nil || source.downloads.Load() != 1 {
				t.Fatal("replaying an admitted unavailable image tried its source again:", err)
			}
			files, err := list[domain.FileRef](p.Context, p.Store, "files")
			if err != nil || len(files) != 0 {
				t.Fatal("failed preview reception registered attachment files")
			}
		})
	}
}

func TestImagePreviewCancellationRemainsRetryable(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancelled"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("received after retry") })
			s := New(p.Store, p.Harness)
			var cancel context.CancelFunc
			var ctx context.Context
			source := &inboxImageAdapter{download: func(ctx context.Context, _ domain.Attachment) (Download, error) {
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				return Download{}, ctx.Err()
			}}
			account, binding := imageFailureChat(t, p, s, source)
			if deadline {
				ctx, cancel = context.WithTimeout(p.Context, 100*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(p.Context)
			}
			defer cancel()
			event := domain.Observation{SchemaVersion: 1, ID: "cancelled-image", MessageID: "10", ConnectionID: account.ID, ConversationID: binding.ConversationID, Trigger: "mention",
				Attachments: []domain.Attachment{{Ref: "10:0", ExternalID: "original", Kind: "image", MediaType: "image/png"}}}
			err := s.deliver(ctx, binding, event)
			if !errors.Is(err, ctx.Err()) {
				t.Fatalf("cancelled admission was silently accepted: %v", err)
			}
			if source.downloads.Load() != 1 {
				t.Fatal("cancellation test did not reach optional image acquisition")
			}
			if _, err := p.Harness.Receipt(p.Context, binding.SessionID, event.ID+":trigger"); !domain.IsMissing(err) {
				t.Fatal("cancelled image admission created a receipt:", err)
			}
			data := observationPNG(t, 2, 2)
			source.download = func(context.Context, domain.Attachment) (Download, error) {
				return Download{Body: io.NopCloser(bytes.NewReader(data))}, nil
			}
			if err := s.deliver(p.Context, binding, event); err != nil {
				t.Fatal("cancelled image could not be retried:", err)
			}
			p.Idle(t, binding.SessionID)
			if p.Requests.Load() != 1 {
				t.Fatal("retry did not wake the Agent exactly once")
			}
		})
	}
}

func TestImagePreviewRequiredSourceErrorsDoNotBecomeUnavailableNotes(t *testing.T) {
	for _, scenario := range []string{"adapter-credentials", "media-credentials", "file-permission"} {
		t.Run(scenario, func(t *testing.T) {
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("must not run") })
			s := New(p.Store, p.Harness)
			source := &inboxImageAdapter{download: func(context.Context, domain.Attachment) (Download, error) {
				return Download{}, &attachmentPrerequisiteError{domain.Forbidden("local media key access denied")}
			}}
			account, binding := imageFailureChat(t, p, s, source)
			attachment := domain.Attachment{Ref: "10:0", ExternalID: "original", Kind: "image", MediaType: "image/png"}
			if scenario == "adapter-credentials" {
				s.Registry["telegram.bot"] = func(domain.Connection, string, *http.Client) (Adapter, error) {
					return nil, domain.Forbidden("local account access denied")
				}
			}
			if scenario == "file-permission" {
				ref := domain.FileRef{Meta: domain.Meta{ID: "foreign-image"}, AgentID: "another-bot", ComputerID: "other-computer", MediaType: "image/png"}
				if err := p.Store.Save(p.Context, "files", &ref.Meta, &ref); err != nil {
					t.Fatal(err)
				}
				attachment.FileURI = "astral-file://other-computer/foreign-image"
			}
			event := domain.Observation{SchemaVersion: 1, ID: "required-source-error", MessageID: "10", ConnectionID: account.ID, ConversationID: binding.ConversationID, Trigger: "mention", Attachments: []domain.Attachment{attachment}}
			var fault *domain.Error
			if err := s.deliver(p.Context, binding, event); !errors.As(err, &fault) || fault.Status != http.StatusForbidden {
				t.Fatalf("required source failure was lost: %v", err)
			}
			if _, err := p.Harness.Receipt(p.Context, binding.SessionID, event.ID+":trigger"); !domain.IsMissing(err) {
				t.Fatal("required source failure created a receipt:", err)
			}
			if p.Requests.Load() != 0 {
				t.Fatal("required source failure woke the Agent")
			}
		})
	}
}

func TestExpiredImageFileReferenceDoesNotBlockDelivery(t *testing.T) {
	for _, scenario := range []string{"missing-reference", "deleted-source", "changed-source"} {
		t.Run(scenario, func(t *testing.T) {
			p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("received expired image metadata") })
			s := New(p.Store, p.Harness)
			source := &inboxImageAdapter{download: func(context.Context, domain.Attachment) (Download, error) {
				t.Fatal("Computer file preview tried to download a platform attachment")
				return Download{}, nil
			}}
			account, binding := imageFailureChat(t, p, s, source)
			uri := "astral-file://source-computer/expired-file"
			if scenario != "missing-reference" {
				caller, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Daemons[0].Credentials.ComputerID, "")
				if err != nil {
					t.Fatal(err)
				}
				ref := domain.FileRef{Meta: domain.Meta{ID: "expired-file"}, AgentID: p.Agent.ID, ComputerID: caller.ComputerID,
					Path: filepath.Join(caller.CWD, "image.png"), DisplayName: "image.png", MediaType: "image/png", Version: "old-version"}
				if scenario == "changed-source" {
					if err := os.WriteFile(ref.Path, observationPNG(t, 2, 2), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := p.Store.Save(p.Context, "files", &ref.Meta, &ref); err != nil {
					t.Fatal(err)
				}
				uri = filestream.URL(ref)
			}
			event := domain.Observation{SchemaVersion: 1, ID: "expired-file", MessageID: "10", ConnectionID: account.ID, ConversationID: binding.ConversationID, Trigger: "mention",
				Attachments: []domain.Attachment{{Ref: "10:0", Kind: "image", MediaType: "image/png", FileURI: uri}}}
			if err := s.deliver(p.Context, binding, event); err != nil {
				t.Fatal("expired Computer snapshot blocked image metadata delivery:", err)
			}
			p.Idle(t, binding.SessionID)
			if p.Requests.Load() != 1 {
				t.Fatal("expired Computer snapshot did not wake the Agent")
			}
		})
	}
}
