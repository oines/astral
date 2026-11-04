//go:build darwin || linux

package harness

import (
	"bytes"
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
	"github.com/oines/astral-core/models/provider/openai/responses"
	"github.com/oines/astral-core/store/sqlite"
)

const successLifecycleReply = "Offline success lifecycle reply."

// The original baseline Core predates the opt-in borrowed callback boundary.
// A build-tagged helper enables it only for candidate builds.
var successLifecycleConfigure = func(*core.Config) {}

type successLifecycleWire struct {
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	ValidJSON  bool   `json:"valid_json"`
	Streaming  bool   `json:"streaming"`
	BodyReadNS int64  `json:"body_read_ns"`
	DispatchNS int64  `json:"dispatch_ns"`
	SinkNS     int64  `json:"sink_read_validate_hash_ns"`
}

// This is deliberately a full-body sink, not an early synthetic response. Its
// io.ReadAll, JSON validation and hash cost remains inside the measured interval
// and is also reported separately. It never delegates to a network transport.
type successLifecycleTransport struct {
	mu           sync.Mutex
	protocol     string
	started      time.Time
	wires        []successLifecycleWire
	fastReceiver bool
	omitUsage    bool
}

func (s *successLifecycleTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	dispatched := time.Since(s.started).Nanoseconds()
	path := "/chat/completions"
	if s.protocol == responses.Protocol {
		path = "/responses"
	}
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, path) {
		return nil, fmt.Errorf("offline success sink rejects endpoint %s %s", request.Method, request.URL.Path)
	}
	begin := time.Now()
	var body []byte
	var err error
	if s.fastReceiver && request.ContentLength > 0 && request.ContentLength <= 128<<20 {
		body = make([]byte, int(request.ContentLength))
		_, err = io.ReadFull(request.Body, body)
		if err == nil {
			var tail [1]byte
			n, end := request.Body.Read(tail[:])
			if n != 0 || end != io.EOF {
				err = errors.New("body exceeds declared length")
			}
		}
	} else {
		body, err = io.ReadAll(request.Body)
	}
	if err != nil {
		return nil, err
	}
	if err = request.Body.Close(); err != nil {
		return nil, err
	}
	wire := successLifecycleWire{Bytes: len(body), BodyReadNS: time.Since(s.started).Nanoseconds(), DispatchNS: dispatched}
	digest := sha256.Sum256(body)
	wire.SHA256 = hex.EncodeToString(digest[:])
	var envelope struct {
		Stream bool `json:"stream"`
	}
	if s.fastReceiver {
		wire.ValidJSON = len(body) > 0 && body[0] == '{' && ojson.ValidBytes(body)
		if wire.ValidJSON {
			envelope.Stream = ojson.GetBytesNoCopy(body, "stream").Bool()
		}
	} else {
		wire.ValidJSON = json.Valid(body)
		if wire.ValidJSON && json.Unmarshal(body, &envelope) != nil {
			wire.ValidJSON = false
		}
	}
	if !wire.ValidJSON {
		return nil, errors.New("offline success sink received invalid JSON object")
	}
	wire.Streaming = envelope.Stream
	wire.SinkNS = time.Since(begin).Nanoseconds()
	s.mu.Lock()
	s.wires = append(s.wires, wire)
	s.mu.Unlock()
	response := `{"id":"offline-success","choices":[{"index":0,"message":{"role":"assistant","content":"` + successLifecycleReply + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	contentType := "application/json"
	if envelope.Stream {
		response = "data: {\"id\":\"offline-success\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"" + successLifecycleReply + "\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
		contentType = "text/event-stream"
	}
	if s.protocol == responses.Protocol {
		item := `{"type":"message","id":"offline-message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"` + successLifecycleReply + `","annotations":[]}]}`
		response = `{"id":"offline-response","model":"offline-success","status":"completed","output":[` + item + `],"usage":{"input_tokens":1,"output_tokens":1}}`
		if envelope.Stream {
			response = "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":" + item + "}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + item + "}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n"
		}
	}
	if s.omitUsage {
		response = strings.ReplaceAll(response, `,"usage":{"prompt_tokens":1,"completion_tokens":1}`, "")
		response = strings.ReplaceAll(response, `,"usage":{"input_tokens":1,"output_tokens":1}`, "")
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
}

type successLifecycleTarget struct {
	Target           core.ModelSnapshot     `json:"target"`
	HostSnapshot     json.RawMessage        `json:"host_snapshot"`
	Options          core.GenerationOptions `json:"options"`
	CompactionPrompt string                 `json:"compaction_prompt"`
	SourceAttemptID  string                 `json:"source_attempt_id"`
	Source           string                 `json:"source"`
}

type successLifecycleEstimateShape struct {
	Messages int `json:"messages"`
	Images   int `json:"images"`
	Tokens   int `json:"estimated_tokens"`
}

// Captured ordinary requests retain the Host prompt that was actually prepared
// at that historical boundary. Using today's persona/profile here would change
// the usage-anchor digest and can introduce an unrelated compaction. Auxiliary
// requests keep their own prompt, and provider preparation still runs normally.
type successLifecycleHistoricalModel struct {
	provider.Model
	basePrompt, preparedPrompt string
}

func (m successLifecycleHistoricalModel) PrepareRequest(ctx context.Context, request core.Request) (core.Request, error) {
	if request.SystemPrompt == m.basePrompt {
		request.SystemPrompt = m.preparedPrompt
	}
	return m.Model.PrepareRequest(ctx, request)
}

func successLifecycleSHA(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	h := sha256.New()
	n, err := io.Copy(h, file)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func successLifecycleWrite(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		err = os.WriteFile(path, append(raw, '\n'), 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func successLifecycleSameJSON(a, b json.RawMessage) bool {
	var left, right bytes.Buffer
	return json.Compact(&left, a) == nil && json.Compact(&right, b) == nil && bytes.Equal(left.Bytes(), right.Bytes())
}

// Opt-in Core+SDK baseline. It includes durable input admission, preparation,
// budget/compaction, attempt freezing, SDK encoding, a complete HTTP body sink,
// response parsing, assistant provenance and SQLite commits. It is not a full
// Astral Host benchmark: Submit, CollectContext and live environment are absent.
// The interval starts with an already-open, restored session. No model warm-up
// or forced GC occurs; historical request reconstruction is setup, not timing.
func TestSuccessLifecycleReplay(t *testing.T) {
	scenario := os.Getenv("ASTRAL_SUCCESS_REPLAY")
	if scenario == "" {
		t.Skip("set ASTRAL_SUCCESS_REPLAY=control|captured")
	}
	if scenario != "control" && scenario != "captured" {
		t.Fatal("unknown success replay scenario")
	}
	requireAnchor := os.Getenv("ASTRAL_SUCCESS_REPLAY_REQUIRE_ANCHOR") == "1"
	if requireAnchor && scenario != "captured" {
		t.Fatal("strict historical usage anchor requires captured scenario")
	}
	caseDir, err := filepath.EvalSymlinks(os.Getenv("ASTRAL_HOST_REPLAY_CASE"))
	if err != nil || caseDir == "." || strings.Contains(caseDir, "Library/Application Support/astral") || strings.HasSuffix(caseDir, "-original") {
		t.Fatal("explicit disposable private case directory required")
	}
	output, err := filepath.Abs(os.Getenv("ASTRAL_HOST_REPLAY_OUTPUT"))
	if err != nil || os.Getenv("ASTRAL_HOST_REPLAY_OUTPUT") == "" {
		t.Fatal("explicit output directory required")
	}
	metadata := map[string]any{"scope": "Core+SDK", "scenario": scenario,
		"strict_historical_usage_anchor": requireAnchor,
		"timing_boundary":                "warm open Core session: Prompt entry through Run.Wait after reply commit",
		"limitations":                    []string{"Host Submit, CollectContext and live environment are omitted", "fixed offline reply; usage is synthetic in legacy mode and omitted in strict anchor mode; no real model or network", "full-body read, JSON validation and SHA256 sink cost is included", "session opening, optional historical fork and metadata inspection precede measurement"}}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	metadata["binary_sha256"], _, err = successLifecycleSHA(executable)
	if err != nil {
		t.Fatal(err)
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		metadata["build_info"] = info
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var target successLifecycleTarget
	sessionID := "success-control"
	database := filepath.Join(caseDir, "core.db")
	if scenario == "captured" {
		manifestPath := os.Getenv("ASTRAL_HOST_REPLAY_FIXTURE")
		raw, readErr := os.ReadFile(manifestPath)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var manifest struct {
			SessionID string `json:"target_session_id"`
			Files     map[string]struct {
				Bytes  int64  `json:"bytes"`
				SHA256 string `json:"sha256"`
			} `json:"files"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			t.Fatal(err)
		}
		original, err := filepath.EvalSymlinks(filepath.Dir(manifestPath))
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(original, caseDir)
		if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatal("case must be outside sealed fixture")
		}
		if relative, err := filepath.Rel(original, output); err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatal("output must be outside sealed fixture")
		}
		digest, count, err := successLifecycleSHA(database)
		sealed, ok := manifest.Files["core.db"]
		if err != nil || !ok || len(sealed.SHA256) != 64 || digest != sealed.SHA256 || count != sealed.Bytes {
			t.Fatal("disposable database does not match sealed manifest")
		}
		if info, err := os.Stat(database + "-wal"); err == nil && info.Size() != 0 {
			t.Fatal("nonempty initial WAL is forbidden")
		} else if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		metadata["source_core_db_sha256"] = digest
		manifestHash := sha256.Sum256(raw)
		metadata["fixture_manifest_sha256"] = hex.EncodeToString(manifestHash[:])
		sessionID = manifest.SessionID
		if sessionID == "" {
			t.Fatal("fixture has no session ID")
		}
		raw, err = os.ReadFile(os.Getenv("ASTRAL_HOST_REPLAY_TARGET"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &target); err != nil {
			t.Fatal(err)
		}
		if target.Target.Spec.ID == "" {
			var snapshot core.ModelSnapshot
			if err := json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			var host struct {
				Target core.ModelSnapshot `json:"target"`
			}
			if err := json.Unmarshal(snapshot.Data, &host); err != nil {
				t.Fatal(err)
			}
			if host.Target.Spec != snapshot.Spec {
				t.Fatal("outer historical snapshot target mismatch")
			}
			target.Target, target.HostSnapshot = host.Target, snapshot.Data
			target.Source = "exact historical Core snapshot"
		}
		if target.SourceAttemptID == "" {
			target.SourceAttemptID = os.Getenv("ASTRAL_SUCCESS_REPLAY_ATTEMPT")
		}
	} else {
		if _, err := os.Stat(database); !os.IsNotExist(err) {
			t.Fatal("control requires fresh empty case")
		}
		streaming := false
		target.Target, err = provider.Freeze(completions.Protocol, models.Config{Spec: core.ModelSpec{ID: "offline-success", ContextWindow: 128000, MaxOutputTokens: 4096}, Model: "offline-success", BaseURL: "https://offline.invalid/v1", Streaming: &streaming, Images: "supported", ToolCalling: "supported"})
		if err != nil {
			t.Fatal(err)
		}
		target.Source = "synthetic text control"
	}
	var frozen struct {
		Protocol string `json:"protocol"`
	}
	if err := json.Unmarshal(target.Target.Data, &frozen); err != nil || frozen.Protocol != completions.Protocol && frozen.Protocol != responses.Protocol {
		t.Fatal("success baseline supports exact Chat Completions and Responses targets")
	}
	transport := &successLifecycleTransport{protocol: frozen.Protocol, fastReceiver: os.Getenv("ASTRAL_SUCCESS_REPLAY_RECEIVER") == "fast", omitUsage: requireAnchor}
	metadata["receiver"] = os.Getenv("ASTRAL_SUCCESS_REPLAY_RECEIVER")
	credentials := provider.Credentials{APIKey: "offline-placeholder", HTTPClient: &http.Client{Transport: transport}}
	adapter, err := provider.Restore(target.Target, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if len(target.HostSnapshot) == 0 {
		target.HostSnapshot = target.Target.Data
	}
	store, err := sqlite.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var view core.Context
	var anchorTokens int
	var anchorFacts map[string]any
	var model provider.Model = adapter
	if scenario == "captured" {
		state, err := store.LoadState(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		head := state.Branch.HeadID
		if selected := os.Getenv("ASTRAL_SUCCESS_REPLAY_HEAD"); selected != "" {
			head = selected
		}
		view, err = core.ReadContext(ctx, store, state.RolloutID, head, 64<<20)
		if err != nil {
			t.Fatal(err)
		}
		metadata["selected_head"] = head
		if target.SourceAttemptID == "" {
			t.Fatal("captured target requires source_attempt_id proving its frozen historical target")
		}
		attempt, err := store.GetAttempt(ctx, sessionID, target.SourceAttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if attempt.Input.Model != target.Target.Spec || !successLifecycleSameJSON(attempt.Input.Snapshot, target.HostSnapshot) {
			t.Fatal("target differs from named historical attempt snapshot")
		}
		if attempt.Purpose != "generation" || attempt.Outcome == nil || attempt.Outcome.Status != "completed" || attempt.Outcome.EntryID != head || attempt.Input.ConfigurationID != view.ConfigurationID {
			t.Fatal("captured Prompt baseline must fork the named successful generation's committed reply boundary")
		}
		target.HostSnapshot = append(json.RawMessage(nil), attempt.Input.Snapshot...)
		metadata["source_attempt_id"] = target.SourceAttemptID
		request, err := core.RebuildRequest(ctx, store, attempt.Input, 64<<20)
		if err != nil {
			t.Fatal(err)
		}
		target.Options = request.Options
		model = successLifecycleHistoricalModel{Model: adapter, basePrompt: view.Request.SystemPrompt, preparedPrompt: request.SystemPrompt}
		metadata["preparation"] = "fixed historical Host prompt/options followed by real SDK preparation"
		metadata["historical_request_digest"] = attempt.Input.Digest
		metadata["historical_max_output_tokens"] = request.MaxOutputTokens
		if requireAnchor {
			record, err := store.Get(ctx, attempt.Input.RolloutID, head)
			if err != nil {
				t.Fatal(err)
			}
			entry := record.Entry
			usage := entry.Usage
			if entry.Message == nil || entry.Message.Role != core.RoleAssistant || entry.Generation == nil || !usage.Available || usage.InputMissing || usage.OutputMissing || usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.InputTokens > int(^uint(0)>>1)-usage.OutputTokens || usage.InputTokens+usage.OutputTokens == 0 || !jsonEqual(usage, attempt.Outcome.Usage) {
				t.Fatal("source reply lacks complete original provider usage")
			}
			provenance := core.GenerationProvenance(attempt, request, *entry.Message)
			if !jsonEqual(*entry.Generation, provenance) {
				t.Fatal("original reply provenance does not match rebuilt original request")
			}
			anchorTokens = usage.InputTokens + usage.OutputTokens
			anchorFacts = map[string]any{"source_entry_id": entry.ID, "source_attempt_id": attempt.ID, "source_usage": usage, "source_generation": entry.Generation, "source_request_digest": attempt.Input.Digest, "source_reply_provenance_verified": true, "expected_anchored_input_tokens": anchorTokens}
			metadata["historical_usage_anchor"] = anchorFacts
		}
	}
	var estimateMu sync.Mutex
	var estimates []successLifecycleEstimateShape
	var toolExecutions atomic.Int64
	tools := make([]core.Tool, 0, len(view.Request.Tools))
	for _, definition := range view.Request.Tools {
		tools = append(tools, core.ToolFunc{Spec: definition, Run: func(context.Context, core.Invocation, func(core.ToolProgress)) (core.ToolExecution, error) {
			toolExecutions.Add(1)
			return core.ToolExecution{}, errors.New("offline success baseline forbids tool execution")
		}})
	}
	config := core.Config{Store: store, Tools: tools, MaxContextBytes: 64 << 20, MaxTurns: 4,
		Selection:  core.ModelSelection{ID: target.Target.Spec.ID},
		Compaction: core.CompactionPolicy{SystemPrompt: target.CompactionPrompt},
		EstimateTokens: func(ctx context.Context, model core.Model, request core.Request) (int, error) {
			tokens, err := model.(interface {
				EstimateTokens(context.Context, core.Request) (int, error)
			}).EstimateTokens(ctx, request)
			if requireAnchor {
				shape := successLifecycleEstimateShape{Messages: len(request.Messages), Tokens: tokens}
				for _, message := range request.Messages {
					for _, part := range message.Content {
						if part.Type == "image" {
							shape.Images++
						}
					}
				}
				estimateMu.Lock()
				estimates = append(estimates, shape)
				estimateMu.Unlock()
			}
			return tokens, err
		},
		ResolveModel: func(_ context.Context, selection core.ModelSelection) (core.ResolvedModel, error) {
			if scenario == "captured" && selection.ID != view.Selection.ID {
				return core.ResolvedModel{}, errors.New("selection differs from selected historical boundary")
			}
			return core.ResolvedModel{Model: model, Options: target.Options, Snapshot: target.HostSnapshot}, nil
		},
		RestoreModel: func(_ context.Context, snapshot core.ModelSnapshot) (core.Model, error) {
			var host struct {
				Target core.ModelSnapshot `json:"target"`
			}
			if err := json.Unmarshal(snapshot.Data, &host); err != nil {
				return nil, err
			}
			if host.Target.Spec.ID != "" {
				if host.Target.Spec != snapshot.Spec {
					return nil, errors.New("historical wrapper snapshot specification mismatch")
				}
				return provider.Restore(host.Target, credentials)
			}
			return provider.Restore(snapshot, credentials)
		},
	}
	successLifecycleConfigure(&config)
	var session *core.Session
	if scenario == "control" {
		session, err = core.NewSession(ctx, config, core.SessionOptions{ID: sessionID})
	} else {
		session, err = core.OpenSession(ctx, config, sessionID)
		if err == nil && os.Getenv("ASTRAL_SUCCESS_REPLAY_HEAD") != "" {
			source := session
			session, err = source.Fork(ctx, os.Getenv("ASTRAL_SUCCESS_REPLAY_HEAD"), core.ForkOptions{ID: "success-captured-fork"})
			_ = source.Close(ctx)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	stateBefore := session.Snapshot().State
	if !stateBefore.Branch.Complete() || stateBefore.CurrentAttemptID != "" || stateBefore.PendingInputs[core.InputSteer] != 0 || stateBefore.PendingInputs[core.InputFollowUp] != 0 {
		t.Fatal("ordinary Prompt baseline requires a complete boundary with no unfinished generation or driving input")
	}
	view, err = session.ReadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	images := 0
	for _, message := range view.Request.Messages {
		for _, part := range message.Content {
			if part.Type == "image" {
				images++
			}
		}
	}
	metadata["context_messages"], metadata["context_image_parts"] = len(view.Request.Messages), images
	metadata["target_spec"], metadata["target_source"], metadata["operation"] = target.Target.Spec, target.Source, "Prompt"
	if requireAnchor {
		estimateMu.Lock()
		beforeProbe := len(estimates)
		estimateMu.Unlock()
		probe, err := session.InspectContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		estimateMu.Lock()
		probeCalls := len(estimates) - beforeProbe
		estimates = nil
		estimateMu.Unlock()
		if probe.EstimatedInput != anchorTokens || probeCalls != 0 {
			t.Fatalf("original usage anchor not hit: estimated=%d original=%d SDK estimator calls=%d", probe.EstimatedInput, anchorTokens, probeCalls)
		}
		anchorFacts["inspect_context_estimated_input"] = probe.EstimatedInput
		anchorFacts["inspect_context_sdk_estimator_calls"] = probeCalls
		anchorFacts["anchor_hit_verified"] = true
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal("new output directory required: ", err)
	}
	successLifecycleWrite(t, filepath.Join(output, "metadata.json"), metadata)
	var cpuProfile *os.File
	if os.Getenv("ASTRAL_SUCCESS_REPLAY_PROFILES") == "1" {
		cpuProfile, err = os.Create(filepath.Join(output, "cpu.pprof"))
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(cpuProfile); err != nil {
			t.Fatal(err)
		}
	}
	var before, after goruntime.MemStats
	goruntime.ReadMemStats(&before)
	cpuBefore := successReplayCPU(t)
	started := time.Now()
	transport.started = started
	prompt, operationErr := session.Prompt(ctx, core.Input{ID: "success-lifecycle-input", Message: core.User("Reply to this offline success benchmark prompt.")})
	var runResult core.RunResult
	if operationErr == nil && prompt.Run == nil {
		operationErr = errors.New("Prompt returned no new run")
	}
	if operationErr == nil {
		runResult, operationErr = prompt.Run.Wait(ctx)
	}
	wallNS, cpuNS := time.Since(started).Nanoseconds(), successReplayCPU(t)-cpuBefore
	goruntime.ReadMemStats(&after)
	if cpuProfile != nil {
		pprof.StopCPUProfile()
		_ = cpuProfile.Close()
	}
	if os.Getenv("ASTRAL_SUCCESS_REPLAY_PROFILES") == "1" {
		file, err := os.Create(filepath.Join(output, "heap.pprof"))
		if err != nil {
			t.Fatal(err)
		}
		err = pprof.WriteHeapProfile(file)
		_ = file.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	stateAfter := session.Snapshot().State
	attempts, inspectErr := session.Attempts().Scan(ctx, core.AttemptQuery{Purpose: "generation", Reverse: true, Limit: 1, MaxBytes: 128 << 20})
	if operationErr == nil {
		operationErr = inspectErr
	}
	if operationErr == nil && (runResult.Status != "completed" || runResult.LastMessage == nil || runResult.LastMessage.Role != core.RoleAssistant || len(runResult.LastMessage.ToolCalls()) != 0 || len(runResult.LastMessage.Content) != 1 || runResult.LastMessage.Content[0].Text != successLifecycleReply) {
		operationErr = fmt.Errorf("ordinary assistant generation was not committed: status=%s", runResult.Status)
	}
	if operationErr == nil && (len(attempts.Attempts) != 1 || attempts.Attempts[0].RunID != runResult.RunID || attempts.Attempts[0].Outcome == nil || attempts.Attempts[0].Outcome.Status != "completed" || attempts.Attempts[0].Outcome.EntryID != runResult.LastMessageID) {
		operationErr = errors.New("final generation attempt was not completed and adopted")
	}
	compactions, compactionErr := session.Attempts().Scan(ctx, core.AttemptQuery{Purpose: "compaction", Reverse: true, Limit: 1, MaxBytes: 128 << 20})
	if operationErr == nil {
		operationErr = compactionErr
	}
	if operationErr == nil && (len(transport.wires) != 1 || len(compactions.Attempts) != 0 || toolExecutions.Load() != 0 || stateAfter.CurrentAttemptID != "" || stateAfter.LastRun == nil || stateAfter.LastRun.Status != "completed") {
		operationErr = errors.New("incomplete success lifecycle")
	}
	finalRequestVerified := false
	var finalRequest core.Request
	if operationErr == nil {
		// RebuildRequest applies the persisted recipe and verifies the original
		// canonical SHA256 digest. This post-measurement assertion does not
		// replace or exclude Core's timed production freeze/digest work.
		finalRequest, operationErr = core.RebuildRequest(ctx, store, attempts.Attempts[0].Input, 64<<20)
		finalRequestVerified = operationErr == nil
	}
	if operationErr == nil {
		receipt, err := session.LookupInput(ctx, "success-lifecycle-input")
		if err != nil || receipt.State != core.InputApplied {
			operationErr = errors.New("new benchmark input was not applied")
		}
	}
	var timedEstimates []successLifecycleEstimateShape
	if requireAnchor {
		estimateMu.Lock()
		timedEstimates = append(timedEstimates, estimates...)
		estimateMu.Unlock()
		if operationErr == nil && (len(timedEstimates) != 2 || timedEstimates[0].Messages != 0 || timedEstimates[1].Messages != 1 || timedEstimates[0].Images != 0 || timedEstimates[1].Images != 0) {
			operationErr = fmt.Errorf("captured Prompt did not use only empty/tail SDK estimates: %+v", timedEstimates)
		}
		if operationErr == nil && attempts.Attempts[0].Outcome.Usage.Available {
			operationErr = errors.New("strict anchor reply unexpectedly reported synthetic usage")
		}
	}
	result := map[string]any{"success": operationErr == nil, "scope": "Core+SDK", "scenario": scenario, "operation": "Prompt", "wall_ns": wallNS, "cpu_ns": cpuNS,
		"total_alloc_bytes": after.TotalAlloc - before.TotalAlloc, "mallocs": after.Mallocs - before.Mallocs, "num_gc": after.NumGC - before.NumGC, "forced_gc": 0,
		"heap_alloc_before": before.HeapAlloc, "heap_alloc_after": after.HeapAlloc, "heap_inuse_before": before.HeapInuse, "heap_inuse_after": after.HeapInuse,
		"wire_requests": transport.wires, "http_calls": len(transport.wires), "tool_executions": toolExecutions.Load(), "run_status": runResult.Status, "version_before": stateBefore.Version, "version_after": stateAfter.Version,
		"pending_before": stateBefore.PendingInputs, "pending_after": stateAfter.PendingInputs, "profiles_enabled": cpuProfile != nil}
	result["new_compaction_attempts"], result["final_request_digest_verified"] = len(compactions.Attempts), finalRequestVerified
	result["strict_historical_usage_anchor"], result["historical_usage_anchor_verified"], result["new_reply_usage_omitted"] = requireAnchor, requireAnchor && operationErr == nil, requireAnchor
	if requireAnchor {
		result["historical_usage_anchor"], result["timed_sdk_estimator_calls"] = anchorFacts, timedEstimates
	}
	var sinkNS int64
	for _, wire := range transport.wires {
		sinkNS += wire.SinkNS
	}
	result["mock_receiver_read_validate_hash_ns"] = sinkNS
	result["local_wall_excluding_receiver_sink_ns"] = wallNS - sinkNS
	result["local_wall_definition"] = "derived original wall minus measured receiver read/JSON/SHA intervals; response construction remains included; receiver-induced GC may remain outside those intervals"
	result["final_max_output_tokens"] = finalRequest.MaxOutputTokens
	if len(transport.wires) != 0 {
		result["first_body_read_ns"] = transport.wires[0].BodyReadNS
		result["last_body_read_ns"] = transport.wires[len(transport.wires)-1].BodyReadNS
	}
	if len(attempts.Attempts) != 0 {
		attempt := attempts.Attempts[0]
		result["final_request_digest"], result["generation_attempt_outcome"] = attempt.Input.Digest, attempt.Outcome
	}
	if operationErr != nil {
		result["error"] = operationErr.Error()
	}
	successLifecycleWrite(t, filepath.Join(output, "result.json"), result)
	if operationErr != nil {
		t.Fatal(operationErr)
	}
	t.Logf("Core+SDK %s success: wall=%s CPU=%s alloc=%d HTTP=%d", scenario, time.Duration(wallNS), time.Duration(cpuNS), after.TotalAlloc-before.TotalAlloc, len(transport.wires))
}
