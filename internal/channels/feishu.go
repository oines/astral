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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
)

// FeishuConfig is for a tenant's self-built application Bot, with event delivery
// configured as a long connection. The App Secret remains in the credential store.
type FeishuConfig struct {
	AppID   string `json:"app_id"`
	BaseURL string `json:"base_url,omitempty"`
}

type feishu struct {
	connection domain.Connection
	config     FeishuConfig
	secret     string
	client     *http.Client
	tokenMu    sync.Mutex
	token      string
	tokenUntil time.Time
	pollMu     sync.Mutex
	stream     *feishuStream
	chatMu     sync.Mutex
	chatNames  map[string]string
}

type feishuAPIError struct {
	Code int
	Text string
}

func (e *feishuAPIError) Error() string {
	return fmt.Sprintf("Feishu request failed (%d): %s", e.Code, e.Text)
}

func NewFeishu(connection domain.Connection, secret string, client *http.Client) (Adapter, error) {
	var config FeishuConfig
	decoder := json.NewDecoder(bytes.NewReader(connection.Config))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || !feishuID(config.AppID) || strings.TrimSpace(secret) == "" || strings.ContainsAny(secret, "\r\n") {
		return nil, domain.Invalid("Feishu App ID and App Secret are required")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, domain.Invalid("invalid Feishu connection config")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://open.feishu.cn"
	}
	if err := modelbridge.ValidateURL(config.BaseURL); err != nil {
		return nil, err
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	if client == nil {
		client = http.DefaultClient
	}
	owned := *client
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &feishu{connection: connection, config: config, secret: secret, client: &owned, chatNames: map[string]string{}}, nil
}

func feishuID(value string) bool {
	if value == "" || len(value) > 512 {
		return false
	}
	for _, r := range value {
		allowed := r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r == '_' || r == '-'
		if !allowed {
			return false
		}
	}
	return true
}

func (f *feishu) Capabilities() Capabilities {
	// Text fragments stay safely under Feishu's 150 KB JSON request limit,
	// including JSON's two layers of escaping and all addressing fields.
	return Capabilities{Adapter: "feishu.bot", Platform: "feishu", Name: "飞书 Bot",
		MessageFormats: []string{"text", "markdown"}, MessageParameters: []string{"title"},
		ConversationKinds: []string{"private", "group"}, ReplyReferences: true, FileSending: true,
		TextLimit: 16000}
}

func (f *feishu) redact(value string) string {
	value = strings.ReplaceAll(value, f.secret, "[redacted]")
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func (f *feishu) request(ctx context.Context, method, path, token, contentType string, body io.Reader) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, f.config.BaseURL+path, body)
	if err != nil {
		return nil, domain.Invalid("invalid Feishu endpoint")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response, err := f.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, domain.Offline("Feishu request could not be completed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return nil, domain.Offline("Feishu response was interrupted or exceeded the size limit")
	}
	var envelope struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Code == nil {
		return nil, domain.Offline(fmt.Sprintf("Feishu returned an invalid response (HTTP %d)", response.StatusCode))
	}
	if *envelope.Code != 0 {
		text := f.redact(envelope.Msg)
		if token != "" {
			text = strings.ReplaceAll(text, token, "[redacted]")
		}
		return nil, &feishuAPIError{Code: *envelope.Code, Text: text}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, domain.Offline("Feishu returned inconsistent success status")
	}
	return raw, nil
}

func (f *feishu) accessToken(ctx context.Context) (string, error) {
	f.tokenMu.Lock()
	defer f.tokenMu.Unlock()
	if f.token != "" && time.Now().Before(f.tokenUntil) {
		return f.token, nil
	}
	raw, err := f.request(ctx, http.MethodPost, "/open-apis/auth/v3/tenant_access_token/internal", "", "application/json",
		bytes.NewReader(encoded(map[string]string{"app_id": f.config.AppID, "app_secret": f.secret})))
	if err != nil {
		return "", err
	}
	var result struct {
		Token  string `json:"tenant_access_token"`
		Expire int64  `json:"expire"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Token == "" || result.Expire <= 0 {
		return "", domain.Invalid("Feishu returned an invalid tenant access token")
	}
	f.token = result.Token
	f.tokenUntil = time.Now().Add(time.Duration(result.Expire)*time.Second - 30*time.Second)
	return f.token, nil
}

func (f *feishu) call(ctx context.Context, method, path string, body, result any) error {
	token, err := f.accessToken(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(encoded(body))
	}
	raw, err := f.request(ctx, method, path, token, "application/json", reader)
	if err != nil {
		return err
	}
	if result != nil && json.Unmarshal(raw, result) != nil {
		return domain.Invalid("Feishu returned an invalid result")
	}
	return nil
}

func (f *feishu) Validate(ctx context.Context) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var result struct {
		Bot struct {
			OpenID         string `json:"open_id"`
			Name           string `json:"app_name"`
			ActivateStatus int    `json:"activate_status"`
		} `json:"bot"`
	}
	if err := f.call(ctx, http.MethodGet, "/open-apis/bot/v3/info", nil, &result); err != nil {
		return Identity{}, err
	}
	if result.Bot.OpenID == "" {
		return Identity{}, domain.Invalid("Feishu credentials did not identify an application Bot")
	}
	if result.Bot.ActivateStatus != 2 {
		return Identity{}, domain.Invalid("Feishu application Bot is not activated; publish and enable it first")
	}
	// A valid application secret alone does not prove that this application
	// supports the configured inbound delivery mode. Check long-connection
	// discovery too, without exposing its signed websocket URL to the UI.
	raw, err := f.request(ctx, http.MethodPost, "/callback/ws/endpoint", "", "application/json", bytes.NewReader(encoded(map[string]string{"AppID": f.config.AppID, "AppSecret": f.secret})))
	if err != nil {
		return Identity{}, err
	}
	var endpoint struct {
		Data struct {
			URL string `json:"URL"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &endpoint) != nil || endpoint.Data.URL == "" {
		return Identity{}, domain.Invalid("Feishu did not provide a long-connection endpoint")
	}
	return Identity{ID: result.Bot.OpenID, Name: result.Bot.Name, Diagnostics: []string{
		"Use a self-built application Bot with long connection events and subscribe to im.message.receive_v1. Enable im:message.p2p_msg:readonly and im:message.group_at_msg:readonly (or im:message.group_msg to observe ordinary group messages), im:message:send_as_bot, im:resource, and im:chat:readonly as needed. Publish permission changes and add the Bot to each group.",
		"Feishu files are sent as separate file messages without captions. Astral currently groups thread messages under their parent chat; thread-specific grants, edit events and read receipts are not exposed.",
	}}, nil
}

type feishuQueuedEvent struct {
	event  Event
	cursor string
	ack    chan struct{}
}

type feishuStream struct {
	queue   chan *feishuQueuedEvent
	failure chan error
	ready   chan struct{}
	pending *feishuQueuedEvent
}

func (f *feishu) openStream(ctx context.Context) *feishuStream {
	stream := &feishuStream{queue: make(chan *feishuQueuedEvent, 128), failure: make(chan error, 1), ready: make(chan struct{})}
	var readyOnce sync.Once
	handler := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(func(callbackCtx context.Context, message *larkim.P2MessageReceiveV1) error {
		event, err := f.normalize(message)
		if err != nil {
			return err
		}
		f.enrichChat(callbackCtx, &event)
		if event.Observation.ReplyID != "" && !event.Observation.Self && feishuValue(message.Event.Sender.SenderType) != "bot" {
			f.enrichReply(callbackCtx, &event)
		}
		return stream.deliver(callbackCtx, event)
	})
	handler.Config.Logger = feishuSDKLogger{}
	client := larkws.NewClient(f.config.AppID, f.secret, larkws.WithDomain(f.config.BaseURL), larkws.WithHttpClient(f.client),
		larkws.WithEventHandler(handler), larkws.WithLogger(feishuSDKLogger{}), larkws.WithAutoReconnect(false), larkws.WithOnReady(func() { readyOnce.Do(func() { close(stream.ready) }) }))
	go func() {
		err := client.Start(ctx)
		if err == nil {
			err = domain.Offline("Feishu long connection closed")
		}
		// SDK error messages may contain the authenticated websocket URL. Keep
		// those credentials out of status records and logs returned by Astral.
		if ctx.Err() == nil {
			err = domain.Offline("Feishu long connection failed; check application event mode and permissions")
		}
		stream.failure <- err
	}()
	return stream
}

// SDK diagnostics may contain the signed websocket URL or message bodies.
// Connection failures are surfaced through Astral's sanitized account status.
type feishuSDKLogger struct{}

func (feishuSDKLogger) Debug(context.Context, ...interface{}) {}
func (feishuSDKLogger) Info(context.Context, ...interface{})  {}
func (feishuSDKLogger) Warn(context.Context, ...interface{})  {}
func (feishuSDKLogger) Error(context.Context, ...interface{}) {}

func (s *feishuStream) deliver(ctx context.Context, event Event) error {
	queued := &feishuQueuedEvent{event: event, cursor: event.Observation.ExternalEventID, ack: make(chan struct{})}
	select {
	case s.queue <- queued:
	case <-ctx.Done():
		return ctx.Err()
	}
	// The official SDK only returns a success ACK after this callback returns.
	// The next Poll proves the prior batch reached Astral's durable cursor.
	select {
	case <-queued.ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *feishu) Poll(ctx context.Context, cursor string) (Batch, error) {
	f.pollMu.Lock()
	defer f.pollMu.Unlock()
	if f.stream == nil {
		f.stream = f.openStream(ctx)
	}
	stream := f.stream
	if stream.pending != nil {
		if cursor != stream.pending.cursor {
			return Batch{Events: []Event{stream.pending.event}, NextCursor: stream.pending.cursor}, nil
		}
		close(stream.pending.ack)
		stream.pending = nil
	}
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	for {
		select {
		case queued := <-stream.queue:
			stream.pending = queued
			return Batch{Events: []Event{queued.event}, NextCursor: queued.cursor}, nil
		case <-stream.ready:
			// A ready channel is a one-time latch, not a repeating select case.
			stream.ready = nil
		case err := <-stream.failure:
			f.stream = nil
			return Batch{}, err
		case <-timer.C:
			if stream.ready != nil {
				return Batch{}, domain.Offline("Feishu long connection has not become ready")
			}
			return Batch{NextCursor: cursor}, nil
		case <-ctx.Done():
			return Batch{}, ctx.Err()
		}
	}
}

func feishuValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (f *feishu) normalize(in *larkim.P2MessageReceiveV1) (Event, error) {
	if in == nil || in.EventV2Base == nil || in.EventV2Base.Header == nil || in.EventV2Base.Header.EventID == "" || in.Event == nil || in.Event.Message == nil || in.Event.Sender == nil {
		return Event{}, domain.Invalid("Feishu delivered an incomplete message event")
	}
	if in.EventV2Base.Header.AppID != f.config.AppID {
		return Event{}, domain.Forbidden("Feishu event belongs to another application")
	}
	message, sender := in.Event.Message, in.Event.Sender
	chatID, messageID := feishuValue(message.ChatId), feishuValue(message.MessageId)
	if !feishuID(chatID) || !feishuID(messageID) {
		return Event{}, domain.Invalid("Feishu event has invalid chat or message IDs")
	}
	kind := "group"
	if feishuValue(message.ChatType) == "p2p" {
		kind = "private"
	} else if feishuValue(message.ChatType) != "group" {
		return Event{}, domain.Invalid("Feishu event has an unsupported chat type")
	}
	conversation := domain.Conversation{ConnectionID: f.connection.ID, ExternalID: chatID, Kind: kind, Title: chatID}
	conversation.ID = stableID("conversation", f.connection.ID, chatID, "")
	observation := domain.Observation{SchemaVersion: 1, ConnectionID: f.connection.ID, ConversationID: conversation.ID,
		ExternalEventID: in.EventV2Base.Header.EventID, MessageID: messageID, Kind: "message.created", ReceivedAt: time.Now().UTC(), ReplyID: feishuValue(message.ParentId)}
	observation.ID = stableID("event", f.connection.ID, in.EventV2Base.Header.EventID)
	if sender.SenderId != nil {
		observation.Sender.ID = feishuValue(sender.SenderId.OpenId)
	}
	observation.Self = observation.Sender.ID != "" && observation.Sender.ID == f.connection.AccountID
	observation.Text, observation.Attachments = feishuContent(feishuValue(message.MessageType), feishuValue(message.Content), messageID)
	utilityText := observation.Text
	for _, mention := range message.Mentions {
		if mention == nil {
			continue
		}
		if name := feishuValue(mention.Name); name != "" && feishuValue(mention.Key) != "" {
			observation.Text = strings.ReplaceAll(observation.Text, feishuValue(mention.Key), "@"+name)
		}
		if mention.Id != nil && feishuValue(mention.Id.OpenId) != "" && feishuValue(mention.Id.OpenId) == f.connection.AccountID {
			observation.Trigger = "mention"
			for _, label := range []string{feishuValue(mention.Key), "@" + feishuValue(mention.Name), "@" + f.connection.AccountID} {
				if label != "" && label != "@" {
					utilityText = strings.ReplaceAll(utilityText, label, "")
				}
			}
		}
	}
	millis, err := strconv.ParseInt(feishuValue(message.CreateTime), 10, 64)
	if err != nil || millis <= 0 {
		return Event{}, domain.Invalid("Feishu event has an invalid message timestamp")
	}
	observation.SentAt = time.UnixMilli(millis).UTC()
	if kind == "private" {
		observation.Trigger = "private"
	}
	if observation.Self || feishuValue(sender.SenderType) == "bot" {
		observation.Trigger = ""
	}
	event := Event{Conversation: conversation, Observation: observation}
	if strings.TrimSpace(utilityText) == "/chat_id" {
		event.Command = &Command{Name: "chat_id"}
		if !observation.Self && feishuValue(sender.SenderType) != "bot" {
			event.Command.Text = "Chat ID: " + chatID
		}
	}
	return event, nil
}

func feishuContent(kind, raw, messageID string) (string, []domain.Attachment) {
	var content struct {
		Text     string `json:"text"`
		ImageKey string `json:"image_key"`
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}
	if json.Unmarshal([]byte(raw), &content) != nil {
		return "[unsupported Feishu " + kind + " message]", nil
	}
	if kind == "text" {
		return content.Text, nil
	}
	if kind == "post" {
		return feishuPost(raw, messageID)
	}
	key, attachmentKind, name, mediaType := content.FileKey, "file", content.FileName, "application/octet-stream"
	switch kind {
	case "image":
		key, attachmentKind, mediaType = content.ImageKey, "image", "image/jpeg"
		name = "image-" + messageID + ".jpg"
	case "audio":
		attachmentKind, mediaType = "audio", "audio/ogg"
		if name == "" {
			name = "audio-" + messageID + ".ogg"
		}
	case "media":
		attachmentKind, mediaType = "video", "video/mp4"
		if name == "" {
			name = "video-" + messageID + ".mp4"
		}
	case "file":
	default:
		return "[unsupported Feishu " + kind + " message]", nil
	}
	if !feishuID(key) {
		return "[Feishu attachment unavailable]", nil
	}
	if name == "" {
		name = "file-" + messageID
	}
	return "", []domain.Attachment{{Ref: messageID + ":0", ExternalID: messageID + "/" + key, Kind: attachmentKind, Name: name, MediaType: mediaType}}
}

func feishuPost(raw, messageID string) (string, []domain.Attachment) {
	type node struct {
		Tag      string `json:"tag"`
		Text     string `json:"text"`
		Href     string `json:"href"`
		UserID   string `json:"user_id"`
		UserName string `json:"user_name"`
		ImageKey string `json:"image_key"`
	}
	var post struct {
		Title   string   `json:"title"`
		Content [][]node `json:"content"`
	}
	if json.Unmarshal([]byte(raw), &post) != nil || post.Content == nil {
		return "[unsupported Feishu post message]", nil
	}
	var lines []string
	if post.Title != "" {
		lines = append(lines, post.Title)
	}
	var attachments []domain.Attachment
	for _, row := range post.Content {
		var line strings.Builder
		for _, item := range row {
			switch item.Tag {
			case "text", "md", "code_block":
				line.WriteString(item.Text)
			case "a":
				line.WriteString(item.Text + " (" + item.Href + ")")
			case "at":
				name := item.UserName
				if name == "" {
					name = item.UserID
				}
				line.WriteString("@" + name)
			case "img":
				if feishuID(item.ImageKey) {
					attachments = append(attachments, domain.Attachment{Ref: messageID + ":" + strconv.Itoa(len(attachments)), ExternalID: messageID + "/" + item.ImageKey, Kind: "image", Name: "image-" + messageID + ".jpg", MediaType: "image/jpeg"})
				}
			default:
				line.WriteString("[" + item.Tag + "]")
			}
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n"), attachments
}

type feishuMessage struct {
	raw    json.RawMessage
	ID     string `json:"message_id"`
	ChatID string `json:"chat_id"`
	Sender struct {
		ID   string `json:"id"`
		Type string `json:"sender_type"`
	} `json:"sender"`
	Body struct {
		Content string `json:"content"`
	} `json:"body"`
	Type string `json:"msg_type"`
}

func (f *feishu) getMessage(ctx context.Context, id string) (feishuMessage, error) {
	if !feishuID(id) {
		return feishuMessage{}, domain.Invalid("invalid Feishu message ID")
	}
	var result struct {
		Data struct {
			Items []json.RawMessage `json:"items"`
		} `json:"data"`
	}
	if err := f.call(ctx, http.MethodGet, "/open-apis/im/v1/messages/"+url.PathEscape(id), nil, &result); err != nil {
		return feishuMessage{}, err
	}
	var message feishuMessage
	if len(result.Data.Items) != 1 || json.Unmarshal(result.Data.Items[0], &message) != nil || message.ID != id {
		return feishuMessage{}, domain.Invalid("Feishu did not return the requested message")
	}
	message.raw = result.Data.Items[0]
	return message, nil
}

func (f *feishu) enrichReply(ctx context.Context, event *Event) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	parent, err := f.getMessage(ctx, event.Observation.ReplyID)
	if err != nil || parent.ChatID != event.Conversation.ExternalID {
		return
	}
	event.Observation.ReplySender = parent.Sender.ID
	preview, _ := feishuContent(parent.Type, parent.Body.Content, parent.ID)
	if utf8.RuneCountInString(preview) > 500 {
		preview = string([]rune(preview)[:500])
	}
	event.Observation.ReplyPreview = preview
	if event.Observation.Trigger == "" && (parent.Sender.ID == f.connection.AccountID || parent.Sender.Type == "app" && parent.Sender.ID == f.config.AppID) {
		event.Observation.Trigger = "reply"
	}
}

func (f *feishu) enrichChat(ctx context.Context, event *Event) {
	f.chatMu.Lock()
	title, known := f.chatNames[event.Conversation.ExternalID]
	f.chatMu.Unlock()
	if !known {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if conversation, err := f.ResolveConversation(ctx, event.Conversation.ExternalID, ""); err == nil {
			title = conversation.Title
		}
		// A failed optional metadata lookup does not stop message persistence or
		// add the same API delay to every message in a chat without this scope.
		f.chatMu.Lock()
		f.chatNames[event.Conversation.ExternalID] = title
		f.chatMu.Unlock()
	}
	if title != "" {
		event.Conversation.Title = title
	}
}

func (f *feishu) ResolveConversation(ctx context.Context, id, topic string) (domain.Conversation, error) {
	if !feishuID(id) || topic != "" {
		return domain.Conversation{}, domain.Invalid("Feishu requires a chat_id without a Topic ID")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var result struct {
		Data struct {
			Name string `json:"name"`
			Mode string `json:"chat_mode"`
		} `json:"data"`
	}
	if err := f.call(ctx, http.MethodGet, "/open-apis/im/v1/chats/"+url.PathEscape(id), nil, &result); err != nil {
		return domain.Conversation{}, err
	}
	kind := "group"
	if result.Data.Mode == "p2p" {
		kind = "private"
	} else if result.Data.Mode != "group" && result.Data.Mode != "topic" {
		return domain.Conversation{}, domain.Invalid("Feishu returned an unsupported chat mode")
	}
	conversation := domain.Conversation{ConnectionID: f.connection.ID, ExternalID: id, Kind: kind, Title: result.Data.Name}
	conversation.ID = stableID("conversation", f.connection.ID, id, "")
	if conversation.Title == "" {
		conversation.Title = id
	}
	return conversation, nil
}

func (f *feishu) Download(ctx context.Context, attachment domain.Attachment) (Download, error) {
	messageID, fileKey, found := strings.Cut(attachment.ExternalID, "/")
	if !found || !feishuID(messageID) || !feishuID(fileKey) {
		return Download{}, domain.Invalid("Feishu attachment is invalid")
	}
	token, err := f.accessToken(ctx)
	if err != nil {
		return Download{}, err
	}
	typeName := "file"
	if attachment.Kind == "image" {
		typeName = "image"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.config.BaseURL+"/open-apis/im/v1/messages/"+url.PathEscape(messageID)+"/resources/"+url.PathEscape(fileKey)+"?type="+typeName, nil)
	if err != nil {
		return Download{}, domain.Invalid("invalid Feishu download endpoint")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := f.client.Do(request)
	if err != nil {
		return Download{}, domain.Offline("Feishu attachment download could not start")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return Download{}, domain.Offline(fmt.Sprintf("Feishu attachment download returned HTTP %d", response.StatusCode))
	}
	name := attachment.Name
	if _, parameters, err := mime.ParseMediaType(response.Header.Get("Content-Disposition")); err == nil && parameters["filename"] != "" {
		name = filepath.Base(strings.ReplaceAll(parameters["filename"], "\\", "/"))
	}
	return Download{Body: response.Body, Size: response.ContentLength, Name: name}, nil
}

func (f *feishu) SplitText(text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, domain.Invalid("message text is empty")
	}
	runes := []rune(text)
	var parts []string
	for len(runes) > f.Capabilities().TextLimit {
		end := f.Capabilities().TextLimit
		for candidate := end; candidate > end/2; candidate-- {
			if runes[candidate-1] == '\n' || runes[candidate-1] == ' ' {
				end = candidate
				break
			}
		}
		if part := string(runes[:end]); strings.TrimSpace(part) != "" {
			parts = append(parts, part)
		}
		runes = runes[end:]
	}
	if strings.TrimSpace(string(runes)) != "" {
		parts = append(parts, string(runes))
	}
	return parts, nil
}

func (f *feishu) Send(ctx context.Context, send Send) (Receipt, error) {
	if !feishuID(send.Target.ExternalID) || send.Target.TopicID != "" || send.ReplyTo != "" && !feishuID(send.ReplyTo) {
		return Receipt{}, &SendError{Message: "invalid Feishu chat or reply target"}
	}
	if send.File == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
	}
	token, err := f.accessToken(ctx)
	if err != nil {
		return Receipt{}, &SendError{Message: "Feishu authentication failed before any chat message was sent", Retryable: true}
	}
	// Reply endpoints address a message globally rather than accepting chat_id.
	// Verify that reference before an operation can escape the selected chat.
	if send.ReplyTo != "" {
		parent, err := f.getMessage(ctx, send.ReplyTo)
		if err != nil {
			return Receipt{}, &SendError{Message: "Feishu reply target could not be verified", Retryable: true}
		}
		if parent.ChatID != send.Target.ExternalID {
			return Receipt{}, &SendError{Message: "Feishu reply message belongs to another chat"}
		}
	}
	kind := "text"
	var content any = map[string]string{"text": send.Text}
	if send.File == nil {
		if strings.TrimSpace(send.Text) == "" || send.TextFormat() == "text" && utf8.RuneCountInString(send.Text) > f.Capabilities().TextLimit {
			return Receipt{}, &SendError{Message: "Feishu text fragment is empty or too long"}
		}
		if send.TextFormat() == "markdown" {
			kind = "post"
			content = map[string]any{"zh_cn": map[string]any{"title": send.Title, "content": [][]map[string]string{{{"tag": "md", "text": send.Text}}}}}
		}
	} else {
		if strings.TrimSpace(send.Text) != "" {
			return Receipt{}, &SendError{Message: "Feishu file messages do not support captions; send text and file as separate messages"}
		}
		key, err := f.upload(ctx, send)
		if err != nil {
			return Receipt{}, &SendError{Message: "Feishu file upload failed before any chat message was sent", Retryable: true}
		}
		kind, content = "file", map[string]string{"file_key": key}
	}
	body := map[string]any{"msg_type": kind, "content": string(encoded(content))}
	path := "/open-apis/im/v1/messages?receive_id_type=chat_id"
	if send.ReplyTo != "" {
		path = "/open-apis/im/v1/messages/" + url.PathEscape(send.ReplyTo) + "/reply"
	} else {
		body["receive_id"] = send.Target.ExternalID
	}
	var result struct {
		Data struct {
			ID string `json:"message_id"`
		} `json:"data"`
	}
	raw, err := f.request(ctx, http.MethodPost, path, token, "application/json", bytes.NewReader(encoded(body)))
	if err != nil {
		var known *feishuAPIError
		if errors.As(err, &known) {
			return Receipt{}, &SendError{Message: known.Error(), Retryable: known.Code == 99991400 || known.Code == 99991401 || known.Code == 230020}
		}
		return Receipt{}, &SendError{Message: "Feishu send outcome is unknown; inspect the conversation before any retry", Unknown: true}
	}
	if json.Unmarshal(raw, &result) != nil || result.Data.ID == "" {
		return Receipt{}, &SendError{Message: "Feishu accepted the request without a usable message receipt", Unknown: true}
	}
	return Receipt{MessageIDs: []string{result.Data.ID}}, nil
}

func (f *feishu) upload(ctx context.Context, send Send) (string, error) {
	token, err := f.accessToken(ctx)
	if err != nil {
		return "", err
	}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan error, 1)
	go func() {
		writeErr := form.WriteField("file_type", "stream")
		if writeErr == nil {
			writeErr = form.WriteField("file_name", send.FileName)
		}
		if writeErr == nil {
			var part io.Writer
			part, writeErr = form.CreateFormFile("file", send.FileName)
			if writeErr == nil {
				var copied int64
				copied, writeErr = io.Copy(part, io.LimitReader(send.File, send.Size+1))
				if writeErr == nil && copied != send.Size {
					writeErr = domain.Invalid("Feishu upload size differs from the source snapshot")
				}
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
		_ = send.File.Close()
	})
	raw, err := f.request(ctx, http.MethodPost, "/open-apis/im/v1/files", token, form.FormDataContentType(), reader)
	_ = reader.CloseWithError(err)
	_ = send.File.Close()
	stopClose()
	copyErr := <-done
	if err != nil || copyErr != nil {
		return "", errors.Join(err, copyErr)
	}
	var result struct {
		Data struct {
			Key string `json:"file_key"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || !feishuID(result.Data.Key) {
		return "", domain.Invalid("Feishu did not return an uploaded file key")
	}
	return result.Data.Key, nil
}
