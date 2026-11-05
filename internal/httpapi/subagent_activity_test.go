package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/testkit"
)

// Seed the existing revision journal directly: these API tests isolate paging
// and invalidation from model execution. Lifecycle-source tests cover producer
// deduplication and the mapping from actual child Run facts.
func appendActivityHTTPFixture(t *testing.T, p *testkit.Platform, prefix string, count int, at time.Time) []domain.SubagentActivity {
	t.Helper()
	var appended []domain.SubagentActivity
	err := p.Store.Update(p.Context, func(tx *persistence.Tx) error {
		var record struct {
			domain.Meta
			AgentID  string                  `json:"agent_id"`
			Activity domain.SubagentActivity `json:"activity"`
		}
		if err := tx.Get(p.Context, persistence.SubagentActivityKind, p.Session.ID, &record); err != nil && !domain.IsMissing(err) {
			return err
		}
		record.ID, record.AgentID = p.Session.ID, p.Agent.ID
		for i := range count {
			activity := domain.SubagentActivity{
				ID: fmt.Sprintf("%s-%03d", prefix, i), SessionID: p.Session.ID,
				ChildSessionID: "historical-child", Name: "资料核查", RunID: fmt.Sprintf("run-%03d", i/2),
				Status: "started", Time: at.Add(time.Duration(i) * time.Second), Sequence: uint64(record.Revision + 1),
			}
			if i%2 != 0 {
				activity.Status = "completed"
			}
			record.Activity = activity
			if err := tx.Save(p.Context, persistence.SubagentActivityKind, &record.Meta, &record); err != nil {
				return err
			}
			appended = append(appended, activity)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return appended
}

func TestSubagentActivitySnapshotAndOlderPagesStayBounded(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("reading activity invoked a model")
		return testkit.Text("unexpected")
	})
	_, server := apiFixture(t, p)
	const count = 235
	seed := appendActivityHTTPFixture(t, p, "history", count, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	response := request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/snapshot", nil, true)
	var snapshot Snapshot
	err := json.NewDecoder(response.Body).Decode(&snapshot)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot HTTP%d: %v", response.StatusCode, err)
	}
	if snapshot.SubagentActivityCursor != strconv.Itoa(count) || snapshot.SubagentActivityNextCursor == "" || len(snapshot.SubagentActivity) == 0 || len(snapshot.SubagentActivity) > 100 {
		t.Fatalf("snapshot did not return one bounded latest activity page: cursor=%s older=%s count=%d", snapshot.SubagentActivityCursor, snapshot.SubagentActivityNextCursor, len(snapshot.SubagentActivity))
	}
	seen := map[string]domain.SubagentActivity{}
	add := func(items []domain.SubagentActivity) {
		t.Helper()
		for _, item := range items {
			if _, exists := seen[item.ID]; exists {
				t.Fatalf("activity %s repeated across older pages", item.ID)
			}
			seen[item.ID] = item
		}
	}
	add(snapshot.SubagentActivity)
	cursor := snapshot.SubagentActivityNextCursor
	for pages := 0; cursor != ""; pages++ {
		if pages > count {
			t.Fatal("activity pagination did not terminate")
		}
		response = request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/timeline?activity_cursor="+cursor+"&limit=23", nil, true)
		var page gateway.HistoryPage
		err = json.NewDecoder(response.Body).Decode(&page)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || len(page.Entries) != 0 || len(page.SubagentActivity) > 23 {
			t.Fatalf("activity-only older page returned Core entries or exceeded its budget: HTTP%d entries=%d activity=%d err=%v", response.StatusCode, len(page.Entries), len(page.SubagentActivity), err)
		}
		add(page.SubagentActivity)
		if page.SubagentActivityNextCursor == cursor {
			t.Fatal("older activity cursor did not advance")
		}
		cursor = page.SubagentActivityNextCursor
	}
	if len(seen) != count {
		t.Fatalf("older pages lost activity: got=%d want=%d", len(seen), count)
	}
	for _, want := range seed {
		got := seen[want.ID]
		if got != want {
			t.Fatalf("activity changed while paging: got=%+v want=%+v", got, want)
		}
	}
	if p.Requests.Load() != 0 {
		t.Fatal("history reads generated a reply")
	}
}

type activityHTTPNoCoreStore struct {
	core.Store
	reads atomic.Int64
}

func (s *activityHTTPNoCoreStore) LoadState(context.Context, string) (core.RuntimeState, error) {
	s.reads.Add(1)
	return core.RuntimeState{}, errors.New("activity-only request read Core state")
}
func (s *activityHTTPNoCoreStore) Get(context.Context, string, string) (core.Record, error) {
	s.reads.Add(1)
	return core.Record{}, errors.New("activity-only request read a Core entry")
}
func (s *activityHTTPNoCoreStore) Scan(context.Context, string, core.Query) (core.Page, error) {
	s.reads.Add(1)
	return core.Page{}, errors.New("activity-only request scanned Core history")
}

func TestSubagentActivityCursorOnlyDoesNotReadCoreHistory(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	appendActivityHTTPFixture(t, p, "host-only", 30, time.Now().UTC())
	blocked := &activityHTTPNoCoreStore{Store: p.Harness.CoreStore}
	p.Harness.CoreStore = blocked
	for _, tc := range []struct {
		cursor string
		status int
	}{{cursor: "20", status: http.StatusOK}, {cursor: "not-a-number", status: http.StatusBadRequest}} {
		response := request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/timeline?activity_cursor="+tc.cursor+"&limit=7", nil, true)
		if response.StatusCode != tc.status {
			response.Body.Close()
			t.Fatalf("activity cursor %q: HTTP%d want=%d", tc.cursor, response.StatusCode, tc.status)
		}
		if tc.status == http.StatusOK {
			var page gateway.HistoryPage
			if err := json.NewDecoder(response.Body).Decode(&page); err != nil {
				response.Body.Close()
				t.Fatal(err)
			}
			if len(page.Entries) != 0 || len(page.SubagentActivity) != 7 || page.SubagentActivity[0].Sequence != 19 {
				response.Body.Close()
				t.Fatalf("activity-only page did not honor its own cursor: %+v", page)
			}
		}
		response.Body.Close()
	}
	if blocked.reads.Load() != 0 {
		t.Fatalf("activity paging performed %d Core reads", blocked.reads.Load())
	}
}

func TestSubagentActivitySSEDrainsCoalescedBurstToEveryReaderAndReconnects(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	appendActivityHTTPFixture(t, p, "initial", 3, time.Now().UTC())
	first, initial := openInvalidationStream(t, p, server)
	second, _ := openInvalidationStream(t, p, server)
	if initial.SubagentActivityCursor != "3" {
		t.Fatalf("initial activity cursor=%s", initial.SubagentActivityCursor)
	}
	// One transaction produces one coalesced Store wake. More than the maximum
	// page must drain without waiting for another commit. Its source timestamps
	// are deliberately older than the snapshot, so time cannot be the cursor.
	const count = 225
	seed := appendActivityHTTPFixture(t, p, "burst", count, time.Now().UTC().Add(-time.Hour))
	for _, stream := range []*invalidationStream{first, second} {
		seen := map[string]bool{}
		cursor := uint64(3)
		deadline, cancel := context.WithTimeout(p.Context, 5*time.Second)
		for len(seen) != count {
			select {
			case frame, ok := <-stream.frames:
				if !ok || frame.err != nil || frame.kind == "resync" {
					cancel()
					t.Fatalf("SSE stopped while draining activity: %+v", frame)
				}
				items := frame.event.SubagentActivity
				if len(items) == 0 {
					continue
				}
				last := cursor
				for _, item := range items {
					if seen[item.ID] || item.Sequence <= cursor || !item.Time.Before(initial.SubagentActivity[0].Time) {
						cancel()
						t.Fatalf("SSE repeated, reordered, or retimed an activity: %+v cursor=%d", item, cursor)
					}
					seen[item.ID] = true
					last = max(last, item.Sequence)
				}
				parsed, err := strconv.ParseUint(frame.event.SubagentActivityCursor, 10, 64)
				if err != nil || parsed != last {
					cancel()
					t.Fatalf("SSE cursor skips its actual page: cursor=%q pageLast=%d err=%v", frame.event.SubagentActivityCursor, last, err)
				}
				cursor = parsed
			case <-deadline.Done():
				cancel()
				t.Fatalf("one committed burst delivered only %d/%d activities", len(seen), count)
			}
		}
		cancel()
		if cursor != uint64(count+3) {
			t.Fatalf("drained cursor=%d", cursor)
		}
		for _, activity := range seed {
			if !seen[activity.ID] {
				t.Fatalf("SSE omitted %s", activity.ID)
			}
		}
	}
	_, restored := openInvalidationStream(t, p, server)
	if restored.SubagentActivityCursor != strconv.Itoa(count+3) || restored.SubagentActivityNextCursor == "" || len(restored.SubagentActivity) > 100 {
		t.Fatalf("reconnect did not restore the durable bounded activity page: %+v", restored)
	}
}
