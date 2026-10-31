package channels

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/testkit"
)

func TestNapCatExtendedOperationsRouteOnlyThroughSelectedGroup(t *testing.T) {
	for _, test := range []struct {
		name, method string
		parameters   map[string]any
	}{
		{"get_group_info", "get_group_info", map[string]any{"no_cache": true}},
		{"get_group_info_ex", "get_group_info_ex", nil},
		{"get_members", "get_group_member_list", nil},
		{"get_member", "get_group_member_info", map[string]any{"user_id": "123"}},
		{"get_group_member_list", "get_group_member_list", map[string]any{"no_cache": false}},
		{"get_group_member_info", "get_group_member_info", map[string]any{"user_id": "123", "no_cache": true}},
		{"get_group_honor_info", "get_group_honor_info", map[string]any{"type": "talkative"}},
		{"get_group_at_all_remain", "get_group_at_all_remain", nil},
		{"get_group_shut_list", "get_group_shut_list", nil},
		{"set_group_kick", "set_group_kick", map[string]any{"user_id": "123", "reject_add_request": true}},
		{"set_group_whole_ban", "set_group_whole_ban", map[string]any{"enable": false}},
		{"set_group_admin", "set_group_admin", map[string]any{"user_id": "123", "enable": true}},
		{"set_group_card", "set_group_card", map[string]any{"user_id": "123", "card": ""}},
		{"set_group_name", "set_group_name", map[string]any{"group_name": "New group"}},
		{"set_group_special_title", "set_group_special_title", map[string]any{"user_id": "123", "special_title": "Title"}},
		{"set_group_leave", "set_group_leave", map[string]any{"is_dismiss": false}},
		{"set_group_remark", "set_group_remark", map[string]any{"remark": "My label"}},
		{"_get_group_notice", "_get_group_notice", nil},
		{"_send_group_notice", "_send_group_notice", map[string]any{"content": "Announcement", "pinned": float64(1)}},
		{"get_group_file_system_info", "get_group_file_system_info", nil},
		{"get_group_root_files", "get_group_root_files", map[string]any{"file_count": "150"}},
		{"create_group_file_folder", "create_group_file_folder", map[string]any{"name": "Reports"}},
		{"get_essence_msg_list", "get_essence_msg_list", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if r.URL.Path != "/"+test.method || payload["group_id"] != "456" {
					t.Errorf("escaped selected group: %s %+v", r.URL.Path, payload)
				}
				for key, value := range test.parameters {
					if key != "file_count" && !reflect.DeepEqual(payload[key], value) {
						t.Errorf("parameter changed: %s = %v, want %v", key, payload[key], value)
					}
				}
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"platform_result":"preserved"}}`)
			}))
			defer server.Close()
			raw, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), test.name, domain.Conversation{ExternalID: "group:456"}, test.parameters)
			if err != nil || string(raw) != `{"platform_result":"preserved"}` || calls != 1 {
				t.Fatalf("operation result=%s err=%v calls=%d", raw, err, calls)
			}
		})
	}
}

func TestNapCatUserProfilesStayInsideSelectedChat(t *testing.T) {
	for _, test := range []struct {
		name, chat, user, member string
		allowed                  bool
	}{
		{"private peer", "private:123", "123", "", true},
		{"private inferred peer", "private:123", "", "", true},
		{"other private user", "private:123", "789", "", false},
		{"verified member", "group:456", "123", `{"group_id":456,"user_id":123}`, true},
		{"wrong member group", "group:456", "123", `{"group_id":789,"user_id":123}`, false},
		{"wrong member identity", "group:456", "123", `{"group_id":456,"user_id":789}`, false},
		{"group user required", "group:456", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			profiles := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/get_group_member_info" {
					fmt.Fprint(w, `{"status":"ok","retcode":0,"data":`+test.member+`}`)
					return
				}
				profiles++
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if r.URL.Path != "/get_stranger_info" || payload["user_id"] != "123" || payload["group_id"] != nil {
					t.Errorf("profile lookup escaped selected person: %s %+v", r.URL.Path, payload)
				}
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"user_id":123,"nickname":"Alice"}}`)
			}))
			defer server.Close()
			parameters := map[string]any{}
			if test.user != "" {
				parameters["user_id"] = test.user
			}
			_, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), "get_stranger_info", domain.Conversation{ExternalID: test.chat}, parameters)
			if (err == nil) != test.allowed || !test.allowed && profiles != 0 {
				t.Fatalf("profile authorization err=%v calls=%d", err, profiles)
			}
		})
	}
}

func TestNapCatGroupResourceOperationsVerifyCurrentGroupLists(t *testing.T) {
	for _, test := range []struct {
		name, method string
		parameters   map[string]any
		allowed      bool
	}{
		{"_del_group_notice", "_del_group_notice", map[string]any{"notice_id": "notice-allowed"}, true},
		{"_del_group_notice", "_del_group_notice", map[string]any{"notice_id": "notice-other"}, false},
		{"get_group_files_by_folder", "get_group_files_by_folder", map[string]any{"folder_id": "folder-allowed"}, true},
		{"delete_group_folder", "delete_group_folder", map[string]any{"folder_id": "folder-other"}, false},
		{"delete_group_folder", "delete_group_folder", map[string]any{"folder_id": "/"}, false},
		{"delete_group_folder", "delete_group_folder", map[string]any{"folder_id": "folder-allowed"}, true},
		{"get_group_file_url", "get_group_file_url", map[string]any{"file_id": "file-root"}, true},
		{"get_group_file_url", "get_group_file_url", map[string]any{"file_id": "file-sub", "folder_id": "folder-allowed"}, true},
		{"get_group_file_url", "get_group_file_url", map[string]any{"file_id": "file-sub"}, false},
		{"delete_group_file", "delete_group_file", map[string]any{"file_id": "file-other"}, false},
		{"delete_group_file", "delete_group_file", map[string]any{"file_id": "file-root", "busid": float64(102)}, true},
		{"rename_group_file", "rename_group_file", map[string]any{"file_id": "file-sub", "current_parent_directory": "folder-allowed", "new_name": "report.txt"}, true},
		{"rename_group_file", "rename_group_file", map[string]any{"file_id": "file-other", "current_parent_directory": "/", "new_name": "report.txt"}, false},
		{"move_group_file", "move_group_file", map[string]any{"file_id": "file-root", "current_parent_directory": "/", "target_parent_directory": "folder-allowed"}, true},
		{"move_group_file", "move_group_file", map[string]any{"file_id": "file-root", "current_parent_directory": "/", "target_parent_directory": "folder-other"}, false},
	} {
		t.Run(test.name+fmt.Sprint(test.parameters), func(t *testing.T) {
			actions := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if payload["group_id"] != "456" {
					t.Errorf("resource escaped selected group: %+v", payload)
				}
				switch r.URL.Path {
				case "/_get_group_notice":
					fmt.Fprint(w, `{"status":"ok","retcode":0,"data":[{"notice_id":"notice-allowed"}]}`)
				case "/get_group_root_files":
					fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"files":[{"file_id":"file-root"}],"folders":[{"folder_id":"folder-allowed"}]}}`)
				case "/get_group_files_by_folder":
					if payload["folder_id"] != "folder-allowed" {
						t.Error("unverified folder reached NapCat")
					}
					fmt.Fprint(w, `{"status":"ok","retcode":0,"data":{"files":[{"file_id":"file-sub"}],"folders":[]}}`)
				default:
					actions++
					if r.URL.Path != "/"+test.method {
						t.Errorf("wrong operation: %s", r.URL.Path)
					}
					if (test.name == "get_group_file_url" || test.name == "delete_group_file") && payload["folder_id"] != nil {
						t.Error("Host-only proof parameter leaked to platform")
					}
					fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
				}
			}))
			defer server.Close()
			_, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), test.name, domain.Conversation{ExternalID: "group:456"}, test.parameters)
			if (err == nil) != test.allowed || !test.allowed && actions != 0 {
				t.Fatalf("resource authorization err=%v actions=%d", err, actions)
			}
		})
	}
}

func TestNapCatEssenceMutationsVerifyCurrentMessageMapping(t *testing.T) {
	for _, name := range []string{"set_essence_msg"} {
		for _, group := range []int{456, 789} {
			actions := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/get_msg" {
					fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"message_id":-42,"message_type":"group","group_id":%d}}`, group)
					return
				}
				actions++
				var payload map[string]any
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if r.URL.Path != "/"+name || payload["message_id"] != "-42" || payload["group_id"] != nil {
					t.Errorf("wrong global operation: %s %+v", r.URL.Path, payload)
				}
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
			}))
			_, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), name, domain.Conversation{ExternalID: "group:456"}, map[string]any{"message_id": "-42"})
			server.Close()
			if (err == nil) != (group == 456) || group != 456 && actions != 0 {
				t.Fatalf("essence escaped selected group: %s group=%d err=%v actions=%d", name, group, err, actions)
			}
		}
	}
}

func TestNapCatDeletesHistoricalEssenceOnlyFromCurrentGroupList(t *testing.T) {
	for _, messageID := range []string{"-42", "1951837", "777777"} {
		actions := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["group_id"] != "456" {
				t.Errorf("essence escaped selected group: %+v", payload)
			}
			if r.URL.Path == "/get_essence_msg_list" {
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":[{"message_id":-42,"msg_seq":29,"msg_random":8301},{"message_id":1951837,"msg_seq":"31","msg_random":"0","content":[{"type":"image","data":{"file":"","url":"https://example.com/image"}}]}]}`)
				return
			}
			actions++
			if r.URL.Path != "/delete_essence_msg" || payload["message_id"] != nil {
				t.Errorf("essence deletion used a global message mapping: %s %+v", r.URL.Path, payload)
			}
			wantSeq, wantRandom := "29", "8301"
			if messageID == "1951837" {
				wantSeq, wantRandom = "31", "0"
			}
			if payload["msg_seq"] != wantSeq || payload["msg_random"] != wantRandom {
				t.Errorf("incorrect native essence coordinates: %+v", payload)
			}
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
		}))
		_, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), "delete_essence_msg", domain.Conversation{ExternalID: "group:456"}, map[string]any{"message_id": messageID})
		server.Close()
		allowed := messageID != "777777"
		if (err == nil) != allowed || !allowed && actions != 0 {
			t.Fatalf("historical essence scope: id=%s err=%v actions=%d", messageID, err, actions)
		}
	}
}

func TestNapCatGatewayCanDeleteHistoricalEssenceWithoutImportingIt(t *testing.T) {
	p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
	actions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_essence_msg_list":
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":[{"message_id":1951837,"msg_seq":31,"msg_random":8301}]}`)
		case "/delete_essence_msg":
			actions++
			fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
		default:
			t.Errorf("historical essence attempted an unrelated lookup: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	s := New(p.Store, p.Harness)
	connection := domain.Connection{Meta: domain.Meta{ID: "napcat-account"}, AgentID: p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all", Config: encoded(NapCatConfig{BaseURL: server.URL, WebSocketURL: strings.Replace(server.URL, "http:", "ws:", 1) + "/ws"})}
	if err := p.Store.Save(p.Context, "connections", &connection.Meta, &connection); err != nil {
		t.Fatal(err)
	}
	chat := domain.Conversation{Meta: domain.Meta{ID: "allowed-group"}, ConnectionID: connection.ID, ExternalID: "group:456", Kind: "group", Title: "Allowed"}
	if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
		t.Fatal(err)
	}
	_, err := s.AccountOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}, connection.ID, "delete_essence_msg", ChatOperationRequest{ConversationID: chat.ID, Parameters: map[string]any{"message_id": "1951837"}})
	if err != nil || actions != 1 {
		t.Fatalf("historical essence could not be deleted through Gateway: %v actions=%d", err, actions)
	}
	if _, err := p.Store.Message(p.Context, chat.ID, "1951837"); !domain.IsMissing(err) {
		t.Fatalf("historical essence was imported into IM archive: %v", err)
	}
	if p.Requests.Load() != 0 {
		t.Fatal("platform operation woke the main Agent")
	}
}

func TestNapCatGatewayCanSetUnarchivedMessageAsEssenceOnlyInSelectedGroup(t *testing.T) {
	for _, remoteGroup := range []int{456, 789} {
		t.Run(fmt.Sprint(remoteGroup), func(t *testing.T) {
			p := testkit.New(t, 0, func(context.Context, int, map[string]any) any { return testkit.Text("unused") })
			actions := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/get_msg" {
					fmt.Fprintf(w, `{"status":"ok","retcode":0,"data":{"message_id":-42,"message_type":"group","group_id":%d,"user_id":123}}`, remoteGroup)
					return
				}
				actions++
				if r.URL.Path != "/set_essence_msg" {
					t.Errorf("unexpected essence operation: %s", r.URL.Path)
				}
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
			}))
			defer server.Close()
			s := New(p.Store, p.Harness)
			connection := domain.Connection{Meta: domain.Meta{ID: "napcat-account"}, AgentID: p.Agent.ID, Adapter: "qq.napcat", Platform: "qq", AccountID: "999", Enabled: true, AccessMode: "all", Config: encoded(NapCatConfig{BaseURL: server.URL, WebSocketURL: strings.Replace(server.URL, "http:", "ws:", 1) + "/ws"})}
			if err := p.Store.Save(p.Context, "connections", &connection.Meta, &connection); err != nil {
				t.Fatal(err)
			}
			chat := domain.Conversation{Meta: domain.Meta{ID: "allowed-group"}, ConnectionID: connection.ID, ExternalID: "group:456", Kind: "group", Title: "Allowed"}
			if err := p.Store.Save(p.Context, "conversations", &chat.Meta, &chat); err != nil {
				t.Fatal(err)
			}
			_, err := s.AccountOperation(p.Context, domain.ExecutionBinding{AgentID: p.Agent.ID, SessionID: p.Session.ID}, connection.ID, "set_essence_msg", ChatOperationRequest{ConversationID: chat.ID, Parameters: map[string]any{"message_id": "-42"}})
			allowed := remoteGroup == 456
			if (err == nil) != allowed || actions != map[bool]int{true: 1, false: 0}[allowed] {
				t.Fatalf("unarchived essence scope: err=%v actions=%d", err, actions)
			}
			if _, err := p.Store.Message(p.Context, chat.ID, "-42"); !domain.IsMissing(err) {
				t.Fatalf("setting essence imported original to IM archive: %v", err)
			}
			if p.Requests.Load() != 0 {
				t.Fatal("setting essence woke the main Agent")
			}
		})
	}
}

func TestNapCatEssenceReadPreservesOrderedTextAndMedia(t *testing.T) {
	const native = `[{"msg_seq":29,"msg_random":8301,"sender_id":123,"sender_nick":"Alice","operator_id":999,"operator_nick":"Moderator","message_id":-42,"operator_time":1793451200,"content":[{"type":"text","data":{"text":"Before the photo"}},{"type":"image","data":{"file":"image-reference","url":"https://multimedia.nt.qq.com.cn/example","sub_type":0}},{"type":"text","data":{"text":"After the photo"}},{"type":"video","data":{"file":"video-reference","url":"https://multimedia.nt.qq.com.cn/video"}}]}]`
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/get_essence_msg_list" {
			t.Errorf("essence read fetched media or another resource: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"status":"ok","retcode":0,"data":`+native+`}`)
	}))
	defer server.Close()
	raw, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), "get_essence_msg_list", domain.Conversation{ExternalID: "group:456"}, nil)
	if err != nil || string(raw) != native || calls != 1 {
		t.Fatalf("essence rich content changed or was fetched eagerly: %s err=%v calls=%d", raw, err, calls)
	}
}

func TestNapCatOperationUploadsStreamComputerBytes(t *testing.T) {
	for _, test := range []struct {
		name, field string
		parameters  map[string]any
	}{
		{"upload_group_file", "file", map[string]any{"name": "report.txt"}},
		{"set_group_portrait", "file", nil},
		{"_send_group_notice", "image", map[string]any{"content": "Notice"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			content := strings.Repeat("Computer content", 10000)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				wire, _ := payload[test.field].(string)
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(wire, "base64://"))
				if err != nil || string(decoded) != content || payload["group_id"] != "456" || r.URL.Path != "/"+test.name {
					t.Errorf("invalid streamed upload: path=%s group=%v decode=%v", r.URL.Path, payload["group_id"], err)
				}
				fmt.Fprint(w, `{"status":"ok","retcode":0,"data":null}`)
			}))
			defer server.Close()
			uploads := map[string]Download{test.field: {Body: io.NopCloser(strings.NewReader(content)), Size: int64(len(content)), Name: "report.txt"}}
			_, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), test.name, domain.Conversation{ExternalID: "group:456"}, test.parameters, uploads)
			if err != nil || calls != 1 {
				t.Fatalf("streamed upload err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestNapCatExtendedOperationsRejectBadParametersBeforeCallingPlatform(t *testing.T) {
	for _, test := range []struct {
		name, chat string
		parameters map[string]any
	}{
		{"set_group_whole_ban", "group:456", nil},
		{"set_group_admin", "group:456", map[string]any{"user_id": "123", "enable": "true"}},
		{"set_group_name", "private:123", map[string]any{"group_name": "Wrong chat"}},
		{"set_group_name", "group:456", map[string]any{"group_name": ""}},
		{"set_group_kick", "group:456", map[string]any{"user_id": "123", "group_id": "789"}},
		{"get_stranger_info", "group:456", map[string]any{"user_id": float64(1 << 54)}},
		{"get_stranger_info", "private:123", map[string]any{"user_id": 1.5}},
		{"get_group_root_files", "group:456", map[string]any{"file_count": float64(10001)}},
		{"get_group_honor_info", "group:456", map[string]any{"type": "unknown"}},
		{"_send_group_notice", "group:456", map[string]any{"content": "notice", "image": "file:///napcat/private.png"}},
		{"upload_group_file", "group:456", map[string]any{"name": "secret", "file": "/napcat/private"}},
		{"upload_group_file", "group:456", map[string]any{"name": "secret", "file": "https://example.com/file"}},
		{"set_group_portrait", "group:456", nil},
	} {
		t.Run(test.name+fmt.Sprint(test.parameters), func(t *testing.T) {
			n := &napcat{}
			if _, err := napcatOperation(context.Background(), n, test.name, domain.Conversation{ExternalID: test.chat}, test.parameters); err == nil {
				t.Fatalf("accepted invalid operation: %+v", test)
			}
		})
	}
}

func TestNapCatExtendedMutationsClassifyUncertainFailure(t *testing.T) {
	for _, name := range []string{"set_group_kick", "set_group_admin", "set_group_whole_ban", "set_group_name", "create_group_file_folder", "_send_group_notice"} {
		for _, response := range []string{`{"status":"failed","retcode":200}`, `broken`} {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; fmt.Fprint(w, response) }))
			params := map[string]any{}
			spec := napcatOperationSpecs[slices.IndexFunc(napcatOperationSpecs, func(s napcatOperationSpec) bool { return s.name == name })]
			for _, p := range spec.parameters {
				if p.required {
					switch p.kind {
					case 'u':
						params[p.name] = "123"
					case 's':
						params[p.name] = "Content"
					case 'b':
						params[p.name] = true
					}
				}
			}
			_, err := napcatOperation(context.Background(), napcatTestAdapter(t, server), name, domain.Conversation{ExternalID: "group:456"}, params)
			server.Close()
			var uncertain *SendError
			if !errors.As(err, &uncertain) || !uncertain.Unknown || calls != 1 {
				t.Fatalf("mutation %s lost uncertainty or retried: %v calls=%d", name, err, calls)
			}
		}
	}
}

func TestNapCatCatalogIncludesOnlyScopedOperationsAndUploadFields(t *testing.T) {
	operations := NapCatOperationsCatalog()
	for _, name := range []string{"get_members", "get_stranger_info", "set_group_kick", "_send_group_notice", "delete_group_file", "get_essence_msg_list", "set_essence_msg", "delete_essence_msg"} {
		if !slices.ContainsFunc(operations, func(op ChatOperation) bool { return op.Name == name }) {
			t.Errorf("missing platform operation: %s", name)
		}
	}
	for _, op := range operations {
		if op.AccountScoped || slices.Contains(op.Parameters, "group_id") || slices.Contains(op.Parameters, "url") {
			t.Errorf("operation exposes alternate routing: %+v", op)
		}
		if len(op.FileFields) > 0 && op.Name != "upload_group_file" && op.Name != "set_group_portrait" && op.Name != "_send_group_notice" {
			t.Errorf("unexpected upload operation: %+v", op)
		}
	}
}
