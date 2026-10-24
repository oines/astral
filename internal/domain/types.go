package domain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-models"
)

func ID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

type Meta struct {
	ID        string    `json:"id"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Agent struct {
	Meta
	Name              string                 `json:"name"`
	Avatar            string                 `json:"avatar,omitempty"`
	DefaultModelID    string                 `json:"default_model_id,omitempty"`
	DefaultReasoning  *core.ReasoningOptions `json:"default_reasoning,omitempty"`
	ProbeModelID      string                 `json:"probe_model_id,omitempty"`
	ProbeReasoning    *core.ReasoningOptions `json:"probe_reasoning,omitempty"`
	SearchProviderID  string                 `json:"search_provider_id,omitempty"`
	AllowedComputers  []string               `json:"allowed_computers"`
	DefaultComputerID string                 `json:"default_computer_id,omitempty"`
}

type Session struct {
	Meta
	AgentID         string                 `json:"agent_id"`
	Title           string                 `json:"title"`
	TitleSource     string                 `json:"title_source,omitempty"`
	Source          string                 `json:"source"`
	ProfileID       string                 `json:"profile_id,omitempty"`
	ModelRef        string                 `json:"model_ref,omitempty"`
	Options         core.GenerationOptions `json:"options"`
	Reasoning       *core.ReasoningOptions `json:"reasoning,omitempty"`
	ComputerID      string                 `json:"computer_id,omitempty"`
	CWD             string                 `json:"cwd,omitempty"`
	ConversationID  string                 `json:"conversation_id,omitempty"`
	BindingID       string                 `json:"binding_id,omitempty"`
	ChannelPlatform string                 `json:"channel_platform,omitempty"`
	ParentSessionID string                 `json:"parent_session_id,omitempty"`
	ForkEntryID     string                 `json:"fork_entry_id,omitempty"`
	Pinned          bool                   `json:"pinned"`
	Archived        bool                   `json:"archived"`
	Deletion        *SessionDeletion       `json:"deletion,omitempty"`
}

// SessionDeletion is a durable maintenance intent, retained until every store
// has removed the Session's data. It is not a recoverable archive state.
type SessionDeletion struct {
	RolloutID string `json:"rollout_id"`
}

const (
	TitleDefault   = "default"
	TitlePrompt    = "prompt"
	TitleGenerated = "generated"
	TitleManual    = "manual"
)

type Provider struct {
	Meta
	PresetID            string                   `json:"preset_id,omitempty"`
	Enabled             *bool                    `json:"enabled,omitempty"`
	Name                string                   `json:"name"`
	Protocol            string                   `json:"protocol"`
	Dialect             string                   `json:"dialect"`
	BaseURL             string                   `json:"base_url"`
	HeaderNames         []string                 `json:"header_names,omitempty"`
	HeadersCredentialID string                   `json:"headers_credential_id,omitempty"`
	CredentialID        string                   `json:"credential_id,omitempty"`
	ReasoningFormat     models.ReasoningFormat   `json:"reasoning_format,omitempty"`
	ReasoningMapping    *models.ReasoningMapping `json:"reasoning_mapping,omitempty"`
	// ReplayRevision changes with API credentials/routing, not display labels
	// or enable switches. Zero keeps the legacy provider-revision scope.
	ReplayRevision int64 `json:"replay_revision,omitempty"`
}

// Recommendations and Overrides keep their provenance independently. An
// override is removed to restore the recommendation, never overwritten during
// a catalog refresh. Unknown capability values remain unknown.
type ModelProfile struct {
	Meta
	Description           string                          `json:"description,omitempty"`
	Enabled               *bool                           `json:"enabled,omitempty"`
	ProviderID            string                          `json:"provider_id"`
	Protocol              string                          `json:"protocol,omitempty"`
	Dialect               string                          `json:"dialect,omitempty"`
	ModelID               string                          `json:"model_id"`
	Name                  string                          `json:"name"`
	Recommendations       map[string]json.RawMessage      `json:"recommendations"`
	Overrides             map[string]json.RawMessage      `json:"overrides"`
	CatalogSource         string                          `json:"catalog_source,omitempty"`
	CatalogAt             *time.Time                      `json:"catalog_at,omitempty"`
	Verification          *ModelVerification              `json:"verification,omitempty"`
	RecommendationSources map[string]RecommendationSource `json:"recommendation_sources,omitempty"`
	// Controls are derived from the saved connection and profile. They are never
	// trusted as input or used as a second configuration source.
	ReasoningControls *models.ReasoningControls `json:"reasoning_controls,omitempty"`
	ReasoningError    string                    `json:"reasoning_error,omitempty"`
}

type RecommendationSource struct {
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ModelVerification struct {
	TestedAt         time.Time `json:"tested_at"`
	ProviderRevision int64     `json:"provider_revision"`
	ProfileRevision  int64     `json:"profile_revision"`
	ToolCalling      string    `json:"tool_calling"`
	ToolReplay       string    `json:"tool_replay"`
	Message          string    `json:"message,omitempty"`
}

type ModelValues struct {
	ContextWindow          int                      `json:"context_window"`
	MaxInputTokens         int                      `json:"max_input_tokens,omitempty"`
	MaxOutputTokens        int                      `json:"max_output_tokens"`
	ToolCalling            string                   `json:"tool_calling,omitempty"`
	Images                 string                   `json:"images,omitempty"`
	Streaming              *bool                    `json:"streaming,omitempty"`
	Reasoning              json.RawMessage          `json:"reasoning,omitempty"`
	ReasoningFormat        models.ReasoningFormat   `json:"reasoning_format,omitempty"`
	ReasoningMapping       *models.ReasoningMapping `json:"reasoning_mapping,omitempty"`
	ReasoningReplay        string                   `json:"reasoning_replay,omitempty"`
	RequireReasoningReplay bool                     `json:"require_reasoning_replay,omitempty"`
	ImageTokenBudget       int                      `json:"image_token_budget,omitempty"`
	Defaults               core.GenerationOptions   `json:"defaults"`
	InputPrice             *float64                 `json:"input_price,omitempty"`
	OutputPrice            *float64                 `json:"output_price,omitempty"`
}

func (p ModelProfile) Effective() (ModelValues, map[string]string, error) {
	fields := map[string]json.RawMessage{}
	sources := map[string]string{}
	for key, value := range p.Recommendations {
		fields[key] = value
		sources[key] = "recommended"
	}
	for key, value := range p.Overrides {
		fields[key] = value
		sources[key] = "override"
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return ModelValues{}, nil, err
	}
	var values ModelValues
	if err = json.Unmarshal(raw, &values); err != nil {
		return values, nil, err
	}
	if values.ContextWindow < 1 || values.MaxOutputTokens < 1 || values.MaxInputTokens < 0 {
		return values, nil, Invalid("model token limits must be positive")
	}
	for _, capability := range []string{values.ToolCalling, values.Images} {
		if capability != "" && capability != "unknown" && capability != "supported" && capability != "unsupported" {
			return values, nil, Invalid("invalid capability status")
		}
	}
	if err = values.Defaults.Validate(); err != nil {
		return values, nil, Invalid(err.Error())
	}
	return values, sources, nil
}

type SearchProvider struct {
	Meta
	Name             string            `json:"name"`
	Adapter          string            `json:"adapter"`
	BaseURL          string            `json:"base_url,omitempty"`
	Config           map[string]string `json:"config,omitempty"`
	CredentialID     string            `json:"credential_id,omitempty"`
	CredentialFields []string          `json:"credential_fields,omitempty"`
}

type ContainerInfo struct {
	Name      string `json:"name"`
	Host      string `json:"host,omitempty"`
	Image     string `json:"image,omitempty"`
	Volume    string `json:"volume,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
}

type Computer struct {
	Meta
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Hostname  string         `json:"hostname,omitempty"`
	OS        string         `json:"os,omitempty"`
	Arch      string         `json:"arch,omitempty"`
	Shell     string         `json:"shell,omitempty"`
	Home      string         `json:"home,omitempty"`
	Root      string         `json:"root,omitempty"`
	Container *ContainerInfo `json:"container,omitempty"`
	TokenHash string         `json:"-"`
	TokenID   string         `json:"token_id,omitempty"`
	Local     bool           `json:"local"`
	Revoked   bool           `json:"revoked"`
}

type ComputerStatus struct {
	Computer
	Online      bool       `json:"online"`
	BootID      string     `json:"boot_id,omitempty"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

type Enrollment struct {
	Meta
	TokenHash    string    `json:"token_hash"`
	ExpiresAt    time.Time `json:"expires_at"`
	ComputerID   string    `json:"computer_id,omitempty"`
	CredentialID string    `json:"credential_id,omitempty"`
	Container    bool      `json:"container"`
	Local        bool      `json:"local"`
	State        string    `json:"state"`
	Error        string    `json:"error,omitempty"`
}

// Binding is carried in authenticated Server/Daemon calls. Request bodies at
// the Agent gateway never supply this identity.
type ExecutionBinding struct {
	GrantID    string `json:"grant_id"`
	AgentID    string `json:"agent_id"`
	SessionID  string `json:"session_id"`
	ComputerID string `json:"computer_id"`
	BootID     string `json:"boot_id"`
	CWD        string `json:"cwd"`
	Shell      string `json:"shell"`
	AgentHome  string `json:"agent_home"`
	APIURL     string `json:"api_url"`
}

type FileRef struct {
	Meta
	AgentID     string `json:"agent_id"`
	ComputerID  string `json:"computer_id"`
	Path        string `json:"path"`
	DisplayName string `json:"display_name"`
	MediaType   string `json:"media_type"`
	Size        int64  `json:"size"`
	Version     string `json:"version,omitempty"`
}

type Connection struct {
	Meta
	AgentID      string          `json:"agent_id"`
	AccessMode   string          `json:"access_mode" enum:"all,specified"`
	Name         string          `json:"name"`
	Platform     string          `json:"platform"`
	Adapter      string          `json:"adapter"`
	CredentialID string          `json:"credential_id,omitempty"`
	Enabled      bool            `json:"enabled"`
	AccountID    string          `json:"account_id,omitempty"`
	Username     string          `json:"username,omitempty"`
	Cursor       string          `json:"cursor,omitempty"`
	Config       json.RawMessage `json:"config,omitempty"`
	Diagnostics  []string        `json:"diagnostics,omitempty"`
}

type Conversation struct {
	Meta
	ConnectionID     string `json:"connection_id"`
	ExternalID       string `json:"external_id"`
	TopicID          string `json:"topic_id,omitempty"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	ProactiveEnabled bool   `json:"proactive_enabled"`
}

type ConversationBinding struct {
	Meta
	ConversationID string `json:"conversation_id"`
	AgentID        string `json:"agent_id"`
	SessionID      string `json:"session_id"`
	Enabled        bool   `json:"enabled"`
	StartSequence  int64  `json:"start_sequence"`
}

type Sender struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
}

type Attachment struct {
	Ref        string `json:"ref"`
	ExternalID string `json:"external_id"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	MediaType  string `json:"media_type,omitempty"`
	Size       int64  `json:"size,omitempty"`
	FileURI    string `json:"file_uri,omitempty"`
}

type Observation struct {
	SchemaVersion   int          `json:"schema_version"`
	ID              string       `json:"id"`
	ConnectionID    string       `json:"connection_id"`
	ConversationID  string       `json:"conversation_id"`
	ExternalEventID string       `json:"external_event_id"`
	Kind            string       `json:"kind"`
	MessageID       string       `json:"message_id"`
	Sender          Sender       `json:"sender"`
	Text            string       `json:"text"`
	Format          string       `json:"format,omitempty"`
	SentAt          time.Time    `json:"sent_at"`
	EditedAt        *time.Time   `json:"edited_at,omitempty"`
	ReceivedAt      time.Time    `json:"received_at"`
	ReplyID         string       `json:"reply_id,omitempty"`
	ReplySender     string       `json:"reply_sender,omitempty"`
	ReplyPreview    string       `json:"reply_preview,omitempty"`
	Attachments     []Attachment `json:"attachments,omitempty"`
	Trigger         string       `json:"trigger,omitempty"`
	Self            bool         `json:"self"`
}

type Outbox struct {
	Meta
	MessageOptions
	IntentKey           string   `json:"intent_key"`
	GroupID             string   `json:"group_id"`
	AgentID             string   `json:"agent_id"`
	SessionID           string   `json:"session_id"`
	BindingID           string   `json:"binding_id,omitempty"`
	ConversationID      string   `json:"conversation_id"`
	AssistantEntryID    string   `json:"assistant_entry_id,omitempty"`
	Text                string   `json:"text,omitempty"`
	ReplyTo             string   `json:"reply_to,omitempty"`
	FileRefID           string   `json:"file_ref_id,omitempty"`
	Status              string   `json:"status"`
	PlatformMessageIDs  []string `json:"platform_message_ids,omitempty"`
	Error               string   `json:"error,omitempty"`
	RequestDigest       string   `json:"request_digest"`
	ConnectionID        string   `json:"connection_id"`
	ConnectionAccountID string   `json:"connection_account_id"`
	ExternalID          string   `json:"external_id"`
	TopicID             string   `json:"topic_id,omitempty"`
	Fragment            int      `json:"fragment"`
}

type Preferences struct {
	Theme        string `json:"theme"`
	Language     string `json:"language"`
	KeepRunning  bool   `json:"keep_running"`
	LoginStart   bool   `json:"login_start"`
	RunningInput string `json:"running_input"`
}

type HostInfo struct {
	Hostname           string `json:"hostname"`
	OS                 string `json:"os"`
	Arch               string `json:"arch"`
	ContainerRuntime   string `json:"container_runtime,omitempty"`
	ContainerAvailable bool   `json:"container_available"`
	ContainerError     string `json:"container_error,omitempty"`
	LocalError         string `json:"local_error,omitempty"`
}
type Provisioning struct {
	Meta
	IntentID     string   `json:"intent_id"`
	Kind         string   `json:"kind"`
	State        string   `json:"state"`
	Phase        string   `json:"phase"`
	ComputerID   string   `json:"computer_id,omitempty"`
	EnrollmentID string   `json:"enrollment_id,omitempty"`
	ContainerID  string   `json:"container_id,omitempty"`
	Log          []string `json:"log"`
	Error        string   `json:"error,omitempty"`
}

type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string     { return e.Message }
func Invalid(message string) error { return &Error{Status: 400, Code: "invalid", Message: message} }
func Missing(kind, id string) error {
	return &Error{Status: 404, Code: "not_found", Message: fmt.Sprintf("%s %q is unavailable", kind, id)}
}
func Conflict(message string) error  { return &Error{Status: 409, Code: "conflict", Message: message} }
func Forbidden(message string) error { return &Error{Status: 403, Code: "forbidden", Message: message} }
func Offline(message string) error   { return &Error{Status: 503, Code: "offline", Message: message} }
func IsMissing(err error) bool {
	var problem *Error
	return errors.As(err, &problem) && problem.Status == 404
}
