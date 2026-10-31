package channels

import (
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

type telegramMessageOrigin struct {
	Type            string        `json:"type"`
	Date            int64         `json:"date"`
	SenderUser      *telegramUser `json:"sender_user"`
	SenderUserName  string        `json:"sender_user_name"`
	SenderChat      *telegramChat `json:"sender_chat"`
	Chat            *telegramChat `json:"chat"`
	MessageID       int64         `json:"message_id"`
	AuthorSignature string        `json:"author_signature"`
}

func telegramForwardOrigin(source *telegramMessageOrigin) *domain.ForwardOrigin {
	if source == nil {
		return nil
	}
	origin := &domain.ForwardOrigin{Type: source.Type}
	switch source.Type {
	case "user":
		if source.SenderUser == nil {
			return nil
		}
		user := source.SenderUser
		origin.Sender = domain.Sender{Name: strings.TrimSpace(user.FirstName + " " + user.LastName), Username: user.Username}
		if user.ID != 0 {
			origin.Sender.ID = strconv.FormatInt(user.ID, 10)
		}
	case "hidden_user":
		origin.Sender.Name = source.SenderUserName
	case "chat", "channel":
		chat := source.SenderChat
		if source.Type == "channel" {
			chat = source.Chat
			if source.MessageID != 0 {
				origin.MessageID = strconv.FormatInt(source.MessageID, 10)
			}
		}
		if chat == nil {
			return nil
		}
		origin.Sender = domain.Sender{Name: conversationFromChat(*chat, "").Title, Username: chat.Username}
		if chat.ID != 0 {
			origin.Sender.ID = "chat:" + strconv.FormatInt(chat.ID, 10)
		}
		origin.AuthorSignature = source.AuthorSignature
	default:
		return nil
	}
	if source.Date > 0 {
		at := time.Unix(source.Date, 0).UTC()
		origin.SentAt = &at
	}
	return origin
}
