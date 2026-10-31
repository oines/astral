package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

type telegramUserCall struct {
	method     string
	parameters map[string]any
}

type telegramUserFixture struct {
	mu       sync.Mutex
	calls    []telegramUserCall
	member   map[string]any
	response any
	before   func(string)
	server   *httptest.Server
}

func newTelegramUserFixture(t *testing.T) (*testkit.Platform, *Service, *telegramUserFixture, domain.Connection) {
	t.Helper()
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	s := New(p.Store, p.Harness)
	f := &telegramUserFixture{response: map[string]any{"profile_metadata": true, "unknown_future_field": "preserved"}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/bottest-token/")
		var parameters map[string]any
		if err := json.NewDecoder(r.Body).Decode(&parameters); err != nil {
			t.Error(err)
		}
		reply := func(value any) { _ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": value}) }
		switch method {
		case "getMe":
			reply(telegramUser{ID: 999, IsBot: true, Username: "astral_bot", FirstName: "Astral"})
		case "getWebhookInfo":
			reply(map[string]any{"url": ""})
		case "getChat":
			id, _ := strconv.ParseInt(parameters["chat_id"].(string), 10, 64)
			kind := "supergroup"
			if id > 0 {
				kind = "private"
			}
			reply(telegramChat{ID: id, Title: "Selected chat", Type: kind, IsForum: kind == "supergroup"})
		default:
			f.mu.Lock()
			f.calls = append(f.calls, telegramUserCall{method, parameters})
			member, response, before := f.member, f.response, f.before
			f.mu.Unlock()
			if before != nil {
				before(method)
			}
			switch method {
			case "getChatMember":
				if member == nil {
					member = map[string]any{"status": "member", "user": map[string]any{"id": parameters["user_id"], "first_name": "Ada"}}
				}
				reply(member)
			case "getUserProfilePhotos", "getUserProfileAudios", "getUserPersonalChatMessages":
				reply(response)
			case "banChatMember", "restrictChatMember", "setChatTitle", "approveChatJoinRequest":
				reply(true)
			case "getChatAdministrators":
				reply([]any{map[string]any{"status": "administrator", "user": map[string]any{"id": 42, "first_name": "Ada"}}})
			default:
				t.Errorf("unexpected Telegram method %s", method)
				http.NotFound(w, r)
			}
		}
	}))
	t.Cleanup(f.server.Close)
	connection := domain.Connection{AgentID: p.Agent.ID, Name: "test bot", Adapter: "telegram.bot", Enabled: true, Config: encoded(TelegramConfig{BaseURL: f.server.URL})}
	token := "test-token"
	if err := s.SaveConnection(p.Context, &connection, &token); err != nil {
		t.Fatal(err)
	}
	return p, s, f, connection
}

func (f *telegramUserFixture) recorded() []telegramUserCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func TestTelegramUserOperationsScopeAndPreserveMetadata(t *testing.T) {
	p, s, bot, account := newTelegramUserFixture(t)
	group := createBinding(t, p, s, account, "-100", "")
	private := createBinding(t, p, s, account, "42", "")
	caller := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
	catalog, err := s.AccountOperations(p.Context, p.Agent.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"getUserProfilePhotos", "getUserProfileAudios", "getUserPersonalChatMessages"} {
		op := slices.IndexFunc(catalog, func(op ChatOperation) bool { return op.Name == method })
		if op < 0 || catalog[op].AccountScoped {
			t.Fatalf("user read missing or escaped chat scope: %s", method)
		}
		for _, target := range []domain.ConversationBinding{group, private} {
			parameters := map[string]any{"limit": float64(2)}
			if target.ID == group.ID {
				parameters["user_id"] = "42"
			}
			if method != "getUserPersonalChatMessages" {
				parameters["offset"] = float64(0)
			}
			result, err := s.AccountOperation(p.Context, caller, account.ID, method, ChatOperationRequest{ConversationID: target.ConversationID, Parameters: parameters})
			if err != nil || string(result.Result) != string(encoded(bot.response)) {
				t.Fatalf("%s result changed: %s %v", method, result.Result, err)
			}
			if target.ID == group.ID && parameters["user_id"] != "42" {
				t.Fatal("operation mutated caller parameters")
			}
		}
	}
	calls := bot.recorded()
	if len(calls) != 9 {
		t.Fatalf("unexpected calls: %+v", calls)
	}
	for i := 0; i < len(calls); i += 3 {
		if calls[i].method != "getChatMember" || calls[i].parameters["chat_id"] != "-100" || calls[i].parameters["user_id"] != float64(42) {
			t.Fatalf("missing group membership proof: %+v", calls[i])
		}
		for _, call := range calls[i+1 : i+3] {
			if call.parameters["user_id"] != float64(42) || call.parameters["chat_id"] != nil {
				t.Fatalf("profile read acquired alternate routing: %+v", call)
			}
		}
	}
	for _, target := range []domain.ConversationBinding{group, private} {
		events, err := p.Store.Observations(p.Context, target.ConversationID, 0, 20)
		if err != nil || len(events) != 0 {
			t.Fatalf("user resource was archived: %+v %v", events, err)
		}
	}
	if p.Requests.Load() != 0 {
		t.Fatal("reading user resources activated a model")
	}
}

func TestTelegramUserPersonalChatMessagesRemainUnarchivedProfileData(t *testing.T) {
	p, s, bot, account := newTelegramUserFixture(t)
	chat := createBinding(t, p, s, account, "42", "")
	bot.response = []any{map[string]any{"message_id": 37, "date": 1700000000, "chat": map[string]any{"id": -777, "type": "channel", "title": "Linked personal channel"}, "text": "Profile-linked content"}}
	before, err := list[domain.Conversation](p.Context, p.Store, "conversations")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.AccountOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: chat.SessionID}, account.ID, "getUserPersonalChatMessages", ChatOperationRequest{Parameters: map[string]any{"limit": "20"}})
	if err != nil || string(result.Result) != string(encoded(bot.response)) {
		t.Fatalf("linked profile messages changed: %s %v", result.Result, err)
	}
	after, err := list[domain.Conversation](p.Context, p.Store, "conversations")
	if err != nil || len(before) != len(after) {
		t.Fatalf("profile read discovered an unauthorized chat: %d -> %d %v", len(before), len(after), err)
	}
	events, err := p.Store.Observations(p.Context, chat.ConversationID, 0, 20)
	if err != nil || len(events) != 0 || p.Requests.Load() != 0 {
		t.Fatalf("profile messages became inbound observations: %+v %v", events, err)
	}
}

func TestTelegramUserOperationsRejectInvalidAndAlternateTargetsBeforeDispatch(t *testing.T) {
	p, s, bot, account := newTelegramUserFixture(t)
	group := createBinding(t, p, s, account, "-100", "")
	private := createBinding(t, p, s, account, "42", "")
	topic := createBinding(t, p, s, account, "-200", "22")
	caller := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}
	for name, test := range map[string]struct {
		method string
		chat   string
		params map[string]any
		source string
		files  map[string]string
	}{
		"group needs user":        {chat: group.ConversationID},
		"foreign private user":    {chat: private.ConversationID, params: map[string]any{"user_id": "43"}},
		"fractional user":         {chat: group.ConversationID, params: map[string]any{"user_id": 42.5}},
		"rounded user":            {chat: group.ConversationID, params: map[string]any{"user_id": float64(1 << 53)}},
		"negative user":           {chat: group.ConversationID, params: map[string]any{"user_id": "-42"}},
		"zero user":               {chat: group.ConversationID, params: map[string]any{"user_id": "0"}},
		"whitespace user":         {chat: group.ConversationID, params: map[string]any{"user_id": " 42"}},
		"negative offset":         {chat: private.ConversationID, params: map[string]any{"offset": -1}},
		"too many photos":         {chat: private.ConversationID, params: map[string]any{"limit": 101}},
		"zero limit":              {chat: private.ConversationID, params: map[string]any{"limit": 0}},
		"personal limit required": {method: "getUserPersonalChatMessages", chat: private.ConversationID},
		"personal limit maximum":  {method: "getUserPersonalChatMessages", chat: private.ConversationID, params: map[string]any{"limit": 21}},
		"chat override":           {chat: private.ConversationID, params: map[string]any{"chat_id": "43"}},
		"nested alternate route":  {chat: private.ConversationID, params: map[string]any{"business_connection_id": "foreign"}},
		"source chat":             {chat: private.ConversationID, source: group.ConversationID},
		"files":                   {chat: private.ConversationID, files: map[string]string{"photo": "/tmp/unread"}},
		"topic grant":             {chat: topic.ConversationID, params: map[string]any{"user_id": "42"}},
	} {
		t.Run(name, func(t *testing.T) {
			method := test.method
			if method == "" {
				method = "getUserProfilePhotos"
			}
			_, err := s.AccountOperation(p.Context, caller, account.ID, method, ChatOperationRequest{ConversationID: test.chat, SourceConversationID: test.source, Parameters: test.params, Files: test.files})
			if err == nil {
				t.Fatal("invalid user read reached Telegram")
			}
		})
	}
	if len(bot.recorded()) != 0 {
		t.Fatalf("invalid requests reached Telegram: %+v", bot.recorded())
	}
}

func TestTelegramUserGroupReadsRequireCurrentMembership(t *testing.T) {
	for _, test := range []struct {
		status string
		member bool
		user   int64
		allow  bool
	}{
		{"creator", false, 42, true}, {"administrator", false, 42, true}, {"member", false, 42, true},
		{"restricted", true, 42, true}, {"restricted", false, 42, false},
		{"left", false, 42, false}, {"kicked", false, 42, false}, {"member", false, 43, false},
	} {
		t.Run(fmt.Sprintf("%s/%t/%d", test.status, test.member, test.user), func(t *testing.T) {
			p, s, bot, account := newTelegramUserFixture(t)
			chat := createBinding(t, p, s, account, "-100", "")
			bot.member = map[string]any{"status": test.status, "is_member": test.member, "user": map[string]any{"id": test.user}}
			_, err := s.AccountOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: chat.SessionID}, account.ID, "getUserProfilePhotos", ChatOperationRequest{Parameters: map[string]any{"user_id": "42"}})
			if (err == nil) != test.allow {
				t.Fatalf("membership decision: allowed=%t error=%v", test.allow, err)
			}
			want := 1
			if test.allow {
				want = 2
			}
			if len(bot.recorded()) != want {
				t.Fatalf("unexpected reads: %+v", bot.recorded())
			}
		})
	}
}

func TestTelegramUserReadsRecheckRevokedChatAccess(t *testing.T) {
	for _, during := range []string{"getChatMember", "getUserProfilePhotos"} {
		t.Run(during, func(t *testing.T) {
			p, s, bot, account := newTelegramUserFixture(t)
			chat := createBinding(t, p, s, account, "-100", "")
			bot.before = func(method string) {
				if method == during {
					chat.Enabled = false
					if err := p.Store.Save(p.Context, "bindings", &chat.Meta, &chat); err != nil {
						t.Error(err)
					}
				}
			}
			_, err := s.AccountOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: chat.SessionID}, account.ID, "getUserProfilePhotos", ChatOperationRequest{Parameters: map[string]any{"user_id": "42"}})
			if err == nil {
				t.Fatal("revoked access returned profile data")
			}
			if during == "getChatMember" && len(bot.recorded()) != 1 {
				t.Fatal("profile read continued after chat access was revoked")
			}
		})
	}
}

func TestTelegramExistingMemberAndManagementMethodsDispatchWithinSelectedChat(t *testing.T) {
	p, s, bot, account := newTelegramUserFixture(t)
	chat := createBinding(t, p, s, account, "-100", "")
	caller := domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: chat.SessionID}
	for method, parameters := range map[string]map[string]any{
		"getChatAdministrators":  {"return_bots": true},
		"getChatMember":          {"user_id": "42"},
		"banChatMember":          {"user_id": "42", "revoke_messages": true},
		"restrictChatMember":     {"user_id": "42", "permissions": map[string]any{"can_send_messages": false}},
		"approveChatJoinRequest": {"user_id": "42"},
		"setChatTitle":           {"title": "Updated group"},
	} {
		if _, err := s.AccountOperation(p.Context, caller, account.ID, method, ChatOperationRequest{Parameters: parameters}); err != nil {
			t.Fatalf("%s did not dispatch: %v", method, err)
		}
	}
	for _, call := range bot.recorded() {
		if call.parameters["chat_id"] != "-100" {
			t.Fatalf("operation escaped selected chat: %+v", call)
		}
	}
	if len(bot.recorded()) != 6 {
		t.Fatalf("unexpected management requests: %+v", bot.recorded())
	}
}
