package channels

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

// Historical live references registered metadata for a path but no snapshot.
func historicalLiveFile(t *testing.T, p *testkit.Platform, b domain.ExecutionBinding, path string) domain.FileRef {
	t.Helper()
	ref := domain.FileRef{Meta: domain.Meta{ID: domain.ID("file")}, AgentID: b.AgentID, ComputerID: b.ComputerID, Path: path, DisplayName: filepath.Base(path)}
	if err := p.Store.Save(p.Context, "files", &ref.Meta, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestDirectFileSendUsesSourceWithoutSnapshot(t *testing.T) {
	for _, route := range []string{"send-message-attachment", "ordered-path", "path-uri", "file-reference"} {
		t.Run(route, func(t *testing.T) {
			bot := newBot(t)
			p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
			s := New(p.Store, p.Harness)
			account := bot.connection(t, s, p.Agent.ID)
			binding := createBinding(t, p, s, account, "-100", "")
			source, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Agent.DefaultComputerID, "")
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(source.CWD, "报告 a+b %2F #.txt")
			content := "direct file contents"
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			var receipts []domain.Outbox
			if route == "send-message-attachment" {
				startChannels(t, s, p.Context)
				receipts, err = s.SendMessage(p.Context, p.Agent.ID, binding.SessionID, route, harness.MessageTarget{
					BindingID: binding.ID, ConversationID: binding.ConversationID, Source: &source,
				}, harness.MessageArgs{Attachments: []string{path}})
			} else {
				part := domain.OutboundPart{Type: "file", Path: path}
				if route == "path-uri" {
					part.Path, part.FileURI = "", filestream.PathURL(source.ComputerID, path)
				}
				if route == "file-reference" {
					ref := historicalLiveFile(t, p, source, path)
					part.Path, part.FileURI = "", filestream.URL(ref)
					// Existing live references must resolve the current size before
					// platform upload preparation, without making a file copy.
					content = "updated source file with a different length"
					if err := os.WriteFile(path, []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				receipts, err = s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{
					IntentID: route, Parts: []domain.OutboundPart{part}, SourceComputerID: source.ComputerID, SourceBinding: &source,
				})
				if err == nil && len(receipts) == 1 {
					err = s.dispatch(p.Context, receipts[0].ID)
				}
			}
			if err != nil || len(receipts) != 1 || len(receipts[0].Parts) != 1 {
				t.Fatalf("send failed: %#v, %v", receipts, err)
			}
			part := receipts[0].Parts[0]
			if part.FileRefID != "" || part.File == nil || part.File.URI != filestream.PathURL(source.ComputerID, path) || part.File.Size != int64(len(content)) {
				t.Fatalf("missing live source: %+v", part)
			}
			files, err := list[domain.FileRef](p.Context, p.Store, "files")
			wantReferences := 0
			if route == "file-reference" {
				wantReferences = 1
			}
			if err != nil || len(files) != wantReferences {
				t.Fatalf("send registered extra file references: %d, %v", len(files), err)
			}
			var sent domain.Outbox
			if err := p.Store.Get(p.Context, "outbox", receipts[0].ID, &sent); err != nil || len(sent.PlatformMessageIDs) != 1 {
				t.Fatalf("missing accepted message: %+v %v", sent, err)
			}
			ref := "outbox:" + sent.ID + ":0"
			msgID := sent.PlatformMessageIDs[0]
			download, _, err := s.AttachmentContent(p.Context, source, binding.ConversationID, msgID, ref)
			if err != nil {
				t.Fatal(err)
			}
			bytes, err := io.ReadAll(download.Body)
			_ = download.Body.Close()
			if err != nil || string(bytes) != content {
				t.Fatalf("archived attachment lost its source: %q %v", bytes, err)
			}
			if route == "path-uri" {
				attachment := domain.Attachment{Ref: ref, FileURI: part.File.URI}
				for _, bad := range []struct{ uri, message string }{{filestream.PathURL(source.ComputerID, filepath.Join(source.CWD, "secrets")), msgID}, {part.File.URI, "unrelated-message"}} {
					attachment.FileURI = bad.uri
					if _, err := s.downloadSource(p.Context, source, binding.ConversationID, bad.message, attachment, account); err == nil {
						t.Fatal("unverified incoming Computer path was opened")
					}
				}
			}
			assertNoSendAttachmentCopies(t, source.AgentHome)
			bot.mu.Lock()
			defer bot.mu.Unlock()
			if len(bot.uploads) != 1 || string(bot.uploads[0]) != content {
				t.Fatalf("platform did not receive the original file: %q", bot.uploads)
			}
		})
	}
}

func TestDirectFileSendReadsSourceAtDispatch(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	account := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, account, "-100", "")
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source.CWD, "report.txt")
	if err := os.WriteFile(path, []byte("initial bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	receipts, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{
		IntentID: "read-at-dispatch", Path: path, SourceComputerID: source.ComputerID, SourceBinding: &source,
	})
	if err != nil || len(receipts) != 1 {
		t.Fatalf("enqueue failed: %#v, %v", receipts, err)
	}
	if err := os.WriteFile(path, []byte("current bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.dispatch(p.Context, receipts[0].ID); err != nil {
		t.Fatal(err)
	}
	assertNoSendAttachmentCopies(t, source.AgentHome)
	bot.mu.Lock()
	defer bot.mu.Unlock()
	if len(bot.uploads) != 1 || string(bot.uploads[0]) != "current bytes" {
		t.Fatalf("send read an earlier copy instead of the source: %q", bot.uploads)
	}
}

func TestDirectFileSendRejectsRevokedReferenceBeforeAnyFragment(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	account := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, account, "-100", "")
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Daemons[0].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source.CWD, "report.txt")
	if err := os.WriteFile(path, []byte("restricted file"), 0600); err != nil {
		t.Fatal(err)
	}
	ref := historicalLiveFile(t, p, source, path)
	p.Agent.AllowedComputers = []string{p.Daemons[1].Credentials.ComputerID}
	p.Agent.DefaultComputerID = p.Daemons[1].Credentials.ComputerID
	if err := p.Harness.SaveAgent(p.Context, &p.Agent); err != nil {
		t.Fatal(err)
	}
	receipts, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{
		IntentID: "revoked-file-reference", Parts: []domain.OutboundPart{
			{Type: "text", Text: "Do not send this before the attachment is authorized."},
			{Type: "file", FileURI: filestream.URL(ref)},
		},
	})
	if err == nil || len(receipts) != 0 || bot.sends.Load() != 0 {
		t.Fatalf("revoked source reached the send queue or platform: %#v, %v, sends=%d", receipts, err, bot.sends.Load())
	}
	queued, listErr := list[domain.Outbox](p.Context, p.Store, "outbox")
	if listErr != nil || len(queued) != 0 {
		t.Fatalf("revoked source left queued fragments: %#v, %v", queued, listErr)
	}
	assertNoSendAttachmentCopies(t, source.AgentHome)
}

func TestDirectFileSendReportsMissingQueuedSource(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	account := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, account, "-100", "")
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, binding.SessionID, p.Agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source.CWD, "report.txt")
	if err := os.WriteFile(path, []byte("temporary file"), 0600); err != nil {
		t.Fatal(err)
	}
	receipts, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{
		IntentID: "removed-source", Path: path, SourceComputerID: source.ComputerID, SourceBinding: &source,
	})
	if err != nil || len(receipts) != 1 {
		t.Fatalf("enqueue failed: %#v, %v", receipts, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.dispatch(p.Context, receipts[0].ID); err != nil {
		t.Fatal(err)
	}
	var record domain.Outbox
	if err := p.Store.Get(p.Context, "outbox", receipts[0].ID, &record); err != nil {
		t.Fatal(err)
	}
	if record.Status != "failed" || strings.TrimSpace(record.Error) == "" || bot.sends.Load() != 0 {
		t.Fatalf("missing source did not fail before platform submission: %#v, sends=%d", record, bot.sends.Load())
	}
	if _, err := s.waitReceipts(p.Context, receipts); err == nil {
		t.Fatal("the caller did not receive the source read error")
	}
	assertNoSendAttachmentCopies(t, source.AgentHome)
}

func assertNoSendAttachmentCopies(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, ".astral", "attachments")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("sending created an attachment copy directory: %s, %v", path, err)
	}
}
