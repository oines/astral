// Package messages renders external input as escaped User-role content.
package messages

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/filestream"
)

func Escape(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

type Metadata struct {
	Source         string           `json:"source"`
	DeliveryID     string           `json:"delivery_id"`
	EventID        string           `json:"event_id,omitempty"`
	ConversationID string           `json:"conversation_id,omitempty"`
	DisplayText    string           `json:"display_text"`
	Sender         domain.Sender    `json:"sender"`
	Files          []domain.FileRef `json:"files,omitempty"`
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
	fmt.Fprintf(&body, "<message id=\"%s\" event-id=\"%s\" source=\"im\" sender=\"%s\" sender-id=\"%s\" t=\"%s\"",
		Escape(event.MessageID), Escape(event.ID), Escape(sender), Escape(event.Sender.ID), event.SentAt.Format(time.RFC3339Nano))
	if event.EditedAt != nil {
		fmt.Fprintf(&body, " edited=\"%s\"", event.EditedAt.Format(time.RFC3339Nano))
	}
	if event.Kind == "message.deleted" {
		body.WriteString(" deleted=\"true\"/>\n")
	} else {
		body.WriteString(">\n")
		if event.ReplyID != "" {
			fmt.Fprintf(&body, "<in-reply-to id=\"%s\" sender=\"%s\">%s</in-reply-to>\n", Escape(event.ReplyID), Escape(event.ReplySender), Escape(event.ReplyPreview))
		}
		body.WriteString(Escape(event.Text))
		for _, a := range event.Attachments {
			fmt.Fprintf(&body, "\n<attachment type=\"%s\" name=\"%s\" mime=\"%s\" file-id=\"%s\"/>", Escape(a.Kind), Escape(a.Name), Escape(a.MediaType), Escape(a.Ref))
		}
		local, _ := AttachmentParts(files)
		body.WriteString(local)
		if mediaError != "" {
			fmt.Fprintf(&body, "\n<attachment-unavailable>%s</attachment-unavailable>", Escape(mediaError))
		}
		body.WriteString("\n</message>")
	}
	_, images := AttachmentParts(files)
	metadata, _ := json.Marshal(Metadata{Source: "im", DeliveryID: event.ID, EventID: event.ID, ConversationID: conversation.ID, DisplayText: event.Text, Sender: event.Sender, Files: files})
	return core.Message{Role: core.RoleUser, ContextKind: "im.observation", Content: append(core.Text(body.String()), images...), Metadata: metadata}
}

func Trigger(event domain.Observation) core.Message {
	body := fmt.Sprintf("<im_trigger message-id=\"%s\" event-id=\"%s\" reason=\"%s\"/>\nProcess the referenced observation.", Escape(event.MessageID), Escape(event.ID), Escape(event.Trigger))
	metadata, _ := json.Marshal(Metadata{Source: "im.trigger", DeliveryID: event.ID + ":trigger", EventID: event.ID, ConversationID: event.ConversationID, Sender: event.Sender})
	return core.Message{Role: core.RoleUser, Content: core.Text(body), Metadata: metadata}
}
