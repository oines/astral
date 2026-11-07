package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
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
	referencePath := "/api/v1/computers/" + b.ComputerID + "/files/reference" + fileQuery(p.Session.ID, path)
	response := request(t, p.Context, server, "POST", referencePath, nil, true)
	var result gateway.ReferenceResponse
	err = json.NewDecoder(response.Body).Decode(&result)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || result.File.Path != path || result.File.ComputerID != b.ComputerID || result.File.Version != "" || result.URI == "" {
		t.Fatalf("ordinary reference was not source-bound/non-snapshot: %+v, %d, %v", result, response.StatusCode, err)
	}
	if err = os.WriteFile(path, []byte("changed after reference"), 0600); err != nil {
		t.Fatal(err)
	}
	response = request(t, p.Context, server, "GET", "/api/v1/files/"+result.File.ID+"/content"+fileQuery(p.Session.ID, ""), nil, true)
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || string(raw) != "changed after reference" {
		t.Fatalf("live reference did not reuse file content route: %d %q, %v", response.StatusCode, raw, err)
	}
	metadataPath := "/api/v1/files/" + result.File.ID + fileQuery(p.Session.ID, "")
	response = request(t, p.Context, server, "GET", metadataPath, nil, false)
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("metadata accepted anonymous access")
	}
	response = request(t, p.Context, server, "GET", "/api/v1/files/"+result.File.ID, nil, true)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal("metadata accepted missing Session identity")
	}
	other := domain.Session{Meta: domain.Meta{ID: domain.ID("session")}, AgentID: domain.ID("agent")}
	if err = p.Store.Save(p.Context, "sessions", &other.Meta, &other); err != nil {
		t.Fatal(err)
	}
	response = request(t, p.Context, server, "GET", "/api/v1/files/"+result.File.ID+fileQuery(other.ID, ""), nil, true)
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
	var ref domain.FileRef
	err = json.NewDecoder(response.Body).Decode(&ref)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || ref.ID != result.File.ID || ref.Path != path || ref.AgentID != p.Agent.ID {
		t.Fatalf("offline historical metadata was lost: %+v, %d, %v", ref, response.StatusCode, err)
	}
	response = request(t, p.Context, server, "GET", "/api/v1/files/"+result.File.ID+"/content"+fileQuery(p.Session.ID, ""), nil, true)
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

func TestOwnerDirectoryAndFileReferenceRespectComputerPermissions(t *testing.T) {
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
	referencePath := "/api/v1/computers/" + otherID + "/files/reference"
	response = request(t, p.Context, server, "POST", referencePath+fileQuery(p.Session.ID, root), nil, true)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("directory accepted as regular file reference: %d", response.StatusCode)
	}
	response = request(t, p.Context, server, "POST", referencePath+fileQuery(p.Session.ID, ""), nil, true)
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal("file reference accepted missing path")
	}
	p.Agent.AllowedComputers = []string{p.Session.ComputerID}
	if err = p.Store.Save(p.Context, "agents", &p.Agent.Meta, &p.Agent); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []struct{ method, path string }{{"GET", directoryPath + fileQuery(p.Session.ID, root)}, {"POST", referencePath + fileQuery(p.Session.ID, path)}} {
		response = request(t, p.Context, server, operation.method, operation.path, nil, true)
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("%s ignored removed Computer permission: %d", operation.method, response.StatusCode)
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
	response = request(t, p.Context, server, "POST", "/api/v1/computers/"+b.ComputerID+"/files/reference"+fileQuery(p.Session.ID, path), nil, true)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("explicit file reference depended on removed task CWD: %d", response.StatusCode)
	}
	response = request(t, p.Context, server, "GET", directoryPath+fileQuery(p.Session.ID, ""), nil, true)
	response.Body.Close()
	if response.StatusCode != 404 {
		t.Fatalf("missing default directory silently changed to another path: %d", response.StatusCode)
	}
}
