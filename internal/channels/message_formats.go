package channels

import (
	"context"
	"fmt"
	"slices"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
)

// MessageTool describes only the selected account's implemented presentation
// capabilities. Resolving it never contacts the platform or exposes credentials.
func (s *Service) MessageTool(ctx context.Context, agentID, conversationID string) (core.ToolDefinition, error) {
	var chat domain.Conversation
	if err := s.Store.Get(ctx, "conversations", conversationID, &chat); err != nil {
		return core.ToolDefinition{}, err
	}
	var account domain.Connection
	if err := s.Store.Get(ctx, "connections", chat.ConnectionID, &account); err != nil {
		return core.ToolDefinition{}, err
	}
	if account.AgentID != agentID {
		return core.ToolDefinition{}, domain.Forbidden("chat belongs to another Bot")
	}
	a, err := s.adapter(ctx, account)
	if err != nil {
		return core.ToolDefinition{}, err
	}
	return messageTool(a.Capabilities()), nil
}

func messageTool(c Capabilities) core.ToolDefinition {
	properties := map[string]any{
		"text": map[string]any{"type": "string", "description": "Message body. Omitted format means literal text. May be empty for attachments or a QQ Markdown template."},
	}
	if c.ReplyReferences {
		properties["reply_to"] = map[string]any{"type": "string", "description": "Message ID in this chat to reply to."}
	}
	if c.FileSending {
		properties["attachments"] = map[string]any{"type": "array", "items": map[string]any{"type": "string", "minLength": 1}, "description": "Computer paths or astral-file:// references; never embed a Computer path in the body as a media URL."}
	}
	if len(c.MessageFormats) > 1 {
		properties["format"] = map[string]any{"type": "string", "enum": c.MessageFormats, "description": "Choose the message body's format; defaults to text."}
	}
	for _, name := range c.MessageParameters {
		switch name {
		case "title":
			properties[name] = map[string]any{"type": "string", "description": "Optional Feishu post title; requires format=markdown."}
		case "disable_notification":
			properties[name] = map[string]any{"type": "boolean", "description": "Send silently in Telegram."}
		case "as_document":
			properties[name] = map[string]any{"type": "boolean", "description": "Send Telegram image attachments as original documents instead of photos."}
		case "markdown_template":
			properties[name] = map[string]any{"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"custom_template_id": map[string]any{"type": "string", "minLength": 1},
					"params": map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false,
						"properties": map[string]any{"key": map[string]any{"type": "string", "minLength": 1}, "values": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}}}, "required": []string{"key", "values"}}},
				}, "required": []string{"custom_template_id", "params"}, "description": "QQ's approved template ID and values; requires format=markdown_template and empty text."}
		}
	}
	def := harness.DefaultMessageTool()
	def.Version = "astral-3/" + c.Adapter
	def.Parameters = encoded(map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": []string{"text"}})
	def.Description += " Destination: " + c.Name + ". "
	switch c.Adapter {
	case "telegram.bot":
		def.Description += "markdown and html use Telegram Rich Messages (headings, lists, code, tables). Use supported rich syntax, not MarkdownV2. Formatted text and attachments are sent as separate ordered messages."
	case "feishu.bot":
		def.Description += "markdown uses a Feishu rich-text post with an md block; supports CommonMark/GFM with Feishu restrictions. An optional title belongs to that post. Text and files have separate receipts."
	case "qq.bot":
		def.Description += "markdown uses QQ's native Markdown subset; markdown_template uses an approved QQ template. Availability is controlled by this account's QQ permissions. Formatted bodies and attachments have separate receipts; a definitive rejection is returned without silently changing format."
	case "qq.napcat":
		def.Description += "Body is sent unchanged as ordinary text, even if it contains Markdown syntax. Rich messages, Markdown segments and JSON/XML cards are not available. Ordinary images, files and reply references are supported."
	case "weixin.ilink":
		def.Description += "markdown uses Weixin's partial Markdown support: CJK italics and level 5/6 headings are simplified; inline image syntax becomes readable text, so send actual images through attachments. Outbound reply references are unavailable."
	}
	return def
}

func validateMessageOptions(c Capabilities, o domain.MessageOptions) error {
	formats := c.MessageFormats
	if len(formats) == 0 {
		formats = []string{"text"}
	}
	if !slices.Contains(formats, o.TextFormat()) {
		return domain.Invalid(fmt.Sprintf("%s does not support message format %q", c.Name, o.TextFormat()))
	}
	if o.Title != "" && (!slices.Contains(c.MessageParameters, "title") || o.TextFormat() != "markdown") {
		return domain.Invalid("title is only supported for Feishu Markdown posts")
	}
	if o.DisableNotification && !slices.Contains(c.MessageParameters, "disable_notification") {
		return domain.Invalid("disable_notification is only supported for Telegram")
	}
	if o.AsDocument && !slices.Contains(c.MessageParameters, "as_document") {
		return domain.Invalid("as_document is only supported for Telegram")
	}
	if o.MarkdownTemplate != nil && (!slices.Contains(c.MessageParameters, "markdown_template") || o.TextFormat() != "markdown_template") {
		return domain.Invalid("markdown_template requires QQ Bot format=markdown_template")
	}
	if o.TextFormat() == "markdown_template" {
		if o.MarkdownTemplate == nil || strings.TrimSpace(o.MarkdownTemplate.ID) == "" {
			return domain.Invalid("a QQ custom_template_id is required")
		}
	}
	return nil
}

type preparedMessage struct {
	domain.MessageOptions
	Text string
	File bool
}

func archiveMessageText(record domain.Outbox) string {
	text := record.Text
	if record.MarkdownTemplate != nil {
		var values []string
		for _, param := range record.MarkdownTemplate.Params {
			values = append(values, param.Key+": "+strings.Join(param.Values, "\n"))
		}
		text = "[QQ Markdown template " + record.MarkdownTemplate.ID + "]\n" + strings.Join(values, "\n")
	}
	if record.Title != "" {
		text = record.Title + "\n\n" + text
	}
	return text
}

// Prepare before claiming the intent. The resulting presentation is stored in
// Outbox, so a retry never reparses the source or chooses another format.
func prepareMessages(a Adapter, text string, options domain.MessageOptions, file bool) ([]preparedMessage, error) {
	c := a.Capabilities()
	if err := validateMessageOptions(c, options); err != nil {
		return nil, err
	}
	format := options.TextFormat()
	if format == "markdown_template" {
		if text != "" {
			return nil, domain.Invalid("QQ template messages require empty text; put content in template params")
		}
		result := []preparedMessage{{MessageOptions: options}}
		if file {
			result = append(result, preparedMessage{MessageOptions: fileMessageOptions(options), File: true})
		}
		return result, nil
	}
	if file && format == "text" && c.CaptionLimit > 0 {
		return []preparedMessage{{MessageOptions: options, Text: text, File: true}}, nil
	}
	var parts []string
	var err error
	if strings.TrimSpace(text) != "" {
		text = presentText(text)
		if format == "text" {
			parts, err = a.SplitText(text)
		} else {
			limit := c.TextLimit
			if c.Adapter == "telegram.bot" {
				limit = 32768
			}
			if c.Adapter == "weixin.ilink" {
				text = weixinMarkdown(text)
			}
			if format == "html" {
				parts, err = splitHTML(text, limit)
			} else {
				parts, err = splitMarkdown(text, limit)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	var result []preparedMessage
	for _, part := range parts {
		result = append(result, preparedMessage{MessageOptions: options, Text: part})
	}
	if file {
		result = append(result, preparedMessage{MessageOptions: fileMessageOptions(options), File: true})
	}
	if len(result) == 0 {
		return nil, domain.Invalid("message text is empty")
	}
	return result, nil
}

func fileMessageOptions(options domain.MessageOptions) domain.MessageOptions {
	return domain.MessageOptions{DisableNotification: options.DisableNotification, AsDocument: options.AsDocument}
}
