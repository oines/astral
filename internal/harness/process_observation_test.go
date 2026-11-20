package harness

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestProcessObserverAsyncLifecyclePersistenceAndPurge(t *testing.T) {
	f := newFixture(t, 0, nil)
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}

	watch, stopWatch := f.service.WatchProcesses(f.session.ID)
	defer stopWatch()

	pr, pw := io.Pipe()
	obs := r.registerObserver("exec-async-1")
	wrapped := &observedOutput{ReadCloser: pr, observer: obs}

	// Simulate initial output before yielding to background.
	go func() {
		_, _ = pw.Write([]byte("line-1\n"))
	}()
	buf := make([]byte, 32)
	if _, err := wrapped.Read(buf); err != nil {
		t.Fatal(err)
	}

	r.markProcessAsync("exec-async-1")

	select {
	case status := <-watch:
		if status.ExecutionID != "exec-async-1" || !status.Running {
			t.Fatalf("unexpected running status: %+v", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async start status")
	}

	// Expanded reader attaches and receives initial buffer plus incremental chunks.
	snap, sub, err := f.service.ObserveProcessOutput(f.ctx, f.session.ID, "exec-async-1")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if !snap.Running || snap.Output != "line-1\n" {
		t.Fatalf("unexpected initial snapshot: %+v", snap)
	}

	go func() {
		_, _ = pw.Write([]byte("line-2\n"))
		_ = pw.Close()
	}()
	for {
		if _, err := wrapped.Read(buf); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	obs.markExited(3, nil)

	var combined strings.Builder
	combined.WriteString(snap.Output)
	var finalExit *int
	ctx, cancel := context.WithTimeout(f.ctx, 3*time.Second)
	defer cancel()
	for {
		update, ok := sub.Next(ctx)
		if !ok {
			t.Fatal("subscription ended before done")
		}
		if update.Reset {
			combined.Reset()
			combined.WriteString(update.Output)
		} else if update.Chunk != "" {
			combined.WriteString(update.Chunk)
		}
		if update.Done {
			finalExit = update.ExitCode
			break
		}
	}
	if combined.String() != "line-1\nline-2\n" || finalExit == nil || *finalExit != 3 {
		t.Fatalf("unexpected streamed result: out=%q exit=%v", combined.String(), finalExit)
	}

	// Wait for terminal status event and durable bounded output persistence.
	select {
	case status := <-watch:
		if status.Running || status.ExitCode == nil || *status.ExitCode != 3 {
			t.Fatalf("unexpected terminal status: %+v", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for terminal status")
	}

	// Simulate restart by dropping the in-memory observer and reading back persisted output.
	r.processMu.Lock()
	delete(r.processObservers, "exec-async-1")
	r.processMu.Unlock()

	eventually(t, f.ctx, func() bool {
		statuses, listErr := f.service.SessionProcesses(f.ctx, f.session.ID)
		if listErr != nil {
			return false
		}
		for _, status := range statuses {
			if status.ExecutionID == "exec-async-1" && !status.Running && status.ExitCode != nil && *status.ExitCode == 3 {
				return true
			}
		}
		return false
	})

	persisted, persistedSub, err := f.service.ObserveProcessOutput(f.ctx, f.session.ID, "exec-async-1")
	if err != nil {
		t.Fatal(err)
	}
	if persistedSub != nil || persisted.Running || persisted.Output != "line-1\nline-2\n" || persisted.ExitCode == nil || *persisted.ExitCode != 3 {
		t.Fatalf("unexpected persisted output: %+v sub=%v", persisted, persistedSub)
	}

	// Synchronous commands that never markAsync must neither notify nor persist.
	syncObs := r.registerObserver("exec-sync-1")
	syncObs.append([]byte("sync output"))
	syncObs.closeOutput()
	syncObs.markExited(0, nil)
	if _, _, err := f.service.ObserveProcessOutput(f.ctx, f.session.ID, "exec-sync-1"); !domain.IsMissing(err) {
		t.Fatalf("synchronous command output should not be persisted, got err=%v", err)
	}

	// Permanent session cleanup must purge process_outputs.
	if err := f.store.Update(f.ctx, func(tx *persistence.Tx) error {
		return purgeSessionProcessOutputs(f.ctx, tx, f.session.ID)
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.service.ObserveProcessOutput(f.ctx, f.session.ID, "exec-async-1"); !domain.IsMissing(err) {
		t.Fatalf("purged process output remained accessible: %v", err)
	}
}

func TestProcessObserverRingBufferIsBoundedAndUTF8Safe(t *testing.T) {
	f := newFixture(t, 0, nil)
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	obs := r.registerObserver("exec-bounded")
	chunk := strings.Repeat("中", 20000) // 60KB per write
	obs.append([]byte(chunk))
	obs.append([]byte(chunk))
	obs.append([]byte(chunk)) // 180KB > 128KB cap

	obs.mu.Lock()
	snap, _ := obs.snapshotLocked()
	obs.mu.Unlock()
	if !snap.Truncated || len(snap.Output) > processMemoryBufferBytes {
		t.Fatalf("buffer was not bounded: len=%d truncated=%v", len(snap.Output), snap.Truncated)
	}
	if strings.ContainsRune(snap.Output, '\uFFFD') {
		t.Fatal("UTF-8 character was corrupted at truncation boundary")
	}
}
