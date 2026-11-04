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
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models/provider"
	"github.com/oines/astral-core/models/provider/openai/completions"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/messages"
	"github.com/oines/astral/internal/modelbridge"
	"golang.org/x/sys/unix"
)

const successScaledHostPrompt = "Complete this offline scaled-context request."

// Reuse the exact full-body receiver and its timing split. Only the sink's URL
// check is adapted to the fixture's actual loopback provider endpoint; the Host
// and SDK see their original URL/headers and no model request can dial a socket.
type scaledHostReceiver struct {
	mu       sync.Mutex
	sink     *successLifecycleTransport
	endpoint url.URL
	toolPath string
	wires    []scaledHostWire
}

type scaledHostWire struct {
	successLifecycleWire
	ReceiverReturnNS   int64 `json:"receiver_return_ns"`
	ReceiverWallNS     int64 `json:"receiver_wall_ns"`
	ServerProcessingNS int64 `json:"server_processing_ns,omitempty"`
	ServerPeakRSSBytes int64 `json:"server_peak_rss_bytes,omitempty"`
	ReceiverPID        int   `json:"receiver_pid,omitempty"`
}

type scaledHostRound struct {
	Index              int              `json:"index"`
	InputID            string           `json:"input_id"`
	RunID              string           `json:"run_id"`
	StartNS            int64            `json:"hot_loop_start_ns"`
	WallNS             int64            `json:"wall_ns"`
	CPUNS              int64            `json:"cpu_ns"`
	TotalAllocBytes    uint64           `json:"total_alloc_bytes"`
	Mallocs            uint64           `json:"mallocs"`
	NumGC              uint32           `json:"num_gc"`
	HeapAllocBefore    uint64           `json:"heap_alloc_before"`
	HeapAllocAfter     uint64           `json:"heap_alloc_after"`
	VersionBefore      uint64           `json:"version_before"`
	VersionAfter       uint64           `json:"version_after"`
	ToolEndNS          int64            `json:"tool_end_event_observed_ns"`
	Wires              []scaledHostWire `json:"wire_requests"`
	ReceiverWallNS     int64            `json:"mock_receiver_wall_ns"`
	ReceiverSinkNS     int64            `json:"mock_receiver_read_validate_hash_ns"`
	LocalWallNS        int64            `json:"local_wall_excluding_mock_receiver_ns"`
	Under50ms          bool             `json:"under_50ms"`
	LocalUnder50ms     bool             `json:"local_under_50ms"`
	EstimatedTokens    []int            `json:"estimated_input_tokens"`
	WireStart, WireEnd int              `json:"-"`
}

func successScaledHostCPU(t *testing.T) int64 {
	t.Helper()
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		t.Fatal(err)
	}
	return int64(usage.Utime.Sec)*1e9 + int64(usage.Utime.Usec)*1000 + int64(usage.Stime.Sec)*1e9 + int64(usage.Stime.Usec)*1000
}

func (r *scaledHostReceiver) RoundTrip(request *http.Request) (*http.Response, error) {
	dispatch := time.Since(r.sink.started).Nanoseconds()
	if request.URL.Scheme != r.endpoint.Scheme || request.URL.Host != r.endpoint.Host || request.URL.Path != strings.TrimRight(r.endpoint.Path, "/")+"/chat/completions" {
		return nil, errors.New("scaled Host sink rejected endpoint")
	}
	response, err := r.sink.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	r.sink.mu.Lock()
	n := len(r.sink.wires)
	wire := scaledHostWire{successLifecycleWire: r.sink.wires[n-1]}
	r.sink.mu.Unlock()
	wire.DispatchNS = dispatch
	// Replace the canned reply in every mode, including its fake usage. The
	// measured Host must estimate both requests rather than accept that anchor.
	body := []byte(`{"id":"scaled-host-offline","choices":[{"index":0,"message":{"role":"assistant","content":"Scaled offline reply."},"finish_reason":"stop"}]}`)
	if r.toolPath != "" && n == 1 {
		arguments, err := json.Marshal(map[string]string{"path": r.toolPath})
		if err != nil {
			return nil, err
		}
		body, err = json.Marshal(map[string]any{"id": "scaled-host-tool", "choices": []any{map[string]any{"index": 0, "finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "scaled-host-read", "type": "function", "function": map[string]any{"name": "read", "arguments": string(arguments)}}}}}}})
		if err != nil {
			return nil, err
		}
	}
	_ = response.Body.Close()
	response.Body = io.NopCloser(strings.NewReader(string(body)))
	// Include the sink adapter's response construction in the mock interval.
	wire.ReceiverReturnNS = time.Since(r.sink.started).Nanoseconds()
	wire.ReceiverWallNS = wire.ReceiverReturnNS - wire.DispatchNS
	r.mu.Lock()
	r.wires = append(r.wires, wire)
	r.mu.Unlock()
	return response, nil
}

func scaledHostHistory(images []core.Content, padding int, toolPath, textStyle string) ([]core.Message, int) {
	const count = 1024
	const prose = "Synthetic benchmark records preserve local filesystem observations and completed task results. "
	const mixed = `func loadConfig(path string) (map[string]any, error) {
    data, err := os.ReadFile(path)
    if err != nil { return nil, fmt.Errorf("read %q: %w", path, err) }
    if len(data) < 4 { return nil, errors.New("short config") }
    // 检查配置并保留诊断信息
    return map[string]any{"path": path, "escaped": "C:\\tmp\\record.json"}, nil
}
{"level":"info","message":"读取完成","path":"C:\\tmp\\record.json","ok":true}
2026-11-05T01:02:03Z INFO task="benchmark" file="record.json" status="ready"
`
	material := prose
	if textStyle == "mixed" {
		material = mixed
	}
	history := make([]core.Message, 0, count+3)
	actualPadding := 0
	imageStart := (count - len(images)) / 2
	for i := range count {
		n := padding / count
		if i < padding%count {
			n++
		}
		text := fmt.Sprintf("Public synthetic historical record %04d. ", i)
		if n > 0 {
			paddingText := strings.Repeat(material, (n+len(material)-1)/len(material))
			cut := n
			// The source is valid UTF-8. If the byte budget ends inside a
			// Chinese rune, shorten to its start rather than normalize bytes.
			for cut > 0 && cut < len(paddingText) && !utf8.RuneStart(paddingText[cut]) {
				cut--
			}
			text += paddingText[:cut]
			actualPadding += cut
		}
		message := core.User(text)
		if i%2 == 1 {
			message.Role, message.FinishReason = core.RoleAssistant, core.FinishStop
		}
		if at := i - imageStart; at >= 0 && at < len(images) {
			message.Role, message.FinishReason = core.RoleUser, ""
			message.Content = append(message.Content, images[at])
		}
		history = append(history, message)
		if i == count/4 {
			call := &core.ToolCall{ID: "scaled-host-history-read", Name: "read", Arguments: encode(map[string]string{"path": toolPath})}
			history = append(history,
				core.Message{Role: core.RoleAssistant, FinishReason: core.FinishToolCalls, Content: []core.Content{{Type: "tool_call", ToolCall: call}}},
				core.Message{Role: core.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: core.Text("Historical local read completed successfully.")},
				core.Message{Role: core.RoleAssistant, FinishReason: core.FinishStop, Content: core.Text("The historical read result is recorded.")})
		}
	}
	return history, actualPadding
}

// The normal Host configuration and callbacks are retained. Calibration calls
// the same collector and projector only during setup, then discards their
// products; Submit must collect and prepare the real measured request again.
func TestHostSuccessScaledContext(t *testing.T) {
	scale := os.Getenv("ASTRAL_SUCCESS_SCALED_HOST")
	if scale == "" {
		t.Skip("set ASTRAL_SUCCESS_SCALED_HOST=900k with explicit seed/new output")
	}
	target := map[string]int{"500k": 500000, "900k": 900000}[scale]
	if target == 0 {
		t.Fatal("expected 500k or 900k")
	}
	mode := os.Getenv("ASTRAL_SUCCESS_SCALED_HOST_MODE")
	if mode == "" {
		mode = "prompt"
	}
	if mode != "prompt" && mode != "tool" {
		t.Fatal("expected prompt or tool mode")
	}
	roundCount := 1
	if value := os.Getenv("ASTRAL_SUCCESS_SCALED_HOST_ROUNDS"); value != "" {
		var err error
		roundCount, err = strconv.Atoi(value)
		if err != nil || roundCount < 1 || roundCount > 20 {
			t.Fatal("rounds must be between 1 and 20")
		}
	}
	if mode != "prompt" && roundCount != 1 {
		t.Fatal("multiple rounds currently require prompt mode")
	}
	textStyle := os.Getenv("ASTRAL_SUCCESS_SCALED_HOST_TEXT_STYLE")
	if textStyle == "" {
		textStyle = "plain"
	}
	if textStyle != "plain" && textStyle != "mixed" {
		t.Fatal("expected plain or mixed text style")
	}
	receiverMode := os.Getenv("ASTRAL_SUCCESS_SCALED_RECEIVER")
	if receiverMode == "" {
		receiverMode = "full"
	}
	if receiverMode != "fast" && receiverMode != "full" && receiverMode != "process" {
		t.Fatal("expected fast, full or process receiver")
	}
	output := os.Getenv("ASTRAL_SUCCESS_SCALED_HOST_OUTPUT")
	if !filepath.IsAbs(output) || os.Mkdir(output, 0700) != nil {
		t.Fatal("absolute, new private output directory required")
	}
	seed := os.Getenv("ASTRAL_SUCCESS_SCALED_SEED")
	if !filepath.IsAbs(seed) {
		t.Fatal("absolute captured request seed required")
	}
	setupStart := time.Now()
	raw, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	seedSum := sha256.Sum256(raw)
	var original core.Request
	if err = json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	var images []core.Content
	imageBytes := 0
	for _, message := range original.Messages {
		for _, part := range message.Content {
			if part.Type == "image" {
				if len(part.Data) == 0 || part.MediaType != "image/png" && part.MediaType != "image/jpeg" {
					t.Fatal("seed must contain frozen PNG/JPEG images")
				}
				images = append(images, core.Content{Type: "image", MediaType: part.MediaType, Data: part.Data})
				imageBytes += len(part.Data)
			}
		}
	}
	if len(images) != 227 {
		t.Fatal("expected the sealed 227-image source")
	}
	original, raw = core.Request{}, nil
	f := newFixture(t, 1, func(context.Context, int, map[string]any) any {
		t.Error("unexpected request escaped the offline model transport")
		return nil
	})
	f.service.RuntimeID = "scaled-host-benchmark"
	var profile domain.ModelProfile
	if err = f.store.Get(f.ctx, "models", f.agent.DefaultModelID, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Overrides == nil {
		profile.Overrides = map[string]json.RawMessage{}
	}
	for key, value := range map[string]any{"context_window": 1000000, "max_output_tokens": 16000, "image_token_budget": 1200, "streaming": false, "images": "supported", "tool_calling": "supported"} {
		profile.Overrides[key] = encode(value)
	}
	if err = f.service.Models.SaveProfile(f.ctx, &profile); err != nil {
		t.Fatal(err)
	}
	var connection domain.Provider
	if err = f.store.Get(f.ctx, "providers", profile.ProviderID, &connection); err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(connection.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	sink := &successLifecycleTransport{protocol: completions.Protocol, fastReceiver: receiverMode == "fast"}
	receiver := &scaledHostReceiver{sink: sink, endpoint: *endpoint}
	f.service.Models.HTTPClient.Transport = receiver
	toolPath := filepath.Join(f.daemons[0].Hello.Home, "scaled-host-read.txt")
	const toolText = "The real loopback Computer read this bounded public benchmark fixture.\n"
	if err = os.WriteFile(toolPath, []byte(toolText), 0600); err != nil {
		t.Fatal(err)
	}
	if mode == "tool" {
		receiver.toolPath = toolPath
	}
	var receiverProcess *scaledHostProcess
	if receiverMode == "process" {
		receiverProcess = startScaledHostProcess(t, output, receiver)
		f.service.Models.HTTPClient.Transport = receiverProcess
	}
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.syncBusinessConfig(f.ctx); err != nil {
		t.Fatal(err)
	}
	session := r.core.Load()
	base, err := session.ReadContext(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := r.resolveModel(f.ctx, base.Selection)
	if err != nil {
		t.Fatal(err)
	}
	_, projected := resolved.Model.(core.RequestProjector)
	_, preparedExecution := resolved.Model.(core.RequestExecutionPreparer)
	_, readonlyValidation := resolved.Model.(core.RequestValidatorReadOnly)
	if !projected || !preparedExecution || !readonlyValidation {
		t.Fatal("Host candidate must carry production projector/execution/read-only validator forwarding; use releaseHost source")
	}
	if resolved.Model.Spec().ContextWindow != 1000000 || resolved.Model.Spec().MaxOutputTokens != 16000 {
		t.Fatal("real Host profile did not select the 1M/16k target")
	}
	location, err := f.service.Timezone.Location(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	const inputID = "scaled-host-input"
	padding, actualPadding, calibratedTokens, calibrationCalls, collectedMessages := 0, 0, 0, 0, 0
	var history []core.Message
	var calibrated core.Request
	calibrationStart := time.Now()
	for ; calibrationCalls < 12; calibrationCalls++ {
		history, actualPadding = scaledHostHistory(images, padding, toolPath, textStyle)
		candidate := base
		candidate.Request.Messages = append(append([]core.Message(nil), base.Request.Messages...), history...)
		candidate.Request.ModelID, candidate.Request.ModelRevision = resolved.Model.Spec().ID, resolved.Model.Spec().Revision
		candidate.Request.MaxOutputTokens, candidate.Request.Options = 16000, resolved.Options
		fresh, err := r.collectContext(f.ctx, core.ContextBoundary{SessionID: f.session.ID, Reason: "boundary", CompactionID: session.State().Branch.CompactionID, Context: candidate})
		if err != nil {
			t.Fatal(err)
		}
		collectedMessages = len(fresh)
		candidate.Request.Messages = append(candidate.Request.Messages, fresh...)
		candidate.Request.Messages = append(candidate.Request.Messages, messages.Web(inputID, successScaledHostPrompt, "Owner", time.Now().In(location), nil))
		calibrated, err = core.ProjectRequest(f.ctx, resolved.Model, candidate.Request)
		if err != nil {
			t.Fatal(err)
		}
		calibratedTokens, err = modelbridge.EstimateTokens(f.ctx, resolved.Model, calibrated)
		if err != nil {
			t.Fatal(err)
		}
		if delta := calibratedTokens - target; delta <= target/1000 && delta >= -target/1000 {
			calibrationCalls++
			break
		}
		padding += (target - calibratedTokens) * 4
		if padding < 0 {
			t.Fatal("Host base context exceeds requested token target")
		}
	}
	if delta := calibratedTokens - target; delta > target/1000 || delta < -target/1000 {
		t.Fatal("Host+SDK token calibration did not converge")
	}
	calibrationNS := time.Since(calibrationStart).Nanoseconds()
	fixtureRaw, err := json.Marshal(calibrated)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSum := sha256.Sum256(fixtureRaw)
	fixtureBytes := len(fixtureRaw)
	if err = os.WriteFile(filepath.Join(output, "calibrated-host-request.json"), fixtureRaw, 0600); err != nil {
		t.Fatal(err)
	}
	fixtureRaw, calibrated = nil, core.Request{}
	// No runtime may cache the Store while its history is seeded. The normal
	// Service is fully joined before Commit; opening a new Host is setup below.
	bridge := f.service.Models
	if err = f.service.Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	state, err := f.coreStore.LoadState(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent := state.Branch.HeadID
	entries := make([]core.Entry, 0, len(history))
	for i := range history {
		id := fmt.Sprintf("scaled-host-history-%04d", i)
		entry := core.Entry{ID: id, ParentID: parent, Kind: core.EntryMessage, Message: &history[i], SessionID: f.session.ID, Version: state.Version + 1, Time: time.Unix(1700000000+int64(i), 0).UTC()}
		if history[i].Role == core.RoleTool {
			entry.Kind, entry.Message = core.EntryToolResult, nil
			entry.Tool = &core.ToolRecord{Invocation: core.Invocation{SessionID: f.session.ID, AssistantID: parent, ExecutionID: "scaled-host-history-read", Call: history[i-1].ToolCalls()[0]}, Result: &core.ToolResult{Content: history[i].Content, Outcome: core.OutcomeSucceeded}}
		}
		entries = append(entries, entry)
		parent = id
	}
	if _, err = f.coreStore.Commit(f.ctx, f.session.ID, core.Commit{ID: "scaled-host-history", ExpectedVersion: state.Version, Entries: entries}); err != nil {
		t.Fatal(err)
	}
	setupNS := time.Since(setupStart).Nanoseconds()
	openStart, openCPU := time.Now(), successScaledHostCPU(t)
	f.service = New(f.store, f.coreStore, bridge, f.hub)
	f.service.RuntimeID = "scaled-host-benchmark"
	session, err = f.service.Core(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	coldOpenNS, coldOpenCPUNS := time.Since(openStart).Nanoseconds(), successScaledHostCPU(t)-openCPU
	stateBefore := session.Snapshot().State
	sub := session.Subscribe(512)
	defer sub.Close()
	profileEnabled := os.Getenv("ASTRAL_SUCCESS_SCALED_PROFILE") == "1"
	var cpuProfile *os.File
	if profileEnabled {
		cpuProfile, err = os.OpenFile(filepath.Join(output, "cpu.pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err = pprof.StartCPUProfile(cpuProfile); err != nil {
			_ = cpuProfile.Close()
			t.Fatal(err)
		}
		defer func() {
			if cpuProfile != nil {
				pprof.StopCPUProfile()
				_ = cpuProfile.Close()
			}
		}()
	}
	var hotStarted time.Time
	var rounds []scaledHostRound
	runRound := func(index int) scaledHostRound {
		round := scaledHostRound{Index: index + 1, InputID: inputID, VersionBefore: session.Snapshot().State.Version}
		if index != 0 {
			round.InputID = fmt.Sprintf("%s-%02d", inputID, index+1)
		}
		receiver.mu.Lock()
		round.WireStart = len(receiver.wires)
		receiver.mu.Unlock()
		var before, after goruntime.MemStats
		goruntime.ReadMemStats(&before)
		cpuBefore := successScaledHostCPU(t)
		started := time.Now()
		if index == 0 {
			hotStarted, sink.started = started, started
		}
		round.StartNS = started.Sub(hotStarted).Nanoseconds()
		receipt, err := f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: round.InputID, Text: successScaledHostPrompt})
		if err != nil || receipt.Error != "" {
			t.Fatalf("real Host Submit failed in round %d: %v %s", index+1, err, receipt.Error)
		}
		var runEnd core.Event
		for runEnd.Type == "" {
			select {
			case event, ok := <-sub.C:
				if !ok {
					t.Fatalf("Host subscription closed: %v", sub.Err())
				}
				if event.Type == core.EventToolEnd {
					round.ToolEndNS = time.Since(started).Nanoseconds()
				}
				if event.Type == core.EventRunEnd {
					runEnd = event
				}
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
		}
		round.WallNS, round.CPUNS = time.Since(started).Nanoseconds(), successScaledHostCPU(t)-cpuBefore
		goruntime.ReadMemStats(&after)
		if runEnd.Status != "completed" || runEnd.Error != "" {
			t.Fatalf("Host run failed in round %d", index+1)
		}
		round.RunID = runEnd.RunID
		round.TotalAllocBytes, round.Mallocs, round.NumGC = after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs, after.NumGC-before.NumGC
		round.HeapAllocBefore, round.HeapAllocAfter = before.HeapAlloc, after.HeapAlloc
		round.VersionAfter = session.Snapshot().State.Version
		receiver.mu.Lock()
		round.WireEnd = len(receiver.wires)
		round.Wires = append([]scaledHostWire(nil), receiver.wires[round.WireStart:round.WireEnd]...)
		receiver.mu.Unlock()
		for i := range round.Wires {
			wire := &round.Wires[i]
			wire.DispatchNS -= round.StartNS
			wire.BodyReadNS -= round.StartNS
			wire.ReceiverReturnNS -= round.StartNS
			if wire.DispatchNS < 0 || wire.BodyReadNS < wire.DispatchNS || wire.ReceiverReturnNS < wire.BodyReadNS || wire.ReceiverReturnNS > round.WallNS {
				t.Fatal("invalid per-round Host receiver timeline")
			}
			round.ReceiverWallNS += wire.ReceiverWallNS
			round.ReceiverSinkNS += wire.SinkNS
		}
		round.LocalWallNS = round.WallNS - round.ReceiverWallNS
		round.Under50ms, round.LocalUnder50ms = round.WallNS < int64(50*time.Millisecond), round.LocalWallNS < int64(50*time.Millisecond)
		return round
	}
	for index := range roundCount {
		rounds = append(rounds, runRound(index))
	}
	parentPeakRSS, err := scaledHostPeakRSSBytes()
	if err != nil {
		t.Fatal(err)
	}
	if cpuProfile != nil {
		pprof.StopCPUProfile()
		if err = cpuProfile.Close(); err != nil {
			t.Fatal(err)
		}
		cpuProfile = nil
	}
	var wallNS, cpuNS, toolEndNS int64
	var allocBytes, mallocs uint64
	var numGC uint32
	allRoundsUnder50ms, allRoundsLocalUnder50ms := true, true
	for _, round := range rounds {
		wallNS, cpuNS = wallNS+round.WallNS, cpuNS+round.CPUNS
		allocBytes, mallocs, numGC = allocBytes+round.TotalAllocBytes, mallocs+round.Mallocs, numGC+round.NumGC
		allRoundsUnder50ms = allRoundsUnder50ms && round.Under50ms
		allRoundsLocalUnder50ms = allRoundsLocalUnder50ms && round.LocalUnder50ms
	}
	toolEndNS = rounds[0].ToolEndNS
	hotLoopEndNS := rounds[len(rounds)-1].StartNS + rounds[len(rounds)-1].WallNS
	receiver.mu.Lock()
	wires := append([]scaledHostWire(nil), receiver.wires...)
	receiver.mu.Unlock()
	wantCallsPerRound := 1
	if mode == "tool" {
		wantCallsPerRound = 2
	}
	wantCalls := roundCount * wantCallsPerRound
	var attempts []core.ModelAttempt
	cursor := ""
	for {
		page, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "generation", Cursor: cursor, Limit: 10, MaxBytes: 128 << 20})
		if err != nil {
			t.Fatal(err)
		}
		attempts = append(attempts, page.Attempts...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].Version < attempts[j].Version })
	if len(attempts) != wantCalls || len(wires) != wantCalls {
		t.Fatal("Host HTTP/attempt count differs")
	}
	for _, round := range rounds {
		if round.WireEnd-round.WireStart != wantCallsPerRound {
			t.Fatal("per-round HTTP count differs")
		}
	}
	var verified []map[string]any
	var imagePositions []int
	toolResultVerified, hostContextVerified := false, false
	for i, attempt := range attempts {
		if attempt.Outcome == nil || attempt.Outcome.Status != "completed" || attempt.Outcome.Usage.Available {
			t.Fatal("incomplete Host attempt or invented provider usage")
		}
		roundIndex := i / wantCallsPerRound
		if attempt.RunID != rounds[roundIndex].RunID {
			t.Fatal("attempt order differs from HTTP round order")
		}
		request, err := core.RebuildRequest(f.ctx, f.coreStore, attempt.Input, 64<<20)
		if err != nil {
			t.Fatal(err)
		}
		var frozen struct {
			Target core.ModelSnapshot `json:"target"`
		}
		if err = json.Unmarshal(attempt.Input.Snapshot, &frozen); err != nil || frozen.Target.Spec != attempt.Input.Model {
			t.Fatal("Host frozen target identity differs: ", err)
		}
		sdk, err := provider.Restore(frozen.Target, provider.Credentials{APIKey: "offline-placeholder", HTTPClient: bridge.HTTPClient})
		if err != nil {
			t.Fatal(err)
		}
		estimated, err := sdk.EstimateTokens(f.ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (estimated-target > target/1000 || target-estimated > target/1000) {
			t.Fatalf("actual Host request outside SDK target tolerance: target=%d estimated=%d calibration=%d", target, estimated, calibratedTokens)
		}
		body, err := sdk.RequestBody(request)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(encoded)
		if len(encoded) != wires[i].Bytes || hex.EncodeToString(sum[:]) != wires[i].SHA256 || request.MaxOutputTokens != 16000 {
			t.Fatal("Host persisted request differs from the complete transmitted body")
		}
		if !strings.Contains(request.SystemPrompt, identityHeading) {
			t.Fatal("Host system prompt preparation was bypassed")
		}
		for at, message := range request.Messages {
			if i == 0 {
				for _, part := range message.Content {
					if part.Type == "image" {
						imagePositions = append(imagePositions, at)
					}
				}
			}
			hostContextVerified = hostContextVerified || message.ContextKind == environmentKind
			if message.Role == core.RoleTool && message.ToolCallID == "scaled-host-read" && !message.IsError {
				for _, part := range message.Content {
					toolResultVerified = toolResultVerified || strings.Contains(part.Text, strings.TrimSpace(toolText))
				}
			}
		}
		rounds[roundIndex].EstimatedTokens = append(rounds[roundIndex].EstimatedTokens, estimated)
		verified = append(verified, map[string]any{"round": roundIndex + 1, "input_id": rounds[roundIndex].InputID, "digest": attempt.Input.Digest, "digest_rebuild_verified": true, "wire_rebuild_verified": true, "estimated_input_tokens": estimated, "delta_from_calibration": estimated - calibratedTokens, "max_output_tokens": request.MaxOutputTokens, "messages": len(request.Messages), "tools": len(request.Tools)})
	}
	if len(imagePositions) != 227 || imagePositions[0] <= 0 {
		t.Fatal("complete image collection missing from middle of Host history")
	}
	if !hostContextVerified || mode == "tool" && !toolResultVerified {
		t.Fatal("real Host environment or Computer read result missing")
	}
	stateAfter := session.Snapshot().State
	last, err := session.History().Get(f.ctx, stateAfter.Branch.LastMessageID)
	if err != nil || last.Message == nil || len(last.Message.Content) != 1 || last.Message.Content[0].Text != "Scaled offline reply." || stateAfter.CurrentAttemptID != "" {
		t.Fatal("Host final reply was not committed")
	}
	for _, round := range rounds {
		accepted, err := session.LookupInput(f.ctx, round.InputID)
		if err != nil || accepted.State != core.InputApplied {
			t.Fatalf("Host input was not applied in round %d", round.Index)
		}
	}
	compactions, err := session.Attempts().Scan(f.ctx, core.AttemptQuery{Purpose: "compaction", Limit: 1})
	if err != nil || len(compactions.Attempts) != 0 {
		t.Fatal("unexpected Host compaction")
	}
	var receiverWallNS, receiverSinkNS int64
	var segments []int64
	previousReturn := int64(0)
	for _, wire := range wires {
		if wire.DispatchNS < previousReturn || wire.ReceiverReturnNS > hotLoopEndNS {
			t.Fatal("invalid Host receiver timeline")
		}
		receiverWallNS += wire.ReceiverWallNS
		receiverSinkNS += wire.SinkNS
		segments = append(segments, wire.DispatchNS-previousReturn)
		previousReturn = wire.ReceiverReturnNS
	}
	localNS := wallNS - receiverWallNS
	config := f.service.runtimes[f.session.ID].coreConfig(f.session)
	readonlyCollector := reflect.ValueOf(config).FieldByName("CollectContextReadOnly")
	buildInfo, _ := debug.ReadBuildInfo()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binarySHA, _, err := successLifecycleSHA(executable)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]any{"scope": "actual Host Submit + live CollectContext + Core + SDK + one loopback Computer; model replaced by complete offline receiver", "scenario": scale, "mode": mode, "receiver": receiverMode, "text_style": textStyle, "round_count": roundCount,
		"parent_pid": os.Getpid(), "receiver_process_isolated": receiverProcess != nil, "receiver_test_sources_sha256": scaledHostProcessSourceSHA(), "parent_peak_rss_scope": "RUSAGE_SELF lifetime high-water sampled after hot loop before post-verification; includes setup and excludes child",
		"target_estimated_input_tokens": target, "calibrated_estimated_input_tokens": calibratedTokens, "token_count_kind": "real current SDK fallback estimate, not model-reported tokens", "padding_adjusted": padding != 0, "padding_requested_bytes": padding, "padding_actual_bytes": actualPadding, "padding_utf8_boundary_cut": true, "calibration_calls": calibrationCalls, "calibration_ns": calibrationNS, "calibration_fresh_host_messages": collectedMessages,
		"synthetic_profile_override": map[string]int{"context_window": 1000000, "max_output_tokens": 16000, "image_token_budget": 1200, "safety_margin_default": 4096}, "original_historical_target_reused": false,
		"seed_sha256": hex.EncodeToString(seedSum[:]), "calibrated_fixture_sha256": hex.EncodeToString(fixtureSum[:]), "calibrated_fixture_bytes": fixtureBytes, "images": len(images), "image_raw_json_bytes": imageBytes, "history_messages": len(history), "image_message_positions": imagePositions,
		"setup_ns": setupNS, "cold_host_open_ns": coldOpenNS, "cold_host_open_cpu_ns": coldOpenCPUNS, "cold_open_note": "new Host/Core runtime over setup database; OS page cache may be warm; live Computer remains connected",
		"host_projector_forwarding": projected, "host_execution_forwarding": preparedExecution, "host_readonly_validation": readonlyValidation, "host_readonly_estimator": config.EstimateTokensReadOnly != nil, "host_readonly_collector": readonlyCollector.IsValid() && readonlyCollector.Kind() == reflect.Func && !readonlyCollector.IsNil(),
		"binary_sha256": binarySHA, "build_info": buildInfo, "go_version": goruntime.Version(), "profiles_enabled": profileEnabled, "rounds": rounds,
		"round_timing_definition":      "each round starts immediately before its unique service.Submit and ends on durable RunEnd; per-round wire timestamps are relative to that Submit",
		"aggregate_metrics_definition": "top-level wall/cpu/allocation/GC values sum measured round intervals; top-level wire timestamps use the hot-loop origin; profiling includes only the hot loop and its between-round accounting",
		"timing_boundary":              "service.Submit entry through observation of durable Core EventRunEnd; includes live Computer Bind/Discover, Host prompt/configuration, actual budget/freeze, full model body and final reply/run commits",
		"local_wall_definition":        "derived original wall minus complete mock RoundTrip intervals; includes real Computer RPC, SDK response parsing and Core/Host work; mock-induced GC/scheduling can remain outside subtracted intervals", "forced_gc": 0}
	result := map[string]any{"success": true, "scenario": scale, "mode": mode, "receiver": receiverMode, "text_style": textStyle, "scope": "Host+Core+SDK", "wall_ns": wallNS, "cpu_ns": cpuNS, "under_50ms": wallNS < int64(50*time.Millisecond), "round_count": roundCount, "rounds": rounds, "hot_loop_wall_ns": hotLoopEndNS, "all_rounds_under_50ms": allRoundsUnder50ms, "all_rounds_local_under_50ms": allRoundsLocalUnder50ms,
		"parent_peak_rss_bytes":                 parentPeakRSS,
		"local_wall_excluding_mock_receiver_ns": localNS, "local_under_50ms": localNS < int64(50*time.Millisecond), "mock_receiver_wall_ns": receiverWallNS, "mock_receiver_read_validate_hash_ns": receiverSinkNS,
		"first_dispatch_ns": wires[0].DispatchNS, "local_dispatch_segments_ns": segments, "last_receiver_return_to_core_end_ns": hotLoopEndNS - previousReturn, "core_end_ns": hotLoopEndNS,
		"total_alloc_bytes": allocBytes, "mallocs": mallocs, "num_gc": numGC, "heap_alloc_before": rounds[0].HeapAllocBefore, "heap_alloc_after": rounds[len(rounds)-1].HeapAllocAfter, "forced_gc": 0,
		"wire_requests": wires, "verified_requests": verified, "tool_end_event_observed_ns": toolEndNS, "real_computer_read_verified": toolResultVerified, "real_host_environment_verified": hostContextVerified, "version_before": stateBefore.Version, "version_after": stateAfter.Version, "profiles_enabled": profileEnabled}
	if mode == "tool" {
		result["tool_end_event_observed_to_next_dispatch_ns"] = wires[1].DispatchNS - toolEndNS
		result["tool_end_timing_note"] = "subscription receive time after result commit, not the Computer implementation return; scheduling can make this derived interval negative"
	}
	if receiverProcess != nil {
		metadata["receiver_pid"] = receiverProcess.pid
		metadata["receiver_process_difference"] = "measurement isolation only, not a product speedup: complete body read/JSON/SHA and their GC run in the child; parent uses real http.Transport with Proxy:nil to owned 127.0.0.1 only"
		metadata["body_read_ns_kind"] = "upper bound: parent receives child acknowledgement headers after full server body validation; not an exact parent body-read timestamp"
		metadata["mock_interval_definition"] = "complete real loopback HTTP RoundTrip, including upload, child processing and response headers; local wall subtracts that interval"
	}
	successLifecycleWrite(t, filepath.Join(output, "metadata.json"), metadata)
	successLifecycleWrite(t, filepath.Join(output, "result.json"), result)
	t.Logf("Host %s %s text_style=%s receiver=%s real SDK tokens=%v wall=%s local=%s alloc=%d HTTP=%d rounds=%d", scale, mode, textStyle, receiverMode, verified[0]["estimated_input_tokens"], time.Duration(wallNS), time.Duration(localNS), allocBytes, len(wires), roundCount)
}
