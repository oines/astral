package messages

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
)

type RichPreviewOptions struct {
	MaxDepth, MaxMessages, MaxTextChars, MaxImages int
	IncludeRootImages                              bool
}

type RichPreviewImage struct {
	Ref        string
	Attachment domain.Attachment
	MessageID  string
}

type RichPreview struct {
	Text      string
	Images    []RichPreviewImage
	Truncated bool
}

var partRefPattern = regexp.MustCompile(`^p[0-9]+(?:\.m[0-9]+\.p[0-9]+)*$`)

func MessagePartReadPath(conversationID, messageID, ref string) string {
	return MessagePartsReadPath(conversationID, messageID) + "/" + url.PathEscape(ref)
}

func MessagePartsReadPath(conversationID, messageID string) string {
	return MessageReadPath(conversationID, messageID) + "/parts"
}

func MessageReadPath(conversationID, messageID string) string {
	return "/v1/conversations/" + url.PathEscape(conversationID) + "/messages/" + url.PathEscape(messageID)
}

func MessagePartContentPath(conversationID, messageID, ref string) string {
	return MessagePartReadPath(conversationID, messageID, ref) + "/content"
}

// RichPartsPreview renders a bounded, ordered quotation. The image selection is
// produced by the same traversal so omitted forward nodes never acquire pixels.
// Root images belong to the existing Observation.Attachments pipeline.
func RichPartsPreview(parts []domain.MessagePart, conversationID, messageID string, location *time.Location, options ...RichPreviewOptions) RichPreview {
	opts := RichPreviewOptions{MaxDepth: 2, MaxMessages: 12, MaxTextChars: 12000, MaxImages: 4}
	if len(options) != 0 {
		provided := options[0]
		if provided.MaxDepth > 0 {
			opts.MaxDepth = provided.MaxDepth
		}
		if provided.MaxMessages > 0 {
			opts.MaxMessages = provided.MaxMessages
		}
		if provided.MaxTextChars > 0 {
			opts.MaxTextChars = provided.MaxTextChars
		}
		if provided.MaxImages > 0 {
			opts.MaxImages = provided.MaxImages
		}
		opts.IncludeRootImages = provided.IncludeRootImages
	}
	if location == nil {
		location = time.UTC
	}
	w := richWriter{options: opts, conversationID: conversationID, messageID: messageID, location: location, textLeft: opts.MaxTextChars, partsLeft: 256}
	w.parts(parts, "", 0)
	w.result.Text = w.body.String()
	return w.result
}

type richWriter struct {
	options                       RichPreviewOptions
	conversationID, messageID     string
	location                      *time.Location
	textLeft, partsLeft, messages int
	textTruncated                 bool
	body                          strings.Builder
	result                        RichPreview
}

func (w *richWriter) escaped(value string) string {
	if count := utf8.RuneCountInString(value); count > w.textLeft {
		value = string([]rune(value)[:w.textLeft])
		w.textLeft = 0
		w.textTruncated = true
		w.result.Truncated = true
	} else {
		w.textLeft -= count
	}
	return Escape(value)
}

func (w *richWriter) path(ref string) string {
	return MessagePartReadPath(w.conversationID, w.messageID, ref)
}

func (w *richWriter) omitted(reason, ref string, count int) {
	w.result.Truncated = true
	fmt.Fprintf(&w.body, "\n<omitted reason=\"%s\" read-url=\"%s\"", Escape(reason), Escape(w.path(ref)))
	if count > 0 {
		fmt.Fprintf(&w.body, " count=\"%d\"", count)
	}
	w.body.WriteString("/>")
}

func (w *richWriter) parts(parts []domain.MessagePart, prefix string, depth int) {
	for index, part := range parts {
		ref := part.Ref
		if !partRefPattern.MatchString(ref) {
			ref = fmt.Sprintf("%sp%d", prefix, index)
		}
		if w.partsLeft == 0 {
			w.omitted("part-budget", ref, len(parts)-index)
			return
		}
		w.partsLeft--
		if w.textLeft == 0 {
			// Still expose the first omitted resource, including a forward's own
			// status and read path, rather than silently dropping the remainder.
			if part.Type == "forward" && part.Forward != nil {
				w.forward(part, ref, depth, "text-budget")
			} else {
				w.omitted("text-budget", ref, len(parts)-index)
			}
			return
		}
		switch part.Type {
		case "text":
			fmt.Fprintf(&w.body, "\n<text ref=\"%s\">%s</text>", Escape(ref), w.escaped(part.Text))
		case "card":
			w.card(part.Card, ref)
		case "attachment":
			w.attachment(part.Attachment, ref, depth)
		case "forward":
			w.forward(part, ref, depth, "")
		default:
			fmt.Fprintf(&w.body, "\n<unsupported ref=\"%s\" read-url=\"%s\">%s</unsupported>", Escape(ref), Escape(w.path(ref)), w.escaped(part.Text))
		}
		if w.textLeft == 0 && (w.textTruncated || index+1 < len(parts)) {
			w.omitted("text-budget", ref, len(parts)-index-1)
			return
		}
	}
}

func safeCardURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || (strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https") {
		return ""
	}
	return parsed.String()
}

func (w *richWriter) card(card *domain.MessageCard, ref string) {
	if card == nil {
		return
	}
	fmt.Fprintf(&w.body, "\n<card ref=\"%s\" kind=\"%s\" read-url=\"%s\">", Escape(ref), w.escaped(card.Kind), Escape(w.path(ref)))
	for _, field := range []struct{ tag, value string }{{"source", card.Source}, {"title", card.Title}, {"description", card.Description}} {
		if field.value != "" {
			fmt.Fprintf(&w.body, "\n<%s>%s</%s>", field.tag, w.escaped(field.value), field.tag)
		}
	}
	if address := safeCardURL(card.URL); address != "" {
		if utf8.RuneCountInString(address) <= w.textLeft {
			fmt.Fprintf(&w.body, "\n<url>%s</url>", w.escaped(address))
		} else {
			w.omitted("text-budget", ref, 0)
		}
	}
	w.body.WriteString("\n</card>")
}

func (w *richWriter) attachment(attachment *domain.Attachment, ref string, depth int) {
	if attachment == nil {
		return
	}
	fmt.Fprintf(&w.body, "\n<attachment ref=\"%s\" type=\"%s\" name=\"%s\" mime=\"%s\" size=\"%d\" read-url=\"%s\" download-url=\"%s\"", Escape(ref), w.escaped(attachment.Kind), w.escaped(attachment.Name), w.escaped(attachment.MediaType), attachment.Size, Escape(w.path(ref)), Escape(MessagePartContentPath(w.conversationID, w.messageID, ref)))
	image := attachment.Kind == "image" || strings.HasPrefix(attachment.MediaType, "image/")
	if image && (depth > 0 || w.options.IncludeRootImages) {
		if len(w.result.Images) < w.options.MaxImages {
			w.result.Images = append(w.result.Images, RichPreviewImage{Ref: ref, Attachment: *attachment, MessageID: w.messageID})
		} else {
			w.result.Truncated = true
			w.body.WriteString(` preview="omitted" preview-reason="image-budget"`)
		}
	}
	w.body.WriteString("/>")
}

func (w *richWriter) forward(part domain.MessagePart, ref string, depth int, forcedReason string) {
	forward := part.Forward
	if forward == nil {
		w.omitted("unavailable", ref, 0)
		return
	}
	status := forward.Status
	if status == "" {
		status = "inline"
	}
	fmt.Fprintf(&w.body, "\n<forward ref=\"%s\" status=\"%s\" total=\"%d\" read-url=\"%s\">", Escape(ref), Escape(status), forward.Total, Escape(w.path(ref)))
	if forcedReason != "" {
		w.omitted(forcedReason, ref, len(forward.Messages))
		w.body.WriteString("\n</forward>")
		return
	}
	if part.Text != "" {
		fmt.Fprintf(&w.body, "\n<summary>%s</summary>", w.escaped(part.Text))
	}
	w.card(part.Card, ref)
	if depth >= w.options.MaxDepth {
		w.omitted("depth-budget", ref, len(forward.Messages))
		w.body.WriteString("\n</forward>")
		return
	}
	for index, child := range forward.Messages {
		if w.messages >= w.options.MaxMessages {
			w.omitted("message-budget", ref, len(forward.Messages)-index)
			break
		}
		if w.textLeft == 0 {
			w.omitted("text-budget", ref, len(forward.Messages)-index)
			break
		}
		w.messages++
		fmt.Fprintf(&w.body, "\n<forwarded-message index=\"%d\" sender=\"%s\" sender-id=\"%s\"", index, w.escaped(child.Sender.Name), w.escaped(child.Sender.ID))
		if child.Sender.Username != "" {
			fmt.Fprintf(&w.body, " username=\"%s\"", w.escaped(child.Sender.Username))
		}
		if child.SentAt != nil && !child.SentAt.IsZero() {
			fmt.Fprintf(&w.body, " t=\"%s\"", child.SentAt.In(w.location).Format(time.RFC3339Nano))
		}
		w.body.WriteString(">")
		w.parts(child.Parts, fmt.Sprintf("%s.m%d.", ref, index), depth+1)
		w.body.WriteString("\n</forwarded-message>")
	}
	if status != "inline" || forward.Total > len(forward.Messages) {
		reason := status
		if reason == "inline" {
			reason = "source-truncated"
		}
		w.omitted(reason, ref, forward.Total-len(forward.Messages))
	}
	w.body.WriteString("\n</forward>")
}
