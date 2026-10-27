package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/securetransport"
)

func TestComputerProbeWorksWithExistingTunnelWithoutEnrollment(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	identity, err := securetransport.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := computers.NewHub(store)
	hub.SetIdentity(identity)
	defer hub.Close()
	server := httptest.NewServer(http.HandlerFunc(hub.Connect))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out, stderr bytes.Buffer
	args := []string{"computer", "probe", "--server", server.URL, "--server-pin", identity.Pin()}
	if err = run(ctx, args, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("successful internal route probe must be silent")
	}
	args[len(args)-1] = strings.Repeat("0", 64)
	if err = run(ctx, args, &out, &stderr); err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("CLI probe did not reject a different Server: %v", err)
	}
	for _, collection := range []string{"computers", "enrollments"} {
		records, err := store.List(ctx, collection)
		if err != nil || len(records) != 0 {
			t.Fatalf("credential-free probe mutated %s: %d records, %v", collection, len(records), err)
		}
	}
}

func TestComputerProbeRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--server", "http://127.0.0.1:1"},
		{"--server-pin", strings.Repeat("0", 64)},
		{"--server", "http://127.0.0.1:1", "--server-pin", strings.Repeat("0", 64), "extra"},
		{"--server", "http://127.0.0.1:1", "--server-pin", strings.Repeat("0", 64), "--token", "must-not-be-accepted"},
	} {
		if err := computerProbe(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("invalid internal probe arguments accepted: %q", args)
		}
	}
}

func TestComputerProbeBoundsUnresponsiveEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	started := time.Now()
	err := computerProbe(context.Background(), []string{"--server", server.URL, "--server-pin", strings.Repeat("0", 64)}, io.Discard)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unresponsive endpoint did not hit the internal probe deadline: %v", err)
	}
	t.Logf("unresponsive probe stopped after %s", time.Since(started))
}
