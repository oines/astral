package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestPermanentSessionDeleteOwnerRevisionAndIdempotence(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	_, server := apiFixture(t, p)
	path := "/api/v1/sessions/" + p.Session.ID
	check := func(method, url, body string, auth bool, status int) {
		t.Helper()
		response := request(t, p.Context, server, method, url, strings.NewReader(body), auth)
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("%s %s: got %d, want %d", method, url, response.StatusCode, status)
		}
	}
	check(http.MethodDelete, path+"?revision=1", "", false, 401)
	var record domain.Session
	if err := p.Store.Get(p.Context, "sessions", p.Session.ID, &record); err != nil {
		t.Fatal(err)
	}
	check(http.MethodDelete, path+"?revision="+strconv.FormatInt(record.Revision, 10), "", true, 409)
	check(http.MethodPatch, path, `{"archived":true}`, true, 200)
	if err := p.Store.Get(p.Context, "sessions", p.Session.ID, &record); err != nil {
		t.Fatal(err)
	}
	check(http.MethodDelete, path+"?revision="+strconv.FormatInt(record.Revision-1, 10), "", true, 409)
	confirmed := path + "?revision=" + strconv.FormatInt(record.Revision, 10)
	check(http.MethodDelete, confirmed, "", true, 200)
	check(http.MethodDelete, confirmed, "", true, 200)
	for _, suffix := range []string{"", "/snapshot", "/history", "/attempts", "/rollout"} {
		check(http.MethodGet, path+suffix, "", true, 404)
	}
}
