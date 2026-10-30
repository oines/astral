package domain

// OutboundPart is an ordered piece of an outbound message. Whitespace belongs
// to text parts; no separators are inserted between adjacent parts.
type OutboundPart struct {
	Type    string `json:"type" enum:"text,mention,image,file" description:"Ordered content kind. Mention support depends on the selected platform."`
	Text    string `json:"text,omitempty" description:"For text only. Spaces and newlines are preserved; no separator is inserted between adjacent parts."`
	UserID  string `json:"user_id,omitempty" description:"For mention only. Native platform user ID: Telegram numeric ID, NapCat QQ number, or Feishu application open_id."`
	Name    string `json:"name,omitempty" description:"Optional display label for a native mention."`
	Path    string `json:"path,omitempty" description:"For image/file only. Source path on the authenticated calling Computer; mutually exclusive with file_uri."`
	FileURI string `json:"file_uri,omitempty" description:"For image/file only. An astral-file:// reference; mutually exclusive with path."`
}

// OutboxPart is a prepared part whose media has an immutable Computer snapshot.
// Its internal file reference is never accepted as a public sending argument.
type OutboxPart struct {
	OutboundPart
	FileRefID string `json:"file_ref_id,omitempty"`
}

// MessageOptions describes a frozen outbound presentation, independently of
// credentials and destination. An empty format retains legacy literal text.
type MessageOptions struct {
	Format              string            `json:"format,omitempty"`
	Title               string            `json:"title,omitempty"`
	MarkdownTemplate    *MarkdownTemplate `json:"markdown_template,omitempty"`
	DisableNotification bool              `json:"disable_notification,omitempty"`
	AsDocument          bool              `json:"as_document,omitempty"`
}

type MarkdownTemplate struct {
	ID     string                  `json:"custom_template_id"`
	Params []MarkdownTemplateParam `json:"params"`
}

type MarkdownTemplateParam struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

func (o MessageOptions) TextFormat() string {
	if o.Format == "" {
		return "text"
	}
	return o.Format
}
