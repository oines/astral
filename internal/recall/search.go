package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
)

// Reachability, rather than the entry writer, defines a Session's history.
// Grouping by source entry removes repeated Fork ancestors. Prefer the writer
// as the reading Session when it is included in this query's authorized scope.
const searchPaths = `WITH RECURSIVE paths(session_id,rollout_id,entry_id) AS (
 SELECT session_id,rollout_id,head_id FROM snapshot_sessions WHERE snapshot_id=? AND head_id<>''
 UNION ALL
 SELECT p.session_id,p.rollout_id,e.parent_id FROM paths p
 JOIN entries e ON e.rollout_id=p.rollout_id AND e.id=p.entry_id WHERE e.parent_id<>''
), visible AS (
 SELECT p.rollout_id,p.entry_id,coalesce(min(CASE WHEN p.session_id=e.writer_id THEN p.session_id END),min(p.session_id)) AS session_id
 FROM paths p JOIN entries e ON e.rollout_id=p.rollout_id AND e.id=p.entry_id
 GROUP BY p.rollout_id,p.entry_id
) `

func normalizeSearch(request SearchRequest) (SearchRequest, []string, error) {
	request.Cursor, request.Limit = "", 0
	request.Query = strings.TrimSpace(request.Query)
	if request.Query == "" || utf8.RuneCountInString(request.Query) > 512 || strings.ContainsRune(request.Query, 0) || !utf8.ValidString(request.Query) {
		return request, nil, domain.Invalid("query must contain 1–512 Unicode characters without NUL")
	}
	if request.Match == "" {
		request.Match = "all"
	}
	if !slices.Contains([]string{"all", "any", "phrase"}, request.Match) {
		return request, nil, domain.Invalid("match must be all, any or phrase")
	}
	terms := []string{request.Query}
	if request.Match != "phrase" {
		terms = strings.Fields(request.Query)
	}
	if len(terms) > 16 {
		return request, nil, domain.Invalid("query supports at most 16 whitespace-separated terms; use phrase for a literal passage")
	}
	if !request.CaseSensitive {
		for i := range terms {
			terms[i] = fold(terms[i])
		}
	}
	slices.Sort(terms)
	terms = slices.Compact(terms)
	if request.Order == "" {
		request.Order = "newest"
	}
	if request.Order != "newest" && request.Order != "oldest" {
		return request, nil, domain.Invalid("order must be newest or oldest")
	}
	if request.ContextChars == 0 {
		request.ContextChars = 600
	}
	if request.ContextChars < 32 || request.ContextChars > 4096 {
		return request, nil, domain.Invalid("context_chars must be between 32 and 4096")
	}
	if request.After != nil && request.Before != nil && !request.After.Before(*request.Before) {
		return request, nil, domain.Invalid("after must be earlier than before")
	}
	for _, role := range request.Roles {
		if !slices.Contains([]string{"user", "assistant", "tool"}, role) {
			return request, nil, domain.Invalid("roles must contain user, assistant or tool")
		}
	}
	for _, kind := range request.Kinds {
		if !slices.Contains([]string{"message", "observation", "tool_call", "tool_result", "attachment", "summary"}, kind) {
			return request, nil, domain.Invalid("unsupported history content kind")
		}
	}
	if len(request.SessionIDs) > 200 {
		return request, nil, domain.Invalid("session_ids supports at most 200 explicit Sessions; omit it to search all")
	}
	for _, values := range []*[]string{&request.SessionIDs, &request.Kinds, &request.Roles} {
		if len(*values) == 0 {
			*values = nil
			continue
		}
		*values = slices.Clone(*values)
		slices.Sort(*values)
		*values = slices.Compact(*values)
	}
	return request, terms, nil
}

func (s *Service) Search(ctx context.Context, agentID string, request SearchRequest) (SearchPage, error) {
	page := SearchPage{Hits: []Hit{}}
	limit, err := pageLimit(request.Limit, 50)
	if err != nil {
		return page, err
	}
	var db *sql.DB
	var parameters SearchRequest
	var terms []string
	position := cursor{Version: 1, Kind: "search"}
	if request.Cursor == "" {
		parameters, terms, err = normalizeSearch(request)
		if err == nil {
			db, position.Snapshot, err = s.snapshot(ctx, agentID, "search", parameters, scope{ids: parameters.SessionIDs, source: parameters.Source, conversationID: parameters.ConversationID, archived: parameters.Archived})
		}
	} else {
		db, position, err = s.resume(ctx, agentID, "search", request.Cursor, &parameters)
		if err == nil {
			var supplied SearchRequest
			supplied = request
			supplied.Cursor, supplied.Limit = "", 0
			if !reflect.DeepEqual(supplied, SearchRequest{}) {
				supplied, _, err = normalizeSearch(supplied)
				if err == nil && !reflect.DeepEqual(supplied, parameters) {
					err = domain.Invalid("cursor filters cannot change; pass cursor and optional limit to continue")
				}
			}
			if err == nil {
				parameters, terms, err = normalizeSearch(parameters)
			}
		}
	}
	if err != nil {
		return page, err
	}
	query := searchPaths + `SELECT b.id,b.rollout_id,b.entry_id,e.writer_id,e.sequence,e.time,b.part,b.kind,b.role,b.tool_name,b.text,ss.data
 FROM visible v JOIN blocks b ON b.rollout_id=v.rollout_id AND b.entry_id=v.entry_id
 JOIN entries e ON e.rollout_id=b.rollout_id AND e.id=b.entry_id
 JOIN snapshot_sessions ss ON ss.snapshot_id=? AND ss.session_id=v.session_id WHERE `
	args := []any{position.Snapshot, position.Snapshot}
	conditions := make([]string, 0, len(terms))
	for _, term := range terms {
		column := "b.folded"
		if parameters.CaseSensitive {
			column = "b.text"
		}
		condition := "instr(" + column + ",?)>0"
		args = append(args, term)
		// Trigrams accelerate arbitrary substrings, including punctuation and
		// CJK. Short terms use exact matching on the text index; they must not
		// silently return zero results as FTS MATCH alone would.
		if utf8.RuneCountInString(term) >= 3 {
			condition = "(" + condition + " AND b.id IN (SELECT rowid FROM blocks_fts WHERE blocks_fts MATCH ?))"
			args = append(args, `"`+strings.ReplaceAll(fold(term), `"`, `""`)+`"`)
		}
		conditions = append(conditions, condition)
	}
	join := " AND "
	if parameters.Match == "any" {
		join = " OR "
	}
	query += "(" + strings.Join(conditions, join) + ")"
	filterValues := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		query += " AND " + column + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(values)), ",") + ")"
		for _, value := range values {
			args = append(args, value)
		}
	}
	filterValues("b.role", parameters.Roles)
	filterValues("b.kind", parameters.Kinds)
	if len(parameters.Kinds) == 0 {
		query += " AND b.kind<>'summary'"
	}
	if parameters.After != nil {
		query += " AND e.time>=?"
		args = append(args, parameters.After.UnixNano())
	}
	if parameters.Before != nil {
		query += " AND e.time<?"
		args = append(args, parameters.Before.UnixNano())
	}
	order, comparison := "DESC", "<"
	if parameters.Order == "oldest" {
		order, comparison = "ASC", ">"
	}
	if position.Block != 0 {
		query += " AND (e.time" + comparison + "? OR (e.time=? AND b.id" + comparison + "=?))"
		args = append(args, position.Time, position.Time, position.Block)
	}
	query += " ORDER BY e.time " + order + ",b.id " + order
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item indexedText
		var raw []byte
		if err = rows.Scan(&item.ID, &item.RolloutID, &item.EntryID, &item.WriterID, &item.Sequence, &item.Time, &item.Part, &item.Kind, &item.Role, &item.ToolName, &item.Body, &raw); err != nil {
			return page, err
		}
		var session Session
		if err = json.Unmarshal(raw, &session); err != nil {
			return page, err
		}
		body := []rune(item.Body)
		for _, window := range passages(item.Body, terms, parameters.CaseSensitive, parameters.ContextChars) {
			if item.ID == position.Block && window.start < position.Offset {
				continue
			}
			if len(page.Hits) == limit {
				page.NextCursor = encode(cursor{Version: 1, Kind: "search", Snapshot: position.Snapshot, Time: item.Time, Block: item.ID, Offset: window.start})
				return page, nil
			}
			ref := reference{Version: 1, SessionID: session.ID, RolloutID: item.RolloutID, HeadID: session.HeadID, EntryID: item.EntryID, Part: item.Part, Start: window.focus.start, End: window.focus.end}
			page.Hits = append(page.Hits, Hit{SessionID: session.ID, SessionTitle: session.Title,
				Excerpt: sliceText(item, body, window.start, window.end, ref), MatchCount: window.count})
		}
	}
	return page, rows.Err()
}

type span struct{ start, end int }
type passage struct {
	span
	focus span
	count int
}

func passages(text string, terms []string, caseSensitive bool, width int) []passage {
	matched := text
	if !caseSensitive {
		matched = fold(text)
	}
	var found []span
	for _, term := range terms {
		length := utf8.RuneCountInString(term)
		width = max(width, length)
		for byteAt, charAt := 0, 0; byteAt < len(matched); {
			n := strings.Index(matched[byteAt:], term)
			if n < 0 {
				break
			}
			start := charAt + utf8.RuneCountInString(matched[byteAt:byteAt+n])
			found = append(found, span{start, start + length})
			_, size := utf8.DecodeRuneInString(matched[byteAt+n:])
			byteAt, charAt = byteAt+n+size, start+1
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].start == found[j].start {
			return found[i].end < found[j].end
		}
		return found[i].start < found[j].start
	})
	found = slices.Compact(found)
	body := []rune(text)
	var windows []passage
	previousEnd := 0
	for i := 0; i < len(found); {
		focus := found[i]
		start, end := centered(len(body), focus, width)
		// Adjacent hits share a passage, but dense matches never grow a
		// passage to the size of an entire log. Every occurrence is pageable.
		start = max(start, min(previousEnd, focus.start))
		end = min(len(body), start+width)
		for j := start; j < focus.start && j < start+width/4; j++ {
			if body[j] == '\n' {
				start = j + 1
				break
			}
		}
		last := i
		for i < len(found) && found[i].end <= end {
			i++
		}
		windows = append(windows, passage{span: span{start, end}, focus: focus, count: i - last})
		previousEnd = end
	}
	return windows
}

func centered(total int, focus span, width int) (int, int) {
	width = max(width, focus.end-focus.start)
	start := max(0, focus.start-(width-(focus.end-focus.start))/2)
	end := min(total, start+width)
	return max(0, min(start, end-width)), end
}
