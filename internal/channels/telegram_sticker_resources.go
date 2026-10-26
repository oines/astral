package channels

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

// TelegramStickerSource selects a file from a freshly verified account resource.
// Exactly one of SetName and CustomEmojiID is required. FileID can identify an
// original sticker or its separate thumbnail; it is never a download URL.
type TelegramStickerSource struct {
	SetName       string `json:"set_name,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
	FileID        string `json:"file_id"`
}

func validTelegramStickerSetName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, letter := range name {
		if letter != '_' && (letter < 'a' || letter > 'z') && (letter < 'A' || letter > 'Z') && (letter < '0' || letter > '9') {
			return false
		}
	}
	return true
}

func validTelegramCustomEmojiID(id string) bool {
	if len(id) == 0 || len(id) > 20 {
		return false
	}
	for _, digit := range id {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	value, err := strconv.ParseUint(id, 10, 64)
	return err == nil && value != 0
}

func telegramResourceParameters(method string, parameters map[string]any) (map[string]any, error) {
	switch method {
	case "getStickerSet":
		name, ok := parameters["name"].(string)
		if len(parameters) != 1 || !ok || !validTelegramStickerSetName(name) {
			return nil, domain.Invalid("name must be a sticker set name of 1-64 ASCII letters, digits or underscores")
		}
		return map[string]any{"name": name}, nil
	case "getCustomEmojiStickers":
		var ids []string
		switch raw := parameters["custom_emoji_ids"].(type) {
		case []string:
			ids = append(ids, raw...)
		case []any:
			for _, value := range raw {
				id, ok := value.(string)
				if !ok {
					return nil, domain.Invalid("custom_emoji_ids must contain decimal identifier strings")
				}
				ids = append(ids, id)
			}
		}
		if len(parameters) != 1 || len(ids) == 0 || len(ids) > 200 {
			return nil, domain.Invalid("custom_emoji_ids must contain 1-200 decimal identifier strings")
		}
		for _, id := range ids {
			if !validTelegramCustomEmojiID(id) {
				return nil, domain.Invalid("custom_emoji_ids must contain valid nonzero decimal identifier strings")
			}
		}
		return map[string]any{"custom_emoji_ids": ids}, nil
	default:
		return nil, domain.Invalid("unknown account resource operation")
	}
}

func (s *Service) telegramResourceOperation(ctx context.Context, b domain.ExecutionBinding, accountID, method string, request TelegramRequest) (TelegramResult, error) {
	t, _, err := s.telegramAccount(ctx, b.AgentID, accountID)
	if err != nil {
		return TelegramResult{}, err
	}
	if request.SourceConversationID != "" || len(request.Files) != 0 {
		return TelegramResult{}, domain.Invalid("account resource reads do not accept source chats or file uploads")
	}
	if request.ConversationID != "" {
		_, _, owner, err := s.target(ctx, b.AgentID, b.SessionID, request.ConversationID)
		if err != nil {
			return TelegramResult{}, err
		}
		if owner.ID != accountID {
			return TelegramResult{}, domain.Forbidden("chat belongs to another account")
		}
	}
	parameters, err := telegramResourceParameters(method, request.Parameters)
	if err != nil {
		return TelegramResult{}, err
	}
	var result json.RawMessage
	if err = t.call(ctx, method, parameters, &result); err != nil {
		return TelegramResult{}, err
	}
	if _, _, err = s.telegramAccount(ctx, b.AgentID, accountID); err != nil {
		return TelegramResult{}, err
	}
	if request.ConversationID != "" {
		if err = s.AuthorizeConversation(ctx, b.AgentID, request.ConversationID); err != nil {
			return TelegramResult{}, err
		}
	}
	// These results are resource metadata, never sent or received IM messages.
	return TelegramResult{Result: result}, nil
}

func (source TelegramStickerSource) parameters() (string, map[string]any, error) {
	if (source.SetName == "") == (source.CustomEmojiID == "") {
		return "", nil, domain.Invalid("select exactly one set_name or custom_emoji_id")
	}
	if source.FileID == "" || len(source.FileID) > 4096 || strings.TrimSpace(source.FileID) != source.FileID || strings.ContainsAny(source.FileID, "\x00\r\n") {
		return "", nil, domain.Invalid("file_id must identify an original or thumbnail in the selected resource")
	}
	method, parameters := "getStickerSet", map[string]any{"name": source.SetName}
	if source.CustomEmojiID != "" {
		method, parameters = "getCustomEmojiStickers", map[string]any{"custom_emoji_ids": []string{source.CustomEmojiID}}
	}
	validated, err := telegramResourceParameters(method, parameters)
	return method, validated, err
}

func telegramResourceAttachment(result json.RawMessage, source TelegramStickerSource) (domain.Attachment, error) {
	var stickers []telegramSticker
	var thumbnail *telegramFile
	if source.SetName != "" {
		var set struct {
			Name      string            `json:"name"`
			Stickers  []telegramSticker `json:"stickers"`
			Thumbnail *telegramFile     `json:"thumbnail"`
		}
		if json.Unmarshal(result, &set) != nil || !strings.EqualFold(set.Name, source.SetName) {
			return domain.Attachment{}, domain.Invalid("Telegram returned an invalid sticker set")
		}
		stickers, thumbnail = set.Stickers, set.Thumbnail
	} else if json.Unmarshal(result, &stickers) != nil {
		return domain.Attachment{}, domain.Invalid("Telegram returned invalid custom emoji stickers")
	}
	for _, sticker := range stickers {
		if source.CustomEmojiID != "" && sticker.CustomEmojiID != source.CustomEmojiID {
			continue
		}
		if sticker.ID == source.FileID {
			_, extension, mediaType := sticker.format()
			return domain.Attachment{ExternalID: sticker.ID, Kind: "sticker", Name: "sticker" + extension, MediaType: mediaType, Size: sticker.Size}, nil
		}
		if sticker.Thumbnail != nil && sticker.Thumbnail.ID == source.FileID {
			return domain.Attachment{ExternalID: sticker.Thumbnail.ID, Kind: "image", Name: "sticker-thumbnail", Size: sticker.Thumbnail.Size}, nil
		}
	}
	if thumbnail != nil && thumbnail.ID == source.FileID {
		return domain.Attachment{ExternalID: thumbnail.ID, Kind: "image", Name: "sticker-set-thumbnail", Size: thumbnail.Size}, nil
	}
	return domain.Attachment{}, domain.Forbidden("file_id is not an original or thumbnail of the selected sticker resource")
}

// TelegramStickerContent streams a selected official resource without caching
// it, exposing Telegram credentials, or creating a Computer file reference.
func (s *Service) TelegramStickerContent(ctx context.Context, b domain.ExecutionBinding, accountID string, source TelegramStickerSource) (Download, error) {
	method, parameters, err := source.parameters()
	if err != nil {
		return Download{}, err
	}
	result, err := s.telegramResourceOperation(ctx, b, accountID, method, TelegramRequest{Parameters: parameters})
	if err != nil {
		return Download{}, err
	}
	attachment, err := telegramResourceAttachment(result.Result, source)
	if err != nil {
		return Download{}, err
	}
	t, _, err := s.telegramAccount(ctx, b.AgentID, accountID)
	if err != nil {
		return Download{}, err
	}
	download, err := t.Download(ctx, attachment)
	if err != nil {
		return Download{}, err
	}
	if _, _, err = s.telegramAccount(ctx, b.AgentID, accountID); err != nil {
		_ = download.Body.Close()
		return Download{}, err
	}
	return download, nil
}

// DownloadTelegramSticker writes only on an explicit request, to the original
// command's Computer and path, using the same file service as other attachments.
func (s *Service) DownloadTelegramSticker(ctx context.Context, b domain.ExecutionBinding, accountID string, source TelegramStickerSource, destination string) (domain.FileRef, error) {
	if strings.TrimSpace(destination) == "" {
		return domain.FileRef{}, domain.Invalid("an explicit destination path is required")
	}
	download, err := s.TelegramStickerContent(ctx, b, accountID, source)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer download.Body.Close()
	if _, _, err = s.telegramAccount(ctx, b.AgentID, accountID); err != nil {
		return domain.FileRef{}, err
	}
	_, body, err := s.Harness.Hub.Call(ctx, "file.write", computers.Call{Binding: b, Path: destination}, download.Body)
	if err != nil {
		return domain.FileRef{}, err
	}
	_, copyErr := io.Copy(io.Discard, body)
	if err = errors.Join(copyErr, body.Close()); err != nil {
		return domain.FileRef{}, err
	}
	if _, _, err = s.telegramAccount(ctx, b.AgentID, accountID); err != nil {
		return domain.FileRef{}, err
	}
	return s.Harness.Files.Reference(ctx, b, destination, false)
}
