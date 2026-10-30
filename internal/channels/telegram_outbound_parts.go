package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func (t *telegram) sendParts(ctx context.Context, send Send) (Receipt, error) {
	hasMedia := false
	for _, part := range send.Parts {
		if part.Type == "image" || part.Type == "file" {
			hasMedia = true
			break
		}
	}
	if !hasMedia {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	body := map[string]any{"chat_id": send.Target.ExternalID}
	if send.DisableNotification {
		body["disable_notification"] = true
	}
	if send.ReplyTo != "" {
		id, err := strconv.ParseInt(send.ReplyTo, 10, 64)
		if err != nil || id < 1 {
			return Receipt{}, &SendError{Message: "invalid Telegram reply message ID"}
		}
		body["reply_parameters"] = map[string]any{"message_id": id}
	}
	if send.Target.TopicID != "" {
		id, err := strconv.ParseInt(send.Target.TopicID, 10, 64)
		if err != nil || id < 1 {
			return Receipt{}, &SendError{Message: "invalid Telegram topic"}
		}
		body["message_thread_id"] = id
	}
	format := send.TextFormat()
	if format == "text" {
		format = "html"
	}
	var content strings.Builder
	media := []map[string]any{}
	files := map[string]Download{}
	closeFiles := func() {
		for _, f := range files {
			_ = f.Body.Close()
		}
	}
	defer closeFiles()
	for i, p := range send.Parts {
		switch p.Type {
		case "text":
			text := p.Text
			if send.TextFormat() == "text" {
				text = strings.ReplaceAll(html.EscapeString(text), "\n", "<br>")
			}
			content.WriteString(text)
		case "mention":
			id, err := strconv.ParseInt(p.UserID, 10, 64)
			if err != nil || id < 1 {
				return Receipt{}, &SendError{Message: "invalid Telegram mention user ID"}
			}
			name := p.Name
			if name == "" {
				name = p.UserID
			}
			if !strings.HasPrefix(name, "@") {
				name = "@" + name
			}
			if format == "markdown" {
				name = escapeRichMarkdownLabel(name)
				content.WriteString("[" + name + "](tg://user?id=" + p.UserID + ")")
			} else {
				content.WriteString(`<a href="tg://user?id=` + p.UserID + `">` + html.EscapeString(name) + `</a>`)
			}
		case "image", "file":
			if p.OpenFile == nil {
				return Receipt{}, &SendError{Message: "Telegram media source is unavailable"}
			}
			if len(media) >= 50 {
				return Receipt{}, &SendError{Message: "Telegram rich messages support at most 50 media attachments"}
			}
			id := fmt.Sprintf("part_%d", i)
			kind := "document"
			if p.Type == "image" && !send.AsDocument {
				kind = "photo"
			}
			stream, err := p.OpenFile(ctx)
			if err != nil {
				return Receipt{}, &SendError{Message: "Telegram media source could not be opened: " + err.Error()}
			}
			files[id] = Download{Body: stream, Name: p.FileName, MediaType: p.MediaType, Size: p.Size}
			media = append(media, map[string]any{"id": id, "media": map[string]any{"type": kind, "media": "attach://" + id}})
			// Media are separate native blocks; only their required block boundaries
			// are generated. Text/mention adjacency has no inserted whitespace.
			if format == "markdown" {
				content.WriteString("\n\n![](tg://" + kind + "?id=" + id + ")\n\n")
			} else {
				if kind == "photo" {
					content.WriteString(`<img src="tg://photo?id=` + id + `"/>`)
				} else {
					content.WriteString(`<tg-document src="tg://document?id=` + id + `"></tg-document>`)
				}
			}
		default:
			return Receipt{}, &SendError{Message: "unsupported Telegram message part"}
		}
	}
	if utf8.RuneCountInString(content.String()) > 32768 {
		return Receipt{}, &SendError{Message: "Telegram rich message exceeds the supported text limit"}
	}
	rich := map[string]any{format: content.String()}
	if len(media) > 0 {
		rich["media"] = media
	}
	body["rich_message"] = rich
	var raw json.RawMessage
	err := t.operation(ctx, "sendRichMessage", body, files, &raw)
	if err != nil {
		var known *telegramAPIError
		if errors.As(err, &known) {
			return Receipt{}, &SendError{Message: known.Error()}
		}
		return Receipt{}, &SendError{Message: "Telegram rich message request did not complete", Unknown: true}
	}
	var message telegramMessage
	if json.Unmarshal(raw, &message) != nil || message.ID == 0 {
		return Receipt{}, &SendError{Message: "Telegram accepted the request without a usable message receipt", Unknown: true}
	}
	return Receipt{MessageIDs: []string{strconv.FormatInt(message.ID, 10)}}, nil
}

func escapeRichMarkdownLabel(value string) string {
	var out strings.Builder
	for _, r := range value {
		if strings.ContainsRune(`\[]()_*~`+"`"+`<>|=`, r) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	return out.String()
}
