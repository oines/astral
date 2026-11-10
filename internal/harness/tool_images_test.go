package harness

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/securetransport"
	"github.com/oines/astral/internal/transport"
)

type toolImageSourceFixture struct {
	binding domain.ExecutionBinding
	peer    *transport.Peer
	calls   atomic.Int32
	uploads atomic.Int32
}

// A real authenticated Computer tunnel can disappear after a native tool
// finishes. Returning that result must not make any extra Computer call.
func toolImageComputer(t *testing.T, f *fixture) *toolImageSourceFixture {
	t.Helper()
	home := t.TempDir()
	hello := f.daemons[0].Hello
	hello.BootID, hello.Hostname, hello.Home, hello.Root = domain.ID("boot"), "image-source", home, home
	hello.Capabilities = nil
	server := httptest.NewServer(http.HandlerFunc(f.hub.Connect))
	t.Cleanup(server.Close)
	enrollment, err := f.hub.Enroll(f.ctx, server.URL, computers.EnrollmentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := f.hub.Register(f.ctx, enrollment.Enrollment.ID, computers.EnrollmentRequest{Token: enrollment.Token, Hello: hello})
	if err != nil {
		t.Fatal(err)
	}
	f.agent.AllowedComputers = append(f.agent.AllowedComputers, credentials.ComputerID)
	if err = f.service.SaveAgent(f.ctx, &f.agent); err != nil {
		t.Fatal(err)
	}
	ws, _, err := websocket.Dial(f.ctx, strings.Replace(server.URL, "http://", "ws://", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	config, err := securetransport.ClientConfig(f.hub.ServerPin())
	if err != nil {
		t.Fatal(err)
	}
	conn := tls.Client(websocket.NetConn(f.ctx, ws, websocket.MessageBinary), config)
	t.Cleanup(func() { _ = conn.Close() })
	if err = conn.HandshakeContext(f.ctx); err != nil {
		t.Fatal(err)
	}
	raw := encode(map[string]any{"operation": "connect", "computer_id": credentials.ComputerID, "token": credentials.Token, "hello": hello})
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(raw)))
	if _, err = io.Copy(conn, io.MultiReader(bytes.NewReader(prefix[:]), bytes.NewReader(raw))); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadFull(conn, prefix[:]); err != nil {
		t.Fatal(err)
	}
	raw = make([]byte, binary.BigEndian.Uint32(prefix[:]))
	if _, err = io.ReadFull(conn, raw); err != nil {
		t.Fatal(err)
	}
	var authenticated struct{ Error string }
	if err = json.Unmarshal(raw, &authenticated); err != nil || authenticated.Error != "" {
		t.Fatalf("fixture authentication: %v %s", err, authenticated.Error)
	}
	source := &toolImageSourceFixture{}
	source.peer = transport.New(conn, func(ctx context.Context, request transport.Request, out *transport.Responder) error {
		source.calls.Add(1)
		var call computers.Call
		if err := json.Unmarshal(request.Params, &call); err != nil {
			return err
		}
		switch request.Method {
		case "environment":
			call.Binding.CWD, call.Binding.AgentHome = home, home
			return out.JSON(call.Binding)
		case "file.upload":
			source.uploads.Add(1)
			return errors.New("reading an image must not upload a Computer copy")
		default:
			return domain.Missing("fixture method", request.Method)
		}
	})
	t.Cleanup(func() { _ = source.peer.Close() })
	eventually(t, f.ctx, func() bool {
		var computer domain.Computer
		return f.store.Get(f.ctx, "computers", credentials.ComputerID, &computer) == nil && f.hub.Status(computer).Online
	})
	source.binding, err = f.hub.Bind(f.ctx, f.agent.ID, f.session.ID, credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func toolImageBytes(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aK7kAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func toolImageResult(t *testing.T) core.ToolResult {
	t.Helper()
	result, err := core.ProcessImage(t.Context(), toolImageBytes(t), core.ImageOptions{})
	if err != nil || result.IsError {
		t.Fatalf("fixture image: %+v %v", result, err)
	}
	result.Content = append(core.Text("image was read exactly once"), result.Content...)
	result.Details = encode(map[string]string{"source": "original-tool-output"})
	result.Outcome = core.OutcomeSucceeded
	return result
}

func startToolImageRun(t *testing.T, f *fixture, binding domain.ExecutionBinding, calls *atomic.Int32, changeResult ...func(*core.ToolResult)) *core.Run {
	t.Helper()
	r, err := f.service.open(f.ctx, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.core.Load().Close(f.ctx); err != nil {
		t.Fatal(err)
	}
	wrapped := hostTool{runtime: r, Tool: core.ToolFunc{
		Spec: core.ToolDefinition{Name: "read", Version: "image-fixture", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
		Run: func(context.Context, core.Invocation, func(core.ToolProgress)) (core.ToolExecution, error) {
			calls.Add(1)
			result := toolImageResult(t)
			for _, change := range changeResult {
				change(&result)
			}
			return core.ToolExecution{Result: &result}, nil
		},
	}}
	// Fix the captured source binding independently of the currently selected
	// Computer, while leaving execution/cancellation and history owned by Core.
	tool := core.ToolFunc{Spec: wrapped.Definition(), Run: func(ctx context.Context, inv core.Invocation, emit func(core.ToolProgress)) (core.ToolExecution, error) {
		inv.Binding = encode(binding)
		return wrapped.Execute(ctx, inv, emit)
	}}
	config := r.coreConfig(f.session)
	config.Tools = []core.Tool{tool}
	session, err := core.OpenSession(f.ctx, config, f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.core.Store(session)
	configuration := session.Configuration()
	configuration.Tools = []core.ToolDefinition{tool.Definition()}
	if err = session.Configure(f.ctx, configuration); err != nil {
		t.Fatal(err)
	}
	prompt, err := session.Prompt(f.ctx, core.Input{ID: "read-image", Message: core.User("read the image")})
	if err != nil {
		t.Fatal(err)
	}
	return prompt.Run
}

func TestNativeToolImageNeedsNoComputerRoundTrip(t *testing.T) {
	for _, offline := range []bool{false, true} {
		t.Run(map[bool]string{false: "online", true: "computer-offline-after-read"}[offline], func(t *testing.T) {
			f := newFixture(t, 1, func(_ context.Context, n int, request map[string]any) any {
				if n == 1 {
					return toolReply("image", "read", map[string]any{})
				}
				if !strings.Contains(string(encode(request)), "data:image/png;base64,") {
					t.Error("the native tool image did not reach the model")
				}
				return textReply("image received")
			})
			source := toolImageComputer(t, f)
			before := source.calls.Load()
			var calls atomic.Int32
			run := startToolImageRun(t, f, source.binding, &calls, func(*core.ToolResult) {
				if offline {
					_ = source.peer.Close()
				}
			})
			bounded, cancel := context.WithTimeout(f.ctx, 3*time.Second)
			defer cancel()
			if _, err := run.Wait(bounded); err != nil {
				t.Fatal(err)
			}
			result := recordedToolImageResult(t, f)
			if result.IsError || result.Code != "" || result.Outcome != core.OutcomeSucceeded || string(result.Details) != `{"source":"original-tool-output"}` || calls.Load() != 1 || f.count() != 2 {
				t.Fatalf("native result changed: result=%+v tools=%d requests=%d", result, calls.Load(), f.count())
			}
			images := 0
			for _, part := range result.Content {
				if part.Type == "image" {
					images++
					if part.URL != "" || len(part.Data) == 0 {
						t.Fatal("native image gained a Computer URI or lost its bytes")
					}
				}
			}
			if images != 1 || source.calls.Load() != before || source.uploads.Load() != 0 {
				t.Fatalf("native image caused extra RPCs: images=%d calls=%d before=%d uploads=%d", images, source.calls.Load(), before, source.uploads.Load())
			}
			assertToolImageFileCount(t, f, 0)
		})
	}
}

func TestReadImageToolHistoryNeedsNoComputerCopy(t *testing.T) {
	var path string
	f := newFixture(t, 1, func(_ context.Context, n int, request map[string]any) any {
		if n == 1 {
			return toolReply("image", "read", map[string]any{"path": path})
		}
		if !strings.Contains(string(encode(request)), "data:image/png;base64,") {
			t.Error("the tool image did not survive replay")
		}
		return textReply("image received")
	})
	binding, err := f.hub.Bind(f.ctx, f.agent.ID, f.session.ID, f.agent.DefaultComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(binding.CWD, "source.png")
	if err = os.WriteFile(path, toolImageBytes(t), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "read-native-image", Text: "read source.png"}); err != nil {
		t.Fatal(err)
	}
	view := f.idle(t, f.session.ID)
	assertToolImageFileCount(t, f, 0)
	result := recordedToolImageResult(t, f)
	for _, part := range result.Content {
		if part.Type == "image" && (len(part.Data) == 0 || part.URL != "") {
			t.Fatal("native read lost image data or created a Computer reference")
		}
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_ = f.daemons[0].Close(f.ctx)
	f.restart(t)
	if _, err = f.service.Submit(f.ctx, f.session.ID, InputRequest{ID: "replay-native-image", Text: "describe the earlier image"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, f.session.ID)
	child, err := f.service.Fork(f.ctx, f.session.ID, view.HeadID, "image fork", domain.ID("session"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Submit(f.ctx, child.ID, InputRequest{ID: "fork-native-image", Text: "describe the earlier image"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, child.ID)
	assertToolImageFileCount(t, f, 0)
	if f.count() != 4 {
		t.Fatalf("native image replay model requests: %d", f.count())
	}
}

func assertToolImageFileCount(t *testing.T, f *fixture, want int) {
	t.Helper()
	files, err := f.store.List(f.ctx, "files")
	if err != nil || len(files) != want {
		t.Fatalf("FileRef count: got %d, want %d, error=%v", len(files), want, err)
	}
}

func recordedToolImageResult(t *testing.T, f *fixture) *core.ToolResult {
	t.Helper()
	for _, entry := range subagentTestEntries(t, f, f.session.ID) {
		if entry.Kind == core.EntryToolResult && entry.Tool != nil && entry.Tool.Invocation.Call.ID == "image" && entry.Tool.Result != nil {
			return entry.Tool.Result
		}
	}
	t.Fatal("completed image tool result was not recorded")
	return nil
}
