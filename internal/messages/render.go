// Package messages renders external input as escaped User-role content.
package messages

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
)

// IMReplyReminder accompanies each direct trigger and proactive wakeup.
const IMReplyReminder = "Direct assistant text is not sent to IM. To reply to this chat, you must call send_message."

func Escape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

type Metadata struct {
	Source         string              `json:"source"`
	DeliveryID     string              `json:"delivery_id"`
	EventID        string              `json:"event_id,omitempty"`
	ConversationID string              `json:"conversation_id,omitempty"`
	DisplayText    string              `json:"display_text"`
	Sender         domain.Sender       `json:"sender"`
	Files          []domain.FileRef    `json:"files,omitempty"`
	Attachments    []domain.Attachment `json:"attachments,omitempty"`
}

func AttachmentDownloadPath(conversationID, messageID, ref string) string {
	return "/v1/conversations/" + url.PathEscape(conversationID) + "/messages/" + url.PathEscape(messageID) + "/attachments/" + url.PathEscape(ref) + "/content"
}

// AttachmentParts exposes original paths and stable URIs in text while adding
// native image blocks separately. Descriptive XML never substitutes for pixels.
func AttachmentParts(files []domain.FileRef) (string, []core.Content) {
	var text strings.Builder
	var images []core.Content
	for _, file := range files {
		kind := "file"
		if strings.HasPrefix(file.MediaType, "image/") {
			kind = "image"
		}
		fmt.Fprintf(&text, "\n<attachment type=\"%s\" name=\"%s\" mime=\"%s\" uri=\"%s\" computer-id=\"%s\" path=\"%s\" size=\"%d\"/>",
			kind, Escape(file.DisplayName), Escape(file.MediaType), Escape(filestream.URL(file)), Escape(file.ComputerID), Escape(file.Path), file.Size)
		if kind == "image" {
			images = append(images, core.Content{Type: "image", MediaType: file.MediaType, URL: filestream.URL(file)})
		}
	}
	return text.String(), images
}

func Web(id, text, owner string, at time.Time, files []domain.FileRef) core.Message {
	attachments, images := AttachmentParts(files)
	body := fmt.Sprintf("<message id=\"%s\" source=\"web\" sender=\"%s\" sender-id=\"owner\" t=\"%s\">\n%s%s\n</message>",
		Escape(id), Escape(owner), at.Format(time.RFC3339Nano), Escape(text), attachments)
	metadata, _ := json.Marshal(Metadata{Source: "web", DeliveryID: id, DisplayText: text, Sender: domain.Sender{ID: "owner", Name: owner}, Files: files})
	return core.Message{Role: core.RoleUser, Content: append(core.Text(body), images...), Metadata: metadata}
}

func Observation(event domain.Observation, connection domain.Connection, conversation domain.Conversation, files []domain.FileRef, mediaError string) core.Message {
	var body strings.Builder
	fmt.Fprintf(&body, "current-channel: %s\nconnection-id: %s\nconversation-id: %s\nchat-type: %s\nchat-title: %s\nchat-id: %s\n",
		connection.Platform, connection.ID, conversation.ID, conversation.Kind, Escape(conversation.Title), Escape(conversation.ExternalID))
	if conversation.TopicID != "" {
		fmt.Fprintf(&body, "topic-id: %s\n", Escape(conversation.TopicID))
	}
	sender := event.Sender.Name
	if event.Sender.Username != "" {
		sender += " (@" + event.Sender.Username + ")"
	}
	if event.Kind == "message.deleted" || event.Kind == "notice.poke" {
		fmt.Fprintf(&body, "<event id=\"%s\" kind=\"%s\" source=\"im\" sender=\"%s\" sender-id=\"%s\" t=\"%s\"",
			Escape(event.ID), Escape(event.Kind), Escape(sender), Escape(event.Sender.ID), event.SentAt.Format(time.RFC3339Nano))
		if event.MessageID != "" {
			fmt.Fprintf(&body, " message-id=\"%s\"", Escape(event.MessageID))
		}
		text := event.Text
		if text == "" && event.Kind == "message.deleted" {
			text = "The platform reported that this message was deleted."
		}
		fmt.Fprintf(&body, ">%s</event>\n", Escape(text))
	} else {
		fmt.Fprintf(&body, "<message id=\"%s\" event-id=\"%s\" source=\"im\" sender=\"%s\" sender-id=\"%s\" t=\"%s\"",
			Escape(event.MessageID), Escape(event.ID), Escape(sender), Escape(event.Sender.ID), event.SentAt.Format(time.RFC3339Nano))
		if event.EditedAt != nil {
			fmt.Fprintf(&body, " edited=\"%s\"", event.EditedAt.Format(time.RFC3339Nano))
		}
		body.WriteString(">\n")
		if event.ReplyID != "" {
			fmt.Fprintf(&body, "<in-reply-to id=\"%s\" sender=\"%s\">%s", Escape(event.ReplyID), Escape(event.ReplySender), Escape(event.ReplyPreview))
			for _, attachment := range event.ReplyAttachments {
				fmt.Fprintf(&body, "\n<attachment type=\"%s\" name=\"%s\" mime=\"%s\" message-id=\"%s\" file-id=\"%s\" size=\"%d\" download-url=\"%s\"/>",
					Escape(attachment.Kind), Escape(attachment.Name), Escape(attachment.MediaType), Escape(event.ReplyID), Escape(attachment.Ref), attachment.Size,
					Escape(AttachmentDownloadPath(conversation.ID, event.ReplyID, attachment.Ref)))
			}
			body.WriteString("</in-reply-to>\n")
		}
		if quote := event.ReplyQuote; quote != nil {
			fmt.Fprintf(&body, "<quote position-utf16=\"%d\" manual=\"%t\">%s</quote>\n", quote.Position, quote.Manual, Escape(quote.Text))
		}
		body.WriteString(Escape(event.Text))
		for _, a := range event.Attachments {
			fmt.Fprintf(&body, "\n<attachment type=\"%s\" name=\"%s\" mime=\"%s\" file-id=\"%s\" size=\"%d\" download-url=\"%s\"", Escape(a.Kind), Escape(a.Name), Escape(a.MediaType), Escape(a.Ref), a.Size, Escape(AttachmentDownloadPath(conversation.ID, event.MessageID, a.Ref)))
			if sticker := a.Sticker; sticker != nil {
				fmt.Fprintf(&body, " platform-file-id=\"%s\" sticker-type=\"%s\" sticker-format=\"%s\" width=\"%d\" height=\"%d\"", Escape(a.ExternalID), Escape(sticker.Type), Escape(sticker.Format), sticker.Width, sticker.Height)
				for _, attribute := range []struct{ name, value string }{{"emoji", sticker.Emoji}, {"set-name", sticker.SetName}, {"file-unique-id", sticker.UniqueID}, {"custom-emoji-id", sticker.CustomEmojiID}, {"thumbnail-ref", sticker.ThumbnailRef}} {
					if attribute.value != "" {
						fmt.Fprintf(&body, " %s=\"%s\"", attribute.name, Escape(attribute.value))
					}
				}
			}
			for _, original := range event.Attachments {
				if original.Sticker != nil && original.Sticker.ThumbnailRef == a.Ref {
					fmt.Fprintf(&body, " preview-of=\"%s\" preview-only=\"true\"", Escape(original.Ref))
					break
				}
			}
			body.WriteString("/>")
		}
		local, _ := AttachmentParts(files)
		body.WriteString(local)
		if mediaError != "" {
			fmt.Fprintf(&body, "\n<attachment-unavailable>%s</attachment-unavailable>", Escape(mediaError))
		}
		body.WriteString("\n</message>")
	}
	_, images := AttachmentParts(files)
	metadata, _ := json.Marshal(Metadata{Source: "im", DeliveryID: event.ID, EventID: event.ID, ConversationID: conversation.ID, DisplayText: observationDisplayText(event), Sender: event.Sender, Files: files, Attachments: event.Attachments})
	return core.Message{Role: core.RoleUser, ContextKind: "im.observation", Content: append(core.Text(body.String()), images...), Metadata: metadata}
}

func observationDisplayText(event domain.Observation) string {
	if event.Text != "" || event.Kind == "message.deleted" {
		return event.Text
	}
	var labels []string
	for _, attachment := range event.Attachments {
		if sticker := attachment.Sticker; sticker != nil {
			label := "Sticker"
			switch sticker.Format {
			case "animated":
				label = "Animated sticker"
			case "video":
				label = "Video sticker"
			}
			if sticker.Emoji != "" {
				label += " " + sticker.Emoji
			}
			labels = append(labels, "["+label+"]")
		}
	}
	return strings.Join(labels, "\n")
}

func Trigger(event domain.Observation) core.Message {
	messageRef := ""
	if event.MessageID != "" {
		messageRef = fmt.Sprintf(" message-id=\"%s\"", Escape(event.MessageID))
	}
	body := fmt.Sprintf("<im_trigger%s event-id=\"%s\" reason=\"%s\"/>\nProcess the referenced observation.\n%s", messageRef, Escape(event.ID), Escape(event.Trigger), IMReplyReminder)
	metadata, _ := json.Marshal(Metadata{Source: "im.trigger", DeliveryID: event.ID + ":trigger", EventID: event.ID, ConversationID: event.ConversationID, Sender: event.Sender})
	return core.Message{Role: core.RoleUser, Content: core.Text(body), Metadata: metadata}
}
