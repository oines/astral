// Package channels owns platform-neutral inbox delivery and outbound receipts.
// Platform wire types belong exclusively to their Adapter implementations.
package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/oines/astral/internal/domain"
)

type Capabilities struct {
	Adapter                string   `json:"adapter"`
	Platform               string   `json:"platform"`
	Name                   string   `json:"name"`
	ConversationKinds      []string `json:"conversation_kinds"`
	EnumerateConversations bool     `json:"enumerate_conversations"`
	ReplyReferences        bool     `json:"reply_references"`
	MessageEdits           bool     `json:"message_edits"`
	DeletionEvents         bool     `json:"deletion_events"`
	FileSending            bool     `json:"file_sending"`
	DeliveryReceipts       bool     `json:"delivery_receipts"`
	DownloadLimit          int64    `json:"download_limit"`
	UploadLimit            int64    `json:"upload_limit"`
	TextLimit              int      `json:"text_limit"`
	CaptionLimit           int      `json:"caption_limit"`
}
type Identity struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	Name        string   `json:"name"`
	Diagnostics []string `json:"diagnostics"`
}
type Event struct {
	Conversation domain.Conversation
	Observation  domain.Observation
	Command      *Command
}

// Command is a host-handled utility request. A blank reply consumes the request
// without responding (for example, edits or messages sent by another bot).
type Command struct {
	Name string
	Text string
}
type Batch struct {
	Events     []Event
	NextCursor string
}
type Download struct {
	Body io.ReadCloser
	Size int64
	Name string
}
type Send struct {
	Target    domain.Conversation
	Text      string
	ReplyTo   string
	File      io.ReadCloser
	FileName  string
	MediaType string
	Size      int64
}
type Receipt struct{ MessageIDs []string }
type SendError struct {
	Message   string
	Unknown   bool
	Retryable bool
}

func (e *SendError) Error() string { return e.Message }

type Adapter interface {
	Capabilities() Capabilities
	Validate(context.Context) (Identity, error)
	Poll(context.Context, string) (Batch, error)
	ResolveConversation(context.Context, string, string) (domain.Conversation, error)
	Download(context.Context, domain.Attachment) (Download, error)
	SplitText(string) ([]string, error)
	Send(context.Context, Send) (Receipt, error)
}
type Factory func(domain.Connection, string, *http.Client) (Adapter, error)
type Registry map[string]Factory

func DefaultRegistry() Registry {
	return Registry{"telegram.bot": NewTelegram, "qq.bot": NewQQBot, "qq.napcat": NewNapCat, "weixin.ilink": NewWeixin, "feishu.bot": NewFeishu}
}

func stableID(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(hash.Sum(nil)[:16])
}
func encoded(value any) json.RawMessage { raw, _ := json.Marshal(value); return raw }
