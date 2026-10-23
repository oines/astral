package domain

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
