package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/testkit"
)

type computerFileFixture struct {
	p              *testkit.Platform
	source, target domain.ExecutionBinding
	client         *http.Client
}

func newComputerFileFixture(t *testing.T) computerFileFixture {
	t.Helper()
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	p.Hub.Gateway = New(p.Harness, channels.New(p.Store, p.Harness)).Handle
	source, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Daemons[0].Credentials.ComputerID, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Daemons[1].Credentials.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	f := computerFileFixture{p: p, source: source, target: target, client: testkit.ComputerAPI(t, p, source)}
	// The request context bounds these tests. A client-wide timeout would hide
	// whether a long or cancelled transfer obeys its own request lifetime.
	f.client.Timeout = 0
	var before domain.Session
	if err := p.Store.Get(p.Context, "sessions", p.Session.ID, &before); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		var after domain.Session
		if err := p.Store.Get(p.Context, "sessions", before.ID, &after); err != nil {
			t.Error(err)
		} else if after.ComputerID != before.ComputerID || after.CWD != before.CWD {
			t.Errorf("download changed Session selection: before=%s %q after=%s %q", before.ComputerID, before.CWD, after.ComputerID, after.CWD)
		}
		files, err := p.Store.List(p.Context, "files")
		if err != nil || len(files) != 0 {
			t.Errorf("raw download created file references: files=%d err=%v", len(files), err)
		}
		for _, home := range []string{source.AgentHome, target.AgentHome} {
			if _, err := os.Stat(filepath.Join(home, ".astral", "attachments")); !os.IsNotExist(err) {
				t.Errorf("raw download created a snapshot directory in %q: %v", home, err)
			}
		}
	})
	return f
}

func (f computerFileFixture) endpoint(computerID string) string {
	return f.source.APIURL + "/v1/computers/" + computerID + "/files/content"
}

func (f computerFileFixture) get(t *testing.T, computerID, path, rangeHeader string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(f.p.Context, http.MethodGet, f.endpoint(computerID)+"?"+url.Values{"path": {path}}.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", rangeHeader)
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readComputerFileResponse(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (f computerFileFixture) copyFile(t *testing.T, req CopyComputerFileRequest, wantStatus int) CopyComputerFileResponse {
	t.Helper()
	response, err := f.client.Post(f.source.APIURL+"/v1/computers/files/copy", "application/json", bytes.NewReader(testkit.JSON(req)))
	if err != nil {
		t.Fatal(err)
	}
	body := readComputerFileResponse(t, response)
	if response.StatusCode != wantStatus {
		t.Fatalf("copy returned HTTP %d, want %d: %s", response.StatusCode, wantStatus, body)
	}
	var result CopyComputerFileResponse
	if wantStatus == http.StatusOK {
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func TestComputerFileCopyBetweenAuthorizedComputers(t *testing.T) {
	f := newComputerFileFixture(t)
	// The calling command's CWD is outside AgentHome. Each relative path
	// belongs to the AgentHome of the Computer selected for that endpoint.
	for _, b := range []domain.ExecutionBinding{f.source, f.target} {
		if err := os.WriteFile(filepath.Join(b.AgentHome, "source.bin"), []byte(b.ComputerID), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, sourceID, targetID string
		from, to                 domain.ExecutionBinding
	}{
		{"push", "", f.target.ComputerID, f.source, f.target},
		{"pull", f.target.ComputerID, "", f.target, f.source},
		{"both omitted", "", "", f.source, f.source},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := CopyComputerFileRequest{SourceComputerID: test.sourceID, SourcePath: "source.bin", TargetComputerID: test.targetID, TargetPath: "copied.bin"}
			result := f.copyFile(t, req, http.StatusOK)
			expectedPath := filepath.Join(test.to.AgentHome, req.TargetPath)
			if result.ComputerID != test.to.ComputerID || result.Path != expectedPath || result.Size != int64(len(test.from.ComputerID)) || result.MediaType != "application/octet-stream" {
				t.Fatalf("wrong copy metadata: %+v", result)
			}
			got, err := os.ReadFile(expectedPath)
			if err != nil || string(got) != test.from.ComputerID {
				t.Fatalf("destination %q contains %q: %v", expectedPath, got, err)
			}
		})
	}
	for _, req := range []CopyComputerFileRequest{{SourcePath: "source.bin"}, {TargetPath: "copied.bin"}, {SourcePath: "x\x00y", TargetPath: "copied.bin"}} {
		f.copyFile(t, req, http.StatusBadRequest)
	}
	f.p.Agent.AllowedComputers = []string{f.source.ComputerID}
	if err := f.p.Harness.SaveAgent(f.p.Context, &f.p.Agent); err != nil {
		t.Fatal(err)
	}
	// A missing source path must not hide the target's authorization failure.
	f.copyFile(t, CopyComputerFileRequest{SourcePath: "missing.bin", TargetComputerID: f.target.ComputerID, TargetPath: "forbidden.bin"}, http.StatusForbidden)
	f.copyFile(t, CopyComputerFileRequest{SourceComputerID: f.target.ComputerID, SourcePath: "source.bin", TargetPath: "forbidden.bin"}, http.StatusForbidden)
}

func TestComputerFileCopyTemporaryComputerScope(t *testing.T) {
	f := newComputerFileFixture(t)
	p, ctx := f.p, f.p.Context
	url := p.Daemons[0].Credentials.ServerURL
	enrollment, err := p.Hub.Enroll(ctx, url, computers.EnrollmentOptions{Temporary: true, OwnerSessionID: p.Session.ID, OwnerAgentID: p.Agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := computers.NewDaemon(t.TempDir(), computers.Credentials{ServerURL: url, Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	daemon.Hello.InitialCWD = t.TempDir()
	daemon.Credentials, err = p.Hub.Register(ctx, enrollment.Enrollment.ID, computers.EnrollmentRequest{Token: enrollment.Token, Hello: daemon.Hello})
	if err != nil {
		t.Fatal(err)
	}
	daemon.Credentials.ServerURL = url
	p.Daemons = append(p.Daemons, daemon)
	go func() { _ = daemon.Connect(ctx) }()
	id := daemon.Credentials.ComputerID
	testkit.Await(t, ctx, func() bool { return p.Hub.Status(domain.Computer{Meta: domain.Meta{ID: id}}).Online })
	if err := os.WriteFile(filepath.Join(f.target.AgentHome, "empty.bin"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	copied := f.copyFile(t, CopyComputerFileRequest{SourceComputerID: f.target.ComputerID, SourcePath: "empty.bin", TargetComputerID: id, TargetPath: "received.bin"}, http.StatusOK)
	path := filepath.Join(daemon.Hello.InitialCWD, "received.bin")
	data, err := os.ReadFile(path)
	if err != nil || len(data) != 0 || copied.ComputerID != id || copied.Path != path || copied.Size != 0 {
		t.Fatalf("remote-to-remote empty copy: result=%+v bytes=%d err=%v", copied, len(data), err)
	}
	if err := os.WriteFile(filepath.Join(f.source.AgentHome, "local.bin"), []byte("temporary copy"), 0600); err != nil {
		t.Fatal(err)
	}
	push := CopyComputerFileRequest{SourcePath: "local.bin", TargetComputerID: id, TargetPath: "remote.bin"}
	pull := CopyComputerFileRequest{SourceComputerID: id, SourcePath: "remote.bin", TargetPath: "returned.bin"}
	f.copyFile(t, push, http.StatusOK)
	for _, child := range []bool{true, false} {
		session, err := p.Harness.CreateSession(ctx, p.Agent.ID, "Another Session")
		if err != nil {
			t.Fatal(err)
		}
		if child {
			session.SubagentParentID = p.Session.ID
			if err := p.Store.Save(ctx, "sessions", &session.Meta, &session); err != nil {
				t.Fatal(err)
			}
		}
		caller := f
		caller.source, err = p.Hub.Bind(ctx, p.Agent.ID, session.ID, f.source.ComputerID, "")
		if err != nil {
			t.Fatal(err)
		}
		caller.client = testkit.ComputerAPI(t, p, caller.source)
		if child {
			caller.copyFile(t, pull, http.StatusOK)
		} else {
			caller.copyFile(t, push, http.StatusForbidden)
			caller.copyFile(t, pull, http.StatusForbidden)
		}
	}
	if p.Hub.ComputerAPIEnabled(id) {
		t.Fatal("file transfer enabled Astral API on a temporary Computer")
	}
}

func TestComputerFileContentBinaryRangesAndEmptyFile(t *testing.T) {
	f := newComputerFileFixture(t)
	path := filepath.Join(t.TempDir(), "报告 空格.bin")
	payload := []byte{0, 255, 1, 128, 10, 13, 42, 7, 0, 254}
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, rangeHeader, contentRange string
		start, end, status              int
	}{
		{"complete", "", "", 0, 10, 200},
		{"closed range", "bytes=2-5", "bytes 2-5/10", 2, 6, 206},
		{"open range", "bytes=7-", "bytes 7-9/10", 7, 10, 206},
		{"suffix", "bytes=-3", "bytes 7-9/10", 7, 10, 206},
		{"clamped end", "bytes=8-100", "bytes 8-9/10", 8, 10, 206},
		{"whole suffix", "bytes=-100", "bytes 0-9/10", 0, 10, 206},
		{"past end", "bytes=10-", "bytes */10", 0, 0, 416},
		{"backwards", "bytes=4-2", "bytes */10", 0, 0, 416},
		{"empty suffix", "bytes=-0", "bytes */10", 0, 0, 416},
		{"multiple ranges", "bytes=0-1,4-5", "bytes */10", 0, 0, 416},
		{"other unit", "items=1-2", "bytes */10", 0, 0, 416},
		{"overflow", "bytes=0-9223372036854775808", "bytes */10", 0, 0, 416},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := f.get(t, f.target.ComputerID, path, test.rangeHeader)
			body := readComputerFileResponse(t, response)
			if response.StatusCode != test.status || response.Header.Get("Accept-Ranges") != "bytes" || response.Header.Get("Content-Range") != test.contentRange {
				t.Fatalf("status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
			}
			if test.status == http.StatusRequestedRangeNotSatisfiable {
				var problem httpcontract.Error
				if json.Unmarshal(body, &problem) != nil || problem.Code != "invalid_range" {
					t.Fatalf("invalid range lost its JSON error: %q", body)
				}
				return
			}
			if !bytes.Equal(body, payload[test.start:test.end]) || response.ContentLength != int64(test.end-test.start) {
				t.Fatalf("binary bytes or length changed: length=%d body=%v", response.ContentLength, body)
			}
			mediaType, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
			if err != nil || mediaType != "attachment" || parameters["filename"] != filepath.Base(path) || response.Header.Get("Content-Type") != "application/octet-stream" {
				t.Fatalf("download metadata changed: headers=%v err=%v", response.Header, err)
			}
		})
	}
	empty := filepath.Join(t.TempDir(), "empty.bin")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	response := f.get(t, f.target.ComputerID, empty, "")
	body := readComputerFileResponse(t, response)
	if response.StatusCode != 200 || response.ContentLength != 0 || response.Header.Get("Content-Length") != "0" || response.Header.Get("Accept-Ranges") != "bytes" || len(body) != 0 {
		t.Fatalf("empty file response: status=%d headers=%v body=%q", response.StatusCode, response.Header, body)
	}
	response = f.get(t, f.target.ComputerID, empty, "bytes=0-")
	_ = readComputerFileResponse(t, response)
	if response.StatusCode != 416 || response.Header.Get("Content-Range") != "bytes */0" {
		t.Fatalf("empty range response: status=%d headers=%v", response.StatusCode, response.Header)
	}
}

func TestComputerFileContentUsesTargetHomeAndOperatingSystemPaths(t *testing.T) {
	f := newComputerFileFixture(t)
	name := "目标 相对路径.txt"
	for _, entry := range []struct{ home, text string }{
		{f.source.CWD, "source working directory"},
		{f.source.AgentHome, "source AgentHome"},
		{f.target.AgentHome, "target AgentHome"},
	} {
		if err := os.WriteFile(filepath.Join(entry.home, name), []byte(entry.text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	response := f.get(t, f.target.ComputerID, name, "")
	if body := readComputerFileResponse(t, response); response.StatusCode != 200 || string(body) != "target AgentHome" {
		t.Fatalf("relative path used the wrong Computer/CWD: status=%d body=%q", response.StatusCode, body)
	}
	// A file outside AgentHome remains readable, including through an ordinary
	// OS symlink. Windows may lack permission to create symlinks on the runner.
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.txt")
		if err := os.WriteFile(outside, []byte("outside target AgentHome"), 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(f.target.AgentHome, "outside-link")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		response = f.get(t, f.target.ComputerID, link, "")
		if body := readComputerFileResponse(t, response); response.StatusCode != 200 || string(body) != "outside target AgentHome" {
			t.Fatalf("OS symlink acquired a directory restriction: status=%d body=%q", response.StatusCode, body)
		}
	}
	response = f.get(t, f.target.ComputerID, f.target.AgentHome, "")
	_ = readComputerFileResponse(t, response)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("directory returned HTTP %d", response.StatusCode)
	}
	response = f.get(t, f.source.ComputerID, filepath.Join(f.source.AgentHome, name), "")
	if body := readComputerFileResponse(t, response); response.StatusCode != 200 || string(body) != "source AgentHome" {
		t.Fatalf("requesting the source Computer failed: status=%d body=%q", response.StatusCode, body)
	}
}

func TestComputerFileContentRejectsInvalidQueriesAndAnonymousCaller(t *testing.T) {
	f := newComputerFileFixture(t)
	path := filepath.Join(t.TempDir(), "query.bin")
	if err := os.WriteFile(path, []byte("query file"), 0600); err != nil {
		t.Fatal(err)
	}
	valid := "path=" + url.QueryEscape(path)
	for _, query := range []string{"", "path=", valid + "&" + valid, valid + "&agent_id=another-bot", valid + "&computer_id=" + f.source.ComputerID, valid + "&snapshot=true", valid + "&unknown=value"} {
		t.Run(query, func(t *testing.T) {
			request, err := http.NewRequestWithContext(f.p.Context, http.MethodGet, f.endpoint(f.target.ComputerID)+"?"+query, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := f.client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body := readComputerFileResponse(t, response)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("invalid query accepted: status=%d body=%q", response.StatusCode, body)
			}
		})
	}
	request, err := http.NewRequestWithContext(f.p.Context, http.MethodGet, f.endpoint(f.target.ComputerID)+"?"+valid, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = readComputerFileResponse(t, response)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous file request returned HTTP %d", response.StatusCode)
	}
}

func TestComputerFileContentRechecksTargetPermissionsAndConnection(t *testing.T) {
	for _, state := range []string{"not allowed", "revoked", "offline"} {
		t.Run(state, func(t *testing.T) {
			f := newComputerFileFixture(t)
			// Both real Daemons share this process's filesystem. The source can
			// still read this absolute path after the target disconnects, so the
			// target request must fail rather than falling back to local access.
			path := filepath.Join(t.TempDir(), "shared-OS-file.bin")
			payload := []byte("read only through the requested Computer")
			if err := os.WriteFile(path, payload, 0600); err != nil {
				t.Fatal(err)
			}
			response := f.get(t, f.target.ComputerID, path, "")
			if body := readComputerFileResponse(t, response); response.StatusCode != 200 || !bytes.Equal(body, payload) {
				t.Fatalf("initial target read failed: status=%d body=%q", response.StatusCode, body)
			}
			want := http.StatusForbidden
			switch state {
			case "not allowed":
				f.p.Agent.AllowedComputers = []string{f.source.ComputerID}
				if err := f.p.Harness.SaveAgent(f.p.Context, &f.p.Agent); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if err := f.p.Hub.Revoke(f.p.Context, f.target.ComputerID); err != nil {
					t.Fatal(err)
				}
			case "offline":
				if err := f.p.Daemons[1].Close(f.p.Context); err != nil {
					t.Fatal(err)
				}
				testkit.Await(t, f.p.Context, func() bool {
					var record domain.Computer
					return f.p.Store.Get(f.p.Context, "computers", f.target.ComputerID, &record) == nil && !f.p.Hub.Status(record).Online
				})
				want = http.StatusServiceUnavailable
			}
			response = f.get(t, f.target.ComputerID, path, "")
			body := readComputerFileResponse(t, response)
			if response.StatusCode != want {
				t.Fatalf("target %s ignored: status=%d body=%q", state, response.StatusCode, body)
			}
			response = f.get(t, f.source.ComputerID, path, "")
			if body := readComputerFileResponse(t, response); response.StatusCode != 200 || !bytes.Equal(body, payload) {
				t.Fatalf("target failure affected the authorized source: status=%d body=%q", response.StatusCode, body)
			}
		})
	}
}

func TestComputerFileContentAndCopyStreamPastToolLimit(t *testing.T) {
	f := newComputerFileFixture(t)
	path := filepath.Join(t.TempDir(), "over-32-MiB.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 64<<10)
	for i := range chunk {
		chunk[i] = byte(i * 31)
	}
	want := sha256.New()
	const size int64 = (33 << 20) + 17
	for remaining := size; remaining > 0; {
		n := min(int64(len(chunk)), remaining)
		if _, err := io.MultiWriter(file, want).Write(chunk[:n]); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		remaining -= n
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	response := f.get(t, f.target.ComputerID, path, "")
	defer response.Body.Close()
	actual := sha256.New()
	n, err := io.Copy(actual, response.Body)
	if err != nil || response.StatusCode != 200 || n != size || response.ContentLength != size || !bytes.Equal(actual.Sum(nil), want.Sum(nil)) {
		t.Fatalf("large binary stream changed: status=%d bytes=%d length=%d err=%v", response.StatusCode, n, response.ContentLength, err)
	}
	if response.Header.Get("Content-Length") != strconv.FormatInt(size, 10) {
		t.Fatalf("large stream lost its exact length: %v", response.Header)
	}
	copied := f.copyFile(t, CopyComputerFileRequest{SourceComputerID: f.target.ComputerID, SourcePath: path, TargetPath: "large-copy.bin"}, http.StatusOK)
	targetFile, err := os.Open(filepath.Join(f.source.AgentHome, "large-copy.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer targetFile.Close()
	actual.Reset()
	n, err = io.Copy(actual, targetFile)
	if err != nil || n != size || copied.Size != size || !bytes.Equal(actual.Sum(nil), want.Sum(nil)) {
		t.Fatalf("large copy changed: read %d bytes, returned %+v, err %v", n, copied, err)
	}
}

func TestComputerFileContentRequestCancellationDoesNotPoisonNextRead(t *testing.T) {
	f := newComputerFileFixture(t)
	path := filepath.Join(t.TempDir(), "cancel-large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const size int64 = 64 << 20
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.p.Context)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.endpoint(f.target.ComputerID)+"?"+url.Values{"path": {path}}.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("cancellation source did not start: HTTP %d", response.StatusCode)
	}
	if _, err := io.ReadFull(response.Body, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, response.Body); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled file transfer appeared complete")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled file transfer remained blocked")
	}
	response = f.get(t, f.target.ComputerID, path, "bytes=0-3")
	if body := readComputerFileResponse(t, response); response.StatusCode != 206 || !bytes.Equal(body, make([]byte, 4)) {
		t.Fatalf("cancelled transfer poisoned the next read: status=%d body=%q", response.StatusCode, body)
	}
}

func TestComputerFileCopyCancelledBeforeCommit(t *testing.T) {
	f := newComputerFileFixture(t)
	path := filepath.Join(f.source.AgentHome, "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(128 << 20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "existing.bin")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.p.Context)
	defer cancel()
	copyRequest := CopyComputerFileRequest{SourcePath: path, TargetComputerID: f.target.ComputerID, TargetPath: target}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.source.APIURL+"/v1/computers/files/copy", bytes.NewReader(testkit.JSON(copyRequest)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		res, err := f.client.Do(req)
		if err == nil {
			res.Body.Close()
		}
		done <- err
	}()
	countFiles := func() int {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	testkit.Await(t, f.p.Context, func() bool { return countFiles() == 2 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted copy returned %v", err)
	}
	testkit.Await(t, f.p.Context, func() bool { return countFiles() == 1 })
	if data, err := os.ReadFile(target); err != nil || string(data) != "original" {
		t.Fatalf("interrupted copy changed the original file: %q, %v", data, err)
	}
}
