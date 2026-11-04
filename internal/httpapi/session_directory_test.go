package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/testkit"
)

type directoryReadStore struct {
	core.Store
	paths atomic.Int64
}

func (s *directoryReadStore) Scan(ctx context.Context, rollout string, query core.Query) (core.Page, error) {
	if query.PathHead != "" {
		s.paths.Add(1)
	}
	return s.Store.Scan(ctx, rollout, query)
}

func TestSessionDirectoryStaysColdAndDetailsRemainComplete(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("No model request is expected.") })
	models := p.Harness.Models
	if err := p.Harness.Close(p.Context); err != nil {
		t.Fatal(err)
	}
	counted := &directoryReadStore{Store: p.CoreStore}
	p.Harness = harness.New(p.Store, counted, models, p.Hub)
	_, server := apiFixture(t, p)
	for range 3 {
		response := request(t, p.Context, server, http.MethodGet, "/api/v1/sessions", nil, true)
		var rows []map[string]any
		err := json.NewDecoder(response.Body).Decode(&rows)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || len(rows) != 1 {
			t.Fatalf("directory response differs: rows=%+v status=%d err=%v", rows, response.StatusCode, err)
		}
		row := rows[0]
		if row["id"] != p.Session.ID || row["status"] != "idle" || row["model_available"] != true {
			t.Fatalf("directory omitted its real status: %+v", row)
		}
		for _, field := range []string{"pending", "approvals", "unresolved", "can_continue", "context_usage", "event_sequence"} {
			if _, exists := row[field]; exists {
				t.Fatalf("directory included execution-only field %s", field)
			}
		}
	}
	if counted.paths.Load() != 0 {
		t.Fatalf("directory loaded active conversation paths %d times", counted.paths.Load())
	}
	response := request(t, p.Context, server, http.MethodGet, "/api/v1/sessions/"+p.Session.ID, nil, true)
	var detail map[string]any
	err := json.NewDecoder(response.Body).Decode(&detail)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("execution detail failed: status=%d err=%v", response.StatusCode, err)
	}
	for _, field := range []string{"pending", "can_continue", "event_sequence", "rollout_id"} {
		if _, exists := detail[field]; !exists {
			t.Fatalf("full detail lost field %s", field)
		}
	}
	if counted.paths.Load() == 0 || p.Requests.Load() != 0 {
		t.Fatalf("detail must open its history without generating: paths=%d requests=%d", counted.paths.Load(), p.Requests.Load())
	}
}
