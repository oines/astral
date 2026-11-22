package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func fileQuery(sessionID, path string) string {
	query := url.Values{"session_id": {sessionID}}
	if path != "" {
		query.Set("path", path)
	}
	return "?" + query.Encode()
}

func TestOwnerFileReferenceKeepsSourceAndReturnsMetadataOffline(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	b, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Session.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.CWD, "original file.txt")
	if err = os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	// Historical sessions can already contain a live reference with no version.
	ref := domain.FileRef{Meta: domain.Meta{ID: domain.ID("file")}, Path: path, AgentID: p.Agent.ID, ComputerID: b.ComputerID, DisplayName: filepath.Base(path)}
	if err = p.Store.Save(p.Context, "files", &ref.Meta, &ref); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("changed after reference"), 0600); err != nil {
		t.Fatal(err)
	}
	response := request(t, p.Context, server, "GET", "/api/v1/files/"+ref.ID+"/content"+fileQuery(p.Session.ID, ""), nil, true)
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(raw) != "changed after reference" {
		t.Fatalf("live reference did not reuse file content route: %d %q, %v", response.StatusCode, raw, err)
	}
	metadataPath := "/api/v1/files/" + ref.ID + fileQuery(p.Session.ID, "")
	response = request(t, p.Context, server, "GET", metadataPath, nil, false)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("metadata accepted anonymous access")
	}
	response = request(t, p.Context, server, "GET", "/api/v1/files/"+ref.ID, nil, true)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal("metadata accepted missing Session identity")
	}
	other := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: domain.ID("agent")}
	if err = p.Store.Save(p.Context, "sessions", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	response = request(t, p.Context, server, "GET", "/api/v1/files/"+ref.ID+fileQuery(other.ID, ""), nil, true)
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("metadata accepted another Bot's Session")
	}
	// A historical reference remains inspectable even when the computer is
	// offline and the Bot's permission has since been removed.
	if err = p.Daemons[0].Close(p.Context); err != nil {
		t.Fatal(err)
	}
	p.Agent.AllowedComputers = nil
	if err = p.Store.Save(p.Context, "agents", &p.Agent.Meta, &p.Agent); err != nil {
		t.Fatal(err)
	}
	response = request(t, p.Context, server, "GET", metadataPath, nil, true)
	var retrieved domain.FileRef
	err = json.NewDecoder(response.Body).Decode(&retrieved)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || retrieved.ID != ref.ID || retrieved.Path != path || retrieved.AgentID != p.Agent.ID {
		t.Fatalf("offline historical metadata was lost: %+v, %d, %v", retrieved, response.StatusCode, err)
	}
	response = request(t, p.Context, server, "GET", "/api/v1/files/"+ref.ID+"/content"+fileQuery(p.Session.ID, ""), nil, true)
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("metadata access bypassed permission checks for source bytes")
	}
	var stored domain.Session
	if err = p.Store.Get(p.Context, "sessions", p.Session.ID, &stored); err != nil || stored.ComputerID != p.Session.ComputerID || stored.CWD != p.Session.CWD || p.Requests.Load() != 0 {
		t.Fatalf("file browsing changed/ran the Session: %+v, %v", stored, err)
	}
	p.Session.Deletion = &domain.SessionDeletion{RolloutID: p.Session.ID}
	if err = p.Store.Save(p.Context, "sessions", &p.Session.Meta, &p.Session); err != nil {
		t.Fatal(err)
	}
	response = request(t, p.Context, server, "GET", metadataPath, nil, true)
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatal("metadata accepted a deleting Session")
	}
}

func TestOwnerDirectoryAndFileContentRespectComputerPermissions(t *testing.T) {
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	otherID := p.Daemons[1].Credentials.ComputerID
	b, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, otherID, "")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(b.CWD, "directory")
	if err = os.MkdirAll(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "file.txt")
	if err = os.WriteFile(path, []byte("ordinary"), 0600); err != nil {
		t.Fatal(err)
	}
	directoryPath := "/api/v1/computers/" + otherID + "/files/directory"
	response := request(t, p.Context, server, "GET", directoryPath+fileQuery(p.Session.ID, path)+"&limit=1", nil, true)
	var page computers.DirectoryPage
	err = json.NewDecoder(response.Body).Decode(&page)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || page.ComputerID != otherID || page.Path != root || page.Parent != b.CWD || len(page.Entries) != 1 || page.Entries[0].Name != "folder" || !page.Entries[0].IsDir || page.NextCursor == "" {
		t.Fatalf("Owner directory route: %+v, %d, %v", page, response.StatusCode, err)
	}
	response = request(t, p.Context, server, "GET", directoryPath+fileQuery(p.Session.ID, root)+"&limit=1&cursor="+url.QueryEscape(page.NextCursor), nil, true)
	page = computers.DirectoryPage{}
	err = json.NewDecoder(response.Body).Decode(&page)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || len(page.Entries) != 1 || page.Entries[0].Name != "file.txt" || page.Entries[0].IsDir || page.NextCursor != "" {
		t.Fatalf("Owner directory continuation: %+v, %d, %v", page, response.StatusCode, err)
	}
	for _, query := range []string{fileQuery(p.Session.ID, root) + "&limit=501", fileQuery(p.Session.ID, root) + "&limit=0", fileQuery(p.Session.ID, root) + "&limit=abc", fileQuery(p.Session.ID, "bad\x00path"), "?path=" + url.QueryEscape(root)} {
		response = request(t, p.Context, server, "GET", directoryPath+query, nil, true)
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("invalid directory query accepted: %q: %d", query, response.StatusCode)
		}
	}
	response = request(t, p.Context, server, "GET", directoryPath+fileQuery(p.Session.ID, root), nil, false)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("directory route accepted anonymous access")
	}
	response = request(t, p.Context, server, "GET", directoryPath+fileQuery(p.Session.ID, filepath.Join(root, "missing")), nil, true)
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("missing directory did not return 404: %d", response.StatusCode)
	}
	contentPath := "/api/v1/sessions/" + p.Session.ID + "/computers/" + otherID + "/files/content"
	for _, query := range []string{"", "?path=bad%00path", "?path=" + url.QueryEscape(root)} {
		response = request(t, p.Context, server, "GET", contentPath+query, nil, true)
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("invalid file path accepted: %q: %d", query, response.StatusCode)
		}
	}
	p.Agent.AllowedComputers = []string{p.Session.ComputerID}
	if err = p.Store.Save(p.Context, "agents", &p.Agent.Meta, &p.Agent); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directoryPath + fileQuery(p.Session.ID, root), contentPath + "?path=" + url.QueryEscape(path)} {
		response = request(t, p.Context, server, "GET", path, nil, true)
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("%s ignored removed Computer permission: %d", path, response.StatusCode)
		}
	}
	var stored domain.Session
	if err = p.Store.Get(p.Context, "sessions", p.Session.ID, &stored); err != nil || stored.ComputerID != p.Session.ComputerID || stored.CWD != p.Session.CWD || p.Requests.Load() != 0 {
		t.Fatalf("browsing another Computer changed/ran Session: %+v, %v", stored, err)
	}
}

func TestOwnerDirectoryDefaultDoesNotBindBrowsingToTaskCWD(t *testing.T) {
	p := testkit.New(t, 1, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	b, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, p.Session.ComputerID, "")
	if err != nil {
		t.Fatal(err)
	}
	p.Session.CWD = filepath.Join(b.CWD, "task-directory")
	if err = os.MkdirAll(p.Session.CWD, 0700); err != nil {
		t.Fatal(err)
	}
	if err = p.Store.Save(p.Context, "sessions", &p.Session.Meta, &p.Session); err != nil {
		t.Fatal(err)
	}
	directoryPath := "/api/v1/computers/" + b.ComputerID + "/files/directory"
	response := request(t, p.Context, server, "GET", directoryPath+fileQuery(p.Session.ID, ""), nil, true)
	var page computers.DirectoryPage
	err = json.NewDecoder(response.Body).Decode(&page)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || page.Path != p.Session.CWD {
		t.Fatalf("default directory ignored Session CWD: %+v, %d, %v", page, response.StatusCode, err)
	}
	if err = os.Remove(p.Session.CWD); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.AgentHome, "accessible.txt")
	if err = os.WriteFile(path, []byte("available outside removed CWD"), 0600); err != nil {
		t.Fatal(err)
	}
	response = request(t, p.Context, server, "GET", directoryPath+fileQuery(p.Session.ID, path), nil, true)
	err = json.NewDecoder(response.Body).Decode(&page)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || page.Path != b.AgentHome {
		t.Fatalf("explicit browsing depended on removed task CWD: %+v, %d, %v", page, response.StatusCode, err)
	}
	response = request(t, p.Context, server, "GET", "/api/v1/sessions/"+p.Session.ID+"/computers/"+b.ComputerID+"/files/content?path="+url.QueryEscape(path), nil, true)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("explicit file read depended on removed task CWD: %d", response.StatusCode)
	}
	response = request(t, p.Context, server, "GET", directoryPath+fileQuery(p.Session.ID, ""), nil, true)
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("missing default directory silently changed to another path: %d", response.StatusCode)
	}
}

func TestOwnerPathFilePreviewAndRange(t *testing.T) {
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	source := p.Daemons[1].Credentials.ComputerID
	b, err := p.Hub.Bind(p.Context, p.Agent.ID, p.Session.ID, source, "")
	if err != nil {
		t.Fatal(err)
	}
	const content = "file sample 012345"
	base := "/api/v1/sessions/" + p.Session.ID + "/computers/" + source + "/files/content?path="
	for _, file := range []struct{ name, disposition string }{
		{"report.pdf", "inline"}, {"日 图.png", "inline"}, {"document.html", "attachment"}, {"drawing.svg", "attachment"},
	} {
		if err := os.WriteFile(filepath.Join(b.AgentHome, file.name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		path := base + url.QueryEscape(file.name)
		for _, download := range []string{"", "&download=1"} {
			response := request(t, p.Context, server, "GET", path+download, nil, true)
			raw, err := io.ReadAll(response.Body)
			response.Body.Close()
			want := file.disposition
			if download != "" {
				want = "attachment"
			}
			disposition, params, parseErr := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
			if err != nil || response.StatusCode != 200 || string(raw) != content || parseErr != nil || disposition != want || params["filename"] != file.name ||
				response.Header.Get("X-Content-Type-Options") != "nosniff" || response.Header.Get("Content-Security-Policy") != "sandbox; default-src 'none'" {
				t.Fatalf("preview %s%s: %d %v, body %q, %v", file.name, download, response.StatusCode, response.Header, raw, err)
			}
		}
	}
	for _, test := range []struct {
		header, contentRange, bytes string
		status                      int
	}{
		{"bytes=0-3", "bytes 0-3/18", "file", 206}, {"bytes=-3", "bytes 15-17/18", "345", 206},
		{"bytes=1-0", "bytes */18", "", 416}, {"bytes=1-2,4-5", "bytes */18", "", 416},
	} {
		req, err := http.NewRequestWithContext(p.Context, "GET", server.URL+base+"report.pdf", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Range", test.header)
		req.Header.Set("Authorization", "Desktop desktop-test-key")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != test.status || response.Header.Get("Content-Range") != test.contentRange || (test.status == 206 && string(raw) != test.bytes) {
			t.Fatalf("range %q: status=%d, range=%q, body=%q, err=%v", test.header, response.StatusCode, response.Header.Get("Content-Range"), raw, err)
		}
	}
	response := request(t, p.Context, server, "GET", base+"report.pdf", nil, false)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("anonymous preview got %d", response.StatusCode)
	}
	if refs, err := p.Store.List(p.Context, "files"); err != nil || len(refs) != 0 {
		t.Fatalf("path reads wrote FileRef records: %d, %v", len(refs), err)
	}
	large, err := os.Create(filepath.Join(b.AgentHome, "large.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(64 << 20); err != nil {
		large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(p.Context)
	defer cancel()
	response = request(t, ctx, server, "GET", base+"large.bin", nil, true)
	defer response.Body.Close()
	if _, err := io.ReadFull(response.Body, make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := io.Copy(io.Discard, response.Body); err == nil {
		t.Fatal("canceled path download appeared complete")
	}
	response = request(t, p.Context, server, "GET", base+"report.pdf", nil, true)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("subsequent preview failed after cancellation: %d", response.StatusCode)
	}
}

func TestOwnerPathFilesRespectTemporaryComputerOwner(t *testing.T) {
	p := testkit.New(t, 2, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	id := p.Daemons[1].Credentials.ComputerID
	var computer domain.Computer
	if err := p.Store.Get(p.Context, "computers", id, &computer); err != nil {
		t.Fatal(err)
	}
	computer.Temporary, computer.OwnerSessionID, computer.OwnerAgentID = true, p.Session.ID, p.Agent.ID
	if err := p.Store.Save(p.Context, "computers", &computer.Meta, &computer); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "original.txt")
	if err := os.WriteFile(path, []byte("owned"), 0600); err != nil {
		t.Fatal(err)
	}
	check := func(session string, want int) {
		t.Helper()
		url := "/api/v1/sessions/" + session + "/computers/" + id + "/files/content?path=" + url.QueryEscape(path)
		response := request(t, p.Context, server, "GET", url, nil, true)
		response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("session %s: got %d want %d", session, response.StatusCode, want)
		}
	}
	check(p.Session.ID, 200)
	other := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: p.Agent.ID}
	if err := p.Store.Save(p.Context, "sessions", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	check(other.ID, 403)
	other.SubagentParentID = p.Session.ID
	if err := p.Store.Save(p.Context, "sessions", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	check(other.ID, 200)
	expired := time.Now().Add(-time.Hour)
	computer.ExpiresAt = &expired
	if err := p.Store.Save(p.Context, "computers", &computer.Meta, &computer); err != nil {
		t.Fatal(err)
	}
	check(p.Session.ID, 403)
	computer.ExpiresAt, computer.Revoked = nil, true
	if err := p.Store.Save(p.Context, "computers", &computer.Meta, &computer); err != nil {
		t.Fatal(err)
	}
	check(p.Session.ID, 403)
}
