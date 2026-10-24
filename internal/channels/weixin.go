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
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
)

// Wire behavior follows Tencent/openclaw-weixin at protocol version 2.4.10.
// See https://github.com/Tencent/openclaw-weixin/blob/main/docs/protocol.md.
// Astral owns delivery, access, sessions, and receipts; this adapter only handles
// iLink transport, normalization, and the per-peer context needed by that API.
const weixinProtocolVersion = "2.4.10"

type WeixinConfig struct {
	BaseURL    string `json:"base_url,omitempty"`
	CDNBaseURL string `json:"cdn_base_url,omitempty"`
}

type weixinAdapter struct {
	connection domain.Connection
	credential WeixinCredential
	config     WeixinConfig
	client     *http.Client
	store      *persistence.Store
	mu         sync.Mutex
	wire       map[string]string
}

// setStore is injected by the channel Service. Context tokens and CDN keys stay
// in its existing encrypted secret store, rather than model-visible events or
// editable Connection.Config. Poll saves these before committing its cursor.
func (w *weixinAdapter) setStore(store *persistence.Store) { w.store = store }

func weixinHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	owned := *client
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &owned
}

func NewWeixin(connection domain.Connection, secret string, client *http.Client) (Adapter, error) {
	var config WeixinConfig
	if len(connection.Config) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(connection.Config))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return nil, domain.Invalid("invalid Weixin connection config")
		}
	}
	var credential WeixinCredential
	if json.Unmarshal([]byte(secret), &credential) != nil || credential.BotToken == "" || credential.BotID == "" || credential.UserID == "" || strings.ContainsAny(credential.BotToken, "\r\n") {
		return nil, domain.Invalid("Weixin requires a QR-issued login grant; sign in with Weixin")
	}
	if credential.BaseURL == "" {
		credential.BaseURL = weixinDefaultURL
	}
	if config.BaseURL == "" {
		config.BaseURL = credential.BaseURL
	}
	if config.CDNBaseURL == "" {
		config.CDNBaseURL = weixinDefaultCDN
	}
	for _, endpoint := range []string{config.BaseURL, config.CDNBaseURL} {
		if err := modelbridge.ValidateURL(endpoint); err != nil {
			return nil, err
		}
	}
	config.BaseURL, config.CDNBaseURL = strings.TrimRight(config.BaseURL, "/"), strings.TrimRight(config.CDNBaseURL, "/")
	return &weixinAdapter{connection: connection, credential: credential, config: config, client: weixinHTTPClient(client), wire: map[string]string{}}, nil
}

func (w *weixinAdapter) Capabilities() Capabilities {
	return Capabilities{Adapter: "weixin.ilink", Platform: "weixin", Name: "微信 Bot (iLink)", ConversationKinds: []string{"private"}, FileSending: true,
		MessageFormats: []string{"text", "markdown"}, MessageParameters: []string{},
		TextLimit: 4000, CaptionLimit: 0}
}

type weixinAPIError struct{ Code int }

func (e *weixinAPIError) Error() string {
	if e.Code == -14 {
		return "Weixin login session expired; sign in with Weixin again"
	}
	return fmt.Sprintf("Weixin rejected the request (code %d)", e.Code)
}

func (e *weixinAPIError) Unwrap() error { return domain.Offline(e.Error()) }

func weixinRequest(ctx context.Context, client *http.Client, base, token, method, endpoint string, body, result any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(encoded(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+endpoint, reader)
	if err != nil {
		return domain.Invalid("invalid Weixin endpoint")
	}
	request.Header.Set("iLink-App-Id", "bot")
	request.Header.Set("iLink-App-ClientVersion", "132106") // 0x0002040A.
	if method == http.MethodPost {
		uin := make([]byte, 4)
		if _, err := rand.Read(uin); err != nil {
			return domain.Offline("could not generate Weixin request metadata")
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("AuthorizationType", "ilink_bot_token")
		request.Header.Set("X-WECHAT-UIN", base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(binary.BigEndian.Uint32(uin)), 10))))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return domain.Offline("Weixin request could not be completed")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return domain.Offline("Weixin response was interrupted")
	}
	if len(raw) > 8<<20 {
		return domain.Offline("Weixin response exceeded the size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Response bodies and errors can echo bearer tokens, context tokens, and
		// CDN keys. Expose only protocol codes, never arbitrary remote text.
		return domain.Offline(fmt.Sprintf("Weixin returned HTTP %d", response.StatusCode))
	}
	var status struct {
		Ret     *int `json:"ret"`
		ErrCode int  `json:"errcode"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return domain.Offline("Weixin returned invalid JSON")
	}
	if status.ErrCode != 0 {
		return &weixinAPIError{Code: status.ErrCode}
	}
	if status.Ret != nil && *status.Ret != 0 {
		return &weixinAPIError{Code: *status.Ret}
	}
	if result != nil && json.Unmarshal(raw, result) != nil {
		return domain.Offline("Weixin returned an invalid result")
	}
	return nil
}

func (w *weixinAdapter) call(ctx context.Context, endpoint string, body map[string]any, result any) error {
	body["base_info"] = map[string]string{"channel_version": weixinProtocolVersion, "bot_agent": "Astral/1.0"}
	return weixinRequest(ctx, w.client, w.config.BaseURL, w.credential.BotToken, http.MethodPost, "/ilink/bot/"+endpoint, body, result)
}

func (w *weixinAdapter) Validate(ctx context.Context) (Identity, error) {
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	// The server obtains this identity from a confirmed Tencent QR grant;
	// public SaveConnection cannot create or replace Weixin credentials.
	// getconfig supplies per-peer typing configuration, not account identity,
	// and must not gate accepting an already confirmed authorization.
	return Identity{ID: w.credential.BotID, Name: w.credential.BotID, Diagnostics: []string{
		"iLink supports private chats received by this account. Contact enumeration, group chats, outbound quote references, edits and delivery confirmations are not exposed by this adapter.",
		"The account identity comes from QR authorization; iLink has no separate bot identity endpoint. Files are streamed; file captions are separate messages and platform file limits are reported by iLink.",
	}}, nil
}

// uint64 message IDs must survive JSON decoding without float64 rounding.
type weixinID string

func (id *weixinID) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		*id = ""
		return nil
	}
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		*id = weixinID(value)
		return nil
	}
	if _, err := strconv.ParseUint(string(raw), 10, 64); err != nil {
		return err
	}
	*id = weixinID(raw)
	return nil
}
func (id weixinID) MarshalJSON() ([]byte, error) { return json.Marshal(string(id)) }

type weixinMessage struct {
	ID           weixinID     `json:"message_id"`
	Sequence     weixinID     `json:"seq"`
	From         string       `json:"from_user_id"`
	To           string       `json:"to_user_id"`
	ClientID     string       `json:"client_id"`
	Created      int64        `json:"create_time_ms"`
	GroupID      string       `json:"group_id"`
	Type         int          `json:"message_type"`
	State        int          `json:"message_state"`
	Items        []weixinItem `json:"item_list"`
	ContextToken string       `json:"context_token"`
}

func (w *weixinAdapter) Poll(ctx context.Context, cursor string) (Batch, error) {
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	var response struct {
		Messages []weixinMessage `json:"msgs"`
		Cursor   string          `json:"get_updates_buf"`
	}
	if err := w.call(ctx, "getupdates", map[string]any{"get_updates_buf": cursor}, &response); err != nil {
		// A normal long-poll timeout doesn't invalidate or reset the committed
		// cursor. Cancellation of the worker itself still propagates normally.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
			return Batch{NextCursor: cursor}, nil
		}
		return Batch{}, err
	}
	batch := Batch{NextCursor: cursor}
	if response.Cursor != "" {
		batch.NextCursor = response.Cursor
	}
	for _, message := range response.Messages {
		// Group fields exist in the types but do not establish group support.
		// Nor should streaming fragments or bot echoes activate private sessions.
		if message.From == "" || message.GroupID != "" || message.Type != 1 || message.State == 1 {
			continue
		}
		if message.ContextToken != "" {
			if err := w.putWire(ctx, "context", message.From, message.ContextToken); err != nil {
				return Batch{}, err
			}
		}
		event, err := w.normalize(ctx, message)
		if err != nil {
			return Batch{}, err
		}
		if event.Observation.MessageID != "" {
			batch.Events = append(batch.Events, event)
		}
	}
	return batch, nil
}

func (w *weixinAdapter) conversation(id string) domain.Conversation {
	return domain.Conversation{Meta: domain.Meta{ID: stableID("conversation", w.connection.ID, id, "")}, ConnectionID: w.connection.ID, ExternalID: id, Kind: "private", Title: id}
}

func (w *weixinAdapter) ResolveConversation(ctx context.Context, id, topic string) (domain.Conversation, error) {
	if topic != "" {
		return domain.Conversation{}, domain.Invalid("Weixin iLink does not expose Topics")
	}
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, "\r\n") || len(id) > 512 {
		return domain.Conversation{}, domain.Invalid("a Weixin iLink user ID is required")
	}
	if token, err := w.getWire(ctx, "context", id); err != nil {
		return domain.Conversation{}, err
	} else if token == "" {
		return domain.Conversation{}, domain.Invalid("this Weixin user has not messaged this account; send a private message to the Bot first")
	}
	return w.conversation(id), nil
}

type weixinWireRecord struct {
	domain.Meta
	CredentialID string `json:"credential_id"`
}

func (w *weixinAdapter) wireID(kind, id string) string {
	return stableID("weixin_"+kind, w.connection.ID, w.credential.BotID, id)
}

func (w *weixinAdapter) putWire(ctx context.Context, kind, id, value string) error {
	key := w.wireID(kind, id)
	if w.store == nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.wire[key] = value
		return nil
	}
	var record weixinWireRecord
	err := w.store.Get(ctx, "weixin_wire", key, &record)
	if err == nil {
		return w.store.RotateSecret(ctx, record.CredentialID, value)
	}
	if !domain.IsMissing(err) {
		return err
	}
	record.ID = key
	return w.store.Update(ctx, func(tx *persistence.Tx) error {
		var err error
		record.CredentialID, err = tx.PutSecret(ctx, value)
		if err != nil {
			return err
		}
		return tx.Save(ctx, "weixin_wire", &record.Meta, &record)
	})
}

func (w *weixinAdapter) getWire(ctx context.Context, kind, id string) (string, error) {
	key := w.wireID(kind, id)
	if w.store == nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.wire[key], nil
	}
	var record weixinWireRecord
	if err := w.store.Get(ctx, "weixin_wire", key, &record); err != nil {
		if domain.IsMissing(err) {
			return "", nil
		}
		return "", err
	}
	return w.store.Secret(ctx, record.CredentialID)
}

func (w *weixinAdapter) SplitText(text string) ([]string, error) {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return nil, domain.Invalid("message text is empty or invalid UTF-8")
	}
	var chunks []string
	runes := []rune(text)
	for len(runes) > 0 {
		end := min(len(runes), w.Capabilities().TextLimit)
		if end < len(runes) {
			for i := end; i > end/2; i-- {
				if runes[i-1] == '\n' || runes[i-1] == ' ' {
					end = i
					break
				}
			}
		}
		chunks = append(chunks, string(runes[:end]))
		runes = runes[end:]
	}
	return chunks, nil
}

func (w *weixinAdapter) Send(ctx context.Context, send Send) (Receipt, error) {
	if send.ReplyTo != "" {
		return Receipt{}, &SendError{Message: "Weixin iLink does not expose outbound quote references"}
	}
	// Outbox snapshots retain the immutable external destination and Topic,
	// without copying display metadata such as Conversation.Kind.
	if send.Target.TopicID != "" || send.Target.Kind != "" && send.Target.Kind != "private" || send.Target.ExternalID == "" {
		return Receipt{}, &SendError{Message: "Weixin iLink only supports private chats"}
	}
	if send.Target.ConnectionID != "" && send.Target.ConnectionID != w.connection.ID {
		return Receipt{}, &SendError{Message: "Weixin destination belongs to another account"}
	}
	contextToken, err := w.getWire(ctx, "context", send.Target.ExternalID)
	if err != nil {
		return Receipt{}, &SendError{Message: "Weixin conversation context is unavailable"}
	}
	if contextToken == "" {
		return Receipt{}, &SendError{Message: "this Weixin user must message the Bot before it can reply"}
	}
	if send.File == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
	}
	var item weixinItem
	if send.File == nil {
		if !utf8.ValidString(send.Text) || strings.TrimSpace(send.Text) == "" || len([]rune(send.Text)) > w.Capabilities().TextLimit {
			return Receipt{}, &SendError{Message: "Weixin text fragment is empty or too long"}
		}
		item = weixinItem{Type: 1, Text: &weixinText{Text: send.Text}}
	} else {
		defer send.File.Close()
		if send.Text != "" {
			return Receipt{}, &SendError{Message: "Weixin file captions must be queued as separate text messages"}
		}
		item, err = w.upload(ctx, send)
		if err != nil {
			// CDN preparation does not send a chat message, so this failure is
			// known even when an upload response was lost. Never retry internally.
			return Receipt{}, &SendError{Message: err.Error(), Retryable: true}
		}
	}
	clientID := domain.ID("astral_weixin")
	var response struct {
		Ret *int     `json:"ret"`
		ID  weixinID `json:"message_id"`
	}
	err = w.call(ctx, "sendmessage", map[string]any{"msg": map[string]any{
		"from_user_id": "", "to_user_id": send.Target.ExternalID, "client_id": clientID,
		"message_type": 2, "message_state": 2, "context_token": contextToken, "item_list": []weixinItem{item},
	}}, &response)
	if err != nil {
		var rejected *weixinAPIError
		if errors.As(err, &rejected) {
			return Receipt{}, &SendError{Message: rejected.Error(), Retryable: rejected.Code != -14}
		}
		return Receipt{}, &SendError{Message: "Weixin send outcome is unknown; inspect the chat before any retry", Unknown: true}
	}
	if response.Ret == nil {
		return Receipt{}, &SendError{Message: "Weixin returned no explicit acceptance status; inspect the chat before any retry", Unknown: true}
	}
	id := string(response.ID)
	if id == "" {
		// iLink's documented successful response may omit a server message ID.
		// Retain the actual outgoing client_id, qualified to prevent confusion
		// with server IDs used by inbound quote references.
		id = "client:" + clientID
	}
	return Receipt{MessageIDs: []string{id}}, nil
}
