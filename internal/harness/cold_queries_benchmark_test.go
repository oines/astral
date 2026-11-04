package harness

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math/rand"
	"os"
	goruntime "runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

// This opt-in measurement uses public Core APIs and synthetic files. It does
// not run in ordinary CI and never reads production data or calls a provider.
// ASTRAL_COLD_DIRECTORY_BENCH=1 go test -run '^TestRepresentativeColdDirectory$'
func TestRepresentativeColdDirectory(t *testing.T) {
	if os.Getenv("ASTRAL_COLD_DIRECTORY_BENCH") != "1" {
		t.Skip("explicit bounded performance measurement")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	f := newFixture(t, 0, func(context.Context, int, map[string]any) any {
		t.Error("the synthetic benchmark contacted a model provider")
		return textReply("Unexpected provider call.")
	})
	f.ctx = ctx
	models := f.service.Models
	f.session.Archived = true
	if err := f.store.Save(ctx, "sessions", &f.session.Meta, &f.session); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteSession(ctx, f.session.ID, f.session.Revision); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	setupStart := time.Now()
	var images []core.Content
	for seed := range 3 {
		picture := image.NewNRGBA(image.Rect(0, 0, 320, 240))
		noise := rand.New(rand.NewSource(int64(seed)))
		for offset := 0; offset < len(picture.Pix); offset += 4 {
			picture.Pix[offset], picture.Pix[offset+1], picture.Pix[offset+2], picture.Pix[offset+3] = byte(noise.Intn(256)), byte(noise.Intn(256)), byte(noise.Intn(256)), 255
		}
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, picture); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(base64.StdEncoding.EncodeToString(encoded.Bytes()))
		images = append(images, core.Content{Type: "image", MediaType: "image/png", Data: data})
	}
	imageJSON, _ := json.Marshal(images)
	var records []domain.Session
	var logicalBytes int
	var largest int
	for index := range 82 {
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		record := domain.Session{Meta: domain.Meta{ID: fmt.Sprintf("session_perf_%02d", index)}, AgentID: f.agent.ID, Title: fmt.Sprintf("Synthetic %02d", index), TitleSource: domain.TitleManual, Source: "web"}
		// Keep most histories compact; one root below receives the actual
		// largest-window scale. This bounds construction work separately from
		// the list measurement instead of repeatedly generating 338 MB.
		target := 1 << 20
		if index >= 47 {
			record.ID, record.Source, record.SubagentParentID = fmt.Sprintf("session_subagent_perf_%02d", index), "subagent", records[0].ID
		}
		if err := f.store.Save(ctx, "sessions", &record.Meta, &record); err != nil {
			t.Fatal(err)
		}
		content := append([]core.Content{{Type: "text", Text: strings.Repeat("Synthetic file search output and structured tool detail.\n", max(1, (target-len(imageJSON))/56))}}, images...)
		model := core.ModelFunc{Specification: core.ModelSpec{ID: f.agent.DefaultModelID, ContextWindow: 2000000, MaxOutputTokens: 256}, Generate: func(_ context.Context, request core.Request, _ func(core.ModelEvent)) (core.Reply, error) {
			message := core.Message{Role: core.RoleAssistant, Content: core.Text("Completed synthetic tool inspection."), FinishReason: core.FinishStop}
			if request.Messages[len(request.Messages)-1].Role != core.RoleTool {
				message.Content = []core.Content{{Type: "tool_call", ToolCall: &core.ToolCall{ID: "synthetic-read", Name: "read", Arguments: json.RawMessage(`{"path":"synthetic.png"}`)}}}
				message.FinishReason = core.FinishToolCalls
			}
			return core.Reply{Message: message, Usage: core.Usage{InputTokens: 100, OutputTokens: 10, Available: true}}, nil
		}}
		tool := core.ToolFunc{Spec: core.ToolDefinition{Name: "read", Version: "synthetic", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}, Run: func(context.Context, core.Invocation, func(core.ToolProgress)) (core.ToolExecution, error) {
			return core.ToolExecution{Result: &core.ToolResult{Content: content, Outcome: core.OutcomeSucceeded}}, nil
		}}
		session, err := core.NewSession(ctx, core.Config{Store: f.coreStore, Model: model, Selection: core.ModelSelection{ID: f.agent.DefaultModelID}, SystemPrompt: systemPromptFor(record), Tools: []core.Tool{tool}, MaxContextBytes: 64 << 20, Compaction: core.CompactionPolicy{Disabled: true}, EstimateTokens: func(context.Context, core.Model, core.Request) (int, error) { return 100, nil }}, core.SessionOptions{ID: record.ID})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = session.Steer(ctx, core.Input{ID: "synthetic-task", Message: core.User("Inspect the synthetic files.")}); err != nil {
			t.Fatal(err)
		}
		run, err := session.Continue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = run.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			for observation := range 9 {
				message := core.User(strings.Repeat("Synthetic archived observation. ", (4<<20)/32))
				message.ContextKind = "im.observation"
				message.Metadata = encode(map[string]string{"source": "im", "conversation_id": "synthetic", "event_id": fmt.Sprintf("large-%d", observation)})
				if _, err = session.InjectContext(ctx, core.Input{ID: fmt.Sprintf("large-%d", observation), Contexts: []core.Message{message}}); err != nil {
					t.Fatal(err)
				}
			}
		}
		stats := session.Stats()
		logicalBytes += stats.ContextBytes
		largest = max(largest, stats.ContextBytes)
		if index >= 47 {
			settings := subagentSettings{Meta: domain.Meta{ID: record.ID}, Configuration: session.Configuration(), MessageTool: DefaultMessageTool(), Task: "Completed synthetic task.", Started: true}
			if err = f.store.Save(ctx, "subagent_settings", &settings.Meta, &settings); err != nil {
				t.Fatal(err)
			}
			if err = f.store.SaveMaintenance(ctx, "subagent_delivery", record.ID, "running", subagentCheckpoint{Protocol: subagentDeliveryProtocol, Version: session.State().Version}); err != nil {
				t.Fatal(err)
			}
		}
		if err = session.Close(ctx); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	t.Logf("SYNTHETIC_SETUP sessions=82 roots=47 children=35 images_per_window=3 context_bytes=%d largest_window_bytes=%d elapsed=%s", logicalBytes, largest, time.Since(setupStart))
	for _, legacy := range []bool{true, false} {
		f.service = New(f.store, f.coreStore, models, f.hub)
		subagentRecoveryStopWorker(f)
		goruntime.GC()
		var before, after goruntime.MemStats
		goruntime.ReadMemStats(&before)
		profile, err := os.CreateTemp("", "astral-cold-directory-*.pprof")
		if err != nil {
			t.Fatal(err)
		}
		if err = pprof.StartCPUProfile(profile); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		for range 3 {
			for _, record := range records {
				if legacy {
					_, err = f.service.View(ctx, record.ID)
				} else {
					_, err = f.service.Summary(ctx, record.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if legacy {
					r, openErr := f.service.open(ctx, record.ID)
					if openErr == nil {
						_, openErr = r.core.Load().LookupInput(ctx, "synthetic-task")
					}
					err = openErr
				} else {
					_, err = f.service.Receipt(ctx, record.ID, "synthetic-task")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, child := range records[47:] {
				if legacy {
					_, err = f.service.open(ctx, child.ID)
				} else {
					err = f.service.reconcileSubagent(ctx, child)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		elapsed := time.Since(started)
		pprof.StopCPUProfile()
		if err = profile.Close(); err != nil {
			t.Fatal(err)
		}
		goruntime.GC()
		goruntime.ReadMemStats(&after)
		loadedBytes := 0
		for _, r := range f.service.runtimes {
			loadedBytes += r.core.Load().Stats().ContextBytes
		}
		t.Logf("SYNTHETIC_RESULT legacy=%v rounds=3 elapsed=%s runtimes=%d loaded_context_bytes=%d allocated_bytes=%d live_heap_delta_bytes=%d heap_inuse_bytes=%d cpu_profile=%s", legacy, elapsed, len(f.service.runtimes), loadedBytes, after.TotalAlloc-before.TotalAlloc, int64(after.HeapAlloc)-int64(before.HeapAlloc), after.HeapInuse, profile.Name())
		if !legacy && len(f.service.runtimes) != 0 {
			t.Fatal("cold directory/recovery retained an execution instance")
		}
		if err = f.service.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.count() != 0 {
		t.Fatalf("benchmark sent %d model HTTP requests", f.count())
	}
}
