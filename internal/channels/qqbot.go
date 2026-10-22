package channels

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

// AppSecret is stored in the existing credential vault, never in Config.
type QQBotConfig struct {
	AppID    string `json:"app_id"`
	BaseURL  string `json:"base_url,omitempty"`
	TokenURL string `json:"token_url,omitempty"`
}

type qqbot struct {
	connection domain.Connection
	config     QQBotConfig
	secret     string
	client     *http.Client
	store      *persistence.Store
	tokenMu    sync.Mutex
	token      string
	tokenUntil time.Time
	socket     *websocket.Conn
	socketCtx  context.Context
	stopSocket context.CancelFunc
	sequence   atomic.Int64
	acked      atomic.Bool
	state      qqGatewayCursor
	pending    *Batch
}

type qqGatewayCursor struct {
	SessionID string `json:"session_id,omitempty"`
	Sequence  int64  `json:"sequence"`
}

type qqAPIError struct {
	Code   int
	Status int
}

func (e *qqAPIError) Error() string {
	return fmt.Sprintf("QQ Bot API rejected the request (HTTP %d, code %d)", e.Status, e.Code)
}

func NewQQBot(connection domain.Connection, token string, client *http.Client) (Adapter, error) {
	var config QQBotConfig
	decoder := json.NewDecoder(bytes.NewReader(connection.Config))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || !qqAddress.MatchString(config.AppID) {
		return nil, domain.Invalid("QQ Bot requires an app_id")
	}
	if config.BaseURL == "" {
		config.BaseURL = "https://api.sgroup.qq.com"
	}
	if config.TokenURL == "" {
		config.TokenURL = "https://bots.qq.com/app/getAppAccessToken"
	}
	for _, endpoint := range []string{config.BaseURL, config.TokenURL} {
		if err := modelbridge.ValidateURL(endpoint); err != nil {
			return nil, err
		}
	}
	if strings.HasPrefix(strings.TrimSpace(token), "{") {
		var credential struct {
			Secret string `json:"app_secret"`
		}
		if json.Unmarshal([]byte(token), &credential) != nil {
			return nil, domain.Invalid("invalid QQ Bot credential")
		}
		token = credential.Secret
	}
	if strings.TrimSpace(token) == "" || strings.ContainsAny(token, "\r\n") {
		return nil, domain.Invalid("QQ Bot App Secret is required")
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	return &qqbot{connection: connection, config: config, secret: token, client: qqHTTPClient(client)}, nil
}

func (q *qqbot) setStore(store *persistence.Store) { q.store = store }

func (q *qqbot) Capabilities() Capabilities {
	return Capabilities{Adapter: "qq.bot", Platform: "qq", Name: "QQ 官方 Bot", ConversationKinds: []string{"private", "group"},
		ReplyReferences: true, FileSending: true, DownloadLimit: 50 << 20, UploadLimit: 20 << 20, TextLimit: 5000, CaptionLimit: 5000}
}

func (q *qqbot) accessToken(ctx context.Context) (string, error) {
	q.tokenMu.Lock()
	defer q.tokenMu.Unlock()
	if q.token != "" && time.Now().Before(q.tokenUntil) {
		return q.token, nil
	}
	var response struct {
		Token   string  `json:"access_token"`
		Expires qqValue `json:"expires_in"`
	}
	if err := q.request(ctx, http.MethodPost, q.config.TokenURL, "", map[string]string{"appId": q.config.AppID, "clientSecret": q.secret}, &response); err != nil {
		return "", err
	}
	var seconds int64
	_, _ = fmt.Sscan(string(response.Expires), &seconds)
	if response.Token == "" || strings.ContainsAny(response.Token, "\r\n") || seconds <= 0 {
		return "", domain.Invalid("QQ token endpoint returned invalid credentials")
	}
	q.token, q.tokenUntil = response.Token, time.Now().Add(time.Duration(max(1, seconds-60))*time.Second)
	return q.token, nil
}

func (q *qqbot) request(ctx context.Context, method, endpoint, token string, body, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(encoded(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return domain.Invalid("invalid QQ Bot endpoint")
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "QQBot "+token)
		request.Header.Set("X-Union-Appid", q.config.AppID)
	}
	response, err := q.client.Do(request)
	if err != nil {
		return domain.Offline("QQ Bot request could not be completed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return domain.Offline("QQ Bot response was interrupted or exceeded the size limit")
	}
	var envelope struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return domain.Offline(fmt.Sprintf("QQ Bot returned an invalid response (HTTP %d)", response.StatusCode))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || envelope.Code != 0 {
		return &qqAPIError{Code: envelope.Code, Status: response.StatusCode}
	}
	if result != nil && json.Unmarshal(raw, result) != nil {
		return domain.Offline("QQ Bot returned an invalid result")
	}
	return nil
}

func (q *qqbot) call(ctx context.Context, method, route string, body, result any) error {
	token, err := q.accessToken(ctx)
	if err != nil {
		return err
	}
	return q.request(ctx, method, q.config.BaseURL+route, token, body, result)
}

func (q *qqbot) Validate(ctx context.Context) (Identity, error) {
	var user struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Bot      bool   `json:"bot"`
	}
	if err := q.call(ctx, http.MethodGet, "/users/@me", nil, &user); err != nil {
		return Identity{}, err
	}
	if user.ID == "" || !user.Bot {
		return Identity{}, domain.Invalid("QQ credentials did not identify a Bot")
	}
	var gateway struct {
		URL string `json:"url"`
	}
	if err := q.call(ctx, http.MethodGet, "/gateway", nil, &gateway); err != nil {
		return Identity{}, err
	}
	if err := qqValidateWebSocket(gateway.URL); err != nil {
		return Identity{}, err
	}
	return Identity{ID: user.ID, Name: user.Username, Diagnostics: []string{
		"群消息推送取决于 QQ 开放平台授权；通常只推送 @ Bot 的消息，普通群消息需要平台额外授权。",
		"主动消息、被动回复的次数与有效窗口以及群文件类型，以 QQ 开放平台实际权限和限制为准。",
	}}, nil
}

func (q *qqbot) dropSocket() {
	if q.stopSocket != nil {
		q.stopSocket()
		q.stopSocket = nil
	}
	if q.socket != nil {
		_ = q.socket.CloseNow()
		q.socket = nil
	}
}

func (q *qqbot) openSocket(ctx context.Context, cursor string) error {
	q.state = qqGatewayCursor{}
	if cursor != "" && json.Unmarshal([]byte(cursor), &q.state) != nil {
		return domain.Invalid("invalid QQ Bot gateway cursor")
	}
	var gateway struct {
		URL string `json:"url"`
	}
	if err := q.call(ctx, http.MethodGet, "/gateway", nil, &gateway); err != nil {
		return err
	}
	if err := qqValidateWebSocket(gateway.URL); err != nil {
		return err
	}
	handshake, cancel := context.WithTimeout(ctx, 25*time.Second)
	socket, _, err := websocket.Dial(handshake, gateway.URL, &websocket.DialOptions{HTTPClient: q.client})
	cancel()
	if err != nil {
		return domain.Offline("QQ Bot gateway could not connect")
	}
	socket.SetReadLimit(8 << 20)
	q.socket = socket
	lifetime, stop := context.WithCancel(ctx)
	q.socketCtx = lifetime
	q.stopSocket = stop
	context.AfterFunc(lifetime, func() { _ = socket.CloseNow() })
	q.sequence.Store(q.state.Sequence)
	q.acked.Store(true)
	return nil
}

func (q *qqbot) Poll(ctx context.Context, cursor string) (Batch, error) {
	// Keep an uncommitted batch available if the IMStore transaction failed.
	if q.pending != nil {
		if cursor != q.pending.NextCursor {
			return *q.pending, nil
		}
		q.pending = nil
	}
	if q.socket == nil {
		if err := q.openSocket(ctx, cursor); err != nil {
			return Batch{}, err
		}
	}
	for {
		var frame struct {
			Op   int             `json:"op"`
			Type string          `json:"t"`
			Seq  *int64          `json:"s"`
			Data json.RawMessage `json:"d"`
		}
		if err := wsjson.Read(ctx, q.socket, &frame); err != nil {
			code := websocket.CloseStatus(err)
			q.dropSocket()
			if code == 4006 || code == 4007 || code == 4009 {
				q.state = qqGatewayCursor{}
				return Batch{NextCursor: string(encoded(q.state))}, nil
			}
			if code == 4004 {
				q.tokenMu.Lock()
				q.token = ""
				q.tokenMu.Unlock()
			}
			if ctx.Err() != nil {
				return Batch{}, ctx.Err()
			}
			return Batch{}, domain.Offline("QQ Bot gateway disconnected")
		}
		if frame.Seq != nil {
			q.state.Sequence = *frame.Seq
			q.sequence.Store(*frame.Seq)
		}
		switch frame.Op {
		case 10:
			var hello struct {
				Interval int64 `json:"heartbeat_interval"`
			}
			if json.Unmarshal(frame.Data, &hello) != nil || hello.Interval < 100 || hello.Interval > 300000 {
				q.dropSocket()
				return Batch{}, domain.Invalid("QQ gateway returned an invalid heartbeat interval")
			}
			token, err := q.accessToken(ctx)
			if err != nil {
				q.dropSocket()
				return Batch{}, err
			}
			body := map[string]any{"op": 2, "d": map[string]any{"token": "QQBot " + token, "intents": 1 << 25, "shard": []int{0, 1}}}
			if q.state.SessionID != "" {
				body = map[string]any{"op": 6, "d": map[string]any{"token": "QQBot " + token, "session_id": q.state.SessionID, "seq": q.state.Sequence}}
			}
			if err := wsjson.Write(ctx, q.socket, body); err != nil {
				q.dropSocket()
				return Batch{}, domain.Offline("QQ gateway authentication could not be sent")
			}
			q.startHeartbeat(q.socketCtx, q.socket, time.Duration(hello.Interval)*time.Millisecond)
		case 11:
			q.acked.Store(true)
		case 7:
			q.dropSocket()
			return Batch{NextCursor: string(encoded(q.state))}, nil
		case 9:
			q.dropSocket()
			q.state = qqGatewayCursor{}
			return Batch{NextCursor: string(encoded(q.state))}, nil
		case 0:
			batch := Batch{}
			if frame.Type == "READY" {
				var ready struct {
					SessionID string `json:"session_id"`
				}
				if json.Unmarshal(frame.Data, &ready) != nil || ready.SessionID == "" {
					q.dropSocket()
					return Batch{}, domain.Invalid("QQ gateway returned an invalid session")
				}
				q.state.SessionID = ready.SessionID
			} else if frame.Type == "C2C_MESSAGE_CREATE" || frame.Type == "GROUP_AT_MESSAGE_CREATE" || frame.Type == "GROUP_MESSAGE_CREATE" {
				event, err := q.normalize(frame.Type, frame.Data)
				if err != nil {
					q.dropSocket()
					return Batch{}, err
				}
				batch.Events = []Event{event}
			}
			batch.NextCursor = string(encoded(q.state))
			q.pending = &batch
			return batch, nil
		}
	}
}

func (q *qqbot) startHeartbeat(ctx context.Context, socket *websocket.Conn, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !q.acked.Swap(false) {
					_ = socket.CloseNow()
					return
				}
				write, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := wsjson.Write(write, socket, map[string]any{"op": 1, "d": q.sequence.Load()})
				cancel()
				if err != nil {
					_ = socket.CloseNow()
					return
				}
			}
		}
	}()
}

func (q *qqbot) normalize(eventType string, raw json.RawMessage) (Event, error) {
	var message struct {
		ID        string `json:"id"`
		Content   string `json:"content"`
		Timestamp string `json:"timestamp"`
		GroupID   string `json:"group_openid"`
		Author    struct {
			ID       string `json:"id"`
			UserID   string `json:"user_openid"`
			MemberID string `json:"member_openid"`
			Username string `json:"username"`
			Bot      bool   `json:"bot"`
		} `json:"author"`
		Reference *struct {
			ID string `json:"message_id"`
		} `json:"message_reference"`
		Attachments []struct {
			URL  string `json:"url"`
			Name string `json:"filename"`
			Type string `json:"content_type"`
			Size int64  `json:"size"`
		} `json:"attachments"`
	}
	if json.Unmarshal(raw, &message) != nil || message.ID == "" {
		return Event{}, domain.Invalid("QQ gateway returned an invalid message")
	}
	kind, id, sender, trigger := "private", message.Author.UserID, message.Author.UserID, "private"
	if eventType != "C2C_MESSAGE_CREATE" {
		kind, id, sender, trigger = "group", message.GroupID, message.Author.MemberID, ""
		if eventType == "GROUP_AT_MESSAGE_CREATE" {
			trigger = "mention"
		}
	}
	if sender == "" {
		sender = message.Author.ID
	}
	if !qqAddress.MatchString(id) || sender == "" {
		return Event{}, domain.Invalid("QQ gateway message did not identify its chat or sender")
	}
	conversation := domain.Conversation{ConnectionID: q.connection.ID, ExternalID: kind + ":" + id, Kind: kind, Title: id}
	if kind == "private" && message.Author.Username != "" {
		conversation.Title = message.Author.Username
	}
	conversation.ID = stableID("conversation", q.connection.ID, conversation.ExternalID, "")
	at, err := time.Parse(time.RFC3339Nano, message.Timestamp)
	if err != nil {
		at = time.Now().UTC()
	}
	externalEvent := "message:" + conversation.ExternalID + ":" + message.ID
	observation := domain.Observation{SchemaVersion: 1, ID: stableID("event", q.connection.ID, externalEvent), ConnectionID: q.connection.ID,
		ConversationID: conversation.ID, ExternalEventID: externalEvent, Kind: "message.created", MessageID: message.ID,
		Text: message.Content, Sender: domain.Sender{ID: sender, Name: message.Author.Username}, SentAt: at.UTC(), ReceivedAt: time.Now().UTC(), Trigger: trigger,
		Self: sender == q.connection.AccountID}
	if observation.Self || message.Author.Bot {
		observation.Trigger = ""
	}
	if message.Reference != nil {
		observation.ReplyID = message.Reference.ID
	}
	for _, file := range message.Attachments {
		kind := "file"
		for _, media := range []string{"image", "audio", "video"} {
			if strings.HasPrefix(file.Type, media+"/") {
				kind = media
			}
		}
		if file.URL != "" {
			observation.Attachments = append(observation.Attachments, domain.Attachment{Ref: fmt.Sprintf("%s:%d", message.ID, len(observation.Attachments)), ExternalID: file.URL,
				Kind: kind, Name: file.Name, MediaType: file.Type, Size: file.Size})
		}
	}
	return Event{Conversation: conversation, Observation: observation}, nil
}

func (q *qqbot) ResolveConversation(_ context.Context, id, topic string) (domain.Conversation, error) {
	if topic != "" {
		return domain.Conversation{}, domain.Invalid("QQ chats do not expose Topics")
	}
	kind, platformID, err := qqTarget(id)
	if err != nil {
		return domain.Conversation{}, err
	}
	return domain.Conversation{Meta: domain.Meta{ID: stableID("conversation", q.connection.ID, id, "")}, ConnectionID: q.connection.ID, ExternalID: id, Kind: kind, Title: platformID}, nil
}

func (q *qqbot) Download(ctx context.Context, attachment domain.Attachment) (Download, error) {
	return qqDownloadURL(ctx, q.client, attachment.ExternalID, attachment, q.Capabilities().DownloadLimit)
}

func (q *qqbot) SplitText(text string) ([]string, error) {
	return qqSplitText(text, q.Capabilities().TextLimit)
}

func (q *qqbot) replyMessage(ctx context.Context, send Send) (string, error) {
	if send.ReplyTo != "" || q.store == nil {
		return send.ReplyTo, nil
	}
	id := stableID("conversation", q.connection.ID, send.Target.ExternalID, "")
	head, err := q.store.ObservationHead(ctx, id)
	if err != nil {
		return "", err
	}
	observations, err := q.store.Observations(ctx, id, max(0, head-500), 500)
	if err != nil {
		return "", err
	}
	for i := len(observations) - 1; i >= 0; i-- {
		event := observations[i].Observation
		if !event.Self && event.Kind == "message.created" && event.MessageID != "" {
			return event.MessageID, nil
		}
	}
	return "", nil
}

func (q *qqbot) Send(ctx context.Context, send Send) (Receipt, error) {
	kind, id, err := qqTarget(send.Target.ExternalID)
	if err != nil || send.Target.TopicID != "" {
		return Receipt{}, &SendError{Message: "invalid QQ Bot destination"}
	}
	if len([]rune(send.Text)) > q.Capabilities().TextLimit || send.File == nil && strings.TrimSpace(send.Text) == "" {
		return Receipt{}, &SendError{Message: "QQ Bot text is empty or exceeds 5000 characters"}
	}
	if _, err := q.accessToken(ctx); err != nil {
		return Receipt{}, &SendError{Message: err.Error(), Retryable: true}
	}
	reply, err := q.replyMessage(ctx, send)
	if err != nil {
		return Receipt{}, &SendError{Message: "QQ reply context could not be read", Retryable: true}
	}
	// QQ deduplicates by msg_id + msg_seq. A fresh 16-bit sequence prevents
	// split text or separate attachment sends from colliding with each other.
	var random [2]byte
	if _, err := rand.Read(random[:]); err != nil {
		return Receipt{}, &SendError{Message: "QQ message sequence could not be generated", Retryable: true}
	}
	body := map[string]any{"content": send.Text, "msg_type": 0, "msg_seq": int(binary.BigEndian.Uint16(random[:])) + 1}
	if reply != "" {
		body["msg_id"] = reply
	}
	route := "/v2/users/" + id
	if kind == "group" {
		route = "/v2/groups/" + id
	}
	if send.File != nil {
		data, err := qqReadUpload(send, q.Capabilities().UploadLimit)
		if err != nil {
			return Receipt{}, err
		}
		fileType := 4
		for i, prefix := range []string{"image/", "video/", "audio/"} {
			if strings.HasPrefix(send.MediaType, prefix) {
				fileType = i + 1
			}
		}
		var upload struct {
			Info string `json:"file_info"`
		}
		if err := q.call(ctx, http.MethodPost, route+"/files", map[string]any{"file_type": fileType, "file_data": base64.StdEncoding.EncodeToString(data), "file_name": send.FileName, "srv_send_msg": false}, &upload); err != nil {
			return Receipt{}, &SendError{Message: err.Error(), Retryable: true}
		}
		if upload.Info == "" {
			return Receipt{}, &SendError{Message: "QQ media upload did not return file_info", Retryable: true}
		}
		body["msg_type"], body["media"] = 7, map[string]string{"file_info": upload.Info}
	}
	var result struct {
		ID string `json:"id"`
	}
	err = q.call(ctx, http.MethodPost, route+"/messages", body, &result)
	if err != nil {
		var api *qqAPIError
		if errors.As(err, &api) {
			return Receipt{}, &SendError{Message: err.Error(), Unknown: api.Status >= 500, Retryable: api.Status == 429}
		}
		return Receipt{}, &SendError{Message: err.Error(), Unknown: true}
	}
	if result.ID == "" {
		return Receipt{}, &SendError{Message: "QQ accepted response did not identify a message", Unknown: true}
	}
	return Receipt{MessageIDs: []string{result.ID}}, nil
}
