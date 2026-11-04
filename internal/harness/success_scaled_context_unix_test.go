//go:build (darwin || linux) && astral_prepared_execution

package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral-core/models/provider"
	"github.com/oines/astral-core/models/provider/openai/completions"
	"github.com/oines/astral-core/store/sqlite"
)

const scaledContextPrompt = "Complete this offline scaled-context request."

type scaledContextReceiver struct {
	mu      sync.Mutex
	started time.Time
	fast    bool
	tool    bool
	wires   []scaledContextWire
}

type scaledContextWire struct {
	successLifecycleWire
	BodyReadStartNS  int64 `json:"body_read_start_ns"`
	BodyReadWallNS   int64 `json:"body_read_wall_ns"`
	ValidateHashNS   int64 `json:"validate_hash_ns"`
	ReceiverReturnNS int64 `json:"receiver_return_ns"`
	ReceiverWallNS   int64 `json:"receiver_wall_ns"`
}

func (r *scaledContextReceiver) RoundTrip(request *http.Request) (*http.Response, error) {
	dispatch := time.Since(r.started).Nanoseconds()
	if request.Method != http.MethodPost || request.URL.Host != "offline.invalid" || request.URL.Path != "/v1/chat/completions" {
		return nil, errors.New("scaled offline receiver rejected endpoint")
	}
	start := time.Now()
	readStartNS := time.Since(r.started).Nanoseconds()
	var raw []byte
	var err error
	if r.fast && request.ContentLength > 0 && request.ContentLength <= 128<<20 {
		raw = make([]byte, int(request.ContentLength))
		_, err = io.ReadFull(request.Body, raw)
		if err == nil {
			var tail [1]byte
			if n, end := request.Body.Read(tail[:]); n != 0 || end != io.EOF {
				err = errors.New("body length mismatch")
			}
		}
	} else {
		raw, err = io.ReadAll(request.Body)
	}
	if err != nil {
		return nil, err
	}
	if err = request.Body.Close(); err != nil {
		return nil, err
	}
	wire := scaledContextWire{successLifecycleWire: successLifecycleWire{Bytes: len(raw), BodyReadNS: time.Since(r.started).Nanoseconds(), DispatchNS: dispatch}, BodyReadStartNS: readStartNS}
	wire.BodyReadWallNS = wire.BodyReadNS - readStartNS
	validateStart := time.Now()
	sum := sha256.Sum256(raw)
	wire.SHA256 = hex.EncodeToString(sum[:])
	var envelope struct {
		Stream bool `json:"stream"`
	}
	if r.fast {
		wire.ValidJSON = len(raw) > 0 && raw[0] == '{' && ojson.ValidBytes(raw)
		envelope.Stream = ojson.GetBytesNoCopy(raw, "stream").Bool()
	} else {
		wire.ValidJSON = json.Valid(raw) && json.Unmarshal(raw, &envelope) == nil
	}
	if !wire.ValidJSON || envelope.Stream {
		return nil, errors.New("scaled receiver requires complete non-streaming JSON")
	}
	wire.ValidateHashNS = time.Since(validateStart).Nanoseconds()
	wire.SinkNS = time.Since(start).Nanoseconds()
	r.mu.Lock()
	n := len(r.wires) + 1
	r.mu.Unlock()
	// This timestamp is immediately before handing the mock response back to
	// the SDK. It includes full body consumption, JSON/SHA work and response
	// construction; recording the result happens after the timestamp.
	defer func() {
		wire.ReceiverReturnNS = time.Since(r.started).Nanoseconds()
		wire.ReceiverWallNS = wire.ReceiverReturnNS - dispatch
		r.mu.Lock()
		r.wires = append(r.wires, wire)
		r.mu.Unlock()
	}()
	// Usage is deliberately absent. Both requests must use the real SDK
	// estimator; an invented provider count must never make the second cheaper.
	response := `{"id":"scaled-offline","choices":[{"index":0,"message":{"role":"assistant","content":"Scaled offline reply."},"finish_reason":"stop"}]}`
	if r.tool && n == 1 {
		response = `{"id":"scaled-tool","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"scaled-live-read","type":"function","function":{"name":"scaled_read","arguments":"{\"path\":\"fixture.txt\"}"}}]},"finish_reason":"tool_calls"}]}`
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
}

func scaledContextMessages(images []core.Content, padding int) []core.Message {
	const count = 1024
	const prose = "Synthetic benchmark records preserve local filesystem observations and completed task results. "
	messages := make([]core.Message, 0, count+3)
	imageStart := (count - len(images)) / 2
	for i := range count {
		n := padding / count
		if i < padding%count {
			n++
		}
		text := fmt.Sprintf("Public synthetic historical record %04d. ", i)
		if n > 0 {
			text += strings.Repeat(prose, (n+len(prose)-1)/len(prose))[:n]
		}
		message := core.User(text)
		if i%2 == 1 {
			message.Role, message.FinishReason = core.RoleAssistant, core.FinishStop
		}
		if at := i - imageStart; at >= 0 && at < len(images) {
			message.Role, message.FinishReason = core.RoleUser, ""
			message.Content = append(message.Content, images[at])
		}
		messages = append(messages, message)
		if i == count/4 {
			messages = append(messages,
				core.Message{Role: core.RoleAssistant, FinishReason: core.FinishToolCalls, Content: []core.Content{{Type: "tool_call", ToolCall: &core.ToolCall{ID: "scaled-history-read", Name: "scaled_read", Arguments: json.RawMessage(`{"path":"fixture.txt"}`)}}}},
				core.Message{Role: core.RoleTool, ToolCallID: "scaled-history-read", Name: "scaled_read", Content: core.Text("Historical read completed; the local fixture contains a bounded public text record.")},
				core.Message{Role: core.RoleAssistant, FinishReason: core.FinishStop, Content: core.Text("The historical read result is recorded.")})
		}
	}
	return messages
}

// Opt-in Core+SDK experiment, not the historical baseline or full Astral Host.
// Seed/history creation and SDK calibration precede timing. Cold-session opening
// is measured separately (the OS page cache may be warm from setup). The warm
// interval includes Prompt admission, all production preparation/estimation/
// validation/Freeze, complete HTTP bodies, reply parsing and durable Run.Wait.
// There are no historical usage anchors, forced GCs or external requests.
func TestSuccessScaledContext(t *testing.T) {
	scale := os.Getenv("ASTRAL_SUCCESS_SCALED")
	if scale == "" {
		t.Skip("set ASTRAL_SUCCESS_SCALED=500k|900k and explicit new output/seed paths")
	}
	target := map[string]int{"500k": 500000, "900k": 900000}[scale]
	if target == 0 {
		t.Fatal("expected 500k or 900k")
	}
	mode := os.Getenv("ASTRAL_SUCCESS_SCALED_MODE")
	if mode == "" {
		mode = "prompt"
	}
	if mode != "prompt" && mode != "tool" {
		t.Fatal("expected prompt or tool mode")
	}
	receiverMode := os.Getenv("ASTRAL_SUCCESS_SCALED_RECEIVER")
	if receiverMode == "" {
		receiverMode = "full"
	}
	if receiverMode != "full" && receiverMode != "fast" {
		t.Fatal("expected full or fast receiver")
	}
	output := os.Getenv("ASTRAL_SUCCESS_SCALED_OUTPUT")
	if !filepath.IsAbs(output) || os.Mkdir(output, 0700) != nil {
		t.Fatal("absolute, new disposable output directory required")
	}
	seed := os.Getenv("ASTRAL_SUCCESS_SCALED_SEED")
	if !filepath.IsAbs(seed) {
		t.Fatal("absolute captured request seed path required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	setupStart := time.Now()
	raw, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	var source core.Request
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	seedSum := sha256.Sum256(raw)
	var images []core.Content
	imageRawBytes := 0
	for _, message := range source.Messages {
		for _, part := range message.Content {
			if part.Type == "image" {
				if len(part.Data) == 0 || part.MediaType != "image/png" && part.MediaType != "image/jpeg" {
					t.Fatal("seed requires frozen PNG/JPEG image data")
				}
				// Keep pixels and media type only; no captured text, local URLs,
				// provider-origin metadata or captured model config enter the case.
				images = append(images, core.Content{Type: "image", MediaType: part.MediaType, Data: part.Data})
				imageRawBytes += len(part.Data)
			}
		}
	}
	if len(images) != 227 {
		t.Fatal("expected the sealed 227-image source")
	}
	source, raw = core.Request{}, nil
	receiver := &scaledContextReceiver{fast: receiverMode == "fast", tool: mode == "tool"}
	streaming := false
	snapshot, err := provider.Freeze(completions.Protocol, models.Config{Spec: core.ModelSpec{ID: "scaled-offline", Revision: "synthetic-1m", ContextWindow: 1000000, MaxOutputTokens: 16000}, Model: "scaled-offline", BaseURL: "https://offline.invalid/v1", Images: "supported", ToolCalling: "supported", Streaming: &streaming})
	if err != nil {
		t.Fatal(err)
	}
	credentials := provider.Credentials{APIKey: "offline-placeholder", HTTPClient: &http.Client{Transport: receiver}}
	sdk, err := provider.Restore(snapshot, credentials)
	if err != nil {
		t.Fatal(err)
	}
	definition := core.ToolDefinition{Name: "scaled_read", Description: "Read the fixed local benchmark fixture without modifying it.", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","enum":["fixture.txt"]}},"required":["path"],"additionalProperties":false}`)}
	request := core.Request{ModelID: snapshot.Spec.ID, ModelRevision: snapshot.Spec.Revision, SystemPrompt: "Complete the current task using the recorded public synthetic history and frozen images.", Tools: []core.ToolDefinition{definition}, MaxOutputTokens: 16000}
	padding, tokens, calibrationCalls := 0, 0, 0
	calibrationStart := time.Now()
	for ; calibrationCalls < 12; calibrationCalls++ {
		request.Messages = append(scaledContextMessages(images, padding), core.User(scaledContextPrompt))
		prepared, err := core.ProjectRequest(ctx, sdk, request)
		if err != nil {
			t.Fatal(err)
		}
		tokens, err = sdk.EstimateTokens(ctx, prepared)
		if err != nil {
			t.Fatal(err)
		}
		if abs := tokens - target; abs <= target/1000 && abs >= -target/1000 {
			calibrationCalls++
			break
		}
		padding += (target - tokens) * 4
		if padding < 0 {
			t.Fatal("image-only material exceeds target estimate")
		}
	}
	if delta := tokens - target; delta > target/1000 || delta < -target/1000 {
		t.Fatalf("SDK calibration failed: target=%d estimated=%d", target, tokens)
	}
	calibrationNS := time.Since(calibrationStart).Nanoseconds()
	fixtureRaw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(output, "scaled-fixture.json"), fixtureRaw, 0600); err != nil {
		t.Fatal(err)
	}
	fixtureSum := sha256.Sum256(fixtureRaw)
	fixtureBytes := len(fixtureRaw)
	fixtureRaw = nil
	history := request.Messages[:len(request.Messages)-1]
	var imagePositions []int
	for i, message := range history {
		for _, part := range message.Content {
			if part.Type == "image" {
				imagePositions = append(imagePositions, i)
			}
		}
	}
	database := filepath.Join(output, "core.db")
	store, err := sqlite.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	const sessionID = "scaled-context"
	entries := []core.Entry{
		{ID: "created", Kind: core.EntryCreated, Created: &core.CreationRecord{FormatVersion: core.FormatVersion, SessionID: sessionID, RolloutID: sessionID}},
		{ID: "config", Kind: core.EntryConfiguration, Configuration: &core.Configuration{SystemPrompt: request.SystemPrompt, Selection: core.ModelSelection{ID: snapshot.Spec.ID}, Tools: []core.ToolDefinition{definition}}},
	}
	parent := "config"
	for i := range history {
		id := fmt.Sprintf("history-%04d", i)
		entry := core.Entry{ID: id, ParentID: parent, Kind: core.EntryMessage, Message: &history[i]}
		if history[i].Role == core.RoleTool {
			call := history[i-1].ToolCalls()[0]
			entry.Kind, entry.Message = core.EntryToolResult, nil
			entry.Tool = &core.ToolRecord{Invocation: core.Invocation{SessionID: sessionID, AssistantID: parent, ExecutionID: "scaled-history-execution", Call: call}, Result: &core.ToolResult{Content: history[i].Content, Outcome: core.OutcomeSucceeded}}
		}
		entries = append(entries, entry)
		parent = id
	}
	for i := range entries {
		entries[i].SessionID, entries[i].Version, entries[i].Time = sessionID, 1, time.Unix(1700000000+int64(i), 0).UTC()
	}
	if _, err = store.Commit(ctx, sessionID, core.Commit{ID: "scaled-history", Entries: entries}); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(output, "fixture.txt")
	const toolText = "A bounded public local record read by the benchmark tool.\n"
	if err = os.WriteFile(fixturePath, []byte(toolText), 0600); err != nil {
		t.Fatal(err)
	}
	var toolCalls atomic.Int64
	var toolReturnNS atomic.Int64
	config := core.Config{Selection: core.ModelSelection{ID: snapshot.Spec.ID}, MaxContextBytes: 64 << 20, MaxTurns: 4, Compaction: core.CompactionPolicy{Disabled: true}, Budget: core.BudgetPolicy{OutputTokens: 16000, SafetyMargin: 4096}, Retry: core.RetryPolicy{MaxAttempts: 1},
		ResolveModel: func(context.Context, core.ModelSelection) (core.ResolvedModel, error) {
			return core.ResolvedModel{Model: sdk, Snapshot: snapshot.Data}, nil
		},
		EstimateTokensReadOnly: func(ctx context.Context, model core.Model, request core.Request) (int, error) {
			return model.(interface {
				EstimateTokens(context.Context, core.Request) (int, error)
			}).EstimateTokens(ctx, request)
		},
		RestoreModel: func(_ context.Context, saved core.ModelSnapshot) (core.Model, error) {
			return provider.Restore(saved, credentials)
		},
		Tools: []core.Tool{core.ToolFunc{Spec: definition, Run: func(_ context.Context, invocation core.Invocation, _ func(core.ToolProgress)) (core.ToolExecution, error) {
			if invocation.Call.Name != definition.Name || string(invocation.Call.Arguments) != `{"path":"fixture.txt"}` {
				return core.ToolExecution{}, errors.New("unexpected local tool arguments")
			}
			body, err := os.ReadFile(fixturePath)
			if err != nil || string(body) != toolText {
				return core.ToolExecution{}, errors.New("local tool fixture differs")
			}
			toolCalls.Add(1)
			defer func() { toolReturnNS.Store(time.Since(receiver.started).Nanoseconds()) }()
			return core.ToolExecution{Result: &core.ToolResult{Content: core.Text(string(body)), Outcome: core.OutcomeSucceeded}}, nil
		}}},
	}
	setupNS := time.Since(setupStart).Nanoseconds()
	openStart, openCPU := time.Now(), successReplayCPU(t)
	store, err = sqlite.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config.Store = store
	session, err := core.OpenSession(ctx, config, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	openNS, openCPUNS := time.Since(openStart).Nanoseconds(), successReplayCPU(t)-openCPU
	stateBefore := session.Snapshot().State
	profileEnabled := os.Getenv("ASTRAL_SUCCESS_SCALED_PROFILE") == "1"
	var profile *os.File
	if profileEnabled {
		profile, err = os.OpenFile(filepath.Join(output, "cpu.pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err = pprof.StartCPUProfile(profile); err != nil {
			_ = profile.Close()
			t.Fatal(err)
		}
		defer func() {
			if profile != nil {
				pprof.StopCPUProfile()
				_ = profile.Close()
			}
		}()
	}
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	cpuBefore := successReplayCPU(t)
	started := time.Now()
	receiver.started = started
	prompt, operationErr := session.Prompt(ctx, core.Input{ID: "scaled-input", Message: core.User(scaledContextPrompt)})
	var run core.RunResult
	if operationErr == nil && prompt.Run != nil {
		run, operationErr = prompt.Run.Wait(ctx)
	} else if operationErr == nil {
		operationErr = errors.New("Prompt returned no run")
	}
	wallNS, cpuNS := time.Since(started).Nanoseconds(), successReplayCPU(t)-cpuBefore
	goruntime.ReadMemStats(&after)
	// StopCPUProfile waits for its writer. The wall/CPU/allocation sample above
	// ends at Run.Wait, so profiler shutdown never enters the reported interval.
	if profile != nil {
		pprof.StopCPUProfile()
		if err = profile.Close(); err != nil {
			t.Fatal(err)
		}
		profile = nil
	}
	receiver.mu.Lock()
	wires := append([]scaledContextWire(nil), receiver.wires...)
	receiver.mu.Unlock()
	if operationErr != nil {
		t.Fatal(operationErr)
	}
	if run.Status != "completed" || run.LastMessage == nil || len(run.LastMessage.Content) != 1 || run.LastMessage.Content[0].Text != "Scaled offline reply." {
		t.Fatal("scaled reply was not committed")
	}
	wantCalls := 1
	if mode == "tool" {
		wantCalls = 2
	}
	if len(wires) != wantCalls || toolCalls.Load() != int64(wantCalls-1) {
		t.Fatal("unexpected HTTP/tool call count")
	}
	page, err := session.Attempts().Scan(ctx, core.AttemptQuery{Purpose: "generation", Limit: 10, MaxBytes: 128 << 20})
	if err != nil || len(page.Attempts) != wantCalls {
		t.Fatal("generation attempt count differs: ", err)
	}
	var verified []map[string]any
	for i, attempt := range page.Attempts {
		if attempt.Outcome == nil || attempt.Outcome.Status != "completed" || attempt.Outcome.Usage.Available {
			t.Fatal("incomplete attempt or invented usage anchor")
		}
		rebuilt, err := core.RebuildRequest(ctx, store, attempt.Input, 64<<20)
		if err != nil {
			t.Fatal("frozen digest verification: ", err)
		}
		estimated, err := sdk.EstimateTokens(ctx, rebuilt)
		if err != nil {
			t.Fatal(err)
		}
		body, err := sdk.RequestBody(rebuilt)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(encoded)
		if len(encoded) != wires[i].Bytes || hex.EncodeToString(digest[:]) != wires[i].SHA256 || rebuilt.MaxOutputTokens != 16000 {
			t.Fatal("rebuilt complete wire body differs")
		}
		if i == 0 && (estimated-target > target/1000 || target-estimated > target/1000) {
			t.Fatal("timed request differs from calibrated SDK estimate")
		}
		verified = append(verified, map[string]any{"digest": attempt.Input.Digest, "digest_rebuild_verified": true, "wire_rebuild_verified": true, "estimated_input_tokens": estimated, "max_output_tokens": rebuilt.MaxOutputTokens, "messages": len(rebuilt.Messages)})
	}
	compactions, err := session.Attempts().Scan(ctx, core.AttemptQuery{Purpose: "compaction", Limit: 1})
	if err != nil || len(compactions.Attempts) != 0 {
		t.Fatal("unexpected compaction")
	}
	receipt, err := session.LookupInput(ctx, "scaled-input")
	if err != nil || receipt.State != core.InputApplied {
		t.Fatal("scaled input was not committed")
	}
	stateAfter := session.Snapshot().State
	if stateAfter.CurrentAttemptID != "" || stateAfter.LastRun == nil || stateAfter.LastRun.Status != "completed" {
		t.Fatal("unfinished scaled lifecycle")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binarySHA, _, err := successLifecycleSHA(executable)
	if err != nil {
		t.Fatal(err)
	}
	buildInfo, _ := debug.ReadBuildInfo()
	var receiverWallNS, receiverReadNS, receiverValidateHashNS int64
	var dispatchSegments []int64
	allDispatchSegmentsUnder50ms := true
	previousReturnNS := int64(0)
	for _, wire := range wires {
		if wire.DispatchNS < previousReturnNS || wire.BodyReadStartNS < wire.DispatchNS || wire.BodyReadNS < wire.BodyReadStartNS || wire.ReceiverReturnNS < wire.BodyReadNS || wire.ReceiverReturnNS > wallNS {
			t.Fatal("invalid receiver timing order")
		}
		receiverWallNS += wire.ReceiverWallNS
		receiverReadNS += wire.BodyReadWallNS
		receiverValidateHashNS += wire.ValidateHashNS
		segment := wire.DispatchNS - previousReturnNS
		dispatchSegments = append(dispatchSegments, segment)
		allDispatchSegmentsUnder50ms = allDispatchSegmentsUnder50ms && segment < int64(50*time.Millisecond)
		previousReturnNS = wire.ReceiverReturnNS
	}
	localWallNS := wallNS - receiverWallNS
	metadata := map[string]any{"scope": "Core+SDK; Host Submit/CollectContext omitted", "scenario": scale, "mode": mode, "receiver": receiverMode,
		"target_input_tokens": target, "calibrated_estimated_input_tokens": tokens, "token_count_kind": "current SDK fallback estimate, not provider-reported usage", "calibration_calls": calibrationCalls, "calibration_ns": calibrationNS,
		"target_spec": snapshot.Spec, "synthetic_target_differs_from_historical_model": true, "safety_margin_tokens": 4096, "image_token_budget": 1200,
		"seed_sha256": hex.EncodeToString(seedSum[:]), "fixture_sha256": hex.EncodeToString(fixtureSum[:]), "fixture_bytes": fixtureBytes, "image_raw_json_bytes": imageRawBytes, "images": len(images), "image_message_positions": imagePositions, "history_messages": len(history),
		"setup_ns": setupNS, "cold_session_open_ns": openNS, "cold_session_open_cpu_ns": openCPUNS, "cold_open_note": "fresh SQLite/Core handle; OS page cache may be warm after setup", "historical_usage_anchors": 0,
		"binary_sha256": binarySHA, "build_info": buildInfo, "go_version": goruntime.Version(), "timing_boundary": "already-open session Prompt through Run.Wait; full body read/JSON validation/SHA256 and reply/run commits included", "forced_gc": 0,
		"local_wall_definition":        "derived wall_ns minus the sum of mock RoundTrip entry-to-return intervals; includes SDK response parsing and Core reply/run commits; no real model/network delay exists",
		"local_wall_limitations":       "interval subtraction, not a separate run without the receiver; receiver-induced GC or scheduling effects may remain outside its measured intervals",
		"dispatch_segments_definition": "Prompt start or previous mock receiver return through next RoundTrip entry; later segments include response handling and local tool work",
		"profiles_enabled":             profileEnabled, "profile_stop_wait_excluded_from_metrics": true}
	result := map[string]any{"success": true, "scenario": scale, "mode": mode, "receiver": receiverMode, "wall_ns": wallNS, "cpu_ns": cpuNS, "under_50ms": wallNS < int64(50*time.Millisecond), "reply_commit_included": true,
		"total_alloc_bytes": after.TotalAlloc - before.TotalAlloc, "mallocs": after.Mallocs - before.Mallocs, "num_gc": after.NumGC - before.NumGC, "heap_alloc_before": before.HeapAlloc, "heap_alloc_after": after.HeapAlloc, "heap_inuse_before": before.HeapInuse, "heap_inuse_after": after.HeapInuse,
		"wire_requests": wires, "verified_requests": verified, "tool_executions": toolCalls.Load(), "tool_return_ns": toolReturnNS.Load(), "version_before": stateBefore.Version, "version_after": stateAfter.Version, "forced_gc": 0,
		"core_end_ns": wallNS, "mock_receiver_wall_ns": receiverWallNS, "mock_receiver_body_read_ns": receiverReadNS, "mock_receiver_validate_hash_ns": receiverValidateHashNS,
		"local_wall_excluding_mock_receiver_ns": localWallNS, "local_under_50ms": localWallNS < int64(50*time.Millisecond),
		"first_dispatch_ns": wires[0].DispatchNS, "first_dispatch_under_50ms": wires[0].DispatchNS < int64(50*time.Millisecond), "local_dispatch_segments_ns": dispatchSegments, "all_local_dispatch_segments_under_50ms": allDispatchSegmentsUnder50ms,
		"last_receiver_return_to_core_end_ns": wallNS - previousReturnNS, "profiles_enabled": profileEnabled}
	if mode == "tool" {
		result["tool_return_to_second_dispatch_ns"] = wires[1].DispatchNS - toolReturnNS.Load()
		result["tool_return_to_second_body_read_ns"] = wires[1].BodyReadNS - toolReturnNS.Load()
	}
	successLifecycleWrite(t, filepath.Join(output, "metadata.json"), metadata)
	successLifecycleWrite(t, filepath.Join(output, "result.json"), result)
	t.Logf("scaled %s %s receiver=%s SDK-estimated tokens=%d images=%d wall=%s CPU=%s alloc=%d HTTP=%d", scale, mode, receiverMode, tokens, len(images), time.Duration(wallNS), time.Duration(cpuNS), after.TotalAlloc-before.TotalAlloc, len(wires))
}
