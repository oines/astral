//go:build (darwin || linux) && astral_prepared_execution

package harness

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/oines/astral-core/models/provider"
	"github.com/oines/astral-core/models/provider/openai/completions"
	"github.com/oines/astral-core/models/provider/openai/responses"
	"golang.org/x/sys/unix"
)

//go:embed success_scaled_host_unix_test.go success_scaled_host_process_unix_test.go success_lifecycle_replay_unix_test.go success_execution_forward_unix_test.go
var scaledHostProcessSources embed.FS

func scaledHostProcessSourceSHA() string {
	hash := sha256.New()
	for _, name := range []string{"success_scaled_host_unix_test.go", "success_scaled_host_process_unix_test.go", "success_lifecycle_replay_unix_test.go", "success_execution_forward_unix_test.go"} {
		raw, _ := scaledHostProcessSources.ReadFile(name)
		_, _ = hash.Write([]byte(name + "\x00"))
		_, _ = hash.Write(raw)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func scaledHostPeakRSSBytes() (int64, error) {
	var usage unix.Rusage
	if err := unix.Getrusage(unix.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	rss := int64(usage.Maxrss)
	if goruntime.GOOS == "linux" {
		rss *= 1024
	}
	return rss, nil
}

type scaledHostProcess struct {
	receiver *scaledHostReceiver
	endpoint *url.URL
	client   *http.Transport
	pid      int
	path     string
}

func startScaledHostProcess(t *testing.T, output string, receiver *scaledHostReceiver) *scaledHostProcess {
	return startScaledModelProcess(t, output, receiver, completions.Protocol, false)
}

func startSuccessLifecycleProcess(t *testing.T, output string, sdk provider.Model, transport *successLifecycleTransport) successLifecycleProcessReceiver {
	endpoint, err := url.Parse(sdk.SnapshotConfig().BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	collector := &scaledHostReceiver{sink: transport, endpoint: *endpoint}
	process := startScaledModelProcess(t, output, collector, transport.protocol, true)
	return successLifecycleProcessReceiver{Transport: process, Metadata: map[string]any{"receiver_pid": process.pid, "parent_pid": os.Getpid(), "test_sources_sha256": scaledHostProcessSourceSHA(), "protocol": transport.protocol, "usage": "omitted", "difference": "measurement isolation only, not a product speedup; parent http.Transport Proxy:nil sends to owned loopback child", "body_read_ns_kind": "upper bound at receipt of full-body acknowledgement headers"}, Snapshot: func() ([]successLifecycleWire, int64, map[string]any) {
		collector.mu.Lock()
		wires := append([]scaledHostWire(nil), collector.wires...)
		collector.mu.Unlock()
		var common []successLifecycleWire
		var roundtrip int64
		for _, wire := range wires {
			common = append(common, wire.successLifecycleWire)
			roundtrip += wire.ReceiverWallNS
		}
		rss, err := scaledHostPeakRSSBytes()
		if err != nil {
			t.Fatal(err)
		}
		return common, roundtrip, map[string]any{"wire_requests": wires, "parent_peak_rss_bytes": rss, "parent_peak_rss_scope": "RUSAGE_SELF high-water before post-wire verification; excludes child and includes setup"}
	}}
}

func startScaledModelProcess(t *testing.T, output string, receiver *scaledHostReceiver, protocol string, lifecycle bool) *scaledHostProcess {
	t.Helper()
	suffix := "/chat/completions"
	if protocol == responses.Protocol {
		suffix = "/responses"
	} else if protocol != completions.Protocol {
		t.Fatal("unsupported offline child protocol")
	}
	ready, logPath := filepath.Join(output, "receiver-ready.json"), filepath.Join(output, "receiver.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestScaledHostModelProcess$", "-test.timeout=2m")
	command.Env = append(os.Environ(), "ASTRAL_SCALED_HOST_CHILD=1", "ASTRAL_SCALED_HOST_CHILD_READY="+ready, "ASTRAL_SCALED_HOST_CHILD_TOOL="+receiver.toolPath)
	command.Env = append(command.Env, "ASTRAL_SCALED_HOST_CHILD_PROTOCOL="+protocol, "ASTRAL_SCALED_HOST_CHILD_LIFECYCLE="+strconv.FormatBool(lifecycle))
	command.Stdout, command.Stderr = log, log
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	transport := &http.Transport{Proxy: nil, DisableCompression: true}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		_ = stdin.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("receiver process failed; inspect %s: %v", logPath, err)
			}
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Error("receiver did not exit after stdin EOF")
		}
		_ = log.Close()
	})
	var record struct {
		URL       string `json:"url"`
		PID       int    `json:"pid"`
		SourceSHA string `json:"source_sha256"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(ready)
		if err == nil && json.Unmarshal(raw, &record) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("receiver readiness timeout; inspect %s", logPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	endpoint, err := url.Parse(record.URL)
	if err != nil || endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" || endpoint.Port() == "" || endpoint.Path != "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || record.PID != command.Process.Pid || record.SourceSHA != scaledHostProcessSourceSHA() {
		t.Fatal("invalid owned loopback receiver identity")
	}
	return &scaledHostProcess{receiver: receiver, endpoint: endpoint, client: transport, pid: record.PID, path: strings.TrimRight(receiver.endpoint.Path, "/") + suffix}
}

func (p *scaledHostProcess) RoundTrip(request *http.Request) (*http.Response, error) {
	dispatch := time.Since(p.receiver.sink.started).Nanoseconds()
	if request.Method != http.MethodPost || request.URL.Scheme != p.receiver.endpoint.Scheme || request.URL.Host != p.receiver.endpoint.Host || request.URL.Path != p.path || request.URL.RawQuery != "" || request.URL.Fragment != "" {
		return nil, errors.New("process receiver rejected unexpected endpoint")
	}
	forward := *request
	target := *request.URL
	target.Scheme, target.Host = p.endpoint.Scheme, p.endpoint.Host
	forward.URL, forward.Host = &target, p.endpoint.Host
	response, err := p.client.RoundTrip(&forward)
	returned := time.Since(p.receiver.sink.started).Nanoseconds()
	if err != nil {
		return nil, err
	}
	response.Request = request
	size, e1 := strconv.Atoi(response.Header.Get("X-Astral-Body-Bytes"))
	processing, e2 := strconv.ParseInt(response.Header.Get("X-Astral-Processing-Ns"), 10, 64)
	rss, e3 := strconv.ParseInt(response.Header.Get("X-Astral-Peak-Rss-Bytes"), 10, 64)
	pid, e4 := strconv.Atoi(response.Header.Get("X-Astral-Receiver-Pid"))
	digest := response.Header.Get("X-Astral-Body-Sha256")
	digestBytes, e5 := hex.DecodeString(digest)
	streaming, e6 := strconv.ParseBool(response.Header.Get("X-Astral-Streaming"))
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil || len(digestBytes) != 32 || size < 1 || processing < 0 || rss < 1 || pid != p.pid || response.Header.Get("X-Astral-Json-Valid") != "true" {
		_ = response.Body.Close()
		return nil, errors.New("invalid isolated receiver acknowledgement")
	}
	wire := scaledHostWire{successLifecycleWire: successLifecycleWire{Bytes: size, SHA256: digest, ValidJSON: true, Streaming: streaming, DispatchNS: dispatch, BodyReadNS: returned, SinkNS: processing}, ReceiverReturnNS: returned, ReceiverWallNS: returned - dispatch, ServerProcessingNS: processing, ServerPeakRSSBytes: rss, ReceiverPID: pid}
	p.receiver.mu.Lock()
	p.receiver.wires = append(p.receiver.wires, wire)
	p.receiver.mu.Unlock()
	return response, nil
}

// The child owns all full-body validation allocations and their GC. Normal
// test runs skip it. EOF closes the listener and joins the owned test process.
func TestScaledHostModelProcess(t *testing.T) {
	if os.Getenv("ASTRAL_SCALED_HOST_CHILD") != "1" || os.Getenv("ASTRAL_SCALED_HOST_CHILD_READY") == "" {
		t.Skip("owned offline model child only")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _ := url.Parse("http://" + listener.Addr().String())
	protocol := os.Getenv("ASTRAL_SCALED_HOST_CHILD_PROTOCOL")
	if protocol == "" {
		protocol = completions.Protocol
	}
	if protocol != completions.Protocol && protocol != responses.Protocol {
		t.Fatal("invalid child protocol")
	}
	lifecycle := os.Getenv("ASTRAL_SCALED_HOST_CHILD_LIFECYCLE") == "true"
	receiver := &scaledHostReceiver{sink: &successLifecycleTransport{protocol: protocol, started: time.Now(), omitUsage: lifecycle}, endpoint: *endpoint, toolPath: os.Getenv("ASTRAL_SCALED_HOST_CHILD_TOOL")}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		copy := *request
		target := *request.URL
		target.Scheme, target.Host = endpoint.Scheme, endpoint.Host
		copy.URL = &target
		started := time.Now()
		var response *http.Response
		var wire scaledHostWire
		var err error
		if lifecycle {
			response, err = receiver.sink.RoundTrip(&copy)
			if err == nil {
				receiver.sink.mu.Lock()
				wire.successLifecycleWire = receiver.sink.wires[len(receiver.sink.wires)-1]
				receiver.sink.mu.Unlock()
				wire.ReceiverWallNS = time.Since(started).Nanoseconds()
			}
		} else {
			response, err = receiver.RoundTrip(&copy)
			if err == nil {
				receiver.mu.Lock()
				wire = receiver.wires[len(receiver.wires)-1]
				receiver.mu.Unlock()
			}
		}
		if err != nil {
			http.Error(w, "offline body rejected", 400)
			return
		}
		defer response.Body.Close()
		rss, err := scaledHostPeakRSSBytes()
		if err != nil {
			http.Error(w, "RSS unavailable", 500)
			return
		}
		for key, value := range map[string]string{"Content-Type": response.Header.Get("Content-Type"), "X-Astral-Streaming": strconv.FormatBool(wire.Streaming), "X-Astral-Body-Bytes": strconv.Itoa(wire.Bytes), "X-Astral-Body-Sha256": wire.SHA256, "X-Astral-Json-Valid": strconv.FormatBool(wire.ValidJSON), "X-Astral-Processing-Ns": strconv.FormatInt(wire.ReceiverWallNS, 10), "X-Astral-Peak-Rss-Bytes": strconv.FormatInt(rss, 10), "X-Astral-Receiver-Pid": strconv.Itoa(os.Getpid())} {
			w.Header().Set(key, value)
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	})}
	stopped := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		_ = server.Shutdown(context.Background())
		close(stopped)
	}()
	ready := os.Getenv("ASTRAL_SCALED_HOST_CHILD_READY")
	raw, _ := json.Marshal(map[string]any{"url": endpoint.String(), "pid": os.Getpid(), "source_sha256": scaledHostProcessSourceSHA()})
	if err = os.WriteFile(ready+".tmp", raw, 0600); err == nil {
		err = os.Rename(ready+".tmp", ready)
	}
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	<-stopped
}
