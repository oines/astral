//go:build darwin || linux

package harness

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
	"weak"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/models"
	"github.com/oines/astral-core/models/provider"
	"github.com/oines/astral-core/models/provider/openai/completions"
	"github.com/oines/astral-core/store/sqlite"
	"golang.org/x/sys/unix"
)

const longMemorySummaryPrompt = "Summarize the memory lifecycle fixture history. Preserve only the current task."
const longMemoryCompactEvery = 50
const longMemoryActiveLimit = 16 << 20

type longMemoryPhaseKey struct{}

// The receiver consumes each complete body but retains no request, decoded
// history, image or wire bytes. Its counters have a fixed memory footprint.
type longMemoryTransport struct {
	step, normal, summaries, bytes, sinkNS, peakWire atomic.Int64
}

func (m *longMemoryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/chat/completions") {
		return nil, errors.New("memory fixture forbids unexpected network endpoints")
	}
	started := time.Now()
	n, err := io.Copy(io.Discard, request.Body)
	closeErr := request.Body.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	if n == 0 || request.ContentLength > 0 && request.ContentLength != n {
		return nil, errors.New("memory fixture did not consume the complete wire body")
	}
	m.bytes.Add(n)
	m.sinkNS.Add(time.Since(started).Nanoseconds())
	longMemoryMax(&m.peakWire, n)
	response := `{"id":"memory-reply","choices":[{"index":0,"message":{"role":"assistant","content":"Step completed."},"finish_reason":"stop"}]}`
	if summary, _ := request.Context().Value(longMemoryPhaseKey{}).(bool); summary {
		m.summaries.Add(1)
		response = `{"id":"memory-summary","choices":[{"index":0,"message":{"role":"assistant","content":"Earlier fixture steps completed. Continue the current memory lifecycle check."},"finish_reason":"stop"}]}`
	} else {
		call, step := m.normal.Add(1), m.step.Load()
		if call != 2*step-1 && call != 2*step {
			return nil, fmt.Errorf("memory fixture normal call %d does not belong to step %d", call, step)
		}
		if call%2 == 1 {
			arguments := strconv.Quote(fmt.Sprintf(`{"step":%d}`, step))
			response = fmt.Sprintf(`{"id":"memory-tool","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"memory-call-%d","type":"function","function":{"name":"memory_blob","arguments":%s}}]},"finish_reason":"tool_calls"}]}`, step, arguments)
		}
	}
	// Usage is deliberately absent: no invented usage anchor suppresses real SDK
	// estimation or hides an oversized active context in this lifecycle workload.
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
}

type longMemoryWeak struct {
	text, image weak.Pointer[byte]
}

type longMemoryModel struct {
	provider.Model
	mu        sync.Mutex
	probes    map[int]longMemoryWeak
	estimates atomic.Int64
	peakInput atomic.Int64
}

func (m *longMemoryModel) EstimateTokens(ctx context.Context, request core.Request) (int, error) {
	n, err := m.Model.EstimateTokens(ctx, request)
	m.estimates.Add(1)
	if err == nil {
		longMemoryMax(&m.peakInput, int64(n))
	}
	return n, err
}

func (m *longMemoryModel) ValidateRequestReadOnly(ctx context.Context, request core.Request) error {
	return core.ValidateRequest(ctx, m.Model, request)
}

func (m *longMemoryModel) ProjectRequest(ctx context.Context, request core.Request) (core.Request, error) {
	// This observes the actual borrowed Core window, rather than the temporary
	// tool-return value that Core necessarily copies before storing it.
	m.mu.Lock()
	for _, message := range request.Messages {
		if message.Role != core.RoleTool || message.Name != "memory_blob" {
			continue
		}
		step, err := strconv.Atoi(strings.TrimPrefix(message.ToolCallID, "memory-call-"))
		if err != nil {
			continue
		}
		if _, exists := m.probes[step]; exists {
			continue
		}
		var probe longMemoryWeak
		for _, part := range message.Content {
			if part.Type == "text" && len(part.Text) > 0 {
				probe.text = weak.Make(unsafe.StringData(part.Text))
			}
			if part.Type == "image" && len(part.Data) > 0 {
				probe.image = weak.Make(&part.Data[0])
			}
		}
		m.probes[step] = probe
	}
	m.mu.Unlock()
	if projector, ok := m.Model.(core.RequestProjector); ok {
		return projector.ProjectRequest(ctx, request)
	}
	return core.PrepareRequest(ctx, m.Model, request)
}

func (m *longMemoryModel) Stream(ctx context.Context, request core.Request, emit func(core.ModelEvent)) (core.Reply, error) {
	return m.Model.Stream(context.WithValue(ctx, longMemoryPhaseKey{}, request.SystemPrompt == longMemorySummaryPrompt), request, emit)
}

func (m *longMemoryModel) PrepareExecution(ctx context.Context, request core.Request) (core.RequestExecution, error) {
	preparer, ok := m.Model.(core.RequestExecutionPreparer)
	if !ok {
		return nil, nil
	}
	execution, err := preparer.PrepareExecution(ctx, request)
	if err != nil || execution == nil {
		return execution, err
	}
	summary := request.SystemPrompt == longMemorySummaryPrompt
	return core.RequestExecutionFunc(func(ctx context.Context, emit func(core.ModelEvent)) (core.Reply, error) {
		return execution.Stream(context.WithValue(ctx, longMemoryPhaseKey{}, summary), emit)
	}), nil
}

func (m *longMemoryModel) oldBodies(through int) (observed, old, aliveText, aliveImage int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	observed = len(m.probes)
	for step, probe := range m.probes {
		if step > through {
			continue
		}
		old++
		if probe.text.Value() != nil {
			aliveText++
		}
		if probe.image.Value() != nil {
			aliveImage++
		}
	}
	return
}

type longMemorySample struct {
	Phase             string          `json:"phase"`
	Step              int             `json:"step"`
	ElapsedNS         int64           `json:"elapsed_ns"`
	RSS               uint64          `json:"rss_bytes"`
	ProcessPeakRSS    uint64          `json:"process_peak_rss_bytes"`
	HeapAlloc         uint64          `json:"heap_alloc_bytes"`
	HeapInuse         uint64          `json:"heap_inuse_bytes"`
	HeapSys           uint64          `json:"heap_sys_bytes"`
	GoSys             uint64          `json:"go_sys_bytes"`
	TotalAlloc        uint64          `json:"total_alloc_bytes"`
	NumGC             uint32          `json:"num_gc"`
	ForcedGC          uint32          `json:"forced_gc"`
	Context           core.Statistics `json:"context"`
	ArchiveEntries    int64           `json:"archive_entries"`
	ArchivedToolSteps int64           `json:"archived_tool_results"`
	Compactions       int64           `json:"committed_compactions"`
	ArchiveAttempts   int64           `json:"archive_attempts"`
	PayloadBytes      int64           `json:"archived_unique_payload_bytes"`
	DatabaseBytes     int64           `json:"database_file_bytes"`
	WALBytes          int64           `json:"wal_file_bytes"`
	NormalAPI         int64           `json:"normal_api_calls"`
	SummaryAPI        int64           `json:"summary_api_calls"`
	ObservedBodies    int             `json:"observed_core_bodies"`
	OldBodies         int             `json:"bodies_older_than_two_windows"`
	OldTextAlive      int             `json:"old_text_backings_alive"`
	OldImagesAlive    int             `json:"old_image_backings_alive"`
}

type longMemoryTrend struct {
	Samples             int     `json:"samples"`
	MemoryPerArchive    float64 `json:"memory_bytes_per_added_archive_byte"`
	MedianGrowthBytes   int64   `json:"late_minus_early_quartile_median_bytes"`
	BodySizedLinearRise bool    `json:"body_sized_linear_growth_detected"`
}

func longMemoryGrowth(samples []longMemorySample, maximumActive int, memory func(longMemorySample) uint64) longMemoryTrend {
	var stages []longMemorySample
	for _, sample := range samples {
		if sample.Phase == "after_compact" && sample.Step >= 4*longMemoryCompactEvery {
			stages = append(stages, sample)
		}
	}
	trend := longMemoryTrend{Samples: len(stages)}
	if len(stages) < 4 {
		return trend
	}
	var sumX, sumY float64
	for _, sample := range stages {
		sumX += float64(sample.PayloadBytes)
		sumY += float64(memory(sample))
	}
	meanX, meanY := sumX/float64(len(stages)), sumY/float64(len(stages))
	var covariance, variance float64
	for _, sample := range stages {
		x, y := float64(sample.PayloadBytes)-meanX, float64(memory(sample))-meanY
		covariance += x * y
		variance += x * x
	}
	if variance > 0 {
		trend.MemoryPerArchive = covariance / variance
	}
	quartile := max(1, len(stages)/4)
	median := func(values []longMemorySample) int64 {
		numbers := make([]uint64, len(values))
		for i, sample := range values {
			numbers[i] = memory(sample)
		}
		sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
		return int64(numbers[len(numbers)/2])
	}
	trend.MedianGrowthBytes = median(stages[len(stages)-quartile:]) - median(stages[:quartile])
	// This detects body-sized retention, while the separate weak-reference
	// diagnostic checks actual old Core backings. A finite run cannot establish
	// an asymptotic guarantee for every possible small metadata allocation.
	trend.BodySizedLinearRise = trend.MemoryPerArchive > 0.35 && trend.MedianGrowthBytes > int64(max(32<<20, 2*maximumActive))
	return trend
}

func longMemoryRSS() uint64 {
	if goruntime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/self/statm")
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) > 1 {
				pages, _ := strconv.ParseUint(fields[1], 10, 64)
				return pages * uint64(os.Getpagesize())
			}
		}
		return 0
	}
	raw, err := exec.Command("/bin/ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
	if err != nil {
		return 0
	}
	kib, _ := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	return kib * 1024
}

func longMemoryPeakRSS() uint64 {
	var usage unix.Rusage
	if unix.Getrusage(unix.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	if goruntime.GOOS == "darwin" {
		return uint64(usage.Maxrss)
	}
	return uint64(usage.Maxrss) * 1024
}

func longMemoryMax(target *atomic.Int64, value int64) {
	for current := target.Load(); value > current; current = target.Load() {
		if target.CompareAndSwap(current, value) {
			return
		}
	}
}

func longMemoryPNG() []byte {
	image := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	rng := rand.New(rand.NewSource(20261105))
	_, _ = rng.Read(image.Pix)
	for i := 3; i < len(image.Pix); i += 4 {
		image.Pix[i] = 255
	}
	var buffer bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&buffer, image); err != nil {
		panic(err)
	}
	return buffer.Bytes()
}

func longMemoryImage(template []byte, step int) json.RawMessage {
	// A valid, unique ancillary chunk defeats payload deduplication without
	// retaining 1000 prebuilt images or recompressing pixels in the hot loop.
	comment := []byte("memory-step\x00" + strconv.Itoa(step))
	pixels := append(make([]byte, 0, len(template)+len(comment)+12), template[:len(template)-12]...)
	pixels = binary.BigEndian.AppendUint32(pixels, uint32(len(comment)))
	pixels = append(pixels, "tEXt"...)
	pixels = append(pixels, comment...)
	checksum := crc32.Update(crc32.ChecksumIEEE([]byte("tEXt")), crc32.IEEETable, comment)
	pixels = binary.BigEndian.AppendUint32(pixels, checksum)
	pixels = append(pixels, template[len(template)-12:]...)
	raw := make([]byte, 1, base64.StdEncoding.EncodedLen(len(pixels))+2)
	raw[0] = '"'
	raw = base64.StdEncoding.AppendEncode(raw, pixels)
	return append(raw, '"')
}

func longMemoryValidateImage(template []byte) error {
	var encoded string
	if err := json.Unmarshal(longMemoryImage(template, 1), &encoded); err != nil {
		return err
	}
	pixels, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return err
	}
	_, err = png.Decode(bytes.NewReader(pixels))
	return err
}

func longMemoryStep(ctx context.Context, session *core.Session, transport *longMemoryTransport, step int) error {
	transport.step.Store(int64(step))
	result, err := session.Prompt(ctx, core.Input{ID: fmt.Sprintf("memory-step-%d", step), Message: core.User(fmt.Sprintf("Perform memory fixture step %d.", step))})
	if err != nil {
		return err
	}
	finished, err := result.Run.Wait(ctx)
	if err != nil {
		return err
	}
	if finished.Status != "completed" {
		return fmt.Errorf("step %d ended as %s", step, finished.Status)
	}
	return nil
}

// TestSQLiteLongRunMemoryReplay is intentionally opt-in. It exercises a new
// SQLite rollout, real Core tool batches/compaction and the real SDK against a
// full-body offline receiver; it is not a whole Astral desktop RSS benchmark.
func TestSQLiteLongRunMemoryReplay(t *testing.T) {
	if os.Getenv("ASTRAL_MEMORY_REPLAY") != "1" {
		t.Skip("set ASTRAL_MEMORY_REPLAY=1 and ASTRAL_MEMORY_OUTPUT to run the bounded-window archive-growth workload")
	}
	steps, mode := 1000, "longrun1000"
	if diagnostic := os.Getenv("ASTRAL_MEMORY_DIAGNOSTIC_STEPS"); diagnostic != "" {
		if diagnostic != "50" {
			t.Fatal("only the explicit 50-step diagnostic is supported; default acceptance remains 1000 steps")
		}
		steps, mode = 50, "diagnostic50"
	}
	longrunAcceptance := mode == "longrun1000"
	expectedSteps, expectedCompactions := int64(steps), int64(steps/longMemoryCompactEvery)
	profile := os.Getenv("ASTRAL_MEMORY_PROFILE") == "1"
	output, err := filepath.Abs(os.Getenv("ASTRAL_MEMORY_OUTPUT"))
	if err != nil || os.Getenv("ASTRAL_MEMORY_OUTPUT") == "" || strings.Contains(output, "Library/Application Support/astral") || strings.Contains(output, "/.local/share/astral") {
		t.Fatal("a new private non-production output directory is required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	setupStarted := time.Now()
	database := filepath.Join(output, "core.db")
	store, err := sqlite.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	metricsDB, err := sql.Open("sqlite", "file:"+filepath.ToSlash(database)+"?mode=ro&_pragma=cache_size(-2048)")
	if err != nil {
		t.Fatal(err)
	}
	metricsDB.SetMaxOpenConns(1)
	defer metricsDB.Close()
	transport := &longMemoryTransport{}
	streaming := false
	sdk, err := completions.New(models.Config{Spec: core.ModelSpec{ID: "memory-model", Revision: "1", ContextWindow: 4000000, MaxOutputTokens: 4096}, Model: "offline-memory", BaseURL: "https://offline.invalid/v1", HTTPClient: &http.Client{Transport: transport}, Streaming: &streaming, Images: "supported", ToolCalling: "supported"})
	if err != nil {
		t.Fatal(err)
	}
	model := &longMemoryModel{Model: sdk, probes: make(map[int]longMemoryWeak)}
	template := longMemoryPNG() // One necessary fixed generator template, not history.
	if err := longMemoryValidateImage(template); err != nil {
		t.Fatal(err)
	}
	var toolCalls atomic.Int64
	tool := core.ToolFunc{Spec: core.ToolDefinition{Name: "memory_blob", Description: "Produce one unique memory fixture observation.", Parameters: json.RawMessage(`{"type":"object","properties":{"step":{"type":"integer"}},"required":["step"],"additionalProperties":false}`)}, Run: func(_ context.Context, invocation core.Invocation, _ func(core.ToolProgress)) (core.ToolExecution, error) {
		var argument struct {
			Step int `json:"step"`
		}
		if err := json.Unmarshal(invocation.Call.Arguments, &argument); err != nil || argument.Step != int(transport.step.Load()) {
			return core.ToolExecution{}, errors.New("tool arguments did not match the current real step")
		}
		toolCalls.Add(1)
		line := fmt.Sprintf("Memory fixture step %06d: preserve verified observations and continue the current task.\n", argument.Step)
		text := strings.Repeat(line, (64<<10)/len(line)+1)[:64<<10]
		return core.ToolExecution{Result: &core.ToolResult{Content: []core.Content{{Type: "text", Text: text}, {Type: "image", MediaType: "image/png", Data: longMemoryImage(template, argument.Step)}}}}, nil
	}}
	config := core.Config{Model: model, Store: store, SystemPrompt: "Run the bounded active-context memory fixture.", Tools: []core.Tool{tool}, MaxContextBytes: 64 << 20, MaxTurns: 4,
		Compaction: core.CompactionPolicy{Disabled: true, KeepRecentTokens: 40000, OutputTokens: 1024, SystemPrompt: longMemorySummaryPrompt},
		EstimateTokensReadOnly: func(ctx context.Context, _ core.Model, request core.Request) (int, error) {
			return model.EstimateTokens(ctx, request)
		},
	}
	session, err := core.NewSession(ctx, config, core.SessionOptions{ID: "memory-long-run"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	progress, err := os.OpenFile(filepath.Join(output, "natural-memory.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer progress.Close()
	write := func(name string, value any) {
		t.Helper()
		raw, err := json.MarshalIndent(value, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(output, name), append(raw, '\n'), 0600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	sample := func(phase string, step int) longMemorySample {
		t.Helper()
		var memory goruntime.MemStats
		goruntime.ReadMemStats(&memory)
		value := longMemorySample{Phase: phase, Step: step, ElapsedNS: time.Since(started).Nanoseconds(), RSS: longMemoryRSS(), ProcessPeakRSS: longMemoryPeakRSS(), HeapAlloc: memory.HeapAlloc, HeapInuse: memory.HeapInuse, HeapSys: memory.HeapSys, GoSys: memory.Sys, TotalAlloc: memory.TotalAlloc, NumGC: memory.NumGC, ForcedGC: memory.NumForcedGC, Context: session.Stats(), NormalAPI: transport.normal.Load(), SummaryAPI: transport.summaries.Load()}
		if err := metricsDB.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(kind='tool_result'),0),coalesce(sum(kind='compaction'),0) FROM entries`).Scan(&value.ArchiveEntries, &value.ArchivedToolSteps, &value.Compactions); err != nil {
			t.Fatal(err)
		}
		if err := metricsDB.QueryRowContext(ctx, `SELECT count(*) FROM attempts`).Scan(&value.ArchiveAttempts); err != nil {
			t.Fatal(err)
		}
		if err := metricsDB.QueryRowContext(ctx, `SELECT coalesce(sum(byte_length),0) FROM payloads`).Scan(&value.PayloadBytes); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(database); err == nil {
			value.DatabaseBytes = info.Size()
		}
		if info, err := os.Stat(database + "-wal"); err == nil {
			value.WALBytes = info.Size()
		}
		value.ObservedBodies, value.OldBodies, value.OldTextAlive, value.OldImagesAlive = model.oldBodies(step - 2*longMemoryCompactEvery)
		return value
	}
	setup := sample("setup_complete", 0)
	if setup.RSS == 0 || setup.ProcessPeakRSS == 0 {
		t.Fatal("process RSS metrics are unavailable")
	}
	write("setup.json", map[string]any{"setup_ns": time.Since(setupStarted).Nanoseconds(), "scope": "Core + real SDK + SQLite + in-process offline sink; not full desktop/Harness services or the independent 900k latency scenario", "mode": mode, "longrun_acceptance": longrunAcceptance, "profiles_enabled": profile, "receiver_full_body_copy": false, "receiver_sink": "io.Copy(io.Discard), fixed counters only", "steps": steps, "compaction_every": longMemoryCompactEvery, "automatic_compaction_disabled": true, "real_manual_compaction": true, "text_bytes_per_step": 64 << 10, "image_generator_png_bytes": len(template), "active_entry_byte_limit": longMemoryActiveLimit, "rss_target_bytes_aspirational": 100000000, "gogc_environment": os.Getenv("GOGC"), "gomemlimit_environment": os.Getenv("GOMEMLIMIT"), "go_version": goruntime.Version(), "sample": setup})
	var samples []longMemorySample
	var peakHeap, peakHeapInuse atomic.Int64
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				var memory goruntime.MemStats
				goruntime.ReadMemStats(&memory)
				longMemoryMax(&peakHeap, int64(memory.HeapAlloc))
				longMemoryMax(&peakHeapInuse, int64(memory.HeapInuse))
			}
		}
	}()
	defer func() {
		select {
		case <-done:
		default:
			close(done)
		}
		<-stopped
	}()
	record := func(phase string, step int) longMemorySample {
		t.Helper()
		value := sample(phase, step)
		samples = append(samples, value)
		raw, _ := json.Marshal(value)
		if _, err := progress.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
		if err := progress.Sync(); err != nil {
			t.Fatal(err)
		}
		if value.Context.ContextBytes > longMemoryActiveLimit {
			t.Fatalf("active context escaped the fixed window at step %d: %+v", step, value.Context)
		}
		if value.ForcedGC != setup.ForcedGC {
			t.Fatal("forced GC contaminated the natural hot interval")
		}
		return value
	}
	writeProfile := func(name, kind string) {
		t.Helper()
		file, err := os.OpenFile(filepath.Join(output, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		err = pprof.Lookup(kind).WriteTo(file, 0)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(errors.Join(err, closeErr))
		}
	}
	var cpuProfile *os.File
	if profile {
		// Cumulative allocs include process setup; retain its own baseline so
		// pprof -base can isolate the hot interval without forcing a collection.
		writeProfile("allocs-setup.pprof", "allocs")
		cpuProfile, err = os.OpenFile(filepath.Join(output, "cpu.pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := pprof.StartCPUProfile(cpuProfile); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if cpuProfile != nil {
				pprof.StopCPUProfile()
				_ = cpuProfile.Close()
			}
		}()
	}
	hotStarted := time.Now()
	for step := 1; step <= steps; step++ {
		if err := longMemoryStep(ctx, session, transport, step); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		if step%longMemoryCompactEvery == 0 {
			record("before_compact", step)
			before := transport.summaries.Load()
			if _, err := session.Compact(ctx, "Retain only the latest open fixture task."); err != nil {
				t.Fatalf("real compact after step %d: %v", step, err)
			}
			if transport.summaries.Load() != before+1 {
				t.Fatal("explicit Compact did not make exactly one real SDK summary request")
			}
			value := record("after_compact", step)
			if value.Compactions != int64(step/longMemoryCompactEvery) {
				t.Fatalf("summary was not committed: %+v", value)
			}
			t.Logf("MEMORY_STAGE step=%d archive_payload=%d active_entries=%d active_bytes=%d rss=%d heap=%d gc=%d old_text_alive=%d old_image_alive=%d", step, value.PayloadBytes, value.Context.ContextEntries, value.Context.ContextBytes, value.RSS, value.HeapAlloc, value.NumGC, value.OldTextAlive, value.OldImagesAlive)
		}
	}
	hotWall := time.Since(hotStarted)
	if cpuProfile != nil {
		pprof.StopCPUProfile()
		if err := cpuProfile.Close(); err != nil {
			t.Fatal(err)
		}
		cpuProfile = nil
	}
	if profile {
		writeProfile("allocs-hot-end.pprof", "allocs")
	}
	close(done)
	<-stopped
	natural := sample("natural_hot_end", steps)
	if natural.ForcedGC != setup.ForcedGC || toolCalls.Load() != expectedSteps || natural.NormalAPI != 2*expectedSteps || natural.SummaryAPI != expectedCompactions || natural.Compactions != expectedCompactions || natural.ArchivedToolSteps != expectedSteps || natural.ArchiveAttempts != 2*expectedSteps+expectedCompactions || natural.ObservedBodies != steps {
		t.Fatalf("workload counts or natural GC contract changed: tools=%d sample=%+v", toolCalls.Load(), natural)
	}
	if longrunAcceptance && natural.PayloadBytes <= 20*int64(natural.Context.ContextBytes) {
		t.Fatalf("archive did not substantially outgrow the retained active context: %+v", natural)
	}
	maximumActive := 0
	for _, sample := range samples {
		maximumActive = max(maximumActive, sample.Context.ContextBytes)
	}
	heapTrend := longMemoryGrowth(samples, maximumActive, func(s longMemorySample) uint64 { return s.HeapAlloc })
	rssTrend := longMemoryGrowth(samples, maximumActive, func(s longMemorySample) uint64 { return s.RSS })
	write("natural-result.json", map[string]any{"hot_wall_ns": hotWall.Nanoseconds(), "mode": mode, "longrun_acceptance": longrunAcceptance, "profiles_enabled": profile, "allocs_profile_scope": "allocs-hot-end is process cumulative before diagnostic GC; subtract allocs-setup for hot allocation stacks", "forced_gc_during_hot": natural.ForcedGC - setup.ForcedGC, "natural_end": natural, "sampled_peak_heap_alloc": peakHeap.Load(), "sampled_peak_heap_inuse": peakHeapInuse.Load(), "peak_sampling_interval_ms": 250, "process_peak_rss_bytes": natural.ProcessPeakRSS, "sink_bytes": transport.bytes.Load(), "sink_consume_ns": transport.sinkNS.Load(), "peak_wire_bytes": transport.peakWire.Load(), "receiver_retains_bodies": false, "archive_to_final_active_ratio": float64(natural.PayloadBytes) / float64(max(1, natural.Context.ContextBytes)), "archive_to_max_active_ratio": float64(natural.PayloadBytes) / float64(max(1, maximumActive)), "maximum_active_entry_bytes": maximumActive, "sdk_estimate_calls": model.estimates.Load(), "peak_sdk_estimated_input_tokens": model.peakInput.Load(), "context_window_tokens": model.Spec().ContextWindow, "natural_heap_trend": heapTrend, "natural_rss_trend": rssTrend, "natural_samples": samples})
	if model.estimates.Load() < 2*expectedSteps || model.peakInput.Load() <= 0 {
		t.Fatal("real SDK budget estimation did not run for every ordinary request")
	}
	// This diagnostic is outside the completed product-like interval. It does
	// not free OS memory or substitute a GC-after Heap value for natural RSS.
	goruntime.GC()
	retention := sample("post_hot_forced_gc_retention_diagnostic", steps)
	write("retention-diagnostic.json", retention)
	if longrunAcceptance && (retention.OldBodies < steps-2*longMemoryCompactEvery || retention.OldTextAlive != 0 || retention.OldImagesAlive != 0) {
		t.Fatalf("retired Core payloads remain reachable after diagnostic GC: %+v", retention)
	}
	if longrunAcceptance && retention.HeapAlloc > setup.HeapAlloc+uint64(6*maximumActive+32<<20) {
		t.Fatalf("retained heap exceeds bounded working-data allowance: setup=%d after_gc=%d max_active=%d", setup.HeapAlloc, retention.HeapAlloc, maximumActive)
	}
	if longrunAcceptance && (heapTrend.BodySizedLinearRise || rssTrend.BodySizedLinearRise) {
		t.Fatalf("natural memory rose with archived bodies despite a bounded active window: heap=%+v rss=%+v", heapTrend, rssTrend)
	}
	// Cold reopen and request serialization intentionally follow all hot/RSS
	// and retention observations, so diagnostic copies cannot fake a RAM leak.
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	coldStarted := time.Now()
	reopened, err := core.OpenSession(ctx, config, "memory-long-run")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	view, err := reopened.ReadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view.Request)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Stats() != natural.Context {
		t.Fatalf("reopen reconstructed a different active window: hot=%+v cold=%+v", natural.Context, reopened.Stats())
	}
	write("cold-open.json", map[string]any{"cold_session_open_and_read_ns": time.Since(coldStarted).Nanoseconds(), "sqlite_pool_is_warm": true, "stats": reopened.Stats(), "active_request_json_bytes": len(raw), "active_messages": len(view.Request.Messages), "performed_after_natural_and_retention_observations": true})
	if info, ok := debug.ReadBuildInfo(); ok {
		write("build-info.json", info)
	}
}
