package gateway

import (
	"net/http"
	"strconv"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/recall"
)

func (s *Service) recallRoutes() {
	listParameters := []httpcontract.Parameter{
		{Name: "cursor", Description: "Resume a fixed catalog snapshot; cursors expire after 24 hours. Pass cursor and optional limit, or repeat the same filters."},
		{Name: "limit", Type: "integer", Description: "Sessions per page, default 50; 1–200."},
		{Name: "source", Description: "Filter Session source, such as web or im."},
		{Name: "conversation_id", Description: "Filter by the originating IM conversation."},
		{Name: "archived", Type: "boolean", Description: "Omit to include both archived and active Sessions."},
		{Name: "title", Description: "Case-insensitive literal substring of the Session title."},
	}
	httpcontract.Register(s.Contract, s.mux, httpcontract.Operation{Method: "GET", Path: "/v1/sessions", ID: "agentSessions", Tag: "Agent Gateway", Query: listParameters,
		Description: "List this Agent's Sessions across Computers, newest activity first. Returns lightweight metadata, includes archived Sessions by default, and never opens execution runtimes. Follow next_cursor to enumerate the fixed snapshot."},
		func(_ http.ResponseWriter, r *http.Request, _ httpcontract.Empty) (recall.SessionPage, error) {
			if err := validateRecallQuery(r, listParameters); err != nil {
				return recall.SessionPage{}, err
			}
			values := r.URL.Query()
			limit, err := recallInteger(r, "limit")
			if err != nil {
				return recall.SessionPage{}, err
			}
			var archived *bool
			if values.Has("archived") {
				if values.Get("archived") != "true" && values.Get("archived") != "false" {
					return recall.SessionPage{}, domain.Invalid("archived must be true or false")
				}
				value := values.Get("archived") == "true"
				archived = &value
			}
			return s.Harness.Recall.List(r.Context(), binding(r).AgentID, recall.ListRequest{Cursor: values.Get("cursor"), Limit: integerValue(limit),
				Source: values.Get("source"), ConversationID: values.Get("conversation_id"), Archived: archived, Title: values.Get("title")})
		})
	httpcontract.Register(s.Contract, s.mux, httpcontract.Operation{Method: "POST", Path: "/v1/sessions/search", ID: "agentSearchSessions", Tag: "Agent Gateway",
		Description: "Search committed text across this Agent's Sessions. Returns flat passages around real keyword matches; multiple passages from one Session are allowed. Filters precede pagination. Defaults: all whitespace-separated terms, case-insensitive, newest first, 600 characters per passage, 50 passages per page. Short Chinese words, code identifiers and punctuation match literally. No hidden per-Session quota, semantic summarization or global output budget. Independent Fork copies remain distinct. Shared-rollout ancestors are deduplicated, each Session exposes only its own history path, and pre-compaction originals remain searchable. The first request catches the index up to captured heads or fails; an empty page is not an unindexed-history result. Resume with cursor and optional limit. Use each hit's read_url to expand it."},
		func(_ http.ResponseWriter, r *http.Request, request recall.SearchRequest) (recall.SearchPage, error) {
			if len(r.URL.Query()) != 0 {
				return recall.SearchPage{}, domain.Invalid("search parameters belong in the JSON body")
			}
			return s.Harness.Recall.Search(r.Context(), binding(r).AgentID, request)
		})
	readParameters := []httpcontract.Parameter{
		{Name: "around", Description: "Stable source ref returned by search/history. Expands its precise text location on the captured branch."},
		{Name: "cursor", Description: "older_cursor or newer_cursor returned by history. Mutually exclusive with around."},
		{Name: "limit", Type: "integer", Description: "Text parts per history page, default 20; 1–200. Omit with around."},
		{Name: "before", Type: "integer", Description: "Text parts before around, default 2; 0–100."},
		{Name: "after", Type: "integer", Description: "Text parts after around, default 2; 0–100."},
		{Name: "offset", Type: "integer", Description: "Exact zero-based Unicode character offset within around's text part. Omit to center on the reference. Use offset=end, before=0, after=0 to continue a long part."},
		{Name: "length", Type: "integer", Description: "Characters per text part, default 4000; 1–16384. start/end/total_chars describe each returned slice; this is not a total response budget."},
	}
	httpcontract.Register(s.Contract, s.mux, httpcontract.Operation{Method: "GET", Path: "/v1/sessions/{id}/history", ID: "agentHistory", Tag: "Agent Gateway", Query: readParameters,
		Description: "Read normalized history text with roles, tool names, provenance and stable character offsets. With no location, returns recent text parts. With around, returns the hit and its requested neighbors. Items are chronological; follow older_cursor or newer_cursor to navigate. Page cursors keep the original head while the Session grows. References remain usable after restart/index rebuild. This Agent API returns text parts; the Owner history API retains raw Core entries. It reads persistent data without loading a model or running a Session."},
		func(_ http.ResponseWriter, r *http.Request, _ httpcontract.Empty) (recall.HistoryPage, error) {
			if err := validateRecallQuery(r, readParameters); err != nil {
				return recall.HistoryPage{}, err
			}
			values := r.URL.Query()
			request := recall.ReadRequest{Cursor: values.Get("cursor"), Around: values.Get("around")}
			for _, parameter := range []struct {
				name   string
				target **int
			}{{"before", &request.Before}, {"after", &request.After}, {"offset", &request.Offset}} {
				value, err := recallInteger(r, parameter.name)
				if err != nil {
					return recall.HistoryPage{}, err
				}
				*parameter.target = value
			}
			for _, parameter := range []struct {
				name   string
				target *int
			}{{"limit", &request.Limit}, {"length", &request.Length}} {
				value, err := recallInteger(r, parameter.name)
				if err != nil {
					return recall.HistoryPage{}, err
				}
				*parameter.target = integerValue(value)
			}
			return s.Harness.Recall.Read(r.Context(), binding(r).AgentID, r.PathValue("id"), request)
		})
}

func validateRecallQuery(r *http.Request, parameters []httpcontract.Parameter) error {
	allowed := make(map[string]bool, len(parameters))
	for _, parameter := range parameters {
		allowed[parameter.Name] = true
	}
	for name, values := range r.URL.Query() {
		if !allowed[name] || len(values) != 1 {
			return domain.Invalid("unknown or repeated history parameter: " + name)
		}
	}
	return nil
}

func recallInteger(r *http.Request, name string) (*int, error) {
	if !r.URL.Query().Has(name) {
		return nil, nil
	}
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || value < 0 || (name == "limit" || name == "length") && value == 0 {
		return nil, domain.Invalid("invalid integer history parameter: " + name)
	}
	return &value, nil
}

func integerValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}
