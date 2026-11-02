package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/persistence"
)

type rolloutPosition struct {
	Version        int    `json:"v"`
	SessionID      string `json:"session"`
	RolloutID      string `json:"rollout"`
	HeadID         string `json:"head"`
	ThroughVersion uint64 `json:"through_version"`
	Trace          bool   `json:"trace"`
	Cursor         string `json:"cursor"`
}

func (s *Service) rolloutRoutes() {
	parameters := []httpcontract.Parameter{
		{Name: "cursor", Description: "Opaque next_cursor from this Session and the same trace mode. Keeps the original branch head and trace snapshot across new writes and Server restarts."},
		{Name: "limit", Type: "integer", Description: "Raw entries per page, default 50; 1–200."},
		{Name: "trace", Type: "boolean", Description: "Default false: original history on this Session's branch, including inherited ancestors. true: this Session writer's own raw records, including Run status/errors, queued inputs and tool lifecycle; excludes other writers."},
	}
	httpcontract.Register(s.Contract, s.mux, httpcontract.Operation{Method: "GET", Path: "/v1/sessions/{id}/rollout", ID: "agentRollout", Tag: "Agent Gateway", CommandAuth: true, Query: parameters,
		Description: "Read raw persisted Core entries, newest first, without opening a Session runtime or loading a model. Default follows the captured history branch and never includes shared Rollout siblings. trace=true reads only the target Session writer's records and includes Run failures with their errors. Images retain metadata but omit embedded pixel data. Pagination returns a fixed head_id and an opaque next_cursor. Sessions without committed Core state return an empty array."},
		func(_ http.ResponseWriter, r *http.Request, _ httpcontract.Empty) (HistoryPage, error) {
			if err := validateRecallQuery(r, parameters); err != nil {
				return HistoryPage{}, err
			}
			limit := 50
			if value, err := recallInteger(r, "limit"); err != nil {
				return HistoryPage{}, err
			} else if value != nil {
				limit = *value
			}
			if limit > 200 {
				return HistoryPage{}, domain.Invalid("limit must be between 1 and 200")
			}
			trace := false
			if r.URL.Query().Has("trace") {
				if value := r.URL.Query().Get("trace"); value != "true" && value != "false" {
					return HistoryPage{}, domain.Invalid("trace must be true or false")
				} else {
					trace = value == "true"
				}
			}
			return s.rolloutHistory(r.Context(), binding(r).AgentID, r.PathValue("id"), r.URL.Query().Get("cursor"), limit, trace)
		})
}

func (s *Service) rolloutHistory(ctx context.Context, agentID, id, cursor string, limit int, trace bool) (HistoryPage, error) {
	var session domain.Session
	if err := s.Harness.Store.Get(ctx, "sessions", id, &session); err != nil {
		return HistoryPage{}, err
	}
	if session.AgentID != agentID {
		return HistoryPage{}, domain.Forbidden("Session is outside the current Agent view")
	}
	if err := persistence.CheckSessionAccess(ctx, session, s.Harness.Store); err != nil {
		return HistoryPage{}, err
	}
	result := HistoryPage{Entries: []core.Entry{}}
	state, err := s.Harness.CoreStore.LoadState(ctx, id)
	if errors.Is(err, core.ErrNotFound) && cursor == "" {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	position := rolloutPosition{Version: 1, SessionID: id, RolloutID: state.RolloutID, HeadID: state.Branch.HeadID, ThroughVersion: state.Version, Trace: trace}
	if cursor != "" {
		if err := s.decodeRolloutCursor(cursor, &position); err != nil {
			return result, err
		}
		if position.Version != 1 || position.SessionID != id || position.RolloutID != state.RolloutID || position.Trace != trace || position.ThroughVersion > state.Version || position.Cursor == "" {
			return result, domain.Invalid("rollout cursor does not belong to this Session or trace mode")
		}
	}
	result.HeadID = position.HeadID
	if !trace && position.HeadID == "" {
		return result, nil
	}
	query := core.Query{Cursor: position.Cursor, Limit: limit, MaxBytes: 128 << 20}
	if trace {
		query.SessionID, query.Reverse = id, true
	} else {
		query.PathHead, query.HistoryOnly = position.HeadID, true
	}
	for {
		page, err := s.Harness.CoreStore.Scan(ctx, position.RolloutID, query)
		if err != nil {
			return result, err
		}
		for _, record := range page.Records {
			if trace && record.Entry.Version > position.ThroughVersion {
				continue
			}
			entry := record.Entry
			RemoveImageData(reflect.ValueOf(&entry))
			result.Entries = append(result.Entries, entry)
		}
		if len(result.Entries) != 0 || page.Next == "" {
			if page.Next != "" {
				position.Cursor = page.Next
				result.NextCursor, err = s.encodeRolloutCursor(position)
			}
			return result, err
		}
		// Writes racing the first trace read may be newer than its captured
		// version. Consume only those records, then continue into the snapshot.
		if page.Next == query.Cursor {
			return result, core.ErrCorrupt
		}
		query.Cursor = page.Next
	}
}

// A plain Core path cursor is an entry ID and can point at another shared
// branch. Bind every continuation to the authorized target and immutable head.
// Derive a separate signing key from the existing Host key; this read-only API
// creates no secrets or snapshots, and cursors survive Server restarts.
func (s *Service) rolloutCursorKey() ([]byte, error) {
	key, err := os.ReadFile(filepath.Join(s.Harness.Store.Directory, "master.key"))
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("invalid Host key for rollout pagination")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("astral/gateway/rollout-cursor/v1"))
	return mac.Sum(nil), nil
}

func (s *Service) encodeRolloutCursor(position rolloutPosition) (string, error) {
	key, err := s.rolloutCursorKey()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(position)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) decodeRolloutCursor(cursor string, position *rolloutPosition) error {
	invalid := func() error { return domain.Invalid("invalid rollout cursor") }
	if len(cursor) > 8192 {
		return invalid()
	}
	parts := strings.Split(cursor, ".")
	if len(parts) != 2 {
		return invalid()
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return invalid()
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return invalid()
	}
	key, err := s.rolloutCursorKey()
	if err != nil {
		return fmt.Errorf("read rollout pagination key: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(raw, position) != nil {
		return invalid()
	}
	return nil
}
