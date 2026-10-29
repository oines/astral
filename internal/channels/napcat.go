package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

type NapCatConfig struct {
	BaseURL      string `json:"base_url"`
	WebSocketURL string `json:"websocket_url"`
}

type napcat struct {
	connection domain.Connection
	config     NapCatConfig
	token      string
	client     *http.Client
	store      *persistence.Store
	socket     *websocket.Conn
	stopSocket func() bool
	pending    *Batch
}

type napcatAPIError struct {
	Code   int
	Status int
}

func (e *napcatAPIError) Error() string {
	return fmt.Sprintf("NapCat rejected the request (HTTP %d, retcode %d)", e.Status, e.Code)
}

func (e *napcatAPIError) sendError() *SendError {
	// NapCat maps all action exceptions to retcode=200 over HTTP, or 1200
	// over WS, including timeouts after submitting a message to QQ. A failed
	// envelope alone therefore does not establish that no side effect occurred.
	unknown := true
	if e.Status >= 400 && e.Status < 500 {
		unknown = false
	} else if e.Status >= 200 && e.Status < 300 {
		switch e.Code {
		case 400, 401, 403, 404, 429, 1400, 1401, 1403, 1404:
			unknown = false
		}
	}
	return &SendError{Message: e.Error(), Unknown: unknown}
}

func NewNapCat(connection domain.Connection, token string, client *http.Client) (Adapter, error) {
	var config NapCatConfig
	decoder := json.NewDecoder(bytes.NewReader(connection.Config))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil {
		return nil, domain.Invalid("invalid NapCat connection config")
	}
	if err := modelbridge.ValidateURL(config.BaseURL); err != nil {
		return nil, err
	}
	if err := qqValidateWebSocket(config.WebSocketURL); err != nil {
		return nil, err
	}
	if strings.ContainsAny(token, "\r\n") {
		return nil, domain.Invalid("invalid NapCat access token")
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	return &napcat{connection: connection, config: config, token: token, client: qqHTTPClient(client)}, nil
}

func (n *napcat) Capabilities() Capabilities {
	return Capabilities{Adapter: "qq.napcat", Platform: "qq", Name: "QQ NapCat", ConversationKinds: []string{"private", "group"},
		MessageFormats: []string{"text"}, MessageParameters: []string{},
		ReplyReferences: true, DeletionEvents: true, FileSending: true, TextLimit: 4000, CaptionLimit: 4000}
}

func (n *napcat) setStore(store *persistence.Store) { n.store = store }

func (n *napcat) call(ctx context.Context, action string, params, result any) error {
	var body io.Reader = bytes.NewReader(encoded(params))
	if stream, ok := params.(*fileJSONBody); ok {
		body = stream
		defer stream.Close()
	} else {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.config.BaseURL+"/"+action, body)
	if err != nil {
		return domain.Invalid("invalid NapCat endpoint")
	}
	if stream, ok := params.(*fileJSONBody); ok {
		request.ContentLength = stream.length
	}
	request.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		request.Header.Set("Authorization", "Bearer "+n.token)
	}
	response, err := n.client.Do(request)
	if err != nil {
		return domain.Offline("NapCat request could not be completed")
	}
	defer response.Body.Close()
	limit := int64(8 << 20) // API metadata, never attachment bodies.
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return domain.Offline("NapCat response was interrupted or exceeded the size limit")
	}
	var envelope struct {
		Status string          `json:"status"`
		Code   int             `json:"retcode"`
		Data   json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return domain.Offline(fmt.Sprintf("NapCat returned an invalid response (HTTP %d)", response.StatusCode))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Status != "ok" || envelope.Code != 0 {
		return &napcatAPIError{Code: envelope.Code, Status: response.StatusCode}
	}
	if result != nil && json.Unmarshal(envelope.Data, result) != nil {
		return domain.Offline("NapCat returned an invalid result")
	}
	return nil
}

func (n *napcat) Validate(ctx context.Context) (Identity, error) {
	var login struct {
		ID       qqValue `json:"user_id"`
		Nickname string  `json:"nickname"`
	}
	if err := n.call(ctx, "get_login_info", map[string]any{}, &login); err != nil {
		return Identity{}, err
	}
	if !napcatID.MatchString(string(login.ID)) {
		return Identity{}, domain.Invalid("NapCat did not identify a logged-in QQ account")
	}
	var status struct {
		Online bool `json:"online"`
	}
	if err := n.call(ctx, "get_status", map[string]any{}, &status); err != nil {
		return Identity{}, err
	}
	if !status.Online {
		return Identity{}, domain.Offline("NapCat QQ account is offline")
	}
	// HTTP and WebSocket are independently configured in NapCat. Verify both
	// credentials and the login identity before reporting a usable connection.
	verify, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	headers := http.Header{}
	if n.token != "" {
		headers.Set("Authorization", "Bearer "+n.token)
	}
	socket, _, err := websocket.Dial(verify, n.config.WebSocketURL, &websocket.DialOptions{HTTPClient: n.client, HTTPHeader: headers})
	if err != nil {
		return Identity{}, domain.Offline("NapCat forward WebSocket could not connect")
	}
	defer socket.CloseNow()
	socket.SetReadLimit(8 << 20)
	var connected struct {
		PostType string  `json:"post_type"`
		SelfID   qqValue `json:"self_id"`
	}
	if wsjson.Read(verify, socket, &connected) != nil || connected.PostType == "" || connected.SelfID == "" {
		return Identity{}, domain.Invalid("NapCat WebSocket did not report a logged-in event account; use the event-enabled forward WebSocket endpoint, not /api")
	}
	if connected.SelfID != login.ID {
		return Identity{}, domain.Conflict("NapCat HTTP and WebSocket are connected to different QQ accounts")
	}
	return Identity{ID: string(login.ID), Name: login.Nickname, Diagnostics: []string{
		"NapCat 需要同时启用 HTTP 服务和正向 WebSocket 服务，并将消息上报格式设为数组。",
		"正向 WebSocket 不提供断线期间的历史重放；Astral 只能接收连接期间推送的事件。",
		"远程 NapCat 的本地文件需要启用 enableLocalFile2Url（本地文件转 Base64）；Astral 不会读取 NapCat 主机的本地路径。",
	}}, nil
}

func (n *napcat) Poll(ctx context.Context, cursor string) (Batch, error) {
	if n.pending != nil {
		if n.pending.NextCursor != cursor {
			return *n.pending, nil
		}
		n.pending = nil
	}
	if n.socket == nil {
		headers := http.Header{}
		if n.token != "" {
			headers.Set("Authorization", "Bearer "+n.token)
		}
		handshake, cancel := context.WithTimeout(ctx, 25*time.Second)
		socket, _, err := websocket.Dial(handshake, n.config.WebSocketURL, &websocket.DialOptions{HTTPClient: n.client, HTTPHeader: headers})
		cancel()
		if err != nil {
			return Batch{}, domain.Offline("NapCat forward WebSocket could not connect")
		}
		socket.SetReadLimit(8 << 20)
		n.socket = socket
		n.stopSocket = context.AfterFunc(ctx, func() { _ = socket.CloseNow() })
	}
	for {
		var raw json.RawMessage
		if err := wsjson.Read(ctx, n.socket, &raw); err != nil {
			n.stopSocket()
			_ = n.socket.CloseNow()
			n.socket = nil
			if ctx.Err() != nil {
				return Batch{}, ctx.Err()
			}
			return Batch{}, domain.Offline("NapCat WebSocket disconnected")
		}
		event, err := n.normalize(raw)
		if err != nil {
			return Batch{}, err
		}
		if event == nil {
			continue // lifecycle, heartbeat, unrelated notices and API envelopes
		}
		if n.store != nil && event.Observation.ReplyID != "" {
			if reply, err := n.store.Message(ctx, event.Conversation.ID, event.Observation.ReplyID); err == nil {
				event.Observation.ReplySender = reply.Sender.Name
				event.Observation.ReplyPreview = string([]rune(reply.Text)[:min(len([]rune(reply.Text)), 500)])
				event.Observation.ReplyAttachments = reply.Attachments
				if reply.Self && !event.Observation.Self {
					event.Observation.Trigger = "reply"
				}
			} else {
				event.Observation.ReplyPreview = "[Referenced message text and attachments are unavailable in this chat's archive.]"
			}
		}
		batch := Batch{Events: []Event{*event}, NextCursor: event.Observation.ID}
		n.pending = &batch
		return batch, nil
	}
}

var napcatID = regexp.MustCompile(`^[1-9][0-9]*$`)

type napcatSegment struct {
	Type string             `json:"type"`
	Data map[string]qqValue `json:"data"`
}

func (s *napcatSegment) UnmarshalJSON(raw []byte) error {
	var wire struct {
		Type string                     `json:"type"`
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	s.Type, s.Data = wire.Type, map[string]qqValue{}
	for key, raw := range wire.Data {
		var scalar qqValue
		if scalar.UnmarshalJSON(raw) == nil {
			s.Data[key] = scalar
		}
	}
	return nil
}

func (n *napcat) normalize(raw json.RawMessage) (*Event, error) {
	var message struct {
		PostType    string          `json:"post_type"`
		MessageType string          `json:"message_type"`
		NoticeType  string          `json:"notice_type"`
		SubType     string          `json:"sub_type"`
		EventID     qqValue         `json:"event_id"`
		OperatorID  qqValue         `json:"operator_id"`
		SelfID      qqValue         `json:"self_id"`
		MessageID   qqValue         `json:"message_id"`
		UserID      qqValue         `json:"user_id"`
		SenderID    qqValue         `json:"sender_id"`
		TargetID    qqValue         `json:"target_id"`
		RawInfo     json.RawMessage `json:"raw_info"`
		GroupID     qqValue         `json:"group_id"`
		GroupName   string          `json:"group_name"`
		Time        int64           `json:"time"`
		Message     json.RawMessage `json:"message"`
		Sender      struct {
			ID       qqValue `json:"user_id"`
			Nickname string  `json:"nickname"`
			Card     string  `json:"card"`
		} `json:"sender"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return nil, domain.Invalid("NapCat returned an invalid event")
	}
	if message.SelfID != "" && n.connection.AccountID != "" && string(message.SelfID) != n.connection.AccountID {
		return nil, domain.Conflict("NapCat event belongs to a different QQ account")
	}
	if message.PostType == "notice" && message.NoticeType == "group_msg_emoji_like" {
		return n.normalizeReaction(raw)
	}
	if message.PostType != "message" && message.PostType != "message_sent" && message.PostType != "notice" {
		return nil, nil
	}
	poke := message.PostType == "notice" && message.NoticeType == "notify" && message.SubType == "poke"
	actor := message.UserID
	if poke && message.SenderID != "" {
		// NapCat private pokes use user_id for the peer and sender_id for
		// the actual actor; group pokes use user_id for the actor.
		actor = message.SenderID
	}
	if message.PostType == "notice" && message.NoticeType != "group_recall" && message.NoticeType != "friend_recall" && !poke {
		return nil, nil
	}
	kind, id := "private", string(message.UserID)
	if message.PostType == "message_sent" && message.TargetID != "" {
		id = string(message.TargetID)
	}
	if poke && string(message.UserID) == n.connection.AccountID {
		id = string(message.TargetID)
	}
	if message.MessageType == "group" || message.NoticeType == "group_recall" || poke && message.GroupID != "" {
		kind, id = "group", string(message.GroupID)
	}
	if !napcatID.MatchString(id) || !poke && message.MessageID == "" || poke && (!napcatID.MatchString(string(actor)) || !napcatID.MatchString(string(message.TargetID))) {
		return nil, domain.Invalid("NapCat message did not identify its chat or message")
	}
	conversation := domain.Conversation{ConnectionID: n.connection.ID, ExternalID: kind + ":" + id, Kind: kind, Title: id}
	if kind == "private" && message.Sender.Nickname != "" {
		conversation.Title = message.Sender.Nickname
	}
	if kind == "group" && message.GroupName != "" {
		conversation.Title = message.GroupName
	}
	conversation.ID = stableID("conversation", n.connection.ID, conversation.ExternalID, "")
	eventKind := "message.created"
	if message.PostType == "notice" {
		eventKind = "message.deleted"
		if poke {
			eventKind = "notice.poke"
		}
	}
	externalEvent := eventKind + ":" + conversation.ExternalID + ":" + string(message.MessageID)
	if message.PostType == "notice" {
		// OneBot notices do not have a message ID or a replayable stream cursor.
		// Allocate once per received frame; pending Batch retries retain this ID.
		externalEvent = domain.ID("notice")
		if message.EventID != "" {
			externalEvent = eventKind + ":" + conversation.ExternalID + ":" + string(message.EventID)
		}
	}
	name := message.Sender.Card
	if name == "" {
		name = message.Sender.Nickname
	}
	observation := domain.Observation{SchemaVersion: 1, ID: stableID("event", n.connection.ID, externalEvent), ConnectionID: n.connection.ID,
		ConversationID: conversation.ID, ExternalEventID: externalEvent, Kind: eventKind, MessageID: string(message.MessageID),
		Sender: domain.Sender{ID: string(actor), Name: name}, SentAt: time.Unix(message.Time, 0).UTC(), ReceivedAt: time.Now().UTC(),
		Self: string(actor) == n.connection.AccountID || message.PostType == "message_sent"}
	if poke {
		observation.MessageID = "" // A poke is a notice, not a fabricated QQ message.
		observation.Text = napcatPokeText(message.RawInfo, string(actor), string(message.TargetID))
		if string(message.TargetID) == n.connection.AccountID && !observation.Self {
			observation.Trigger = "poke"
		}
	} else if eventKind == "message.deleted" {
		operator := message.OperatorID
		if operator == "" {
			operator = message.UserID
		}
		observation.Text = fmt.Sprintf("QQ user %s recalled message %s originally sent by QQ user %s.", operator, message.MessageID, message.UserID)
	}
	if eventKind == "message.created" {
		var segments []napcatSegment
		if len(message.Message) == 0 || message.Message[0] != '[' || json.Unmarshal(message.Message, &segments) != nil {
			return nil, domain.Invalid("NapCat message format must be array; configure messagePostFormat=array")
		}
		if kind == "private" {
			observation.Trigger = "private"
		}
		for _, segment := range segments {
			data := segment.Data
			switch segment.Type {
			case "text":
				observation.Text += string(data["text"])
			case "at":
				observation.Text += "@" + string(data["qq"])
				if string(data["qq"]) == n.connection.AccountID {
					observation.Trigger = "mention"
				}
			case "reply":
				observation.ReplyID = string(data["id"])
			case "shake":
				observation.Text += "[QQ private window shake]"
			case "image", "record", "video", "file":
				mediaKind := segment.Type
				if mediaKind == "record" {
					mediaKind = "audio"
				}
				file := string(data["file"])
				if file == "" {
					file = string(data["file_id"])
				}
				if file == "" {
					file = string(data["url"])
				}
				if file == "" {
					continue
				}
				var size int64
				_, _ = fmt.Sscan(string(data["file_size"]), &size)
				filename := string(data["name"])
				if filename == "" {
					filename = string(data["file_name"])
				}
				if filename == "" {
					filename = segment.Type + "-" + string(message.MessageID)
				}
				mediaType := mime.TypeByExtension(filepath.Ext(filename))
				if mediaType == "" {
					mediaType = map[string]string{"image": "image/jpeg", "record": "audio/ogg", "video": "video/mp4", "file": "application/octet-stream"}[segment.Type]
				}
				// Keep the opaque platform file handle; URLs expire and get_file
				// can refresh it or return base64 for a remote NapCat host.
				locator := string(encoded(map[string]string{"file": file, "url": string(data["url"])}))
				observation.Attachments = append(observation.Attachments, domain.Attachment{Ref: fmt.Sprintf("%s:%d", message.MessageID, len(observation.Attachments)),
					ExternalID: locator, Kind: mediaKind, Name: filename, MediaType: mediaType, Size: size})
			default:
				observation.Text += "[" + segment.Type + "]"
			}
		}
	}
	if observation.Self {
		observation.Trigger = ""
	}
	event := &Event{Conversation: conversation, Observation: observation}
	if eventKind == "message.created" && strings.TrimSpace(observation.Text) == "/chat_id" {
		event.Command = &Command{Name: "chat_id"}
		if !observation.Self {
			event.Command.Text = "Chat ID: " + conversation.ExternalID
		}
	}
	return event, nil
}

// NapCat forwards the QQ gray-tip json.items as raw_info for both group and
// private pokes. Preserve readable wording and participant identities, never
// image src/jp URLs or opaque QQ UIDs. The two uid items are actor then target
// in NapCat's own parser; malformed/unknown payloads retain an honest fallback.
func napcatPokeText(raw json.RawMessage, actor, target string) string {
	fallback := fmt.Sprintf("QQ user %s poked QQ user %s.", actor, target)
	var items []struct {
		Type string `json:"type"`
		Text string `json:"txt"`
		Name string `json:"nm"`
		UID  string `json:"uid"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return fallback
	}
	participants, wording := 0, false
	var text strings.Builder
	for _, item := range items {
		if item.UID != "" {
			if participants >= 2 {
				return fallback
			}
			id := actor
			if participants == 1 {
				id = target
			}
			if item.Name != "" {
				fmt.Fprintf(&text, " %s (QQ user %s) ", item.Name, id)
			} else {
				fmt.Fprintf(&text, " QQ user %s ", id)
			}
			participants++
		} else if item.Type == "nor" {
			text.WriteString(item.Text)
			wording = wording || strings.TrimSpace(item.Text) != ""
		}
	}
	if participants != 2 || !wording {
		return fallback
	}
	return strings.TrimSpace(text.String())
}

func (n *napcat) ResolveConversation(ctx context.Context, id, topic string) (domain.Conversation, error) {
	if topic != "" {
		return domain.Conversation{}, domain.Invalid("NapCat chats do not expose Topics")
	}
	kind, platformID, err := qqTarget(id)
	if err != nil || !napcatID.MatchString(platformID) {
		return domain.Conversation{}, domain.Invalid("NapCat chat ID must be private:<QQ number> or group:<group number>")
	}
	var result struct {
		GroupName string `json:"group_name"`
		Nickname  string `json:"nickname"`
	}
	action, params := "get_stranger_info", map[string]any{"user_id": platformID}
	if kind == "group" {
		action, params = "get_group_info", map[string]any{"group_id": platformID}
	}
	if err := n.call(ctx, action, params, &result); err != nil {
		return domain.Conversation{}, err
	}
	title := result.Nickname
	if kind == "group" {
		title = result.GroupName
	}
	if title == "" {
		title = platformID
	}
	return domain.Conversation{Meta: domain.Meta{ID: stableID("conversation", n.connection.ID, id, "")}, ConnectionID: n.connection.ID, ExternalID: id, Kind: kind, Title: title}, nil
}

func (n *napcat) Download(ctx context.Context, attachment domain.Attachment) (Download, error) {
	var locator struct {
		File string `json:"file"`
		URL  string `json:"url"`
	}
	if json.Unmarshal([]byte(attachment.ExternalID), &locator) != nil || locator.File == "" {
		return Download{}, domain.Invalid("invalid NapCat attachment handle")
	}
	if locator.URL != "" {
		if result, err := qqDownloadURL(ctx, n.client, locator.URL, attachment); err == nil {
			return result, nil
		}
	}
	return n.downloadFile(ctx, locator.File, attachment)
}

func (n *napcat) SplitText(text string) ([]string, error) {
	return qqSplitText(text, n.Capabilities().TextLimit)
}

func (n *napcat) Send(ctx context.Context, send Send) (Receipt, error) {
	filePlaceholderValue := filePlaceholder()
	kind, id, err := qqTarget(send.Target.ExternalID)
	if err != nil || !napcatID.MatchString(id) || send.Target.TopicID != "" {
		return Receipt{}, &SendError{Message: "invalid NapCat destination"}
	}
	if len([]rune(send.Text)) > n.Capabilities().TextLimit || send.File == nil && strings.TrimSpace(send.Text) == "" {
		return Receipt{}, &SendError{Message: "NapCat text is empty or exceeds the text limit"}
	}
	segments := []map[string]any{}
	if send.ReplyTo != "" {
		if _, err := strconv.ParseInt(send.ReplyTo, 10, 64); err != nil {
			return Receipt{}, &SendError{Message: "invalid NapCat reply message ID"}
		}
		var message json.RawMessage
		if err := n.call(ctx, "get_msg", map[string]any{"message_id": send.ReplyTo}, &message); err != nil {
			return Receipt{}, &SendError{Message: "NapCat reply reference could not be verified: " + err.Error()}
		}
		if err := napcatMessageScope(ctx, n, send.Target, message, send.ReplyTo); err != nil {
			return Receipt{}, &SendError{Message: err.Error()}
		}
		segments = append(segments, map[string]any{"type": "reply", "data": map[string]string{"id": send.ReplyTo}})
	}
	if send.Text != "" {
		segments = append(segments, map[string]any{"type": "text", "data": map[string]string{"text": send.Text}})
	}
	if send.File != nil {
		fileType := "file"
		if strings.HasPrefix(send.MediaType, "image/") {
			fileType = "image"
		}
		segments = append(segments, map[string]any{"type": fileType, "data": map[string]string{"file": "base64://" + filePlaceholderValue, "name": send.FileName}})
	}
	params := map[string]any{"message": segments}
	action := "send_private_msg"
	if kind == "group" {
		action, params["group_id"] = "send_group_msg", id
	} else {
		params["user_id"] = id
	}
	var result struct {
		ID qqValue `json:"message_id"`
	}
	var sendBody any = params
	if send.File != nil {
		sendBody = streamFileJSON(params, filePlaceholderValue, send.File, send.Size)
	}
	if err := n.call(ctx, action, sendBody, &result); err != nil {
		var api *napcatAPIError
		if errors.As(err, &api) {
			return Receipt{}, api.sendError()
		}
		return Receipt{}, &SendError{Message: err.Error(), Unknown: true}
	}
	if result.ID == "" || result.ID == "0" {
		return Receipt{}, &SendError{Message: "NapCat accepted response did not identify a message", Unknown: true}
	}
	return Receipt{MessageIDs: []string{string(result.ID)}}, nil
}
