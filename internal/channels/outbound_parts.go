package channels

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
)

// Every public sending shape reaches this one preparation path before any
// intent is committed or any platform message is sent.
func (s *Service) freezeOutboundParts(ctx context.Context, agentID, sessionID string, r SendRequest, c Capabilities) ([]domain.OutboxPart, error) {
	if err := validateMessageOptions(c, r.MessageOptions); err != nil {
		return nil, err
	}
	inputs := append([]domain.OutboundPart(nil), r.Parts...)
	if len(inputs) > 0 && (r.Text != "" || r.FileURI != "" || r.Path != "") {
		return nil, domain.Invalid("parts cannot be combined with text, path or file_uri")
	}
	if r.MarkdownTemplate != nil && len(inputs) > 0 {
		return nil, domain.Invalid("QQ Markdown templates cannot contain parts")
	}
	if r.TextFormat() == "markdown_template" && (r.Text != "" || r.FileURI != "" || r.Path != "") {
		return nil, domain.Invalid("QQ template messages require empty text and no media; put content in template params")
	}
	if len(inputs) == 0 {
		if r.Text != "" {
			inputs = append(inputs, domain.OutboundPart{Type: "text", Text: r.Text})
		}
		if r.FileURI != "" || r.Path != "" {
			inputs = append(inputs, domain.OutboundPart{Type: "file", FileURI: r.FileURI, Path: r.Path})
		}
	}
	if len(inputs) > 100 {
		return nil, domain.Invalid("a message supports at most 100 parts")
	}
	// Validate every part before snapshotting sources. Unsupported mentions are
	// rejected before earlier text can reach the external chat.
	for _, p := range inputs {
		switch p.Type {
		case "text":
			if p.UserID != "" || p.Name != "" || p.Path != "" || p.FileURI != "" || !utf8.ValidString(p.Text) {
				return nil, domain.Invalid("text parts accept only valid text")
			}
		case "mention":
			if p.Text != "" || p.Path != "" || p.FileURI != "" || strings.TrimSpace(p.UserID) == "" {
				return nil, domain.Invalid("mention parts require user_id and optional name")
			}
			switch c.Adapter {
			case "telegram.bot", "qq.napcat":
				if id, e := strconv.ParseInt(p.UserID, 10, 64); e != nil || id <= 0 || strconv.FormatInt(id, 10) != p.UserID {
					return nil, domain.Invalid("mention user_id must be a positive platform user ID")
				}
			case "feishu.bot":
				if !strings.HasPrefix(p.UserID, "ou_") || !feishuID(p.UserID) {
					return nil, domain.Invalid("invalid Feishu mention user_id")
				}
			default:
				return nil, domain.Invalid(c.Name + " does not support native mentions")
			}
		case "image", "file":
			if !c.FileSending {
				return nil, domain.Invalid("this adapter cannot send files")
			}
			if p.Text != "" || p.UserID != "" || p.Name != "" || (p.Path == "") == (p.FileURI == "") {
				return nil, domain.Invalid("media parts require exactly one of path or file_uri")
			}
		default:
			return nil, domain.Invalid("unsupported outbound part type")
		}
	}
	parts := make([]domain.OutboxPart, len(inputs))
	for i, input := range inputs {
		p := &parts[i]
		p.Type, p.Text, p.UserID, p.Name = input.Type, input.Text, input.UserID, input.Name
		if p.Type == "text" {
			p.Text = presentText(p.Text)
			continue
		}
		if p.Type != "image" && p.Type != "file" {
			continue
		}
		var f domain.FileRef
		var err error
		if input.Path != "" {
			source := r.SourceBinding
			if source == nil || source.AgentID != agentID || source.SessionID != sessionID || source.ComputerID != r.SourceComputerID {
				return nil, domain.Forbidden("file path needs the authenticated source Computer")
			}
			f, err = s.Harness.Files.Reference(ctx, *source, input.Path, true)
		} else {
			f, err = s.Harness.Files.Get(ctx, agentID, input.FileURI)
		}
		if err != nil {
			return nil, err
		}
		// The text/attachments conveniences retain their automatic image
		// presentation. An explicit parts.file always remains a document.
		if p.Type == "file" && (r.autoMediaTypes || len(r.Parts) == 0) && strings.HasPrefix(f.MediaType, "image/") {
			switch c.Adapter {
			case "telegram.bot":
				if f.MediaType == "image/png" || f.MediaType == "image/jpeg" || f.MediaType == "image/webp" {
					p.Type = "image"
				}
			case "feishu.bot": // The convenience previously uploaded a generic file.
			default:
				p.Type = "image"
			}
		}
		if p.Type == "file" && (r.autoMediaTypes || len(r.Parts) == 0) {
			if (c.Adapter == "qq.bot" || c.Adapter == "weixin.ilink") && strings.HasPrefix(f.MediaType, "video/") {
				p.MediaKind = "video"
			}
			if c.Adapter == "qq.bot" && strings.HasPrefix(f.MediaType, "audio/") {
				p.MediaKind = "audio"
			}
		}
		if p.Type == "image" && !strings.HasPrefix(f.MediaType, "image/") {
			return nil, domain.Invalid("image part source is not an image")
		}
		if p.Type == "image" && c.Adapter == "feishu.bot" && f.Size > 10<<20 {
			return nil, domain.Invalid("Feishu images cannot exceed 10 MiB")
		}
		if f.Version == "" {
			source, bindErr := s.Harness.Hub.Bind(ctx, agentID, sessionID, f.ComputerID, "")
			if bindErr != nil {
				return nil, bindErr
			}
			f, err = s.Harness.Files.Reference(ctx, source, f.Path, true)
			if err != nil {
				return nil, err
			}
		}
		if c.UploadLimit > 0 && f.Size > c.UploadLimit {
			return nil, domain.Invalid("file exceeds the platform upload limit")
		}
		p.FileRefID = f.ID
	}
	return parts, nil
}

func prepareOutboundParts(a Adapter, parts []domain.OutboxPart, options domain.MessageOptions) ([]preparedMessage, error) {
	c := a.Capabilities()
	if options.TextFormat() == "markdown_template" {
		return prepareMessages(a, "", options)
	}
	if len(parts) == 0 {
		return nil, domain.Invalid("message content is empty")
	}
	var text strings.Builder
	onlyText := true
	for _, p := range parts {
		if p.Type != "text" {
			onlyText = false
		}
		text.WriteString(p.Text)
	}
	if onlyText {
		return prepareMessages(a, text.String(), options)
	}
	var result []preparedMessage
	appendGroup := func(group []domain.OutboxPart) error {
		if len(group) == 0 {
			return nil
		}
		count, mediaCount := 0, 0
		for _, p := range group {
			if p.FileRefID != "" {
				mediaCount++
			}
			count += utf8.RuneCountInString(p.Text)
			if p.Type == "mention" {
				count += utf8.RuneCountInString(p.Name) + len(p.UserID)
			}
		}
		if c.Adapter == "telegram.bot" && mediaCount > 50 {
			return domain.Invalid("Telegram rich messages support at most 50 media attachments")
		}
		limit := c.TextLimit
		if c.Adapter == "telegram.bot" {
			limit = 32768
		}
		if limit > 0 && count > limit {
			return domain.Invalid("mixed message text exceeds the platform limit; send smaller ordered messages")
		}
		result = append(result, preparedMessage{MessageOptions: options, Parts: append([]domain.OutboxPart(nil), group...)})
		return nil
	}
	switch c.Adapter {
	case "telegram.bot":
		if err := appendGroup(parts); err != nil {
			return nil, err
		}
	case "qq.napcat":
		// OneBot's streaming request carries one file. Each actual send and its
		// acknowledgement remain a separate durable Outbox fragment.
		start, files := 0, 0
		for i, p := range parts {
			if p.FileRefID != "" {
				files++
				if files > 1 {
					if err := appendGroup(parts[start:i]); err != nil {
						return nil, err
					}
					start = i
					files = 1
				}
			}
		}
		if err := appendGroup(parts[start:]); err != nil {
			return nil, err
		}
	case "feishu.bot":
		start := 0
		for i, p := range parts {
			if p.Type == "file" {
				if err := appendGroup(parts[start:i]); err != nil {
					return nil, err
				}
				result = append(result, preparedMessage{MessageOptions: fileMessageOptions(options), Parts: []domain.OutboxPart{p}})
				start = i + 1
			}
		}
		if err := appendGroup(parts[start:]); err != nil {
			return nil, err
		}
	default:
		// QQ official and Weixin expose separate text/media messages. Preserve the
		// ordering and obtain a distinct receipt for each real message.
		var pending strings.Builder
		flush := func() error {
			if pending.Len() == 0 {
				return nil
			}
			prepared, err := prepareMessages(a, pending.String(), options)
			if err != nil {
				return err
			}
			result = append(result, prepared...)
			pending.Reset()
			return nil
		}
		for _, p := range parts {
			if p.Type == "text" {
				pending.WriteString(p.Text)
				continue
			}
			if err := flush(); err != nil {
				return nil, err
			}
			result = append(result, preparedMessage{MessageOptions: fileMessageOptions(options), Parts: []domain.OutboxPart{p}})
		}
		if err := flush(); err != nil {
			return nil, err
		}
	}
	if len(result) == 0 {
		return nil, domain.Invalid("message content is empty")
	}
	return result, nil
}
