package channels

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/domain"
)

type napcatParameter struct {
	name     string
	kind     byte
	required bool
	min, max int64
	fallback any
}

type napcatOperationSpec struct {
	name, method             string
	group, message, mutation bool
	archive                  bool
	parameters               []napcatParameter
	files                    []string
}

func napcatString(name string, required bool, min, max int64) napcatParameter {
	return napcatParameter{name: name, kind: 's', required: required, min: min, max: max}
}
func napcatBool(name string, required bool) napcatParameter {
	return napcatParameter{name: name, kind: 'b', required: required}
}
func napcatInt(name string, required bool, min, max int64, fallback any) napcatParameter {
	return napcatParameter{name: name, kind: 'i', required: required, min: min, max: max, fallback: fallback}
}

var napcatOperationSpecs = func() []napcatOperationSpec {
	user := napcatParameter{name: "user_id", kind: 'u', required: true}
	optionalUser := user
	optionalUser.required = false
	message := napcatString("message_id", true, 1, 20)
	noCache := napcatBool("no_cache", false)
	folder := napcatString("folder_id", false, 0, 1024)
	file := napcatString("file_id", true, 1, 8192)
	count := napcatInt("file_count", false, 1, 10000, int64(50))
	return []napcatOperationSpec{
		{name: "get_chat", parameters: []napcatParameter{noCache}},
		{name: "get_message", method: "get_msg", message: true, archive: true, parameters: []napcatParameter{message}},
		{name: "delete_message", method: "delete_msg", message: true, archive: true, mutation: true, parameters: []napcatParameter{message}},
		{name: "set_reaction", method: "set_msg_emoji_like", message: true, archive: true, mutation: true, parameters: []napcatParameter{message, napcatString("emoji_id", true, 1, 20), napcatBool("set", false)}},
		{name: "poke", method: "send_poke", mutation: true, parameters: []napcatParameter{optionalUser}},
		{name: "get_members", method: "get_group_member_list", group: true, parameters: []napcatParameter{noCache}},
		{name: "get_member", method: "get_group_member_info", group: true, parameters: []napcatParameter{user, noCache}},
		{name: "get_group_info", group: true, parameters: []napcatParameter{noCache}},
		{name: "get_group_info_ex", group: true},
		{name: "get_group_member_list", group: true, parameters: []napcatParameter{noCache}},
		{name: "get_group_member_info", group: true, parameters: []napcatParameter{user, noCache}},
		{name: "get_stranger_info", parameters: []napcatParameter{optionalUser, noCache}},
		{name: "get_group_honor_info", group: true, parameters: []napcatParameter{napcatString("type", false, 1, 32)}},
		{name: "get_group_at_all_remain", group: true},
		{name: "get_group_shut_list", group: true},
		{name: "set_group_ban", group: true, mutation: true, parameters: []napcatParameter{user, napcatInt("duration", false, 0, 2592000, int64(1800))}},
		{name: "set_group_kick", group: true, mutation: true, parameters: []napcatParameter{user, napcatBool("reject_add_request", false)}},
		{name: "set_group_whole_ban", group: true, mutation: true, parameters: []napcatParameter{napcatBool("enable", true)}},
		{name: "set_group_admin", group: true, mutation: true, parameters: []napcatParameter{user, napcatBool("enable", true)}},
		{name: "set_group_card", group: true, mutation: true, parameters: []napcatParameter{user, napcatString("card", true, 0, 512)}},
		{name: "set_group_name", group: true, mutation: true, parameters: []napcatParameter{napcatString("group_name", true, 1, 512)}},
		{name: "set_group_special_title", group: true, mutation: true, parameters: []napcatParameter{user, napcatString("special_title", true, 0, 512)}},
		{name: "set_group_leave", group: true, mutation: true, parameters: []napcatParameter{napcatBool("is_dismiss", false)}},
		{name: "set_group_remark", group: true, mutation: true, parameters: []napcatParameter{napcatString("remark", true, 0, 512)}},
		{name: "_get_group_notice", group: true},
		{name: "_send_group_notice", group: true, mutation: true, parameters: []napcatParameter{napcatString("content", true, 1, 65536), napcatInt("pinned", false, 0, 1, nil), napcatInt("type", false, 0, 1, nil), napcatInt("confirm_required", false, 0, 1, nil), napcatInt("is_show_edit_card", false, 0, 1, nil), napcatInt("tip_window_type", false, 0, 1, nil)}, files: []string{"image"}},
		{name: "_del_group_notice", group: true, mutation: true, parameters: []napcatParameter{napcatString("notice_id", true, 1, 1024)}},
		{name: "get_group_file_system_info", group: true},
		{name: "get_group_root_files", group: true, parameters: []napcatParameter{count}},
		{name: "get_group_files_by_folder", group: true, parameters: []napcatParameter{folder, count}},
		{name: "get_group_file_url", group: true, parameters: []napcatParameter{file, folder}},
		{name: "create_group_file_folder", group: true, mutation: true, parameters: []napcatParameter{napcatString("name", true, 1, 512)}},
		{name: "delete_group_folder", group: true, mutation: true, parameters: []napcatParameter{napcatString("folder_id", true, 1, 1024)}},
		{name: "delete_group_file", group: true, mutation: true, parameters: []napcatParameter{file, folder, napcatInt("busid", false, 1, 4294967295, nil)}},
		{name: "rename_group_file", group: true, mutation: true, parameters: []napcatParameter{file, napcatString("current_parent_directory", true, 0, 1024), napcatString("new_name", true, 1, 512)}},
		{name: "move_group_file", group: true, mutation: true, parameters: []napcatParameter{file, napcatString("current_parent_directory", true, 0, 1024), napcatString("target_parent_directory", true, 0, 1024)}},
		{name: "upload_group_file", group: true, mutation: true, parameters: []napcatParameter{napcatString("name", true, 1, 512), folder}, files: []string{"file"}},
		{name: "set_group_portrait", group: true, mutation: true, files: []string{"file"}},
		{name: "get_essence_msg_list", group: true},
		{name: "set_essence_msg", group: true, message: true, mutation: true, parameters: []napcatParameter{message}},
		{name: "delete_essence_msg", group: true, mutation: true, parameters: []napcatParameter{message}},
	}
}()

// NapCatOperationsCatalog keeps discovery and the adapter's parameter allowlist
// together. Routing identifiers always come from the authorized conversation.
func NapCatOperationsCatalog() []ChatOperation {
	result := make([]ChatOperation, 0, len(napcatOperationSpecs))
	for _, spec := range napcatOperationSpecs {
		parameters := make([]string, 0, len(spec.parameters))
		for _, parameter := range spec.parameters {
			parameters = append(parameters, parameter.name)
		}
		result = append(result, ChatOperation{Name: spec.name, Parameters: parameters, MessageScoped: spec.archive, FileFields: slices.Clone(spec.files), Documentation: "https://napneko.github.io/onebot/api"})
	}
	return result
}

func napcatOperation(ctx context.Context, n *napcat, name string, chat domain.Conversation, params map[string]any, uploads ...map[string]Download) (json.RawMessage, error) {
	index := slices.IndexFunc(napcatOperationSpecs, func(spec napcatOperationSpec) bool { return spec.name == name })
	if index < 0 {
		return nil, domain.Invalid("unknown NapCat chat operation")
	}
	spec := napcatOperationSpecs[index]
	kind, id, err := qqTarget(chat.ExternalID)
	if err != nil {
		return nil, err
	}
	if !napcatNumericID(id) {
		return nil, domain.Invalid("operation requires a numeric QQ chat ID")
	}
	if spec.group && kind != "group" {
		return nil, domain.Invalid("this operation requires a group chat")
	}
	payload, err := napcatOperationParameters(spec, params)
	if err != nil {
		return nil, err
	}
	files := map[string]Download{}
	if len(uploads) > 0 {
		files = uploads[0]
	}
	for field, file := range files {
		if !slices.Contains(spec.files, field) || file.Body == nil {
			return nil, domain.Invalid("unsupported NapCat upload field: " + field)
		}
	}
	if (name == "upload_group_file" || name == "set_group_portrait") && files["file"].Body == nil {
		return nil, domain.Invalid("this operation requires files.file from a Computer path or FileRef")
	}
	method := spec.method
	if method == "" {
		method = name
	}
	if kind == "group" {
		payload["group_id"] = id
	}
	switch name {
	case "get_chat":
		method = "get_group_info"
		if kind == "private" {
			method, payload["user_id"] = "get_stranger_info", id
		}
	case "get_stranger_info", "poke":
		userID, _ := payload["user_id"].(string)
		if userID == "" {
			if kind == "group" {
				return nil, domain.Invalid("user_id is required for a group member")
			}
			userID, payload["user_id"] = id, id
		}
		if kind == "private" && userID != id {
			return nil, domain.Forbidden("user_id must be the selected private chat's peer")
		}
		if kind == "group" {
			if err := napcatGroupMemberScope(ctx, n, id, userID); err != nil {
				return nil, err
			}
		}
		if name == "get_stranger_info" {
			delete(payload, "group_id")
		}
	case "set_reaction":
		emoji := payload["emoji_id"].(string)
		if strings.IndexFunc(emoji, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return nil, domain.Invalid("emoji_id must be a QQ emoji ID string")
		}
		if _, exists := payload["set"]; !exists {
			payload["set"] = true
		}
	case "get_group_honor_info":
		if value, ok := payload["type"].(string); ok && !slices.Contains([]string{"all", "talkative", "performer", "legend", "strong_newbie", "emotion"}, value) {
			return nil, domain.Invalid("unknown group honor type")
		}
	case "_del_group_notice":
		if err := napcatNoticeScope(ctx, n, id, payload["notice_id"].(string)); err != nil {
			return nil, err
		}
	case "delete_essence_msg":
		// Older essence items can have synthetic short IDs with no ordinary
		// get_msg mapping or local archive. Use the selected group's fresh list
		// to resolve its native coordinates, never the global short-ID mapping.
		payload, err = napcatEssenceScope(ctx, n, id, payload["message_id"].(string))
		if err != nil {
			return nil, err
		}
	case "get_group_files_by_folder", "delete_group_folder", "upload_group_file":
		folder, _ := payload["folder_id"].(string)
		if name == "delete_group_folder" && napcatRootDirectory(folder) {
			return nil, domain.Invalid("the group root directory cannot be deleted")
		}
		if !napcatRootDirectory(folder) {
			if _, err := napcatGroupDirectory(ctx, n, id, folder); err != nil {
				return nil, err
			}
		}
	case "get_group_file_url", "delete_group_file", "rename_group_file", "move_group_file":
		folder, _ := payload["folder_id"].(string)
		if name == "rename_group_file" || name == "move_group_file" {
			folder = payload["current_parent_directory"].(string)
		}
		listing, err := napcatGroupDirectory(ctx, n, id, folder)
		if err != nil {
			return nil, err
		}
		if !slices.ContainsFunc(listing.Files, func(file napcatGroupFile) bool { return file.ID == payload["file_id"] }) {
			return nil, domain.Forbidden("file_id is not present in the selected group's directory; supply its folder_id")
		}
		if name == "move_group_file" {
			if _, err := napcatGroupDirectory(ctx, n, id, payload["target_parent_directory"].(string)); err != nil {
				return nil, err
			}
		}
		delete(payload, "folder_id") // Host scope proof; not a native parameter.
	}
	if spec.message {
		messageID := payload["message_id"].(string)
		if number, parseErr := strconv.ParseInt(messageID, 10, 64); parseErr != nil || number == 0 {
			return nil, domain.Invalid("message_id must be a nonzero integer string")
		}
		if spec.mutation {
			var message json.RawMessage
			if err := n.call(ctx, "get_msg", map[string]any{"message_id": messageID}, &message); err != nil {
				return nil, err
			}
			if err := napcatMessageScope(ctx, n, chat, message, messageID); err != nil {
				return nil, err
			}
		}
		// Native globally addressed message methods do not take group_id.
		delete(payload, "group_id")
	}
	var body any = payload
	for field, file := range files {
		placeholder := filePlaceholder()
		payload[field] = "base64://" + placeholder
		body = streamFileJSON(payload, placeholder, file.Body, file.Size)
	}
	var result json.RawMessage
	if err := n.call(ctx, method, body, &result); err != nil {
		if spec.mutation {
			var api *napcatAPIError
			if errors.As(err, &api) {
				return nil, api.sendError()
			}
			return nil, &SendError{Message: err.Error(), Unknown: true}
		}
		return nil, err
	}
	if name == "get_message" {
		if err := napcatMessageScope(ctx, n, chat, result, payload["message_id"].(string)); err != nil {
			return nil, err
		}
	}
	if len(result) == 0 {
		result = encoded(nil)
	}
	return result, nil
}

func napcatOperationParameters(spec napcatOperationSpec, params map[string]any) (map[string]any, error) {
	result := map[string]any{}
	for key := range params {
		if !slices.ContainsFunc(spec.parameters, func(p napcatParameter) bool { return p.name == key }) {
			return nil, domain.Invalid("unsupported operation parameter: " + key)
		}
	}
	for _, p := range spec.parameters {
		value, exists := params[p.name]
		if !exists {
			if p.required {
				return nil, domain.Invalid("required operation parameter: " + p.name)
			}
			if p.fallback != nil {
				result[p.name] = p.fallback
			}
			continue
		}
		switch p.kind {
		case 's':
			text, ok := value.(string)
			if !ok || int64(len(text)) < p.min || int64(len(text)) > p.max || strings.ContainsRune(text, 0) {
				return nil, domain.Invalid(p.name + " must be a string within the supported length")
			}
			result[p.name] = text
		case 'b':
			flag, ok := value.(bool)
			if !ok {
				return nil, domain.Invalid(p.name + " must be a boolean")
			}
			result[p.name] = flag
		case 'u':
			if number, ok := value.(float64); ok && (number > 1<<53 || math.Trunc(number) != number) {
				return nil, domain.Invalid("large user IDs must be exact strings")
			}
			userID := telegramID(value)
			if !napcatNumericID(userID) {
				return nil, domain.Invalid("user_id must be a positive QQ number")
			}
			result[p.name] = userID
		case 'i':
			number := telegramID(value)
			if integer, ok := value.(int64); ok {
				number = strconv.FormatInt(integer, 10)
			}
			integer, err := strconv.ParseInt(number, 10, 64)
			if err != nil || integer < p.min || integer > p.max {
				return nil, domain.Invalid(p.name + " is outside the supported integer range")
			}
			result[p.name] = integer
		}
	}
	return result, nil
}

func napcatGroupMemberScope(ctx context.Context, n *napcat, groupID, userID string) error {
	var member struct {
		GroupID qqValue `json:"group_id"`
		UserID  qqValue `json:"user_id"`
	}
	if err := n.call(ctx, "get_group_member_info", map[string]any{"group_id": groupID, "user_id": userID, "no_cache": true}, &member); err != nil {
		return err
	}
	if string(member.GroupID) != groupID || string(member.UserID) != userID {
		return domain.Forbidden("NapCat could not prove the user belongs to the selected group")
	}
	return nil
}

func napcatNoticeScope(ctx context.Context, n *napcat, groupID, noticeID string) error {
	var notices []struct {
		ID string `json:"notice_id"`
	}
	if err := n.call(ctx, "_get_group_notice", map[string]any{"group_id": groupID}, &notices); err != nil {
		return err
	}
	if !slices.ContainsFunc(notices, func(notice struct {
		ID string `json:"notice_id"`
	}) bool {
		return notice.ID == noticeID
	}) {
		return domain.Forbidden("notice_id is not present in the selected group")
	}
	return nil
}

func napcatEssenceScope(ctx context.Context, n *napcat, groupID, messageID string) (map[string]any, error) {
	if number, err := strconv.ParseInt(messageID, 10, 64); err != nil || number == 0 {
		return nil, domain.Invalid("message_id must be a nonzero integer string")
	}
	var items []struct {
		ID     qqValue `json:"message_id"`
		Seq    qqValue `json:"msg_seq"`
		Random qqValue `json:"msg_random"`
	}
	if err := n.call(ctx, "get_essence_msg_list", map[string]any{"group_id": groupID}, &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		if string(item.ID) != messageID {
			continue
		}
		if _, err := strconv.ParseUint(string(item.Seq), 10, 64); err != nil {
			return nil, domain.Offline("NapCat returned invalid essence message coordinates")
		}
		if _, err := strconv.ParseUint(string(item.Random), 10, 64); err != nil {
			return nil, domain.Offline("NapCat returned invalid essence message coordinates")
		}
		return map[string]any{"group_id": groupID, "msg_seq": string(item.Seq), "msg_random": string(item.Random)}, nil
	}
	return nil, domain.Forbidden("message_id is not present in the selected group's essence list")
}

type napcatGroupFile struct {
	ID string `json:"file_id"`
}
type napcatGroupDirectoryListing struct {
	Files   []napcatGroupFile `json:"files"`
	Folders []struct {
		ID string `json:"folder_id"`
	} `json:"folders"`
}

func napcatRootDirectory(folder string) bool { return folder == "" || folder == "/" }

// NapCat file IDs may embed a different chat, and not every native file method
// checks that identity. Prove IDs from a fresh list in the authorized group.
// QQ's group folders are listed at the root; callers identify a file's folder
// rather than requiring an unbounded traversal of the entire group file store.
func napcatGroupDirectory(ctx context.Context, n *napcat, groupID, folder string) (napcatGroupDirectoryListing, error) {
	var root napcatGroupDirectoryListing
	if err := n.call(ctx, "get_group_root_files", map[string]any{"group_id": groupID, "file_count": 10000}, &root); err != nil {
		return root, err
	}
	if napcatRootDirectory(folder) {
		return root, nil
	}
	if !slices.ContainsFunc(root.Folders, func(f struct {
		ID string `json:"folder_id"`
	}) bool {
		return f.ID == folder
	}) {
		return root, domain.Forbidden("folder_id is not present in the selected group")
	}
	var result napcatGroupDirectoryListing
	err := n.call(ctx, "get_group_files_by_folder", map[string]any{"group_id": groupID, "folder_id": folder, "file_count": 10000}, &result)
	return result, err
}

func napcatMessageScope(ctx context.Context, n *napcat, chat domain.Conversation, raw json.RawMessage, requested string) error {
	kind, id, err := qqTarget(chat.ExternalID)
	if err != nil {
		return err
	}
	var message struct {
		Type     string  `json:"message_type"`
		ID       qqValue `json:"message_id"`
		GroupID  qqValue `json:"group_id"`
		UserID   qqValue `json:"user_id"`
		TargetID qqValue `json:"target_id"`
	}
	if json.Unmarshal(raw, &message) != nil || message.ID == "" || string(message.ID) != requested || message.Type != kind {
		return domain.Forbidden("NapCat could not prove that this message belongs to the selected chat")
	}
	if kind == "group" {
		if string(message.GroupID) != id {
			return domain.Forbidden("NapCat message belongs to another group")
		}
		return nil
	}
	if string(message.UserID) == id && (message.TargetID == "" || string(message.TargetID) == n.connection.AccountID) {
		return nil
	}
	if string(message.UserID) != n.connection.AccountID {
		return domain.Forbidden("NapCat message belongs to another private chat")
	}
	if message.TargetID != "" {
		if string(message.TargetID) != id {
			return domain.Forbidden("NapCat message belongs to another private chat")
		}
		return nil
	}
	// NapCat get_msg omits the peer for self-sent private messages. The
	// explicitly chat-scoped history endpoint can prove recent membership;
	// an account-global message lookup alone cannot.
	var history struct {
		Messages []struct {
			ID qqValue `json:"message_id"`
		} `json:"messages"`
	}
	if err := n.call(ctx, "get_friend_msg_history", map[string]any{"user_id": id, "count": 100}, &history); err != nil {
		return err
	}
	for _, candidate := range history.Messages {
		if candidate.ID == message.ID {
			return nil
		}
	}
	return domain.Forbidden("NapCat could not find this self-sent message in the selected private chat's recent history")
}
func napcatNumericID(value string) bool {
	if len(value) == 0 || len(value) > 20 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != "0"
}
