package testkit

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	testshell "github.com/oines/astral/internal/testkit/shell"
)

// ComputerAPI starts a real command and reads its injected credential. The
// command stays alive until cleanup; no test-only issuance path bypasses the
// Server's normal execution authorization or credential lifecycle.
func ComputerAPI(t *testing.T, p *Platform, binding domain.ExecutionBinding) *http.Client {
	t.Helper()
	command := `printf '%s\n' "$ASTRAL_API_TOKEN"; `
	if runtime.GOOS == "windows" {
		command = "[Console]::WriteLine($env:ASTRAL_API_TOKEN); "
	}
	command += testshell.Sleep(time.Minute)
	process, err := (&computers.Backend{Hub: p.Hub}).StartProcess(p.Context,
		core.Invocation{SessionID: binding.SessionID, ExecutionID: domain.ID("execution"), Binding: computers.BindingJSON(binding)},
		standardtools.ProcessRequest{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _, _ = process.Wait(); close(done) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := process.Terminate(ctx); err != nil {
			t.Error(err)
		}
		_ = process.Output().Close()
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("command API fixture did not exit after termination")
		}
	})
	credential := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(process.Output())
		line, _ := reader.ReadString('\n')
		credential <- strings.TrimSpace(line)
		_, _ = io.Copy(io.Discard, reader)
	}()
	var token string
	select {
	case token = <-credential:
	case <-p.Context.Done():
		t.Fatal(p.Context.Err())
	}
	if token == "" {
		t.Fatal("command API fixture received no injected credential")
	}
	return &http.Client{Timeout: 30 * time.Second, Transport: commandAPITransport{token: token}}
}

type commandAPITransport struct{ token string }

func (t commandAPITransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	if request.Header.Get("Authorization") == "" {
		request.Header.Set("Authorization", "Bearer "+t.token)
	}
	return http.DefaultTransport.RoundTrip(request)
}
