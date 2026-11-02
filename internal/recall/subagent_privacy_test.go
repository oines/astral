package recall

import (
	"testing"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestPrivateSessionReadsPublicAndItsWholeDelegatedHistoryTree(t *testing.T) {
	f := newFixture(t)
	f.session(t, "private-root", "agent")
	f.append(t, "private-root", message(core.RoleUser, "needle root history"))
	update := func(id, parent string, private bool) {
		t.Helper()
		var record domain.Session
		if err := f.store.Get(f.ctx, "sessions", id, &record); err != nil {
			t.Fatal(err)
		}
		record.Private, record.SubagentParentID = private, parent
		if parent != "" {
			record.ParentSessionID = ""
		}
		if err := f.store.Save(f.ctx, "sessions", &record.Meta, &record); err != nil {
			t.Fatal(err)
		}
	}
	update("private-root", "", true)
	rootState, err := f.core.LoadState(f.ctx, "private-root")
	if err != nil {
		t.Fatal(err)
	}
	f.fork(t, "private-root", "shared-child", rootState.Branch.HeadID)
	update("shared-child", "private-root", true)
	f.append(t, "shared-child", message(core.RoleAssistant, "needle shared child result"))
	for _, id := range []string{"fresh-grandchild", "public", "unrelated-private"} {
		f.session(t, id, "agent")
		f.append(t, id, message(core.RoleUser, "needle "+id+" first"), message(core.RoleAssistant, "needle "+id+" second"))
	}
	update("fresh-grandchild", "shared-child", true)
	update("unrelated-private", "", true)
	f.fork(t, "private-root", "ordinary-private-fork", rootState.Branch.HeadID)
	f.session(t, "foreign-public", "foreign-agent")
	f.append(t, "foreign-public", message(core.RoleUser, "needle foreign public"))
	root := persistence.WithSessionScope(f.ctx, "agent", "private-root")
	allowed := map[string]bool{"private-root": true, "shared-child": true, "fresh-grandchild": true, "public": true}
	listed, err := f.recall.List(root, "agent", ListRequest{})
	if err != nil || len(listed.Items) != len(allowed) {
		t.Fatalf("wrong private caller catalog: %+v %v", listed, err)
	}
	for _, item := range listed.Items {
		if !allowed[item.ID] {
			t.Fatalf("catalog exposed unrelated private or foreign Session: %s", item.ID)
		}
	}
	searched, err := f.recall.Search(root, "agent", SearchRequest{Query: "needle"})
	if err != nil || len(searched.Hits) == 0 {
		t.Fatalf("root could not search delegated histories: %+v %v", searched, err)
	}
	for _, hit := range searched.Hits {
		if !allowed[hit.SessionID] {
			t.Fatalf("search exposed unrelated private or foreign Session: %+v", hit)
		}
	}
	for id := range allowed {
		if page, err := f.recall.Read(root, "agent", id, ReadRequest{}); err != nil || len(page.Items) == 0 {
			t.Fatalf("private root cannot read %s: %+v %v", id, page, err)
		}
		if page, err := f.recall.Search(root, "agent", SearchRequest{Query: "needle", SessionIDs: []string{id}}); err != nil || len(page.Hits) == 0 {
			t.Fatalf("private root cannot explicitly search %s: %+v %v", id, page, err)
		}
	}
	child := persistence.WithSessionScope(f.ctx, "agent", "shared-child")
	for _, id := range []string{"shared-child", "fresh-grandchild", "public"} {
		if _, err := f.recall.Read(child, "agent", id, ReadRequest{}); err != nil {
			t.Fatalf("private child cannot read %s: %v", id, err)
		}
	}
	for _, id := range []string{"private-root", "unrelated-private", "ordinary-private-fork"} {
		if _, err := f.recall.Read(child, "agent", id, ReadRequest{}); !domain.IsMissing(err) {
			t.Fatalf("private child read unrelated target %s: %v", id, err)
		}
	}
	for _, id := range []string{"unrelated-private", "ordinary-private-fork"} {
		if _, err := f.recall.Read(root, "agent", id, ReadRequest{}); !domain.IsMissing(err) {
			t.Fatalf("root read unrelated target %s: %v", id, err)
		}
	}
	if _, err := f.recall.Read(root, "agent", "foreign-public", ReadRequest{}); err == nil {
		t.Fatal("private caller read another Bot's public Session")
	}
	// References and snapshots recheck the current delegation relationship.
	oldSearch, err := f.recall.Search(root, "agent", SearchRequest{Query: "needle", SessionIDs: []string{"fresh-grandchild"}, Limit: 1})
	if err != nil || oldSearch.NextCursor == "" || len(oldSearch.Hits) != 1 {
		t.Fatalf("capture descendant search snapshot: %+v %v", oldSearch, err)
	}
	oldList, err := f.recall.List(root, "agent", ListRequest{Limit: 1})
	if err != nil || oldList.NextCursor == "" {
		t.Fatalf("capture descendant catalog snapshot: %+v %v", oldList, err)
	}
	update("shared-child", "unrelated-private", true)
	if _, err := f.recall.Read(root, "agent", "fresh-grandchild", ReadRequest{Around: oldSearch.Hits[0].Ref}); !domain.IsMissing(err) {
		t.Fatalf("old reference retained revoked ancestor access: %v", err)
	}
	if _, err := f.recall.Search(root, "agent", SearchRequest{Cursor: oldSearch.NextCursor}); !domain.IsMissing(err) {
		t.Fatalf("old search cursor retained revoked ancestor access: %v", err)
	}
	if _, err := f.recall.List(root, "agent", ListRequest{Cursor: oldList.NextCursor}); !domain.IsMissing(err) {
		t.Fatalf("old catalog cursor retained revoked ancestor access: %v", err)
	}
}
