package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func TestNapCatSegmentRetainsStructuredAndScalarRawData(t *testing.T) {
	for _, data := range []string{`"opaque text"`, `{"data":{"title":"card"},"counter":9007199254740993123}`, `[{"nested":true}]`} {
		var segment napcatSegment
		if err := json.Unmarshal([]byte(`{"type":"json","data":`+data+`}`), &segment); err != nil || !bytes.Equal(segment.RawData, []byte(data)) {
			t.Fatalf("lost raw scalar/object/array data: %s, %+v, %v", data, segment, err)
		}
	}
}

func TestNapCatJSONCardsRetainOriginalAndReadableMetadata(t *testing.T) {
	const payload = `{"app":"com.tencent.structmsg","meta":{"detail_1":{"title":"A <title>","desc":"Readable description","qqdocurl":"https://example.com/article"}},"counter":9007199254740993123}`
	for _, rawPayload := range []json.RawMessage{json.RawMessage(payload), encoded(payload)} {
		parts, text, attachments := napcatParts([]byte(`[{"type":"json","data":{"data":`+string(rawPayload)+`}}]`), "42", "")
		if len(parts) != 1 || parts[0].Type != "card" || len(attachments) != 0 {
			t.Fatalf("card normalization: %+v", parts)
		}
		card := parts[0].Card
		if card == nil || card.Kind != "json" || card.Source != "com.tencent.structmsg" || card.Title != "A <title>" || card.Description != "Readable description" || card.URL != "https://example.com/article" || !bytes.Equal(card.Raw, rawPayload) {
			t.Fatalf("card body or metadata lost: %+v", card)
		}
		if !strings.Contains(text, "A <title>") || !strings.Contains(text, "Readable description") || strings.Contains(text, "9007199254740993123") {
			t.Fatal("summary omitted useful metadata or exposed raw JSON")
		}
	}
	for _, data := range []string{`{"data":"broken-json"}`, `{"data":[{"title":"Array card"}]}`} {
		parts, _, _ := napcatParts([]byte(`[{"type":"json","data":`+data+`}]`), "42", "")
		if len(parts) != 1 || parts[0].Card == nil || len(parts[0].Card.Raw) == 0 || parts[0].Card.Description == "" && parts[0].Card.Title == "" {
			t.Fatalf("malformed or array card vanished: %+v", parts)
		}
	}
}

func TestNapCatOnlyExplicitMultimsgARKBecomesForwardResource(t *testing.T) {
	for _, test := range []struct {
		app, meta string
		want      bool
	}{
		{"com.tencent.multimsg", `{"detail":{"resid":"resource-id","news":[{"text":"Alice: preview only"}],"summary":"Two messages"}}`, true},
		{"unknown.app", `{"detail":{"resid":"resource-id"}}`, false},
		{"com.tencent.multimsg", `{"arbitrary":{"resid":"resource-id"}}`, false},
		{"com.tencent.multimsg", `{"detail":{"resid":""}}`, false},
	} {
		data := json.RawMessage(fmt.Sprintf(`{"app":%q,"meta":%s}`, test.app, test.meta))
		parts, _, _ := napcatParts(encoded([]any{map[string]any{"type": "json", "data": map[string]any{"data": data}}}), "42", "")
		part := parts[0]
		if (part.Forward != nil) != test.want || part.Card == nil || !bytes.Equal(part.Card.Raw, data) {
			t.Fatalf("incorrect ARK authority: %+v", part)
		}
		if test.want && (part.Type != "forward" || part.Forward.Status != "unexpanded" || part.Forward.ExternalID != "resource-id" || len(part.Forward.Messages) != 0 || len(part.Forward.Raw) != 0) {
			t.Fatal("ARK news preview was mistaken for a complete message tree")
		}
		if test.want && !strings.Contains(part.Card.Description, "Alice: preview only") {
			t.Fatal("ARK news disappeared instead of remaining a labeled card preview")
		}
	}
}

func TestNapCatMiniAppCardRetainsPlatformAndActualVideoTitle(t *testing.T) {
	parts, summary, _ := napcatParts([]byte(`[{"type":"json","data":{"data":{"app":"com.tencent.miniapp_01","meta":{"detail_1":{"title":"哔哩哔哩","desc":"视频真正的标题","qqdocurl":"https://www.bilibili.com/video/BVexample"}}}}}]`), "42", "")
	card := parts[0].Card
	if card == nil || card.Title != "哔哩哔哩" || card.Description != "视频真正的标题" || !strings.Contains(summary, "哔哩哔哩") || !strings.Contains(summary, "视频真正的标题") {
		t.Fatalf("platform label replaced the actual video title: %+v", card)
	}
}

func TestNapCatForwardTreeKeepsOrderQuotedIdentityAndMediaReferences(t *testing.T) {
	raw := json.RawMessage(`[
 {"type":"text","data":{"text":"before"}},
 {"type":"image","data":{"file":"root-image","name":"root.png"}},
 {"type":"forward","data":{"id":"outer","content":[
  {"sender":{"user_id":123,"nickname":"Alice"},"time":1780000000,"group_id":999999,"message":[
   {"type":"at","data":{"qq":"999"}},
   {"type":"image","data":{"file":"nested-image","name":"nested.png"}},
   {"type":"video","data":{"file":"nested-video","name":"movie.mp4"}},
   {"type":"file","data":{"file":"nested-file","name":"notes.txt"}},
   {"type":"json","data":{"data":{"app":"article","meta":{"detail":{"title":"Nested card"}}}}},
   {"type":"forward","data":{"id":"inner","content":[{"type":"node","data":{"uin":"321","name":"Bob","time":"1780000001","content":[{"type":"text","data":{"text":"deeply quoted"}}]}}]}}
  ]}
 ]}},
 {"type":"text","data":{"text":"after"}}
]`)
	n := &napcat{connection: domain.Connection{Meta: domain.Meta{ID: "account"}, AccountID: "999"}}
	event, err := n.normalize(encoded(map[string]any{"post_type": "message", "message_type": "group", "self_id": 999, "message_id": 42, "user_id": 123, "group_id": 456, "message": raw}))
	if err != nil {
		t.Fatal(err)
	}
	observation := event.Observation
	if observation.Trigger != "" || event.Conversation.ExternalID != "group:456" || len(observation.Attachments) != 1 || observation.Attachments[0].Ref != "42:0" {
		t.Fatalf("nested message changed activation/chat/current attachments: %+v", observation)
	}
	parts := observation.Parts
	if len(parts) != 4 || parts[0].Text != "before" || parts[3].Text != "after" || parts[1].Attachment == nil || parts[1].Attachment.Ref != observation.Attachments[0].Ref {
		t.Fatal("root order or ordinary attachment identity changed")
	}
	forward := parts[2].Forward
	if forward == nil || forward.ExternalID != "outer" || forward.Status != "inline" || forward.Total != 1 || len(forward.Raw) == 0 {
		t.Fatalf("forward content lost: %+v", forward)
	}
	node := forward.Messages[0]
	if node.Sender.ID != "123" || node.Sender.Name != "Alice" || node.SentAt == nil || !node.SentAt.Equal(time.Unix(1780000000, 0)) || len(node.Parts) != 6 {
		t.Fatalf("forward sender/time/order lost: %+v", node)
	}
	for index, kind := range []string{"image", "video", "file"} {
		attachment := node.Parts[index+1].Attachment
		if attachment == nil || attachment.Kind != kind || attachment.Ref != fmt.Sprintf("p2.m0.p%d", index+1) {
			t.Fatalf("nested %s identity lost: %+v", kind, attachment)
		}
	}
	inner := node.Parts[5].Forward.Messages[0]
	if inner.Sender.ID != "321" || inner.Sender.Name != "Bob" || inner.Parts[0].Ref != "p2.m0.p5.m0.p0" || inner.Parts[0].Text != "deeply quoted" || !strings.Contains(observation.Text, "Nested card") || !strings.Contains(observation.Text, "deeply quoted") {
		t.Fatal("nested forward tree or searchable summary lost")
	}
}

func TestNapCatForwardPagesRetainAbsolutePathsBeyondPreview(t *testing.T) {
	nodes := make([]any, 9)
	for i := range nodes {
		nodes[i] = map[string]any{"sender": map[string]any{"user_id": i + 100, "nickname": fmt.Sprintf("sender-%d", i)}, "time": 1780000000 + i, "message": []any{map[string]any{"type": "text", "data": map[string]any{"text": fmt.Sprintf("item-%d", i)}}}}
	}
	inline := encoded(nodes)
	parts, _, _ := napcatParts(encoded([]any{map[string]any{"type": "forward", "data": map[string]any{"id": "id-only", "content": json.RawMessage(inline)}}}), "42", "")
	forward := parts[0].Forward
	if forward.Status != "truncated" || len(forward.Messages) != napcatForwardPeek || forward.Total != len(nodes) || !bytes.Equal(forward.Raw, inline) {
		t.Fatal("bounded preview discarded the remainder")
	}
	for _, raw := range []json.RawMessage{forward.Raw, encoded(map[string]any{"messages": json.RawMessage(inline)}), encoded(string(inline))} {
		page, total, err := napcatForwardPage(raw, "42", "p0", 7, 2)
		if err != nil || total != 9 || len(page) != 2 || page[0].Parts[0].Ref != "p0.m7.p0" || page[1].Parts[0].Text != "item-8" {
			t.Fatalf("page shifted source identities: %+v, total=%d, %v", page, total, err)
		}
	}
	if page, total, err := napcatForwardPage(inline, "42", "p0", math.MaxInt, 50); err != nil || total != 9 || len(page) != 0 {
		t.Fatal("out-of-range offset overflowed")
	}
	for _, raw := range []string{`null`, `{}`, `"broken"`} {
		if _, _, err := napcatForwardPage([]byte(raw), "42", "p0", 0, 10); err == nil {
			t.Fatal("malformed forward page was accepted")
		}
	}
}

func TestNapCatForwardDepthAndPartBudgetsRemainExpandable(t *testing.T) {
	var nested any = map[string]any{"type": "text", "data": map[string]any{"text": "deep source"}}
	for range napcatPartDepth + 3 {
		nested = map[string]any{"type": "forward", "data": map[string]any{"id": "deep", "content": []any{map[string]any{"message": []any{nested}}}}}
	}
	parts, _, _ := napcatParts(encoded([]any{nested, map[string]any{"type": "text", "data": map[string]any{"text": "root tail preserved"}}}), "42", "")
	if len(parts) != 2 || parts[1].Text != "root tail preserved" {
		t.Fatal("quoted preview consumed the enclosing message")
	}
	part := parts[0]
	for part.Forward != nil && part.Forward.Status != "truncated" {
		part = part.Forward.Messages[0].Parts[0]
	}
	if part.Forward == nil || len(part.Forward.Raw) == 0 {
		t.Fatal("depth limit discarded the continuation")
	}
	if _, _, err := napcatForwardPage(part.Forward.Raw, "42", part.Ref, 0, 1); err == nil {
		t.Fatal("paging reset the global depth limit")
	}
	parent := parts[0]
	for strings.Count(parent.Ref, ".m") < napcatPartDepth-1 {
		parent = parent.Forward.Messages[0].Parts[0]
	}
	page, _, err := napcatForwardPage(parent.Forward.Raw, "42", parent.Ref, 0, 1)
	if err != nil || len(page) != 1 || page[0].Parts[0].Forward.Status != "truncated" || strings.Count(page[0].Parts[0].Ref, ".m") != napcatPartDepth {
		t.Fatal("a deep page produced references beyond the global depth budget")
	}
	wide := make([]any, napcatPartBudget+5)
	for i := range wide {
		wide[i] = map[string]any{"type": "text", "data": map[string]any{"text": "quoted"}}
	}
	page, _, err = napcatForwardPage(encoded([]any{map[string]any{"message": wide}}), "42", "p0", 0, 1)
	if err != nil || len(page[0].Parts) != napcatPartBudget+1 || !strings.Contains(page[0].Parts[napcatPartBudget].Text, "truncated") {
		t.Fatal("wide content has no bounded marker")
	}
}

func TestNapCatRichReplyUsesOnlySameChatArchiveWithoutNetworkEnrichment(t *testing.T) {
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			t.Error("Poll attempted a remote lookup, expansion or attachment download")
			http.NotFound(w, r)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer socket.CloseNow()
		for _, id := range []int{43, 44} {
			_ = wsjson.Write(r.Context(), socket, json.RawMessage(fmt.Sprintf(`{"post_type":"message","message_type":"group","self_id":999,"group_id":456,"user_id":123,"message_id":%d,"message":[{"type":"reply","data":{"id":"%d"}},{"type":"forward","data":{"id":"only-id"}},{"type":"text","data":{"text":"reply"}}]}`, id, id-1)))
		}
		_, _, _ = socket.Read(r.Context())
	}))
	t.Cleanup(server.Close)
	n := napcatTestAdapter(t, server)
	n.setStore(store)
	parts, text, _ := napcatParts([]byte(`[{"type":"json","data":{"data":{"app":"article","title":"Original card"}}},{"type":"forward","data":{"id":"archive-id","content":[{"message":[{"type":"image","data":{"file":"nested","name":"original.png"}}]}]}}]`), "42", "")
	source := domain.Observation{ID: "original", ExternalEventID: "original", Kind: "message.created", ConnectionID: n.connection.ID, ConversationID: stableID("conversation", n.connection.ID, "group:456", ""), MessageID: "42", Parts: parts, Text: text, Sender: domain.Sender{Name: "Alice"}, Self: true}
	if _, err := store.Archive(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	foreign := source
	foreign.ID, foreign.ExternalEventID, foreign.ConversationID, foreign.MessageID = "foreign", "foreign", "another-chat", "43"
	if _, err := store.Archive(t.Context(), foreign); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, err := n.Poll(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	event := first.Events[0].Observation
	if !reflect.DeepEqual(event.ReplyParts, source.Parts) || event.ReplySender != "Alice" || event.Trigger != "reply" || len(event.Attachments) != 0 || len(event.ReplyAttachments) != 0 || event.Parts[1].Forward.Status != "unexpanded" {
		t.Fatalf("quoted tree lost provenance or became a current attachment: %+v", event)
	}
	next, err := n.Poll(ctx, first.NextCursor)
	if err != nil || len(next.Events[0].Observation.ReplyParts) != 0 || !strings.Contains(next.Events[0].Observation.ReplyPreview, "unavailable") {
		t.Fatal("foreign source supplied a quoted tree or a reference blocked the next message")
	}
}
