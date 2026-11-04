package persistence

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
)

func TestCurrentRevisionFollowsCommittedDocumentWithoutDecodingItsBody(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	value := domain.Agent{Meta: domain.Meta{ID: "revision-fixture"}, Name: "Header", Persona: strings.Repeat("Immutable configuration text. ", 4096)}
	for range 2 {
		if err := s.Save(t.Context(), "agents", &value.Meta, &value); err != nil {
			t.Fatal(err)
		}
		revision, err := s.CurrentRevision(t.Context(), "agents", value.ID)
		if err != nil || revision != value.Revision {
			t.Fatalf("header did not follow the committed body: got %d want %d err=%v", revision, value.Revision, err)
		}
	}
	if _, err := s.CurrentRevision(t.Context(), "agents", "missing"); !domain.IsMissing(err) {
		t.Fatalf("missing document header: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.CurrentRevision(canceled, "agents", value.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled metadata lookup: %v", err)
	}
}
