package channels

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type deletionOperationTransport func(*http.Request) (*http.Response, error)

func (f deletionOperationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestDeletedBotRejectsLateTelegramOperationArchive(t *testing.T) {
	// Exercise both the returned-message projection and the delete receipt
	// projection after an actual successful Telegram HTTP response.
	for _, method := range []string{"editMessageText", "deleteMessage"} {
		t.Run(method, func(t *testing.T) {
			bot := newBot(t)
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
			s := New(p.Store, p.Harness)
			connection := bot.connection(t, s, p.Agent.ID)
			binding := createBinding(t, p, s, connection, "-100", "")
			other := p.Agent
			other.Meta, other.Name = domain.Meta{}, "Other Bot"
			if err := p.Harness.SaveAgent(p.Context, &other); err != nil {
				t.Fatal(err)
			}
			otherSession, err := p.Harness.CreateSession(p.Context, other.ID, "Keep this history")
			if err != nil {
				t.Fatal(err)
			}
			arrived, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			s.Client = &http.Client{Transport: deletionOperationTransport(func(request *http.Request) (*http.Response, error) {
				response, err := http.DefaultTransport.RoundTrip(request)
				if err != nil || !strings.HasSuffix(request.URL.Path, "/"+method) {
					return response, err
				}
				close(arrived)
				select {
				case <-release:
					return response, nil
				case <-request.Context().Done():
					_ = response.Body.Close()
					return nil, request.Context().Err()
				}
			})}
			done := make(chan error, 1)
			parameters := map[string]any{"message_id": float64(701)}
			if method == "editMessageText" {
				parameters["text"] = "Late operation result"
			}
			go func() {
				_, err := s.TelegramOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: binding.SessionID}, connection.ID, method,
					TelegramRequest{ConversationID: binding.ConversationID, Parameters: parameters})
				done <- err
			}()
			select {
			case <-arrived:
			case err := <-done:
				t.Fatal("operation failed before the delayed response:", err)
			case <-p.Context.Done():
				t.Fatal(p.Context.Err())
			}
			if err := p.Harness.DeleteAgent(p.Context, p.Agent.ID, p.Agent.Revision); err != nil {
				t.Fatal(err)
			}
			var removed domain.Agent
			if err := p.Store.Get(p.Context, "agents", p.Agent.ID, &removed); !domain.IsMissing(err) {
				t.Fatal("Bot deletion did not finish before releasing the response:", err)
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-done:
				if !domain.IsMissing(err) {
					t.Fatal("late operation did not reject deleted ownership:", err)
				}
			case <-p.Context.Done():
				t.Fatal(p.Context.Err())
			}
			observations, err := p.Store.Observations(p.Context, binding.ConversationID, 0, 100)
			if err != nil || len(observations) != 0 {
				t.Fatalf("late response resurrected the deleted Bot's archive: %+v %v", observations, err)
			}
			if _, err := p.Harness.View(p.Context, otherSession.ID); err != nil {
				t.Fatal("another Bot's Session was affected:", err)
			}
			if _, err := p.Harness.CreateSession(p.Context, other.ID, "Still available"); err != nil {
				t.Fatal("another Bot could no longer create Sessions:", err)
			}
		})
	}
}
