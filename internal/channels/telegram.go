package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
)

type TelegramConfig struct {
	BaseURL string `json:"base_url,omitempty"`
}
type telegram struct {
	connection domain.Connection
	token      string
	baseURL    string
	client     *http.Client
}
type telegramAPIError struct {
	Method      string
	Code        int
	Description string
}

func (e *telegramAPIError) Error() string {
	return fmt.Sprintf("Telegram %s failed (%d): %s", e.Method, e.Code, e.Description)
}

func NewTelegram(connection domain.Connection, token string, client *http.Client) (Adapter, error) {
	var config TelegramConfig
	if len(connection.Config) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(connection.Config))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return nil, domain.Invalid("invalid Telegram connection config")
		}
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.telegram.org"
	}
	if err := modelbridge.ValidateURL(config.BaseURL); err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsAny(token, "/\r\n?#") {
		return nil, domain.Invalid("Telegram bot token is required")
	}
	if client == nil {
		client = http.DefaultClient
	}
	ownedClient := *client
	// A redirect cannot change the authenticated Bot API endpoint or turn a
	// POST with unknown effect into an implicit second request.
	ownedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &telegram{connection: connection, token: token, baseURL: strings.TrimRight(config.BaseURL, "/"), client: &ownedClient}, nil
}
func (t *telegram) Capabilities() Capabilities {
	return Capabilities{Adapter: "telegram.bot", Platform: "telegram", Name: "Telegram Bot",
		MessageFormats: []string{"text", "markdown", "html"}, MessageParameters: []string{"disable_notification", "as_document"},
		ConversationKinds: []string{"private", "group", "topic", "channel"}, ReplyReferences: true, MessageEdits: true, FileSending: true,
		TextLimit: 4096, CaptionLimit: 1024}
}

func (t *telegram) request(ctx context.Context, method string, body io.Reader, contentType string, result any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/bot"+t.token+"/"+method, body)
	if err != nil {
		return domain.Invalid("invalid Telegram endpoint")
	}
	request.Header.Set("Content-Type", contentType)
	response, err := t.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return domain.Offline("Telegram request could not be completed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return domain.Offline("Telegram response was interrupted")
	}
	if len(raw) > 8<<20 {
		return domain.Invalid("Telegram response exceeded the size limit")
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Code        int             `json:"error_code"`
		Description string          `json:"description"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return domain.Offline(fmt.Sprintf("Telegram returned an invalid response (HTTP %d)", response.StatusCode))
	}
	if !envelope.OK {
		description := strings.ReplaceAll(envelope.Description, t.token, "[redacted]")
		if len(description) > 500 {
			description = description[:500]
		}
		return &telegramAPIError{Method: method, Code: envelope.Code, Description: description}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domain.Offline("Telegram returned inconsistent success status")
	}
	if result != nil && json.Unmarshal(envelope.Result, result) != nil {
		return domain.Invalid("Telegram returned an invalid result")
	}
	return nil
}
func (t *telegram) call(ctx context.Context, method string, body, result any) error {
	return t.request(ctx, method, bytes.NewReader(encoded(body)), "application/json", result)
}

type telegramUser struct {
	ID                      int64  `json:"id"`
	IsBot                   bool   `json:"is_bot"`
	FirstName               string `json:"first_name"`
	LastName                string `json:"last_name"`
	Username                string `json:"username"`
	CanReadAllGroupMessages bool   `json:"can_read_all_group_messages"`
}

func (t *telegram) Validate(ctx context.Context) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var user telegramUser
	if err := t.call(ctx, "getMe", map[string]any{}, &user); err != nil {
		return Identity{}, err
	}
	if !user.IsBot || user.ID == 0 {
		return Identity{}, domain.Invalid("Telegram credentials did not identify a bot")
	}
	var webhook struct {
		URL string `json:"url"`
	}
	if err := t.call(ctx, "getWebhookInfo", map[string]any{}, &webhook); err != nil {
		return Identity{}, err
	}
	if webhook.URL != "" {
		return Identity{}, domain.Conflict("this bot has an active webhook; remove it before using Astral long polling")
	}
	identity := Identity{ID: strconv.FormatInt(user.ID, 10), Username: user.Username, Name: strings.TrimSpace(user.FirstName + " " + user.LastName), Diagnostics: []string{}}
	if !user.CanReadAllGroupMessages {
		identity.Diagnostics = append(identity.Diagnostics, "Telegram privacy mode is enabled. To observe ordinary group messages, disable it in BotFather or grant this bot the required group administrator access.")
	}
	return identity, nil
}

type telegramChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	IsForum   bool   `json:"is_forum"`
}
type telegramEntity struct {
	Type   string        `json:"type"`
	Offset int           `json:"offset"`
	Length int           `json:"length"`
	User   *telegramUser `json:"user"`
}
type telegramFile struct {
	ID       string `json:"file_id"`
	UniqueID string `json:"file_unique_id"`
	Name     string `json:"file_name"`
	MIME     string `json:"mime_type"`
	Size     int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}
type telegramSticker struct {
	telegramFile
	Type          string        `json:"type"`
	IsAnimated    bool          `json:"is_animated"`
	IsVideo       bool          `json:"is_video"`
	Emoji         string        `json:"emoji"`
	SetName       string        `json:"set_name"`
	CustomEmojiID string        `json:"custom_emoji_id"`
	Thumbnail     *telegramFile `json:"thumbnail"`
}

func (s telegramSticker) format() (format, extension, mediaType string) {
	if s.IsVideo {
		return "video", ".webm", "video/webm"
	}
	if s.IsAnimated {
		return "animated", ".tgs", "application/x-tgsticker"
	}
	return "static", ".webp", "image/webp"
}

func (s telegramSticker) preview() string {
	if emoji := strings.TrimSpace(s.Emoji); emoji != "" {
		return "[Sticker " + emoji + "]"
	}
	return "[Sticker]"
}

type telegramMessage struct {
	ID              int64                `json:"message_id"`
	ThreadID        int64                `json:"message_thread_id"`
	IsTopicMessage  bool                 `json:"is_topic_message"`
	Date            int64                `json:"date"`
	EditDate        int64                `json:"edit_date"`
	From            *telegramUser        `json:"from"`
	SenderChat      *telegramChat        `json:"sender_chat"`
	Chat            telegramChat         `json:"chat"`
	Text            string               `json:"text"`
	Caption         string               `json:"caption"`
	RichMessage     *telegramRichMessage `json:"rich_message,omitempty"`
	Entities        []telegramEntity     `json:"entities"`
	CaptionEntities []telegramEntity     `json:"caption_entities"`
	Reply           *telegramMessage     `json:"reply_to_message"`
	Quote           *telegramTextQuote   `json:"quote"`
	Photo           []telegramFile       `json:"photo"`
	Document        *telegramFile        `json:"document"`
	Audio           *telegramFile        `json:"audio"`
	Voice           *telegramFile        `json:"voice"`
	Video           *telegramFile        `json:"video"`
	Animation       *telegramFile        `json:"animation"`
	Sticker         *telegramSticker     `json:"sticker"`
}

type telegramTextQuote struct {
	Text     string `json:"text"`
	Position int    `json:"position"`
	IsManual bool   `json:"is_manual"`
}
type telegramUpdate struct {
	ID                   int64                         `json:"update_id"`
	Message              *telegramMessage              `json:"message"`
	EditedMessage        *telegramMessage              `json:"edited_message"`
	ChannelPost          *telegramMessage              `json:"channel_post"`
	EditedChannelPost    *telegramMessage              `json:"edited_channel_post"`
	MessageReaction      *telegramMessageReaction      `json:"message_reaction"`
	MessageReactionCount *telegramMessageReactionCount `json:"message_reaction_count"`
}

func (t *telegram) Poll(ctx context.Context, cursor string) (Batch, error) {
	var offset int64
	var err error
	if cursor != "" {
		offset, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return Batch{}, domain.Invalid("invalid Telegram polling cursor")
		}
	}
	var updates []telegramUpdate
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	if err = t.call(ctx, "getUpdates", map[string]any{"offset": offset, "limit": 100, "timeout": 25, "allowed_updates": []string{"message", "edited_message", "channel_post", "edited_channel_post", "message_reaction", "message_reaction_count"}}, &updates); err != nil {
		return Batch{}, err
	}
	batch := Batch{NextCursor: cursor}
	for _, update := range updates {
		if update.ID >= offset {
			offset = update.ID + 1
			batch.NextCursor = strconv.FormatInt(offset, 10)
		}
		message, edited := update.Message, false
		if message == nil {
			message = update.EditedMessage
			edited = message != nil
		}
		if message == nil {
			message = update.ChannelPost
		}
		if message == nil {
			message = update.EditedChannelPost
			edited = message != nil
		}
		if message != nil {
			batch.Events = append(batch.Events, t.normalize(update.ID, *message, edited))
		}
		if update.MessageReaction != nil {
			batch.Events = append(batch.Events, t.normalizeReaction(update.ID, *update.MessageReaction)...)
		}
		if update.MessageReactionCount != nil {
			batch.Events = append(batch.Events, t.normalizeReactionCount(update.ID, *update.MessageReactionCount))
		}
	}
	return batch, nil
}
func conversationFromChat(chat telegramChat, topic string) domain.Conversation {
	kind := chat.Type
	if kind == "supergroup" {
		kind = "group"
	}
	if topic != "" {
		kind = "topic"
	}
	title := chat.Title
	if title == "" {
		title = strings.TrimSpace(chat.FirstName + " " + chat.LastName)
	}
	if title == "" {
		title = chat.Username
	}
	return domain.Conversation{ExternalID: strconv.FormatInt(chat.ID, 10), TopicID: topic, Kind: kind, Title: title}
}
func (t *telegram) normalize(updateID int64, message telegramMessage, edited bool) Event {
	topic := ""
	if message.IsTopicMessage && message.ThreadID != 0 {
		topic = strconv.FormatInt(message.ThreadID, 10)
	}
	conversation := conversationFromChat(message.Chat, topic)
	conversation.ConnectionID = t.connection.ID
	conversation.ID = stableID("conversation", t.connection.ID, conversation.ExternalID, topic)
	observation := domain.Observation{SchemaVersion: 1, ID: stableID("event", t.connection.ID, strconv.FormatInt(updateID, 10)), ConnectionID: t.connection.ID, ConversationID: conversation.ID,
		ExternalEventID: strconv.FormatInt(updateID, 10), Kind: "message.created", MessageID: strconv.FormatInt(message.ID, 10), Text: message.Text,
		SentAt: time.Unix(message.Date, 0).UTC(), ReceivedAt: time.Now().UTC()}
	entities := message.Entities
	if message.RichMessage != nil {
		observation.Text = telegramRichBlocks(message.RichMessage.Blocks)
		observation.Format = "rich"
	}
	if observation.Text == "" {
		observation.Text = message.Caption
		entities = message.CaptionEntities
	}
	if edited {
		observation.Kind = "message.edited"
		at := time.Unix(message.EditDate, 0).UTC()
		observation.EditedAt = &at
	}
	if message.From != nil {
		observation.Sender = domain.Sender{ID: strconv.FormatInt(message.From.ID, 10), Name: strings.TrimSpace(message.From.FirstName + " " + message.From.LastName), Username: message.From.Username}
	}
	if message.SenderChat != nil {
		observation.Sender = domain.Sender{ID: "chat:" + strconv.FormatInt(message.SenderChat.ID, 10), Name: message.SenderChat.Title, Username: message.SenderChat.Username}
	}
	observation.Self = observation.Sender.ID == t.connection.AccountID
	if message.Reply != nil {
		observation.ReplyID = strconv.FormatInt(message.Reply.ID, 10)
		if message.Reply.Chat.ID == 0 || message.Reply.Chat.ID == message.Chat.ID {
			observation.ReplyAttachments = telegramAttachments(*message.Reply)
		}
		observation.ReplyPreview = message.Reply.Text
		if observation.ReplyPreview == "" && message.Reply.RichMessage != nil {
			observation.ReplyPreview = telegramRichBlocks(message.Reply.RichMessage.Blocks)
		}
		if observation.ReplyPreview == "" {
			observation.ReplyPreview = message.Reply.Caption
		}
		if observation.ReplyPreview == "" && message.Reply.Sticker != nil {
			observation.ReplyPreview = message.Reply.Sticker.preview()
		}
		if len([]rune(observation.ReplyPreview)) > 500 {
			observation.ReplyPreview = string([]rune(observation.ReplyPreview)[:500])
		}
		if message.Reply.From != nil {
			observation.ReplySender = strings.TrimSpace(message.Reply.From.FirstName + " " + message.Reply.From.LastName)
			if strconv.FormatInt(message.Reply.From.ID, 10) == t.connection.AccountID {
				observation.Trigger = "reply"
			}
		}
	}
	if message.Quote != nil {
		observation.ReplyQuote = &domain.TextQuote{Text: message.Quote.Text, Position: message.Quote.Position, Manual: message.Quote.IsManual}
	}
	for _, entity := range entities {
		if entity.Type == "mention" && t.connection.Username != "" && strings.EqualFold(utf16Substring(observation.Text, entity.Offset, entity.Length), "@"+t.connection.Username) {
			observation.Trigger = "mention"
		}
		if entity.Type == "text_mention" && entity.User != nil && strconv.FormatInt(entity.User.ID, 10) == t.connection.AccountID {
			observation.Trigger = "mention"
		}
	}
	if message.Chat.Type == "private" {
		observation.Trigger = "private"
	}
	if observation.Self {
		observation.Trigger = ""
	}
	observation.Attachments = telegramAttachments(message)
	event := Event{Conversation: conversation, Observation: observation}
	command, username, addressed := strings.Cut(strings.TrimSpace(message.Text), "@")
	if command == "/chat_id" && (!addressed || t.connection.Username != "" && strings.EqualFold(username, t.connection.Username)) {
		event.Command = &Command{Name: "chat_id"}
		// Anonymous administrators can have Telegram's synthetic bot sender;
		// their sender_chat still identifies the human-operated chat identity.
		sentByBot := message.From != nil && (strconv.FormatInt(message.From.ID, 10) == t.connection.AccountID || message.From.IsBot && message.SenderChat == nil)
		if !edited && !observation.Self && !sentByBot {
			event.Command.Text = "Chat ID: " + conversation.ExternalID
			if topic != "" {
				event.Command.Text += "\nTopic ID: " + topic
			}
		}
	}
	return event
}
func telegramAttachments(message telegramMessage) []domain.Attachment {
	observation := domain.Observation{MessageID: strconv.FormatInt(message.ID, 10)}
	add := func(file telegramFile, kind, name, mimeType string) {
		if file.ID == "" {
			return
		}
		if file.Name != "" {
			name = file.Name
		}
		if file.MIME != "" {
			mimeType = file.MIME
		}
		if strings.HasPrefix(mimeType, "image/") {
			kind = "image"
		}
		observation.Attachments = append(observation.Attachments, domain.Attachment{Ref: observation.MessageID + ":" + strconv.Itoa(len(observation.Attachments)), ExternalID: file.ID, Kind: kind, Name: name, MediaType: mimeType, Size: file.Size})
	}
	if len(message.Photo) > 0 {
		largest := message.Photo[0]
		for _, photo := range message.Photo {
			if photo.Width*photo.Height > largest.Width*largest.Height {
				largest = photo
			}
		}
		add(largest, "image", "photo-"+observation.MessageID+".jpg", "image/jpeg")
	}
	if message.Document != nil {
		add(*message.Document, "file", "document-"+observation.MessageID, "application/octet-stream")
	}
	if message.Audio != nil {
		add(*message.Audio, "audio", "audio-"+observation.MessageID+".mp3", "audio/mpeg")
	}
	if message.Voice != nil {
		add(*message.Voice, "audio", "voice-"+observation.MessageID+".ogg", "audio/ogg")
	}
	if message.Video != nil {
		add(*message.Video, "video", "video-"+observation.MessageID+".mp4", "video/mp4")
	}
	if message.Animation != nil {
		add(*message.Animation, "video", "animation-"+observation.MessageID+".mp4", "video/mp4")
	}
	if sticker := message.Sticker; sticker != nil && sticker.ID != "" {
		format, extension, mediaType := sticker.format()
		metadata := &domain.StickerMetadata{
			Type: sticker.Type, Format: format, Emoji: sticker.Emoji, SetName: sticker.SetName,
			UniqueID: sticker.UniqueID, CustomEmojiID: sticker.CustomEmojiID, Width: sticker.Width, Height: sticker.Height,
		}
		observation.Attachments = append(observation.Attachments, domain.Attachment{
			Ref: observation.MessageID + ":" + strconv.Itoa(len(observation.Attachments)), ExternalID: sticker.ID,
			Kind: "sticker", Name: "sticker-" + observation.MessageID + extension, MediaType: mediaType, Size: sticker.Size, Sticker: metadata,
		})
		if thumbnail := sticker.Thumbnail; thumbnail != nil && thumbnail.ID != "" {
			metadata.ThumbnailRef = observation.MessageID + ":" + strconv.Itoa(len(observation.Attachments))
			// PhotoSize has no MIME field. Download learns its JPEG/WebP format
			// from getFile's path or the actual download response.
			observation.Attachments = append(observation.Attachments, domain.Attachment{
				Ref: metadata.ThumbnailRef, ExternalID: thumbnail.ID, Kind: "image", Name: "sticker-thumbnail-" + observation.MessageID, Size: thumbnail.Size,
			})
		}
	}
	return observation.Attachments
}

func utf16Substring(text string, offset, length int) string {
	units := utf16.Encode([]rune(text))
	if offset < 0 || length < 0 || offset > len(units) || length > len(units)-offset {
		return ""
	}
	return string(utf16.Decode(units[offset : offset+length]))
}
func (t *telegram) ResolveConversation(ctx context.Context, id, topic string) (domain.Conversation, error) {
	if id == "" {
		return domain.Conversation{}, domain.Invalid("Telegram chat ID or @username is required")
	}
	if topic != "" {
		number, err := strconv.ParseInt(topic, 10, 64)
		if err != nil || number < 1 {
			return domain.Conversation{}, domain.Invalid("Topic ID must be a positive integer")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var chat telegramChat
	if err := t.call(ctx, "getChat", map[string]any{"chat_id": id}, &chat); err != nil {
		return domain.Conversation{}, err
	}
	if topic != "" && !chat.IsForum {
		return domain.Conversation{}, domain.Invalid("this chat does not expose forum topics")
	}
	result := conversationFromChat(chat, topic)
	result.ConnectionID = t.connection.ID
	result.ID = stableID("conversation", t.connection.ID, result.ExternalID, topic)
	return result, nil
}
func (t *telegram) Download(ctx context.Context, attachment domain.Attachment) (Download, error) {
	var file struct {
		Path string `json:"file_path"`
		Size int64  `json:"file_size"`
	}
	if err := t.call(ctx, "getFile", map[string]any{"file_id": attachment.ExternalID}, &file); err != nil {
		return Download{}, err
	}
	if file.Path == "" || strings.HasPrefix(file.Path, "/") || path.Clean(file.Path) != file.Path || strings.HasPrefix(file.Path, "../") {
		return Download{}, domain.Invalid("Telegram returned an unsupported file path")
	}
	segments := strings.Split(file.Path, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+"/file/bot"+t.token+"/"+strings.Join(segments, "/"), nil)
	if err != nil {
		return Download{}, domain.Invalid("invalid Telegram download URL")
	}
	response, err := t.client.Do(request)
	if err != nil {
		return Download{}, domain.Offline("Telegram attachment download could not start")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return Download{}, domain.Offline(fmt.Sprintf("Telegram attachment download returned HTTP %d", response.StatusCode))
	}
	mediaType := attachment.MediaType
	if mediaType == "" && attachment.Kind == "image" {
		mediaType = telegramThumbnailMediaType(file.Path, response.Header.Get("Content-Type"))
	}
	return Download{Body: response.Body, Size: file.Size, Name: attachment.Name, MediaType: mediaType}, nil
}

func telegramThumbnailMediaType(filePath, contentType string) string {
	var pathType string
	switch strings.ToLower(path.Ext(filePath)) {
	case ".jpg", ".jpeg":
		pathType = "image/jpeg"
	case ".webp":
		pathType = "image/webp"
	}
	responseType, _, _ := mime.ParseMediaType(contentType)
	switch responseType {
	case "image/jpeg", "image/webp":
		if pathType != "" && pathType != responseType {
			return ""
		}
		return responseType
	case "", "application/octet-stream":
		return pathType
	default:
		return ""
	}
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
func (t *telegram) SplitText(text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, domain.Invalid("message text is empty")
	}
	var chunks []string
	runes := []rune(text)
	for len(runes) > 0 {
		length, end, lastBreak := 0, 0, 0
		for end < len(runes) {
			width := 1
			if runes[end] > 0xffff {
				width = 2
			}
			if length+width > t.Capabilities().TextLimit {
				break
			}
			length += width
			end++
			if runes[end-1] == '\n' || runes[end-1] == ' ' {
				lastBreak = end
			}
		}
		if end < len(runes) && lastBreak > end/2 {
			end = lastBreak
		}
		part := string(runes[:end])
		if strings.TrimSpace(part) != "" {
			chunks = append(chunks, part)
		}
		runes = runes[end:]
	}
	return chunks, nil
}
func (t *telegram) Send(ctx context.Context, send Send) (Receipt, error) {
	if len(send.Parts) > 0 {
		var text strings.Builder
		for _, part := range send.Parts {
			if part.Type != "text" {
				return t.sendParts(ctx, send)
			}
			text.WriteString(part.Text)
		}
		send.Text, send.Parts = text.String(), nil
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
		number, err := strconv.ParseInt(send.Target.TopicID, 10, 64)
		if err != nil {
			return Receipt{}, &SendError{Message: "invalid Telegram topic"}
		}
		body["message_thread_id"] = number
	}
	if send.File == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	var message telegramMessage
	var err error
	if send.File == nil {
		if strings.TrimSpace(send.Text) == "" || send.TextFormat() == "text" && utf16Length(send.Text) > t.Capabilities().TextLimit {
			return Receipt{}, &SendError{Message: "Telegram text fragment is empty or too long"}
		}
		if send.TextFormat() == "text" {
			body["text"] = send.Text
			err = t.call(ctx, "sendMessage", body, &message)
		} else {
			body["rich_message"] = map[string]string{send.TextFormat(): send.Text}
			err = t.call(ctx, "sendRichMessage", body, &message)
		}
	} else {
		if utf16Length(send.Text) > t.Capabilities().CaptionLimit {
			return Receipt{}, &SendError{Message: "Telegram caption exceeds the supported limit"}
		}
		method, field := "sendDocument", "document"
		if !send.AsDocument && (send.MediaType == "image/png" || send.MediaType == "image/jpeg" || send.MediaType == "image/webp") {
			method, field = "sendPhoto", "photo"
		}
		body["caption"] = send.Text
		reader, writer := io.Pipe()
		form := multipart.NewWriter(writer)
		done := make(chan error, 1)
		go func() {
			var writeErr error
			for name, value := range body {
				if name == "reply_parameters" {
					value = string(encoded(value))
				}
				if writeErr = form.WriteField(name, fmt.Sprint(value)); writeErr != nil {
					break
				}
			}
			if writeErr == nil {
				var part io.Writer
				// Telegram requires the literal filename parameter. FormatMediaType
				// emits filename*= for Unicode names, which Telegram can mistake
				// for a text field containing a file URL.
				part, writeErr = form.CreateFormFile(field, send.FileName)
				if writeErr == nil {
					_, writeErr = io.CopyBuffer(part, send.File, make([]byte, 32<<10))
				}
			}
			if writeErr == nil {
				writeErr = form.Close()
			}
			_ = writer.CloseWithError(writeErr)
			done <- writeErr
		}()
		stopClose := context.AfterFunc(ctx, func() {
			_ = reader.CloseWithError(ctx.Err())
			if closer, ok := send.File.(io.Closer); ok {
				_ = closer.Close()
			}
		})
		err = t.request(ctx, method, reader, form.FormDataContentType(), &message)
		_ = reader.CloseWithError(err)
		// An early platform response also ends the source stream. In particular
		// a remote source must not keep a cancelled multipart producer blocked.
		if closer, ok := send.File.(io.Closer); ok {
			_ = closer.Close()
		}
		stopClose()
		copyErr := <-done
		if err != nil || message.ID == 0 {
			err = errors.Join(err, copyErr)
		}
	}
	if err != nil {
		var known *telegramAPIError
		if errors.As(err, &known) {
			return Receipt{}, &SendError{Message: known.Error()}
		}
		return Receipt{}, &SendError{Message: "Telegram message request did not complete", Unknown: true}
	}
	if message.ID == 0 {
		return Receipt{}, &SendError{Message: "Telegram accepted the request without a usable message receipt", Unknown: true}
	}
	return Receipt{MessageIDs: []string{strconv.FormatInt(message.ID, 10)}}, nil
}
