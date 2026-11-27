package acp

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
)

const acpSessionListPageSize = 50

type acpSessionInfo struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
}

type acpSessionListResult struct {
	Sessions   []acpSessionInfo `json:"sessions"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func (c *connection) listSessions(request rpcRequest) *rpcError {
	var params struct {
		CWD    *string `json:"cwd"`
		Cursor *string `json:"cursor"`
	}
	if len(request.Params) != 0 {
		if err := decodeParams(request.Params, &params); err != nil {
			return err
		}
	}
	var cwd string
	if params.CWD != nil {
		cwd = *params.CWD
		if !absoluteCWD(cwd) {
			return invalidParams("cwd must be an absolute Computer path.")
		}
	}

	// Bind the position to the Bot and filter without placing either in the
	// cursor. Visibility is checked from the current records on each page.
	scope := sha256.Sum256([]byte("session/list:v1\x00" + c.botID + "\x00" + cwd))
	var afterID string
	if params.Cursor != nil {
		if len(*params.Cursor) > 4096 {
			return invalidParams("Invalid session list cursor.")
		}
		cursor, err := base64.RawURLEncoding.DecodeString(*params.Cursor)
		if err != nil || len(cursor) <= len(scope) || !bytes.Equal(cursor[:len(scope)], scope[:]) {
			return invalidParams("Invalid session list cursor.")
		}
		afterID = string(cursor[len(scope):])
	}

	store := c.service.harness.Store
	result := acpSessionListResult{Sessions: make([]acpSessionInfo, 0, acpSessionListPageSize)}
	for {
		entries, err := store.ListVisibleBotSessions(c.ctx, c.botID, cwd, afterID, acpSessionListPageSize+1)
		if err != nil {
			return toRPCError(err)
		}
		for _, entry := range entries {
			if !absoluteCWD(entry.CWD) {
				continue
			}
			if len(result.Sessions) == acpSessionListPageSize {
				lastID := result.Sessions[len(result.Sessions)-1].SessionID
				result.NextCursor = base64.RawURLEncoding.EncodeToString(append(scope[:], lastID...))
				c.reply(request.ID, result, false)
				return nil
			}
			result.Sessions = append(result.Sessions, acpSessionInfo{
				SessionID: entry.ID,
				CWD:       entry.CWD,
				Title:     entry.Title,
			})
		}
		if len(entries) < acpSessionListPageSize+1 {
			c.reply(request.ID, result, false)
			return nil
		}
		afterID = entries[len(entries)-1].ID
	}
}
