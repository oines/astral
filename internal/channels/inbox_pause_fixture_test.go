package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/testkit"
)

type unavailableTransport func(*http.Request) (*http.Response, error)

func (f unavailableTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Real Core/SQLite/SDK fixture; the HTTP transport is entirely offline.
func unavailableFixtureWithPending(t *testing.T, pending int) (*testkit.Platform, *Service, domain.ConversationBinding, *atomic.Bool, *atomic.Int64) {
	t.Helper()
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { t.Error("workload used a network model"); return nil })
	s := New(p.Store, p.Harness)
	b := activeDeliveryChat(t, p, s, false, false)
	session, err := p.Harness.Core(p.Context, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary readable text, about 1.5 KiB per entry, not opaque provider data.
	for i := range 40 {
		text := fmt.Sprintf("Earlier conversation %d: ", i) + strings.Repeat("核对资料日期和统计口径，记录已确认事项及下一步。", 20)
		if _, err = session.InjectContext(p.Context, core.Input{ID: fmt.Sprintf("prior-%d", i), Contexts: []core.Message{core.User(text)}}); err != nil {
			t.Fatal(err)
		}
	}
	// Seed the *durable queue* of a stopped previous process, without executing
	// repeated costly maintenance failures in setup. Every later call still uses the
	// real InjectContext, SDK decoder, modelbridge wrapper and compactor.
	state := session.State()
	var entries []core.Entry
	var through int64
	var conversation domain.Conversation
	var connection domain.Connection
	if err = p.Store.Get(p.Context, "conversations", b.ConversationID, &conversation); err != nil {
		t.Fatal(err)
	}
	if err = p.Store.Get(p.Context, "connections", conversation.ConnectionID, &connection); err != nil {
		t.Fatal(err)
	}
	for i := range pending {
		event, seq := activeDeliveryArchive(t, p, b, fmt.Sprintf("old-pending-%03d", i), "")
		through = seq
		contexts := []core.Message{messages.Observation(event, connection, conversation, nil, "")}
		raw, _ := json.Marshal(contexts)
		hash := sha256.Sum256(raw)
		entries = append(entries, core.Entry{ID: fmt.Sprintf("old-queue-%03d", i), SessionID: b.SessionID, Version: state.Version + 1, Time: time.Now().UTC(), Kind: core.EntryInputQueued, Input: &core.InputRecord{ID: event.ID, Kind: core.InputContext, Digest: hex.EncodeToString(hash[:]), Contexts: contexts}})
	}
	if _, err = p.CoreStore.Commit(p.Context, b.SessionID, core.Commit{ID: "previous-process-accepted-queue", ExpectedVersion: state.Version, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	if err = session.Reload(p.Context); err != nil {
		t.Fatal(err)
	}
	position := cursor{Meta: domain.Meta{ID: b.ID}, Sequence: through}
	if err = p.Store.Save(p.Context, "inbox_cursors", &position.Meta, &position); err != nil {
		t.Fatal(err)
	}
	if err = p.Store.Get(p.Context, "models", p.Profile.ID, &p.Profile); err != nil {
		t.Fatal(err)
	}
	p.Profile.Recommendations["context_window"] = testkit.JSON(32768)
	if err = p.Harness.Models.SaveProfile(p.Context, &p.Profile); err != nil {
		t.Fatal(err)
	}
	failed := &atomic.Bool{}
	failed.Store(true)
	calls := &atomic.Int64{}
	p.Harness.Models.HTTPClient.Transport = unavailableTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		status := 200
		raw, _ := json.Marshal(testkit.Text("The earlier conversation was summarized; retain the open follow-up."))
		if failed.Load() {
			status = 402
			raw = []byte(`{"error":{"type":"insufficient_quota","code":"insufficient_quota","message":"fixture credit exhausted"}}`)
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
	})
	return p, s, b, failed, calls
}

func firstPendingBatch(t *testing.T, p *testkit.Platform, id string) []string {
	t.Helper()
	page, err := p.CoreStore.Pending(p.Context, id, core.PendingQuery{Kind: core.EntryInputQueued, InputKind: core.InputContext, Limit: 64, MaxBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range page.Records {
		ids = append(ids, r.Entry.Input.ID)
	}
	return ids
}
