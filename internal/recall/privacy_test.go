package recall

import (
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestPrivateSessionsAreHiddenAndOldReferencesReauthorize(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"reader", "secret", "public"} {
		f.session(t, id, "agent")
		f.append(t, id, message(core.RoleUser, "needle "+id+" first"), message(core.RoleAssistant, "needle "+id+" second"))
	}
	reader := persistence.WithSessionScope(f.ctx, "agent", "reader")
	before, err := f.recall.Search(reader, "agent", SearchRequest{Query: "needle", SessionIDs: []string{"secret"}, Limit: 1})
	if err != nil || len(before.Hits) != 1 || before.NextCursor == "" {
		t.Fatalf("initial search: %+v %v", before, err)
	}
	listBefore, err := f.recall.List(reader, "agent", ListRequest{Limit: 1})
	if err != nil || listBefore.NextCursor == "" {
		t.Fatalf("initial list: %+v %v", listBefore, err)
	}
	var record domain.Session
	if err := f.store.Get(f.ctx, "sessions", "secret", &record); err != nil {
		t.Fatal(err)
	}
	record.Private = true
	if err := f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
	listed, err := f.recall.List(reader, "agent", ListRequest{})
	if err != nil || len(listed.Items) != 2 {
		t.Fatalf("private Session was not filtered before paging: %+v %v", listed, err)
	}
	for _, item := range listed.Items {
		if item.ID == "secret" {
			t.Fatal("private Session leaked through catalog")
		}
	}
	if _, err := f.recall.Read(reader, "agent", "secret", ReadRequest{Around: before.Hits[0].Ref}); !domain.IsMissing(err) {
		t.Fatalf("old history reference remained a grant: %v", err)
	}
	if _, err := f.recall.Search(reader, "agent", SearchRequest{Query: "needle", SessionIDs: []string{"secret"}}); !domain.IsMissing(err) {
		t.Fatalf("known private ID was searchable: %v", err)
	}
	if _, err := f.recall.Search(reader, "agent", SearchRequest{Cursor: before.NextCursor}); !domain.IsMissing(err) {
		t.Fatalf("old search cursor remained a grant: %v", err)
	}
	if _, err := f.recall.List(reader, "agent", ListRequest{Cursor: listBefore.NextCursor}); !domain.IsMissing(err) {
		t.Fatalf("old catalog cursor remained a grant: %v", err)
	}
	own := persistence.WithSessionScope(f.ctx, "agent", "secret")
	if page, err := f.recall.Read(own, "agent", "secret", ReadRequest{}); err != nil || len(page.Items) == 0 {
		t.Fatalf("private Session could not read itself: %+v %v", page, err)
	}
	if page, err := f.recall.List(f.ctx, "agent", ListRequest{}); err != nil || len(page.Items) != 3 {
		t.Fatalf("unscoped Owner lost visibility: %+v %v", page, err)
	}
}
