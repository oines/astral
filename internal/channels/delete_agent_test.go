package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

type failingBotDeleteStore struct{ core.Store }

func (*failingBotDeleteStore) DeleteSession(context.Context, string) error {
	return errors.New("interrupted Bot cleanup")
}

type deletionAdapter struct {
	Adapter
	poll func(context.Context) (Batch, error)
	send func(context.Context) (Receipt, error)
}

func (a *deletionAdapter) Poll(ctx context.Context, _ string) (Batch, error) { return a.poll(ctx) }
func (a *deletionAdapter) Send(ctx context.Context, _ Send) (Receipt, error) { return a.send(ctx) }

func TestDeleteAgentDrainsOwnIMWorkersAndErasesChannelStore(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("no send") })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, c, "-100", "")
	other := p.Agent
	other.Meta, other.Name = domain.Meta{}, "other Bot"
	if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
		t.Fatal(err)
	}
	otherConnection := c
	otherConnection.Meta, otherConnection.AgentID, otherConnection.AccountID = domain.Meta{ID: domain.ID("connection")}, other.ID, "other-account"
	// Give the other account an independent credential, as real connections do.
	if err := p.Store.Update(p.Context, func(tx *persistence.Tx) error {
		var err error
		otherConnection.CredentialID, err = tx.PutSecret(p.Context, "test-token")
		if err != nil {
			return err
		}
		return tx.Save(p.Context, "connections", &otherConnection.Meta, &otherConnection)
	}); err != nil {
		t.Fatal(err)
	}
	event := domain.Observation{ID: "owned-archive", ConnectionID: c.ID, ConversationID: binding.ConversationID, ExternalEventID: "owned", Text: "private platform message"}
	if _, err := p.Store.Archive(p.Context, event); err != nil {
		t.Fatal(err)
	}
	queued, err := s.Enqueue(p.Context, p.Agent.ID, binding.SessionID, SendRequest{IntentID: "in-flight", Text: "owned send"})
	if err != nil {
		t.Fatal(err)
	}
	pollStarted, sendStarted := make(chan struct{}), make(chan struct{})
	otherStarted := make(chan struct{})
	pollCanceled, sendCanceled, otherCanceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	factory := s.Registry[c.Adapter]
	s.Registry[c.Adapter] = func(connection domain.Connection, token string, client *http.Client) (Adapter, error) {
		base, err := factory(connection, token, client)
		if err != nil {
			return nil, err
		}
		if connection.AgentID == other.ID {
			return &deletionAdapter{Adapter: base, poll: func(ctx context.Context) (Batch, error) {
				close(otherStarted)
				<-ctx.Done()
				close(otherCanceled)
				return Batch{}, ctx.Err()
			}}, nil
		}
		return &deletionAdapter{Adapter: base, poll: func(ctx context.Context) (Batch, error) {
			close(pollStarted)
			<-ctx.Done()
			close(pollCanceled)
			<-release
			return Batch{NextCursor: "late-cursor", Events: []Event{{Conversation: domain.Conversation{Meta: domain.Meta{ID: binding.ConversationID}, ConnectionID: c.ID, Title: "late chat"}, Observation: event}}}, nil
		}, send: func(ctx context.Context) (Receipt, error) {
			close(sendStarted)
			<-ctx.Done()
			close(sendCanceled)
			<-release
			return Receipt{}, &SendError{Message: "canceled", Unknown: true}
		}}, nil
	}
	startChannels(t, s, p.Context)
	for _, ready := range []<-chan struct{}{pollStarted, sendStarted, otherStarted} {
		select {
		case <-ready:
		case <-p.Context.Done():
			t.Fatal(p.Context.Err())
		}
	}
	done := make(chan error, 1)
	go func() { done <- p.Harness.DeleteAgent(p.Context, p.Agent.ID, p.Agent.Revision) }()
	for _, stopped := range []<-chan struct{}{pollCanceled, sendCanceled} {
		select {
		case <-stopped:
		case <-p.Context.Done():
			t.Fatal(p.Context.Err())
		}
	}
	select {
	case err := <-done:
		t.Fatal("deletion returned before in-flight platform calls finished:", err)
	default:
	}
	select {
	case <-otherCanceled:
		t.Fatal("another Bot's poller was canceled")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-p.Context.Done():
		t.Fatal(p.Context.Err())
	}
	for _, kind := range []string{"connections", "conversations", "bindings", "outbox", "channel_cursors", "inbox_cursors", "probe_cursors", "channel_commands"} {
		records, err := p.Store.Retained(p.Context, kind)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if kind != "connections" || record.ID != otherConnection.ID {
				t.Fatalf("deleted Bot channel data retained: %s %s", kind, record.ID)
			}
		}
	}
	if _, err = p.Store.Observation(p.Context, event.ID); !domain.IsMissing(err) {
		t.Fatal("platform archive retained:", err)
	}
	if _, err = p.Store.Intent(p.Context, queued[0].GroupID); !domain.IsMissing(err) {
		t.Fatal("send intent retained:", err)
	}
	if _, err = p.CoreStore.LoadState(p.Context, binding.SessionID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("bound IM Session history retained:", err)
	}
	if _, err = s.Bind(p.Context, binding.ConversationID, ""); !domain.IsMissing(err) {
		t.Fatal("deleted chat accepted a new binding:", err)
	}
	select {
	case <-otherCanceled:
		t.Fatal("another Bot's poller stopped after deletion")
	case <-time.After(10 * time.Millisecond):
	}
}

func TestDeleteAgentIntentBlocksNewIMConfigurationAndBindings(t *testing.T) {
	bot := newBot(t)
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
	s := New(p.Store, p.Harness)
	c := bot.connection(t, s, p.Agent.ID)
	binding := createBinding(t, p, s, c, "-100", "")
	p.Harness.CoreStore = &failingBotDeleteStore{Store: p.CoreStore}
	if err := p.Harness.DeleteAgent(p.Context, p.Agent.ID, p.Agent.Revision); err == nil {
		t.Fatal("interruption not surfaced")
	}
	if _, err := s.Bind(p.Context, binding.ConversationID, ""); !domain.IsMissing(err) {
		t.Fatal("deleted Bot admitted an IM Session:", err)
	}
	if err := s.SaveConnection(p.Context, &c, nil); !domain.IsMissing(err) {
		t.Fatal("deleted Bot account was re-enabled:", err)
	}
	conversation := domain.Conversation{Meta: domain.Meta{ID: binding.ConversationID}, ConnectionID: c.ID, Title: "late poll result"}
	if err := s.saveConversation(p.Context, &conversation); !domain.IsMissing(err) {
		t.Fatal("late poll rebuilt a deleted Bot chat:", err)
	}
	p.Harness.CoreStore = p.CoreStore
	if err := p.Harness.DeleteAgent(p.Context, p.Agent.ID, p.Agent.Revision); err != nil {
		t.Fatal("retry:", err)
	}
	if _, err := s.archiveEvent(p.Context, domain.Observation{ID: "late-operation-reply", ConnectionID: c.ID, ConversationID: binding.ConversationID, ExternalEventID: "late"}); !domain.IsMissing(err) {
		t.Fatal("late platform operation recreated the deleted archive:", err)
	}
	if _, err := p.Store.Observation(p.Context, "late-operation-reply"); !domain.IsMissing(err) {
		t.Fatal("late platform operation record retained:", err)
	}
}

func TestDeleteAgentErasesOwnedWeixinWireAndCredentials(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
	New(p.Store, p.Harness)
	c := domain.Connection{Meta: domain.Meta{ID: "weixin-account"}, AgentID: p.Agent.ID, Adapter: "weixin.ilink"}
	credential := WeixinCredential{BotID: "weixin-bot"}
	w := &weixinAdapter{connection: c, credential: credential}
	chat := domain.Conversation{Meta: domain.Meta{ID: "weixin-chat"}, ConnectionID: c.ID, ExternalID: "peer"}
	var tokens []string
	err := p.Store.Update(p.Context, func(tx *persistence.Tx) error {
		var err error
		c.CredentialID, err = tx.PutSecret(p.Context, string(encoded(credential)))
		if err != nil {
			return err
		}
		tokens = append(tokens, c.CredentialID)
		if err = tx.Save(p.Context, "connections", &c.Meta, &c); err != nil {
			return err
		}
		if err = tx.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
			return err
		}
		for _, id := range []string{w.wireID("context", "peer"), w.wireID("media", "media-id")} {
			record := weixinWireRecord{Meta: domain.Meta{ID: id}, ConnectionID: c.ID}
			record.CredentialID, err = tx.PutSecret(p.Context, "private-protocol-token")
			if err != nil {
				return err
			}
			tokens = append(tokens, record.CredentialID)
			if err = tx.Save(p.Context, "weixin_wire", &record.Meta, &record); err != nil {
				return err
			}
		}
		other := weixinWireRecord{Meta: domain.Meta{ID: "other-private-wire"}, ConnectionID: "other-account"}
		if err = tx.Save(p.Context, "weixin_wire", &other.Meta, &other); err != nil {
			return err
		}
		_, err = tx.Archive(p.Context, domain.Observation{ID: "weixin-message", ConnectionID: c.ID, ConversationID: chat.ID, ExternalEventID: "message", Attachments: []domain.Attachment{{ExternalID: "media-id"}}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Harness.DeleteAgent(p.Context, p.Agent.ID, p.Agent.Revision); err != nil {
		t.Fatal(err)
	}
	items, err := p.Store.Retained(p.Context, "weixin_wire")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		var record weixinWireRecord
		if err := json.Unmarshal(item.Data, &record); err != nil || record.ID != "other-private-wire" {
			t.Fatalf("owned protocol cache survived: %s %v", item.ID, err)
		}
	}
	for _, token := range tokens {
		if _, err = p.Store.Secret(p.Context, token); err == nil {
			t.Fatal("owned protocol credential survived")
		}
	}
}

func TestWeixinWireOwnershipMaintenanceAndDeletionPreflight(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return nil })
	New(p.Store, p.Harness)
	c := domain.Connection{Meta: domain.Meta{ID: "legacy-weixin-account"}, AgentID: p.Agent.ID, Adapter: "weixin.ilink", AccountID: "legacy-bot"}
	w := &weixinAdapter{connection: c, credential: WeixinCredential{BotID: c.AccountID}}
	chat := domain.Conversation{Meta: domain.Meta{ID: "legacy-weixin-chat"}, ConnectionID: c.ID, ExternalID: "known-peer"}
	var orphanCredential string
	err := p.Store.Update(p.Context, func(tx *persistence.Tx) error {
		if err := tx.Save(p.Context, "connections", &c.Meta, &c); err != nil {
			return err
		}
		if err := tx.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
			return err
		}
		for _, id := range []string{w.wireID("context", chat.ExternalID), "unattributable-orphan"} {
			record := weixinWireRecord{Meta: domain.Meta{ID: id}}
			var err error
			record.CredentialID, err = tx.PutSecret(p.Context, "legacy-private-cache")
			if err != nil {
				return err
			}
			if id == "unattributable-orphan" {
				orphanCredential = record.CredentialID
			}
			if err = tx.Save(p.Context, "weixin_wire", &record.Meta, &record); err != nil {
				return err
			}
		}
		foreign := weixinWireRecord{Meta: domain.Meta{ID: "attributed-foreign-cache"}, ConnectionID: "another-account"}
		return tx.Save(p.Context, "weixin_wire", &foreign.Meta, &foreign)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Harness.DeleteAgent(p.Context, p.Agent.ID, p.Agent.Revision); err == nil {
		t.Fatal("anonymous protocol cache silently survived Bot deletion")
	}
	var agent domain.Agent
	var session domain.Session
	if err = p.Store.Get(p.Context, "agents", p.Agent.ID, &agent); err != nil || agent.Deletion {
		t.Fatal("preflight failure committed Bot deletion intent:", err)
	}
	if err = p.Store.Get(p.Context, "sessions", p.Session.ID, &session); err != nil || session.Deletion != nil {
		t.Fatal("preflight failure changed a Session:", err)
	}
	if _, err = p.CoreStore.LoadState(p.Context, p.Session.ID); err != nil {
		t.Fatal("preflight failure erased history:", err)
	}
	result, err := RepairWeixinWireOwnership(p.Context, p.Store, false)
	if err != nil || result.Attributed != 1 || result.Unowned != 1 || result.Purged != 0 {
		t.Fatalf("safe repair: %+v %v", result, err)
	}
	if _, err = p.Store.Secret(p.Context, orphanCredential); err != nil {
		t.Fatal("safe repair erased unowned credential:", err)
	}
	if err = p.Harness.DeleteAgent(p.Context, p.Agent.ID, p.Agent.Revision); err == nil {
		t.Fatal("remaining anonymous cache did not block deletion")
	}
	result, err = RepairWeixinWireOwnership(p.Context, p.Store, true)
	if err != nil || result.Purged != 1 || result.Unowned != 0 || result.Attributed != 0 {
		t.Fatalf("explicit anonymous cache cleanup: %+v %v", result, err)
	}
	if _, err = p.Store.Secret(p.Context, orphanCredential); err == nil {
		t.Fatal("explicit cleanup retained anonymous credential")
	}
	if err = p.Harness.DeleteAgent(p.Context, p.Agent.ID, p.Agent.Revision); err != nil {
		t.Fatal("deletion after maintenance:", err)
	}
	var foreign weixinWireRecord
	if err = p.Store.Get(p.Context, "weixin_wire", "attributed-foreign-cache", &foreign); err != nil || foreign.ConnectionID != "another-account" {
		t.Fatal("maintenance erased another attributed account cache:", err)
	}
}
