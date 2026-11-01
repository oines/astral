package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func agentSearchFixture(t *testing.T) (context.Context, *Service) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	// Settings validation needs no running Session, model, or online Computer.
	return ctx, &Service{Store: store}
}

func saveSearchFixture(t *testing.T, ctx context.Context, service *Service) domain.SearchProvider {
	t.Helper()
	provider := domain.SearchProvider{Meta: domain.Meta{ID: domain.ID("search")}, Name: "Search"}
	if err := service.Store.Save(ctx, "search", &provider.Meta, &provider); err != nil {
		t.Fatal(err)
	}
	return provider
}

func readSearchAgent(t *testing.T, ctx context.Context, service *Service, id string) domain.Agent {
	t.Helper()
	var agent domain.Agent
	if err := service.Store.Get(ctx, "agents", id, &agent); err != nil {
		t.Fatal(err)
	}
	return agent
}

func requireSearchConflict(t *testing.T, err error) {
	t.Helper()
	var problem *domain.Error
	if !errors.As(err, &problem) || problem.Status != 409 {
		t.Fatalf("want revision conflict, got %v", err)
	}
}

func TestSaveAgentPreservesMissingSearchForUnrelatedEdits(t *testing.T) {
	ctx, service := agentSearchFixture(t)
	provider := saveSearchFixture(t, ctx, service)
	for _, id := range []string{"computer-a", "computer-b"} {
		computer := domain.Computer{Meta: domain.Meta{ID: id}}
		if err := service.Store.Save(ctx, "computers", &computer.Meta, &computer); err != nil {
			t.Fatal(err)
		}
	}
	agent := domain.Agent{Name: "Bot", SearchProviderID: provider.ID, AllowedComputers: []string{"computer-a"}, DefaultComputerID: "computer-a"}
	if err := service.SaveAgent(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	// Reproduce databases written before deletion cleaned Bot references.
	if err := service.Store.Update(ctx, func(tx *persistence.Tx) error {
		return tx.Delete(ctx, "search", provider.ID, provider.Revision)
	}); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []struct {
		name  string
		apply func(*domain.Agent)
	}{
		{"name", func(agent *domain.Agent) { agent.Name = "Renamed Bot" }},
		{"persona", func(agent *domain.Agent) { agent.Persona = "Be precise." }},
		{"grant-computer", func(agent *domain.Agent) { agent.AllowedComputers = append(agent.AllowedComputers, "computer-b") }},
		{"default-computer", func(agent *domain.Agent) { agent.DefaultComputerID = "computer-b" }},
		{"revoke-computer", func(agent *domain.Agent) { agent.AllowedComputers = []string{"computer-b"} }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			before := agent
			edit.apply(&agent)
			if err := service.SaveAgent(ctx, &agent); err != nil {
				t.Fatalf("historical search reference blocked unrelated edit: %v", err)
			}
			saved := readSearchAgent(t, ctx, service, agent.ID)
			if saved.SearchProviderID != provider.ID || saved.Revision != before.Revision+1 || !jsonEqual(saved, agent) {
				t.Fatalf("unrelated edit was not preserved: %+v", saved)
			}
		})
	}
	// An explicit repair can clear the missing selection or choose a valid one.
	agent.SearchProviderID = ""
	if err := service.SaveAgent(ctx, &agent); err != nil {
		t.Fatal("clear missing search:", err)
	}
	replacement := saveSearchFixture(t, ctx, service)
	agent.SearchProviderID = replacement.ID
	if err := service.SaveAgent(ctx, &agent); err != nil {
		t.Fatal("choose replacement search:", err)
	}
}

func TestSaveAgentRejectsNewMissingSearchSelections(t *testing.T) {
	ctx, service := agentSearchFixture(t)
	agent := domain.Agent{Meta: domain.Meta{ID: "legacy-bot"}, Name: "Bot", SearchProviderID: "legacy-missing-search", AllowedComputers: []string{}}
	if err := service.Store.Save(ctx, "agents", &agent.Meta, &agent); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		apply func(*domain.Agent)
	}{
		{"different-missing-search", func(agent *domain.Agent) { agent.SearchProviderID = "another-missing-search" }},
		{"new-bot-with-missing-search", func(agent *domain.Agent) { agent.Meta = domain.Meta{ID: "new-bot"} }},
	} {
		t.Run(change.name, func(t *testing.T) {
			candidate := agent
			change.apply(&candidate)
			if err := service.SaveAgent(ctx, &candidate); !domain.IsMissing(err) {
				t.Fatalf("want missing new search selection, got %v", err)
			}
			if saved := readSearchAgent(t, ctx, service, agent.ID); !jsonEqual(saved, agent) {
				t.Fatal("rejected selection changed existing Bot:", saved)
			}
			if candidate.ID != agent.ID {
				var saved domain.Agent
				if err := service.Store.Get(ctx, "agents", candidate.ID, &saved); !domain.IsMissing(err) {
					t.Fatal("rejected new Bot was persisted:", err)
				}
			}
		})
	}
}

func TestDeleteSearchProviderDisablesOnlyReferencingBots(t *testing.T) {
	ctx, service := agentSearchFixture(t)
	provider := saveSearchFixture(t, ctx, service)
	other := saveSearchFixture(t, ctx, service)
	agents := []domain.Agent{
		{Meta: domain.Meta{ID: "bot-a"}, Name: "A", Persona: "First persona", Avatar: "a", SearchProviderID: provider.ID, DefaultModelID: "historical-model", AllowedComputers: []string{"computer-a"}, DefaultComputerID: "computer-a"},
		{Meta: domain.Meta{ID: "bot-b"}, Name: "B", SearchProviderID: provider.ID, ProbeModelID: "historical-probe", AllowedComputers: []string{}},
		{Meta: domain.Meta{ID: "bot-deleting"}, Name: "Deleting", SearchProviderID: provider.ID, Deletion: true},
		{Meta: domain.Meta{ID: "bot-other"}, Name: "Other", SearchProviderID: other.ID},
		{Meta: domain.Meta{ID: "bot-disabled"}, Name: "Disabled"},
	}
	for i := range agents {
		agent := &agents[i]
		if err := service.Store.Save(ctx, "agents", &agent.Meta, agent); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.DeleteSearchProvider(ctx, provider.ID, provider.Revision); err != nil {
		t.Fatal(err)
	}
	var removed domain.SearchProvider
	if err := service.Store.Get(ctx, "search", provider.ID, &removed); !domain.IsMissing(err) {
		t.Fatal("search service retained:", err)
	}
	for _, before := range agents {
		saved := readSearchAgent(t, ctx, service, before.ID)
		expected := before
		if before.SearchProviderID == provider.ID {
			expected.SearchProviderID = ""
			if saved.Revision != before.Revision+1 || !saved.CreatedAt.Equal(before.CreatedAt) {
				t.Fatal("reference cleanup did not increment only the current revision:", saved)
			}
			expected.Meta = saved.Meta
		}
		if !jsonEqual(saved, expected) {
			t.Fatalf("cleanup changed unrelated Bot fields: got %+v want %+v", saved, expected)
		}
	}
	deleting := readSearchAgent(t, ctx, service, "bot-deleting")
	deleting.Name = "Cannot revive"
	deleting.Deletion = false
	if err := service.SaveAgent(ctx, &deleting); !domain.IsMissing(err) {
		t.Fatal("cleanup weakened Bot deletion boundary:", err)
	}
	// A stale complete Bot payload cannot restore the just-deleted selection.
	stale := agents[1]
	stale.Name = "Stale rename"
	requireSearchConflict(t, service.SaveAgent(ctx, &stale))
	if saved := readSearchAgent(t, ctx, service, stale.ID); saved.SearchProviderID != "" || saved.Name != agents[1].Name {
		t.Fatal("stale save restored deleted search:", saved)
	}
}

func TestDeleteSearchProviderRejectsStaleConfirmationWithoutChangingBots(t *testing.T) {
	ctx, service := agentSearchFixture(t)
	provider := saveSearchFixture(t, ctx, service)
	agent := domain.Agent{Name: "Bot", SearchProviderID: provider.ID}
	if err := service.SaveAgent(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	stale := provider.Revision
	provider.Name = "Updated search service"
	if err := service.Store.Save(ctx, "search", &provider.Meta, &provider); err != nil {
		t.Fatal(err)
	}
	requireSearchConflict(t, service.DeleteSearchProvider(ctx, provider.ID, stale))
	if saved := readSearchAgent(t, ctx, service, agent.ID); !jsonEqual(saved, agent) {
		t.Fatal("stale deletion changed Bot:", saved)
	}
	var saved domain.SearchProvider
	if err := service.Store.Get(ctx, "search", provider.ID, &saved); err != nil || !jsonEqual(saved, provider) {
		t.Fatal("stale deletion changed search service:", saved, err)
	}
}

func TestDeleteSearchProviderRollsBackAllChangesOnError(t *testing.T) {
	ctx, service := agentSearchFixture(t)
	provider := saveSearchFixture(t, ctx, service)
	agent := domain.Agent{Meta: domain.Meta{ID: "a-reference"}, Name: "Bot", SearchProviderID: provider.ID}
	if err := service.SaveAgent(ctx, &agent); err != nil {
		t.Fatal(err)
	}
	// Tx.List orders IDs: the first Bot is updated before decoding this bad
	// historical document fails, so this exercises a real partial-write rollback.
	corrupt := domain.Meta{ID: "z-corrupt-bot"}
	if err := service.Store.Save(ctx, "agents", &corrupt, map[string]any{"name": 123}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteSearchProvider(ctx, provider.ID, provider.Revision); err == nil {
		t.Fatal("malformed Bot did not fail deletion")
	}
	if saved := readSearchAgent(t, ctx, service, agent.ID); !jsonEqual(saved, agent) {
		t.Fatal("failed deletion partially updated Bot:", saved)
	}
	var saved domain.SearchProvider
	if err := service.Store.Get(ctx, "search", provider.ID, &saved); err != nil || !jsonEqual(saved, provider) {
		t.Fatal("failed deletion removed search service:", saved, err)
	}
	var phantom domain.Agent
	if err := service.Store.Version(ctx, "agents", agent.ID, agent.Revision+1, &phantom); !domain.IsMissing(err) {
		t.Fatal("failed deletion left a historical Bot revision:", err)
	}
}

func TestDeleteSearchProviderConcurrentBotWritesCannotRestoreSelection(t *testing.T) {
	ctx, service := agentSearchFixture(t)
	for iteration := range 20 {
		provider := saveSearchFixture(t, ctx, service)
		selected := domain.Agent{Name: "Selected", SearchProviderID: provider.ID}
		unselected := domain.Agent{Name: "Unselected"}
		for _, agent := range []*domain.Agent{&selected, &unselected} {
			if err := service.SaveAgent(ctx, agent); err != nil {
				t.Fatal(err)
			}
		}
		selected.Name = "Concurrent rename"
		unselected.SearchProviderID = provider.ID
		created := domain.Agent{Meta: domain.Meta{ID: fmt.Sprintf("created-%02d", iteration)}, Name: "New Bot", SearchProviderID: provider.ID}
		start := make(chan struct{})
		var group sync.WaitGroup
		var results [4]error
		for i, operation := range []func() error{
			func() error { return service.SaveAgent(ctx, &selected) },
			func() error { return service.SaveAgent(ctx, &unselected) },
			func() error { return service.SaveAgent(ctx, &created) },
			func() error { return service.DeleteSearchProvider(ctx, provider.ID, provider.Revision) },
		} {
			group.Add(1)
			go func(index int, operation func() error) {
				defer group.Done()
				<-start
				results[index] = operation()
			}(i, operation)
		}
		close(start)
		group.Wait()
		if results[3] != nil {
			t.Fatalf("concurrent delete %d failed: %v", iteration, results[3])
		}
		for i, err := range results[:3] {
			var problem *domain.Error
			if err != nil && (!errors.As(err, &problem) || problem.Status != 404 && problem.Status != 409) {
				t.Fatalf("concurrent save %d/%d failed unexpectedly: %v", iteration, i, err)
			}
		}
		records, err := service.Store.List(ctx, "agents")
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			var agent domain.Agent
			if err := json.Unmarshal(record.Data, &agent); err != nil {
				t.Fatal(err)
			}
			if agent.SearchProviderID == provider.ID {
				t.Fatalf("concurrent write restored deleted search on %s in iteration %d", agent.ID, iteration)
			}
		}
	}
}
