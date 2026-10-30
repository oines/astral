package channels

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

const (
	napcatPartBudget   = 256
	napcatPartDepth    = 6
	napcatForwardPeek  = 5
	napcatForwardLimit = 50
	napcatSummaryLimit = 4096
)

// napcatParts performs local parsing only. Nested content is quoted material,
// never another event, chat grant, mention trigger or automatically loaded image.
func napcatParts(raw json.RawMessage, messageID, prefix string) ([]domain.MessagePart, string, []domain.Attachment) {
	remaining := napcatPartBudget
	return parseNapcatParts(raw, messageID, prefix, 0, &remaining)
}

func parseNapcatParts(raw json.RawMessage, messageID, prefix string, depth int, remaining *int) ([]domain.MessagePart, string, []domain.Attachment) {
	var segments []napcatSegment
	if json.Unmarshal(raw, &segments) != nil {
		return []domain.MessagePart{{Ref: napcatPartRef(prefix, 0), Type: "unsupported", Text: "[Malformed QQ message content]"}}, "[Malformed QQ message content]", nil
	}
	parts := make([]domain.MessagePart, 0, min(len(segments), *remaining))
	var attachments []domain.Attachment
	var summary strings.Builder
	for index, segment := range segments {
		ref := napcatPartRef(prefix, index)
		if prefix == "" && index >= napcatPartBudget || prefix != "" && *remaining == 0 {
			parts = append(parts, domain.MessagePart{Ref: ref, Type: "unsupported", Text: "[QQ message preview truncated; read the parent forward resource for more content]"})
			napcatAppendSummary(&summary, "[QQ message preview truncated]")
			break
		}
		if prefix != "" {
			*remaining--
		}
		part := domain.MessagePart{Ref: ref, Type: "text"}
		switch segment.Type {
		case "text":
			part.Text = string(segment.Data["text"])
		case "at":
			part.Text = "@" + string(segment.Data["qq"])
		case "reply":
			part.Type, part.Text = "unsupported", "[QQ reply reference]"
		case "shake":
			part.Text = "[QQ private window shake]"
		case "image", "record", "video", "file":
			attachmentRef := ref
			if prefix == "" {
				attachmentRef = fmt.Sprintf("%s:%d", messageID, len(attachments))
			}
			attachment, ok := napcatPartAttachment(segment, messageID, attachmentRef)
			if !ok {
				part.Type, part.Text = "unsupported", "[QQ media metadata unavailable]"
				break
			}
			part.Type, part.Attachment = "attachment", &attachment
			if prefix == "" {
				attachments = append(attachments, attachment)
			}
		case "json":
			part.Type, part.Card = "card", napcatPartCard(segment.RawData)
			if externalID := napcatCardForwardID(part.Card.Raw); externalID != "" {
				part.Type = "forward"
				part.Forward = &domain.MessageForward{ExternalID: externalID, Status: "unexpanded"}
				napcatMultimsgPreview(part.Card)
			}
		case "forward":
			part.Type = "forward"
			part.Forward = &domain.MessageForward{ExternalID: string(segment.Data["id"]), Status: "unexpanded"}
			if inline, ok := napcatForwardArray(segment.RawData); ok {
				part.Forward.Raw = append(json.RawMessage(nil), inline...)
				var nodes []json.RawMessage
				_ = json.Unmarshal(inline, &nodes)
				part.Forward.Total = len(nodes)
				part.Forward.Status = "inline"
				if depth >= napcatPartDepth || *remaining == 0 {
					part.Forward.Status = "truncated"
				} else {
					count := min(len(nodes), napcatForwardPeek)
					for i := 0; i < count && *remaining > 0; i++ {
						part.Forward.Messages = append(part.Forward.Messages, parseNapcatForwardNode(nodes[i], messageID, fmt.Sprintf("%s.m%d", ref, i), depth+1, remaining))
					}
					if len(part.Forward.Messages) < len(nodes) {
						part.Forward.Status = "truncated"
					}
				}
			} else if napcatHasForwardContent(segment.RawData) || part.Forward.ExternalID == "" {
				part.Forward.Status = "unavailable"
				// Raw is reserved for complete inline node arrays. A known handle
				// can still be requested explicitly; Poll never tries to repair it.
			}
		default:
			part.Type, part.Text = "unsupported", "[Unsupported QQ segment: "+napcatShort(segment.Type, 64)+"]"
		}
		parts = append(parts, part)
		if segment.Type != "reply" {
			napcatAppendSummary(&summary, napcatPartSummary(part))
		}
	}
	return parts, summary.String(), attachments
}

func napcatPartRef(prefix string, index int) string {
	if prefix != "" {
		prefix += "."
	}
	return prefix + "p" + strconv.Itoa(index)
}

func napcatPartAttachment(segment napcatSegment, messageID, ref string) (domain.Attachment, bool) {
	data := segment.Data
	file := string(data["file"])
	if file == "" {
		file = string(data["file_id"])
	}
	if file == "" {
		file = string(data["url"])
	}
	if file == "" {
		return domain.Attachment{}, false
	}
	name := string(data["name"])
	if name == "" {
		name = string(data["file_name"])
	}
	if name == "" {
		name = segment.Type + "-" + messageID
	}
	mediaType := mime.TypeByExtension(filepath.Ext(name))
	if mediaType == "" {
		mediaType = map[string]string{"image": "image/jpeg", "record": "audio/ogg", "video": "video/mp4", "file": "application/octet-stream"}[segment.Type]
	}
	kind := segment.Type
	if kind == "record" {
		kind = "audio"
	}
	var size int64
	_, _ = fmt.Sscan(string(data["file_size"]), &size)
	return domain.Attachment{Ref: ref, ExternalID: string(encoded(map[string]string{"file": file, "url": string(data["url"])})), Kind: kind, Name: name, MediaType: mediaType, Size: size}, true
}

// Forward.Raw is an array, but accepting response wrappers also makes this
// helper useful for get_forward_msg without routing its child chat identifiers.
func napcatForwardArray(raw json.RawMessage) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	for range 3 {
		if len(raw) == 0 {
			return nil, false
		}
		if raw[0] == '[' && json.Valid(raw) {
			return raw, true
		}
		if raw[0] == '"' {
			var text string
			if json.Unmarshal(raw, &text) != nil {
				return nil, false
			}
			raw = bytes.TrimSpace([]byte(text))
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			return nil, false
		}
		found := false
		for _, key := range []string{"content", "messages", "message"} {
			if value, ok := fields[key]; ok {
				raw, found = bytes.TrimSpace(value), true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return nil, false
}

func napcatHasForwardContent(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return true
	}
	for _, key := range []string{"content", "messages", "message"} {
		if _, ok := fields[key]; ok {
			return true
		}
	}
	return false
}

func napcatForwardPage(raw json.RawMessage, messageID, prefix string, offset, limit int) ([]domain.ForwardedMessage, int, error) {
	if offset < 0 || limit < 1 || limit > napcatForwardLimit {
		return nil, 0, domain.Invalid("forward page requires a nonnegative offset and a limit between 1 and 50")
	}
	depth := strings.Count(prefix, ".m") + 1
	if depth > napcatPartDepth {
		return nil, 0, domain.Invalid("forwarded message depth exceeds the supported limit")
	}
	inline, ok := napcatForwardArray(raw)
	if !ok {
		return nil, 0, domain.Invalid("NapCat forwarded content is unavailable or malformed")
	}
	var nodes []json.RawMessage
	if json.Unmarshal(inline, &nodes) != nil {
		return nil, 0, domain.Invalid("NapCat forwarded content is unavailable or malformed")
	}
	result := []domain.ForwardedMessage{}
	remaining := napcatPartBudget
	start := min(offset, len(nodes))
	end := start + min(limit, len(nodes)-start)
	for index := start; index < end; index++ {
		result = append(result, parseNapcatForwardNode(nodes[index], messageID, fmt.Sprintf("%s.m%d", prefix, index), depth, &remaining))
	}
	return result, len(nodes), nil
}

func parseNapcatForwardNode(raw json.RawMessage, messageID, prefix string, depth int, remaining *int) domain.ForwardedMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return domain.ForwardedMessage{Parts: []domain.MessagePart{{Ref: napcatPartRef(prefix, 0), Type: "unsupported", Text: "[Malformed forwarded QQ message]"}}}
	}
	if napcatScalar(fields["type"]) == "node" {
		var data map[string]json.RawMessage
		if json.Unmarshal(fields["data"], &data) == nil && data != nil {
			fields = data
		}
	}
	var sender map[string]json.RawMessage
	_ = json.Unmarshal(fields["sender"], &sender)
	get := func(keys ...string) string {
		for _, source := range []map[string]json.RawMessage{sender, fields} {
			for _, key := range keys {
				if value := napcatScalar(source[key]); value != "" {
					return value
				}
			}
		}
		return ""
	}
	node := domain.ForwardedMessage{Sender: domain.Sender{ID: get("user_id", "uin"), Name: get("card", "nickname", "name"), Username: get("username")}}
	if value := napcatScalar(fields["time"]); value != "" {
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
			at := time.Unix(seconds, 0).UTC()
			node.SentAt = &at
		} else if at, err := time.Parse(time.RFC3339Nano, value); err == nil {
			at = at.UTC()
			node.SentAt = &at
		}
	}
	content := fields["message"]
	if len(content) == 0 {
		content = fields["content"]
	}
	node.Parts, _, _ = parseNapcatParts(content, messageID, prefix, depth, remaining)
	return node
}

func napcatScalar(raw json.RawMessage) string {
	var value qqValue
	_ = value.UnmarshalJSON(raw)
	return string(value)
}

func napcatPartCard(raw json.RawMessage) *domain.MessageCard {
	var outer map[string]json.RawMessage
	if json.Unmarshal(raw, &outer) == nil {
		if body, ok := outer["data"]; ok {
			raw = body
		}
	}
	card := &domain.MessageCard{Kind: "json", Raw: append(json.RawMessage(nil), raw...)}
	decoded := bytes.TrimSpace(raw)
	if len(decoded) > 0 && decoded[0] == '"' {
		var text string
		if json.Unmarshal(decoded, &text) == nil {
			decoded = []byte(text)
		}
	}
	var body any
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.UseNumber()
	if decoder.Decode(&body) != nil {
		card.Description = "[Malformed QQ JSON card; original data is retained]"
		return card
	}
	card.Source = napcatCardValue(body, []string{"app", "source"}, 512)
	card.Title = napcatCardValue(body, []string{"title", "prompt"}, 512)
	card.Description = napcatCardValue(body, []string{"description", "desc", "summary"}, 2048)
	card.URL = napcatCardValue(body, []string{"qqdocurl", "jumpUrl", "jump_url", "url"}, 2048)
	if parsed, err := url.Parse(card.URL); err != nil || parsed.User != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		card.URL = ""
	}
	if card.Title == "" && card.Description == "" {
		card.Description = "[QQ JSON card; original data is available through the message resource]"
	}
	return card
}

// This is the exact ARK shape emitted by NapCat's ForwardMsgBuilder. A resid
// found in an arbitrary card is not authority to read a forwarded resource.
func napcatCardForwardID(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return ""
		}
		raw = []byte(text)
	}
	var card struct {
		App  string `json:"app"`
		Meta struct {
			Detail struct {
				ResID string `json:"resid"`
			} `json:"detail"`
		} `json:"meta"`
	}
	if json.Unmarshal(raw, &card) != nil || card.App != "com.tencent.multimsg" || strings.TrimSpace(card.Meta.Detail.ResID) == "" || len(card.Meta.Detail.ResID) > 4096 {
		return ""
	}
	return card.Meta.Detail.ResID
}

func napcatMultimsgPreview(card *domain.MessageCard) {
	raw := bytes.TrimSpace(card.Raw)
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return
		}
		raw = []byte(text)
	}
	var body struct {
		Meta struct {
			Detail struct {
				Source  string `json:"source"`
				Summary string `json:"summary"`
				News    []struct {
					Text string `json:"text"`
				} `json:"news"`
			} `json:"detail"`
		} `json:"meta"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return
	}
	if body.Meta.Detail.Source != "" {
		card.Title = napcatShort(body.Meta.Detail.Source, 512)
	}
	var preview []string
	for _, item := range body.Meta.Detail.News[:min(len(body.Meta.Detail.News), napcatForwardPeek)] {
		preview = append(preview, napcatShort(item.Text, 512))
	}
	if body.Meta.Detail.Summary != "" {
		preview = append(preview, body.Meta.Detail.Summary)
	}
	if len(preview) > 0 {
		card.Description = napcatShort(strings.Join(preview, "\n"), 2048)
	}
}

func napcatCardValue(value any, keys []string, limit int) string {
	for _, wanted := range keys {
		remaining := 256
		var find func(any, int) string
		find = func(value any, depth int) string {
			if depth > napcatPartDepth || remaining == 0 {
				return ""
			}
			remaining--
			switch value := value.(type) {
			case map[string]any:
				if text, ok := value[wanted].(string); ok && strings.TrimSpace(text) != "" {
					return napcatShort(text, limit)
				}
				names := make([]string, 0, len(value))
				for name := range value {
					names = append(names, name)
				}
				slices.Sort(names)
				for _, name := range names {
					if text := find(value[name], depth+1); text != "" {
						return text
					}
				}
			case []any:
				for _, item := range value {
					if text := find(item, depth+1); text != "" {
						return text
					}
				}
			}
			return ""
		}
		if text := find(value, 0); text != "" {
			return text
		}
	}
	return ""
}

func napcatPartSummary(part domain.MessagePart) string {
	if part.Card != nil {
		return "[QQ card: " + strings.Join(slices.DeleteFunc([]string{part.Card.Source, part.Card.Title, part.Card.Description, part.Card.URL}, func(value string) bool { return value == "" }), " | ") + "]"
	}
	if part.Forward != nil {
		var result strings.Builder
		fmt.Fprintf(&result, "[Merged forward: %s", part.Forward.Status)
		if part.Forward.Total > 0 {
			fmt.Fprintf(&result, ", %d messages", part.Forward.Total)
		}
		for _, node := range part.Forward.Messages {
			napcatAppendSummary(&result, "\n"+node.Sender.Name+": ")
			for _, child := range node.Parts {
				napcatAppendSummary(&result, napcatPartSummary(child))
			}
		}
		napcatAppendSummary(&result, "]")
		return result.String()
	}
	if part.Attachment != nil {
		return "[" + part.Attachment.Kind + ": " + part.Attachment.Name + "]"
	}
	return part.Text
}

func napcatAppendSummary(out *strings.Builder, text string) {
	left := napcatSummaryLimit - len([]rune(out.String()))
	if left > 0 {
		out.WriteString(napcatShort(text, left))
	}
}

func napcatShort(text string, limit int) string {
	runes := []rune(text)
	return string(runes[:min(len(runes), limit)])
}
