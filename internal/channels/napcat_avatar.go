package channels

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"

	_ "golang.org/x/image/webp"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const napcatAvatarLimit int64 = 8 << 20

func napcatAvatarID(id string) bool {
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil && len(id) <= 20 && napcatID.MatchString(id)
}

func (s *Service) authorizeNapCatAvatar(ctx context.Context, b domain.ExecutionBinding, chat domain.Conversation, account domain.Connection) error {
	_, current, owner, err := s.target(ctx, b.AgentID, b.SessionID, chat.ID)
	if err != nil {
		return err
	}
	if current.ExternalID != chat.ExternalID || current.Kind != chat.Kind || owner.ID != account.ID || owner.Revision != account.Revision || owner.Adapter != "qq.napcat" {
		return domain.Forbidden("avatar source account or chat changed during resource access")
	}
	return nil
}

// Empty userID selects the chat avatar: a private peer or the group itself.
// Explicit users remain scoped to that private peer or a verified group member.
func (s *Service) napcatAvatar(ctx context.Context, b domain.ExecutionBinding, conversationID, userID string) (Download, domain.Conversation, domain.Connection, error) {
	ctx, cancel := context.WithTimeout(ctx, messagePartTimeout)
	defer cancel()
	var chat domain.Conversation
	var account domain.Connection
	if userID != "" && !napcatAvatarID(userID) {
		return Download{}, chat, account, domain.Invalid("user_id must be a positive QQ identifier")
	}
	_, chat, account, err := s.target(ctx, b.AgentID, b.SessionID, conversationID)
	if err != nil {
		return Download{}, chat, account, err
	}
	kind, id, err := qqTarget(chat.ExternalID)
	if err != nil || !napcatAvatarID(id) || account.Adapter != "qq.napcat" || chat.TopicID != "" {
		return Download{}, chat, account, domain.Invalid("avatar resources require an authorized NapCat private chat or group")
	}
	adapter, err := s.adapter(ctx, account)
	if err != nil {
		return Download{}, chat, account, err
	}
	n, ok := adapter.(*napcat)
	if !ok {
		return Download{}, chat, account, domain.Invalid("avatar resources require the chat's NapCat adapter")
	}
	if kind == "private" {
		if userID != "" && userID != id {
			return Download{}, chat, account, domain.Forbidden("user_id must identify the selected private chat's peer")
		}
		userID = id
	} else if userID != "" {
		if err := napcatGroupMemberScope(ctx, n, id, userID); err != nil {
			return Download{}, chat, account, err
		}
	}
	if err := s.authorizeNapCatAvatar(ctx, b, chat, account); err != nil {
		return Download{}, chat, account, err
	}
	// These are the same public QQ image services used by NapCat's own UI.
	// The caller can select an authorized identity, never supply a remote URL.
	url, name := "https://p.qlogo.cn/gh/"+id+"/"+id+"/0/", "qq-group-"+id+"-avatar"
	if userID != "" {
		url, name = "https://q1.qlogo.cn/g?b=qq&nk="+userID+"&s=100", "qq-user-"+userID+"-avatar"
	}
	client := *n.client
	client.Jar = nil // Avatar requests never carry cookies from the account client.
	download, err := qqDownloadURL(ctx, &client, url, domain.Attachment{Name: name})
	if err != nil {
		return Download{}, chat, account, err
	}
	defer download.Body.Close()
	if download.Size > napcatAvatarLimit {
		return Download{}, chat, account, domain.Offline("QQ avatar exceeds the size limit")
	}
	data, err := io.ReadAll(io.LimitReader(download.Body, napcatAvatarLimit+1))
	if err != nil || int64(len(data)) > napcatAvatarLimit || download.Size >= 0 && int64(len(data)) != download.Size {
		return Download{}, chat, account, domain.Offline("QQ avatar download was interrupted or exceeded the size limit")
	}
	mediaType := http.DetectContentType(data)
	extension := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/gif": ".gif", "image/webp": ".webp"}[mediaType]
	config, _, decodeErr := image.DecodeConfig(bytes.NewReader(data))
	if extension == "" || decodeErr != nil || config.Width < 1 || config.Height < 1 {
		return Download{}, chat, account, domain.Offline("QQ avatar service did not return a supported image")
	}
	if err := s.authorizeNapCatAvatar(ctx, b, chat, account); err != nil {
		return Download{}, chat, account, err
	}
	return Download{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data)), Name: name + extension, MediaType: mediaType}, chat, account, nil
}

// NapCatAvatarContent returns verified image bytes, without importing messages
// or creating a reusable grant to a QQ account outside the selected chat.
func (s *Service) NapCatAvatarContent(ctx context.Context, b domain.ExecutionBinding, conversationID, userID string) (Download, error) {
	download, _, _, err := s.napcatAvatar(ctx, b, conversationID, userID)
	return download, err
}

func (s *Service) DownloadNapCatAvatar(ctx context.Context, b domain.ExecutionBinding, conversationID, userID, destination string) (domain.FileRef, error) {
	if strings.TrimSpace(destination) == "" {
		return domain.FileRef{}, domain.Invalid("an explicit destination path is required")
	}
	download, chat, account, err := s.napcatAvatar(ctx, b, conversationID, userID)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer download.Body.Close()
	if err := s.authorizeNapCatAvatar(ctx, b, chat, account); err != nil {
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
	if err := s.authorizeNapCatAvatar(ctx, b, chat, account); err != nil {
		return domain.FileRef{}, err
	}
	file, err := s.Harness.Files.Reference(ctx, b, destination, false)
	if err == nil && file.MediaType != download.MediaType {
		file.MediaType = download.MediaType
		err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
			if err := tx.RequireActiveAgent(ctx, b.AgentID); err != nil {
				return err
			}
			return tx.Save(ctx, "files", &file.Meta, &file)
		})
	}
	return file, err
}
