package httpapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/persistence"
)

const BotAPIPrefix = "/api/v1/bot"

func IsBotAPIPath(path string) bool {
	return path == BotAPIPrefix || strings.HasPrefix(path, BotAPIPrefix+"/")
}

func (a *API) BotHandler() http.Handler { return a.botHandler }

func botOperationID(id string) string { return "bot" + strings.ToUpper(id[:1]) + id[1:] }

func botRoute[Request, Response any](a *API, method, path, id, tag string, handler func(*http.Request, Request) (Response, error), query ...httpcontract.Parameter) {
	httpcontract.Register(a.Contract, a.botMux, httpcontract.Operation{
		Method: method, Path: BotAPIPrefix + path, ID: botOperationID(id), Tag: tag, BotAuth: true, Query: query,
	}, func(_ http.ResponseWriter, r *http.Request, request Request) (Response, error) {
		return handler(r, request)
	})
}

func sharedBotRoute[Request, Response any](a *API, method, path, id, tag string, handler func(*http.Request, Request) (Response, error), query ...httpcontract.Parameter) {
	route(a, method, path, id, tag, handler, query...)
	botRoute(a, method, path, id, tag, handler, query...)
}

func sessionRoute[Request, Response any](a *API, method, path, id, tag string, handler func(*http.Request, Request) (Response, error), query ...httpcontract.Parameter) {
	route(a, method, path, id, tag, handler, query...)
	botSessionRoute(a, method, path, id, tag, handler, query...)
}

func botSessionRoute[Request, Response any](a *API, method, path, id, tag string, handler func(*http.Request, Request) (Response, error), query ...httpcontract.Parameter) {
	botRoute(a, method, path, id, tag, func(r *http.Request, input Request) (Response, error) {
		scoped, _, err := a.authorizeBotSession(r, r.PathValue("id"), method != http.MethodGet)
		if err != nil {
			var empty Response
			return empty, err
		}
		return handler(scoped, input)
	}, query...)
}

// authorizeBotSession follows only the durable delegation relationship. A
// forked session is its own root, and an identifier in history is not a grant.
func (a *API) authorizeBotSession(r *http.Request, id string, write bool) (*http.Request, []persistence.ChangeKey, error) {
	if id == "" {
		return r, nil, domain.Invalid("Session is required.")
	}
	ctx, owner := r.Context(), botID(r)
	store := a.Config.Harness.Store
	var record domain.Session
	if err := store.Get(ctx, "sessions", id, &record); err != nil {
		return r, nil, err
	}
	if record.AgentID != owner || record.Deletion != nil {
		return r, nil, domain.Missing("Session", id)
	}
	keys := []persistence.ChangeKey{{Kind: "sessions", ID: id}}
	if record.SubagentParentID != "" {
		seen := map[string]bool{id: true}
		for record.SubagentParentID != "" {
			parent := record.SubagentParentID
			if seen[parent] {
				return r, nil, domain.Missing("Session", id)
			}
			seen[parent] = true
			var ancestor domain.Session
			if err := store.Get(ctx, "sessions", parent, &ancestor); err != nil {
				return r, nil, err
			}
			record = ancestor
			if record.AgentID != owner || record.Deletion != nil {
				return r, nil, domain.Missing("Session", id)
			}
			keys = append(keys, persistence.ChangeKey{Kind: "sessions", ID: parent})
		}
	}
	if record.Private {
		return r, nil, domain.Missing("Session", id)
	}
	if record.ID != id {
		if write {
			return r, nil, domain.Forbidden("Subagent Sessions are managed by their direct parent")
		}
		// Existing access checks recognize this root as the child's ancestor.
		r = r.WithContext(persistence.WithSessionScope(ctx, owner, record.ID))
	}
	return r, keys, nil
}

func (a *API) botRawRoute(op httpcontract.Operation, handler http.HandlerFunc, sessionID func(*http.Request) string) httpcontract.Operation {
	op.Path = BotAPIPrefix + strings.TrimPrefix(op.Path, "/api/v1")
	op.ID = botOperationID(op.ID)
	op.Auth, op.BotAuth = false, true
	a.botMux.HandleFunc(op.Method+" "+op.Path, func(w http.ResponseWriter, r *http.Request) {
		a.botSessionStream(w, r, sessionID(r), op.Method != http.MethodGet, handler)
	})
	return op
}

func sessionPathID(r *http.Request) string  { return r.PathValue("id") }
func sessionQueryID(r *http.Request) string { return r.URL.Query().Get("session_id") }

// An active stream stops if the target or one of its parents is made private
// or deleted. Ordinary high-frequency output never performs an auth query.
func (a *API) botSessionStream(w http.ResponseWriter, r *http.Request, id string, write bool, handler http.HandlerFunc) {
	scoped, keys, err := a.authorizeBotSession(r, id, write)
	if err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	ctx, cancel := context.WithCancel(scoped.Context())
	defer cancel()
	watch := a.Config.Harness.Store.Watch(keys...)
	defer watch.Close()
	changed := watch.Changes()
	if _, _, err = a.authorizeBotSession(r, id, write); err != nil {
		httpcontract.Failure(w, r, err)
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				changed = watch.Changes()
				if _, _, err := a.authorizeBotSession(r.WithContext(ctx), id, write); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	handler(w, scoped.WithContext(ctx))
}

func (a *API) botOpenAPI(w http.ResponseWriter, _ *http.Request) {
	doc := a.Contract.Document()
	paths := make(map[string]any)
	for path, definition := range a.Contract.Paths {
		if IsBotAPIPath(path) {
			paths[path] = definition
		}
	}
	doc["paths"] = paths
	httpcontract.JSON(w, http.StatusOK, doc)
}
