package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

func (a *API) readSessionEntry(r *http.Request, id string) (core.Entry, error) {
	target := r.PathValue("id")
	session, err := a.Config.Harness.Core(r.Context(), target)
	if err != nil {
		return core.Entry{}, err
	}
	entry, err := session.History().Get(r.Context(), id)
	if err != nil || botID(r) == "" || entry.SessionID == target {
		return entry, err
	}
	// A shared rollout can contain records written by different sessions.
	if _, _, err := a.authorizeBotSession(r, entry.SessionID, false); err != nil {
		return core.Entry{}, err
	}
	return entry, nil
}

func (a *API) historyCursor(r *http.Request, view string) (string, error) {
	cursor := r.URL.Query().Get("cursor")
	if botID(r) == "" || cursor == "" {
		return cursor, nil
	}
	if len(cursor) <= 4096 {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err == nil && len(data) > sha256.Size {
			position := string(data[sha256.Size:])
			if hmac.Equal(data[:sha256.Size], a.historyCursorMAC(r, view, position)) {
				return position, nil
			}
		}
	}
	return "", domain.Invalid("Invalid history cursor. Request the first page again.")
}

func (a *API) historyNextCursor(r *http.Request, view, position string) string {
	if botID(r) == "" || position == "" {
		return position
	}
	return base64.RawURLEncoding.EncodeToString(append(a.historyCursorMAC(r, view, position), position...))
}

func historyView(r *http.Request, view string) string {
	if r.URL.Query().Get("trace") == "true" {
		return view + "/trace"
	}
	return view
}

func (a *API) historyCursorMAC(r *http.Request, view, position string) []byte {
	// Core path cursors identify entries within the rollout, including entries
	// outside the requested branch. Only accept a cursor from this API's page.
	mac := hmac.New(sha256.New, a.botCursorKey[:])
	mac.Write([]byte(view + "\x00" + botID(r) + "\x00" + r.PathValue("id") + "\x00" + position))
	return mac.Sum(nil)
}
