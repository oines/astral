package channels

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

// NapCatEssenceSource uses the original group message coordinates when supplied.
// NapCat's synthetic essence ID is deterministic, but can change to a native ID
// if the original message becomes available between two list requests.
type NapCatEssenceSource struct {
	MessageID   string `json:"message_id"`
	MsgSeq      string `json:"msg_seq,omitempty"`
	MsgRandom   string `json:"msg_random,omitempty"`
	MediaDigest string `json:"media_digest,omitempty"`
}

type NapCatEssenceResource struct {
	Source NapCatEssenceSource
	Parts  []domain.MessagePart
}

func (source NapCatEssenceSource) validate() error {
	if value, err := strconv.ParseInt(source.MessageID, 10, 64); err != nil || value == 0 || len(source.MessageID) > 20 {
		return domain.Invalid("message_id must be a nonzero integer string")
	}
	if (source.MsgSeq == "") != (source.MsgRandom == "") {
		return domain.Invalid("msg_seq and msg_random must be supplied together")
	}
	for _, coordinate := range []string{source.MsgSeq, source.MsgRandom} {
		if coordinate == "" {
			continue
		}
		if _, err := strconv.ParseUint(coordinate, 10, 64); err != nil || len(coordinate) > 20 || strings.HasPrefix(coordinate, "+") {
			return domain.Invalid("essence coordinates must be unsigned decimal strings")
		}
	}
	if source.MediaDigest != "" {
		value, err := hex.DecodeString(source.MediaDigest)
		if err != nil || len(value) != sha256.Size || strings.ToLower(source.MediaDigest) != source.MediaDigest {
			return domain.Invalid("media_digest must be the lowercase SHA256 locator fingerprint from a part link")
		}
	}
	return nil
}

// NapCatEssenceMediaDigest identifies the trusted media locator, not its bytes.
// It prevents a stale pN link from silently selecting a different picture when
// NapCat changes from its text/image fallback to the full native message shape.
func NapCatEssenceMediaDigest(attachment domain.Attachment) string {
	sum := sha256.Sum256([]byte(attachment.Kind + "\x00" + attachment.ExternalID))
	return hex.EncodeToString(sum[:])
}

func (s *Service) authorizeNapCatEssence(ctx context.Context, b domain.ExecutionBinding, chat domain.Conversation, account domain.Connection) error {
	_, currentChat, current, err := s.target(ctx, b.AgentID, b.SessionID, chat.ID)
	if err != nil {
		return err
	}
	if current.ID != account.ID || current.Revision != account.Revision || current.Adapter != "qq.napcat" || currentChat.ExternalID != chat.ExternalID {
		return domain.Forbidden("essence source account or group changed during resource access")
	}
	return nil
}

func (s *Service) napcatEssenceResource(ctx context.Context, b domain.ExecutionBinding, conversationID string, source NapCatEssenceSource) (NapCatEssenceResource, domain.Conversation, domain.Connection, *napcat, error) {
	var result NapCatEssenceResource
	var chat domain.Conversation
	var account domain.Connection
	if err := source.validate(); err != nil {
		return result, chat, account, nil, err
	}
	_, chat, account, err := s.target(ctx, b.AgentID, b.SessionID, conversationID)
	if err != nil {
		return result, chat, account, nil, err
	}
	kind, groupID, err := qqTarget(chat.ExternalID)
	if err != nil || kind != "group" || !napcatNumericID(groupID) || account.Adapter != "qq.napcat" {
		return result, chat, account, nil, domain.Invalid("essence resources require an authorized NapCat group")
	}
	adapter, err := s.adapter(ctx, account)
	if err != nil {
		return result, chat, account, nil, err
	}
	n, ok := adapter.(*napcat)
	if !ok {
		return result, chat, account, nil, domain.Invalid("essence resources require the group's NapCat adapter")
	}
	var entries []struct {
		ID      qqValue         `json:"message_id"`
		Seq     qqValue         `json:"msg_seq"`
		Random  qqValue         `json:"msg_random"`
		Content json.RawMessage `json:"content"`
	}
	read, cancel := context.WithTimeout(ctx, messagePartTimeout)
	defer cancel()
	if err = n.call(read, "get_essence_msg_list", map[string]any{"group_id": groupID}, &entries); err != nil {
		return result, chat, account, nil, err
	}
	if err = s.authorizeNapCatEssence(ctx, b, chat, account); err != nil {
		return result, chat, account, nil, err
	}
	found := false
	for _, entry := range entries {
		matches := string(entry.ID) == source.MessageID
		if source.MsgSeq != "" {
			matches = string(entry.Seq) == source.MsgSeq && string(entry.Random) == source.MsgRandom
		}
		if !matches {
			continue
		}
		if found {
			return result, chat, account, nil, domain.Offline("NapCat returned ambiguous essence message identities")
		}
		found = true
		result.Source = NapCatEssenceSource{MessageID: string(entry.ID), MsgSeq: string(entry.Seq), MsgRandom: string(entry.Random)}
		if err := result.Source.validate(); err != nil || result.Source.MsgSeq == "" {
			return result, chat, account, nil, domain.Offline("NapCat returned invalid essence message coordinates")
		}
		result.Parts, _, _ = napcatParts(entry.Content, result.Source.MessageID, "")
	}
	if !found {
		return result, chat, account, nil, domain.Missing("group essence message", source.MessageID)
	}
	return result, chat, account, n, nil
}

// NapCatEssenceParts reads metadata only, without requiring, importing or
// modifying an IM archive entry. Every request proves the current group source.
func (s *Service) NapCatEssenceParts(ctx context.Context, b domain.ExecutionBinding, conversationID string, source NapCatEssenceSource) (NapCatEssenceResource, error) {
	resource, _, _, _, err := s.napcatEssenceResource(ctx, b, conversationID, source)
	return resource, err
}

// Inline forwards can be traversed with the existing parser. Opaque forward
// handles are not independent grants and never cause a get_msg fallback here.
func napcatEssencePart(parts []domain.MessagePart, source NapCatEssenceSource, ref string) (domain.MessagePart, error) {
	path, err := messagePartPath(ref)
	if err != nil {
		return domain.MessagePart{}, err
	}
	for depth := 0; depth < len(path); depth += 2 {
		prefix := strings.Join(path[:depth+1], ".")
		var selected *domain.MessagePart
		for i := range parts {
			if parts[i].Ref == prefix {
				selected = &parts[i]
				break
			}
		}
		if selected == nil {
			return domain.MessagePart{}, domain.Missing("essence message part", ref)
		}
		if depth == len(path)-1 {
			return *selected, nil
		}
		if selected.Forward == nil || len(selected.Forward.Raw) == 0 {
			return domain.MessagePart{}, domain.Offline("this essence part has no inline forwarded content")
		}
		index, _ := strconv.Atoi(path[depth+1][1:])
		messages, _, err := napcatForwardPage(selected.Forward.Raw, source.MessageID, selected.Ref, index, 1)
		if err != nil {
			return domain.MessagePart{}, err
		}
		if len(messages) == 0 {
			return domain.MessagePart{}, domain.Missing("essence forwarded message", path[depth+1])
		}
		parts = messages[0].Parts
	}
	return domain.MessagePart{}, domain.Missing("essence message part", ref)
}

func (s *Service) NapCatEssencePart(ctx context.Context, b domain.ExecutionBinding, conversationID string, source NapCatEssenceSource, ref string) (NapCatEssenceSource, domain.MessagePart, error) {
	if _, err := messagePartPath(ref); err != nil {
		return NapCatEssenceSource{}, domain.MessagePart{}, err
	}
	resource, err := s.NapCatEssenceParts(ctx, b, conversationID, source)
	if err != nil {
		return NapCatEssenceSource{}, domain.MessagePart{}, err
	}
	part, err := napcatEssencePart(resource.Parts, resource.Source, ref)
	return resource.Source, part, err
}

func (s *Service) NapCatEssenceContent(ctx context.Context, b domain.ExecutionBinding, conversationID string, source NapCatEssenceSource, ref string) (Download, error) {
	if _, err := messagePartPath(ref); err != nil {
		return Download{}, err
	}
	resource, chat, account, n, err := s.napcatEssenceResource(ctx, b, conversationID, source)
	if err != nil {
		return Download{}, err
	}
	part, err := napcatEssencePart(resource.Parts, resource.Source, ref)
	if err != nil {
		return Download{}, err
	}
	if part.Attachment == nil || part.Type != "attachment" {
		return Download{}, domain.Invalid("selected essence part is not media")
	}
	if source.MediaDigest != "" && source.MediaDigest != NapCatEssenceMediaDigest(*part.Attachment) {
		return Download{}, domain.Conflict("essence media source changed; read its parts again before downloading")
	}
	download, err := n.Download(ctx, *part.Attachment)
	if err != nil {
		return Download{}, err
	}
	if err = s.authorizeNapCatEssence(ctx, b, chat, account); err != nil {
		_ = download.Body.Close()
		return Download{}, err
	}
	if download.MediaType == "" {
		download.MediaType = part.Attachment.MediaType
	}
	if strings.HasPrefix(download.MediaType, "image/") {
		extension := filepath.Ext(download.Name)
		guessed, _, _ := mime.ParseMediaType(mime.TypeByExtension(extension))
		if guessed != download.MediaType {
			suffix := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "image/avif": ".avif"}[download.MediaType]
			if suffix != "" {
				download.Name = strings.TrimSuffix(filepath.Base(download.Name), extension) + suffix
			}
		}
	}
	return download, nil
}

func (s *Service) DownloadNapCatEssencePart(ctx context.Context, b domain.ExecutionBinding, conversationID string, source NapCatEssenceSource, ref, destination string) (domain.FileRef, error) {
	if strings.TrimSpace(destination) == "" {
		return domain.FileRef{}, domain.Invalid("an explicit destination path is required")
	}
	download, err := s.NapCatEssenceContent(ctx, b, conversationID, source, ref)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer download.Body.Close()
	if err := s.AuthorizeConversation(ctx, b.AgentID, conversationID); err != nil {
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
	if err := s.AuthorizeConversation(ctx, b.AgentID, conversationID); err != nil {
		return domain.FileRef{}, err
	}
	file, err := s.Harness.Files.Reference(ctx, b, destination, false)
	if err == nil && download.MediaType != "" && file.MediaType != download.MediaType {
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
