package persistence

import (
	"context"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
)

func TestSessionReadAccessAllowsPrivateDelegationAncestorsButNotForkOrigins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	records := map[string]domain.Session{}
	for _, value := range []domain.Session{
		{Meta: domain.Meta{ID: "root"}, AgentID: "bot", Private: true},
		{Meta: domain.Meta{ID: "child"}, AgentID: "bot", Private: true, SubagentParentID: "root"},
		{Meta: domain.Meta{ID: "grandchild"}, AgentID: "bot", Private: true, SubagentParentID: "child"},
		{Meta: domain.Meta{ID: "public"}, AgentID: "bot"},
		{Meta: domain.Meta{ID: "unrelated"}, AgentID: "bot", Private: true},
		{Meta: domain.Meta{ID: "ordinary-fork"}, AgentID: "bot", Private: true, ParentSessionID: "root"},
		{Meta: domain.Meta{ID: "foreign"}, AgentID: "foreign-bot"},
	} {
		if err := store.Save(ctx, "sessions", &value.Meta, &value); err != nil {
			t.Fatal(err)
		}
		records[value.ID] = value
	}
	for _, test := range []struct {
		caller, target string
		allowed        bool
	}{
		{"root", "root", true}, {"root", "child", true}, {"root", "grandchild", true},
		{"child", "child", true}, {"child", "grandchild", true}, {"grandchild", "grandchild", true},
		{"root", "public", true}, {"child", "public", true}, {"grandchild", "public", true},
		{"child", "root", false}, {"grandchild", "child", false},
		{"root", "unrelated", false}, {"child", "unrelated", false},
		{"root", "ordinary-fork", false}, {"root", "foreign", false}, {"unrelated", "grandchild", false},
	} {
		t.Run(test.caller+" reads "+test.target, func(t *testing.T) {
			err := CheckSessionAccess(WithSessionScope(ctx, "bot", test.caller), records[test.target], store)
			if test.allowed && err != nil || !test.allowed && !domain.IsMissing(err) {
				t.Fatalf("allowed=%v error=%v", test.allowed, err)
			}
		})
	}
	if err := CheckSessionAccess(WithSessionScope(ctx, "foreign-bot", "root"), records["grandchild"], store); !domain.IsMissing(err) {
		t.Fatalf("cross-Bot caller inherited ancestry rights: %v", err)
	}
	if err := CheckSessionAccess(WithSessionScope(ctx, "bot", ""), records["grandchild"], store); !domain.IsMissing(err) {
		t.Fatalf("empty caller inherited ancestry rights: %v", err)
	}
	if err := CheckSessionAccess(ctx, records["grandchild"], store); err != nil {
		t.Fatalf("Owner lost private descendant access: %v", err)
	}
	// Ancestor reads are explicitly enabled by read boundaries. Management
	// checks without a reader retain the earlier direct-parent scope.
	if err := CheckSessionAccess(WithSessionScope(ctx, "bot", "root"), records["grandchild"]); !domain.IsMissing(err) {
		t.Fatalf("ancestor read support broadened an existing management check: %v", err)
	}
	grand := records["grandchild"]
	grand.Deletion = &domain.SessionDeletion{}
	if err := CheckSessionAccess(WithSessionScope(ctx, "bot", "root"), grand, store); !domain.IsMissing(err) {
		t.Fatalf("deleted target was readable: %v", err)
	}
	child := records["child"]
	for _, mutate := range []func(*domain.Session){
		func(value *domain.Session) { value.SubagentParentID = "missing" },
		func(value *domain.Session) { value.SubagentParentID = "grandchild" },
		func(value *domain.Session) { value.AgentID = "foreign-bot" },
		func(value *domain.Session) { value.Deletion = &domain.SessionDeletion{} },
	} {
		changed := child
		mutate(&changed)
		if err := store.Save(ctx, "sessions", &changed.Meta, &changed); err != nil {
			t.Fatal(err)
		}
		if err := CheckSessionAccess(WithSessionScope(ctx, "bot", "root"), records["grandchild"], store); !domain.IsMissing(err) {
			t.Fatalf("broken current ancestry still granted access: %+v %v", changed, err)
		}
		child.Meta = changed.Meta
		if err := store.Save(ctx, "sessions", &child.Meta, &child); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionReadAccessUsesTransactionReaderWithoutOpeningAnotherConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	root := domain.Session{Meta: domain.Meta{ID: "root"}, AgentID: "bot", Private: true}
	child := domain.Session{Meta: domain.Meta{ID: "child"}, AgentID: "bot", Private: true, SubagentParentID: root.ID}
	grand := domain.Session{Meta: domain.Meta{ID: "grandchild"}, AgentID: "bot", Private: true, SubagentParentID: child.ID}
	other := domain.Session{Meta: domain.Meta{ID: "other"}, AgentID: "bot", Private: true}
	for _, value := range []*domain.Session{&root, &child, &grand, &other} {
		if err := store.Save(ctx, "sessions", &value.Meta, value); err != nil {
			t.Fatal(err)
		}
	}
	scoped := WithSessionScope(ctx, "bot", root.ID)
	if err := store.Update(scoped, func(tx *Tx) error {
		if err := CheckSessionAccess(scoped, grand, tx); err != nil {
			return err
		}
		child.SubagentParentID = other.ID
		if err := tx.Save(scoped, "sessions", &child.Meta, &child); err != nil {
			return err
		}
		if err := CheckSessionAccess(scoped, grand, tx); !domain.IsMissing(err) {
			t.Fatalf("access ignored uncommitted ancestry update: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatalf("transaction ancestry check failed or opened a second connection: %v", err)
	}
}
