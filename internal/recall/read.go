package recall

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"slices"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
)

type reference struct {
	Version   int    `json:"v"`
	SessionID string `json:"session"`
	RolloutID string `json:"rollout"`
	HeadID    string `json:"head"`
	EntryID   string `json:"entry"`
	Part      int    `json:"part"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Direction string `json:"direction,omitempty"`
}

type indexedText struct {
	ID        int64
	RolloutID string
	EntryID   string
	WriterID  string
	Sequence  int64
	Time      int64
	Part      int
	Kind      string
	Role      string
	ToolName  string
	Body      string
}

func sliceText(item indexedText, body []rune, start, end int, ref reference) Excerpt {
	token := encode(ref)
	return Excerpt{EntryID: item.EntryID, SourceSessionID: item.WriterID, Part: item.Part,
		Kind: item.Kind, Role: item.Role, ToolName: item.ToolName, Time: time.Unix(0, item.Time).UTC(),
		Text: string(body[start:end]), Start: start, End: end, TotalChars: len(body), Ref: token,
		ReadURL: "/v1/sessions/" + url.PathEscape(ref.SessionID) + "/history?around=" + url.QueryEscape(token)}
}

const branchPath = `WITH RECURSIVE chain(id,parent_id) AS (
 SELECT id,parent_id FROM entries WHERE rollout_id=? AND id=?
 UNION ALL SELECT e.id,e.parent_id FROM chain c JOIN entries e ON e.rollout_id=? AND e.id=c.parent_id
) `

func onPath(ctx context.Context, db *sql.DB, rollout, head, entry string) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, branchPath+"SELECT 1 FROM chain WHERE id=? LIMIT 1", rollout, head, rollout, entry).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

const textColumns = "b.id,b.rollout_id,b.entry_id,e.writer_id,e.sequence,e.time,b.part,b.kind,b.role,b.tool_name,b.text"

func scanText(row interface{ Scan(...any) error }) (indexedText, error) {
	var item indexedText
	err := row.Scan(&item.ID, &item.RolloutID, &item.EntryID, &item.WriterID, &item.Sequence, &item.Time, &item.Part, &item.Kind, &item.Role, &item.ToolName, &item.Body)
	return item, err
}

func adjacent(ctx context.Context, db *sql.DB, ref reference, anchor *indexedText, direction string, limit int) ([]indexedText, error) {
	items := []indexedText{}
	if limit == 0 {
		return items, nil
	}
	query := branchPath + "SELECT " + textColumns + " FROM chain c JOIN blocks b ON b.rollout_id=? AND b.entry_id=c.id JOIN entries e ON e.rollout_id=b.rollout_id AND e.id=b.entry_id"
	args := []any{ref.RolloutID, ref.HeadID, ref.RolloutID, ref.RolloutID}
	order, comparison := "DESC", "<"
	if direction == "newer" {
		order, comparison = "ASC", ">"
	}
	if anchor != nil {
		query += " WHERE (e.sequence" + comparison + "? OR (e.sequence=? AND b.part" + comparison + "?))"
		args = append(args, anchor.Sequence, anchor.Sequence, anchor.Part)
	}
	query += " ORDER BY e.sequence " + order + ",b.part " + order + " LIMIT ?"
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, readErr := scanText(rows)
		if readErr != nil {
			return nil, readErr
		}
		items = append(items, item)
	}
	if direction == "older" {
		slices.Reverse(items)
	}
	return items, rows.Err()
}

func (s *Service) Read(ctx context.Context, agentID, sessionID string, request ReadRequest) (HistoryPage, error) {
	page := HistoryPage{SessionID: sessionID, Items: []Excerpt{}}
	if request.Cursor != "" && request.Around != "" {
		return page, domain.Invalid("use cursor or around, not both")
	}
	if request.Around == "" && (request.Before != nil || request.After != nil || request.Offset != nil) {
		return page, domain.Invalid("before, after and offset require around")
	}
	if request.Around != "" && request.Limit != 0 {
		return page, domain.Invalid("around uses before/after instead of limit")
	}
	limit, err := pageLimit(request.Limit, 20)
	if err != nil {
		return page, err
	}
	length := request.Length
	if length == 0 {
		length = 4000
	}
	if length < 1 || length > 16384 {
		return page, domain.Invalid("length must be between 1 and 16384 Unicode characters per text part")
	}
	before, after := 2, 2
	if request.Before != nil {
		before = *request.Before
	}
	if request.After != nil {
		after = *request.After
	}
	if before < 0 || after < 0 || before > 100 || after > 100 {
		return page, domain.Invalid("before and after must be between 0 and 100 text parts")
	}
	if _, err = s.authorize(ctx, agentID, sessionID); err != nil {
		return page, err
	}
	state, err := s.core.LoadState(ctx, sessionID)
	if errors.Is(err, core.ErrNotFound) && request.Around == "" && request.Cursor == "" {
		return page, nil
	}
	if err != nil {
		return page, err
	}
	ref := reference{Version: 1, SessionID: sessionID, RolloutID: state.RolloutID, HeadID: state.Branch.HeadID}
	token := request.Around
	if request.Cursor != "" {
		token = request.Cursor
	}
	if token != "" {
		if err = decode(token, &ref); err != nil {
			return page, err
		}
		if ref.Version != 1 || ref.SessionID != sessionID || ref.RolloutID != state.RolloutID || ref.HeadID == "" || ref.EntryID == "" || ref.Part < 0 || ref.Start < 0 || ref.End < ref.Start {
			return page, domain.Invalid("reference does not belong to this Session or text projection")
		}
		if request.Cursor != "" && ref.Direction != "older" && ref.Direction != "newer" || request.Around != "" && ref.Direction != "" {
			return page, domain.Invalid("use a history paging cursor or a source reference in its corresponding parameter")
		}
	}
	page.HeadID = ref.HeadID
	if ref.HeadID == "" {
		return page, nil
	}
	s.mu.Lock()
	db, err := s.database(ctx)
	if err == nil {
		err = s.indexHead(ctx, db, state.RolloutID, state.Branch.HeadID)
	}
	s.mu.Unlock()
	if err != nil {
		return page, err
	}
	var anchor *indexedText
	if token != "" {
		for _, pair := range [][2]string{{state.Branch.HeadID, ref.HeadID}, {ref.HeadID, ref.EntryID}} {
			visible, checkErr := onPath(ctx, db, ref.RolloutID, pair[0], pair[1])
			if checkErr != nil {
				return page, checkErr
			}
			if !visible {
				return page, domain.Forbidden("history reference is outside this Session branch")
			}
		}
		item, readErr := scanText(db.QueryRowContext(ctx, "SELECT "+textColumns+" FROM blocks b JOIN entries e ON e.rollout_id=b.rollout_id AND e.id=b.entry_id WHERE b.rollout_id=? AND b.entry_id=? AND b.part=?", ref.RolloutID, ref.EntryID, ref.Part))
		if errors.Is(readErr, sql.ErrNoRows) {
			return page, domain.Missing("history text part", ref.EntryID)
		}
		if readErr != nil {
			return page, readErr
		}
		if ref.End > len([]rune(item.Body)) {
			return page, domain.Invalid("reference exceeds the source text part")
		}
		anchor = &item
	}
	var items []indexedText
	if request.Around != "" {
		older, readErr := adjacent(ctx, db, ref, anchor, "older", before)
		if readErr != nil {
			return page, readErr
		}
		newer, readErr := adjacent(ctx, db, ref, anchor, "newer", after)
		if readErr != nil {
			return page, readErr
		}
		items = append(append(older, *anchor), newer...)
	} else {
		direction := ref.Direction
		if direction == "" {
			direction = "older"
		}
		items, err = adjacent(ctx, db, ref, anchor, direction, limit)
		if err != nil {
			return page, err
		}
	}
	for _, item := range items {
		body := []rune(item.Body)
		start, end := 0, min(len(body), length)
		if request.Around != "" && item.EntryID == ref.EntryID && item.Part == ref.Part {
			start, end = centered(len(body), span{ref.Start, min(ref.End, ref.Start+length)}, length)
			if request.Offset != nil {
				start = *request.Offset
				if start < 0 || start > len(body) {
					return page, domain.Invalid("offset is outside the source text part")
				}
				end = min(len(body), start+length)
			}
		}
		itemRef := reference{Version: 1, SessionID: sessionID, RolloutID: ref.RolloutID, HeadID: ref.HeadID, EntryID: item.EntryID, Part: item.Part, Start: start, End: end}
		page.Items = append(page.Items, sliceText(item, body, start, end, itemRef))
	}
	if len(items) > 0 {
		for _, boundary := range []struct {
			direction string
			item      indexedText
			target    *string
		}{
			{"older", items[0], &page.OlderCursor}, {"newer", items[len(items)-1], &page.NewerCursor},
		} {
			more, readErr := adjacent(ctx, db, ref, &boundary.item, boundary.direction, 1)
			if readErr != nil {
				return page, readErr
			}
			if len(more) > 0 {
				*boundary.target = encode(reference{Version: 1, SessionID: sessionID, RolloutID: ref.RolloutID, HeadID: ref.HeadID, EntryID: boundary.item.EntryID, Part: boundary.item.Part, Direction: boundary.direction})
			}
		}
	}
	return page, nil
}
