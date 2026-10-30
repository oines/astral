package domain

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-core/models"
	"github.com/oines/astral-core/models/integration"
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
	Persona           string                 `json:"persona,omitempty"`
	DefaultModelID    string                 `json:"default_model_id,omitempty"`
	DefaultReasoning  *core.ReasoningOptions `json:"default_reasoning,omitempty"`
	ProbeModelID      string                 `json:"probe_model_id,omitempty"`
	ProbeReasoning    *core.ReasoningOptions `json:"probe_reasoning,omitempty"`
	SearchProviderID  string                 `json:"search_provider_id,omitempty"`
	AllowedComputers  []string               `json:"allowed_computers"`
	DefaultComputerID string                 `json:"default_computer_id,omitempty"`
	Deletion          bool                   `json:"deletion,omitempty"`
}

type Session struct {
	Meta
	AgentID         string           `json:"agent_id"`
	Title           string           `json:"title"`
	TitleSource     string           `json:"title_source,omitempty"`
	Source          string           `json:"source"`
	ComputerID      string           `json:"computer_id,omitempty"`
	CWD             string           `json:"cwd,omitempty"`
	ConversationID  string           `json:"conversation_id,omitempty"`
	BindingID       string           `json:"binding_id,omitempty"`
	ChannelPlatform string           `json:"channel_platform,omitempty"`
	ParentSessionID string           `json:"parent_session_id,omitempty"`
	ForkEntryID     string           `json:"fork_entry_id,omitempty"`
	Pinned          bool             `json:"pinned"`
	Archived        bool             `json:"archived"`
	Private         bool             `json:"private,omitempty"`
	Deletion        *SessionDeletion `json:"deletion,omitempty"`
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
	// or enable switches.
	ReplayRevision int64 `json:"replay_revision,omitempty"`
}

// Recommendations and Overrides keep their provenance independently. An
// override is removed to restore the recommendation. A user-modified profile
// keeps its whole configuration while still listed by its provider; an absent
// model is removed from current inventory. Unknown capabilities remain unknown.
type ModelProfile struct {
	Meta
	UserModified          bool                              `json:"user_modified,omitempty"`
	Available             *bool                             `json:"available,omitempty"`
	ProviderName          string                            `json:"provider_name,omitempty"`
	UnavailableReason     string                            `json:"unavailable_reason,omitempty"`
	Description           string                            `json:"description,omitempty"`
	Enabled               *bool                             `json:"enabled,omitempty"`
	ProviderID            string                            `json:"provider_id"`
	Protocol              string                            `json:"protocol,omitempty"`
	Dialect               string                            `json:"dialect,omitempty"`
	ModelID               string                            `json:"model_id"`
	Name                  string                            `json:"name"`
	Recommendations       map[string]json.RawMessage        `json:"recommendations"`
	RecommendationTarget  *integration.RecommendationTarget `json:"recommendation_target,omitempty"`
	Overrides             map[string]json.RawMessage        `json:"overrides"`
	CatalogSource         string                            `json:"catalog_source,omitempty"`
	CatalogAt             *time.Time                        `json:"catalog_at,omitempty"`
	Verification          *ModelVerification                `json:"verification,omitempty"`
	RecommendationSources map[string]RecommendationSource   `json:"recommendation_sources,omitempty"`
	// Controls are derived from the saved connection and profile. They are never
	// trusted as input or used as a second configuration source.
	ReasoningControls *models.ReasoningControls `json:"reasoning_controls,omitempty"`
	ReasoningError    string                    `json:"reasoning_error,omitempty"`
}

type RecommendationSource = integration.RecommendationSource

type ModelVerification struct {
	TestedAt         time.Time `json:"tested_at"`
	ProviderRevision int64     `json:"provider_revision"`
	ProfileRevision  int64     `json:"profile_revision"`
	ToolCalling      string    `json:"tool_calling"`
	ToolReplay       string    `json:"tool_replay"`
	Message          string    `json:"message,omitempty"`
}

type ModelValues = integration.ModelValues

func (p ModelProfile) Effective() (ModelValues, map[string]string, error) {
	return integration.EffectiveValues(p.Recommendations, p.Overrides)
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
	Name          string         `json:"name"`
	Kind          string         `json:"kind"`
	Hostname      string         `json:"hostname,omitempty"`
	OS            string         `json:"os,omitempty"`
	Arch          string         `json:"arch,omitempty"`
	Shell         string         `json:"shell,omitempty"`
	Home          string         `json:"home,omitempty"`
	Root          string         `json:"root,omitempty"`
	DaemonVersion string         `json:"daemon_version,omitempty"`
	Container     *ContainerInfo `json:"container,omitempty"`
	TokenHash     string         `json:"-"`
	TokenID       string         `json:"token_id,omitempty"`
	Local         bool           `json:"local"`
	Revoked       bool           `json:"revoked"`
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
	Ref        string           `json:"ref"`
	ExternalID string           `json:"external_id"`
	Kind       string           `json:"kind"`
	Name       string           `json:"name"`
	MediaType  string           `json:"media_type,omitempty"`
	Size       int64            `json:"size,omitempty"`
	FileURI    string           `json:"file_uri,omitempty"`
	Sticker    *StickerMetadata `json:"sticker,omitempty"`
}

// StickerMetadata describes a platform sticker while Attachment identifies its
// original downloadable file. ThumbnailRef points to an image attachment in the
// same observation; it never replaces the animated or video original.
type StickerMetadata struct {
	Type          string `json:"type"`
	Format        string `json:"format"`
	Emoji         string `json:"emoji,omitempty"`
	SetName       string `json:"set_name,omitempty"`
	UniqueID      string `json:"unique_id,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	ThumbnailRef  string `json:"thumbnail_ref,omitempty"`
}

// TextQuote retains the selected source fragment separately from a reply's
// preview. Position is the sender's approximate UTF-16 offset in the source;
// Text is authoritative and must not be reconstructed by slicing that source.
type TextQuote struct {
	Text     string `json:"text"`
	Position int    `json:"position" description:"Approximate sender-specified offset in UTF-16 code units of the original source message; text is authoritative."`
	Manual   bool   `json:"manual"`
}

// Reaction identifies a platform reaction without assuming every platform uses
// Unicode emoji. Count is an absolute total; ReportedCount preserves a native
// value whose aggregation semantics are not guaranteed by the platform.
type Reaction struct {
	Type          string `json:"type"`
	Emoji         string `json:"emoji,omitempty"`
	ID            string `json:"id,omitempty"`
	Count         *int64 `json:"count,omitempty"`
	ReportedCount *int64 `json:"reported_count,omitempty"`
}

// ReactionEvent is an append-only fact about the target Observation.MessageID.
// ActorType distinguishes users, apps, anonymous chat identities and unknown
// actors; it never substitutes the target message's author for the operator.
type ReactionEvent struct {
	ActorType string     `json:"actor_type"`
	Reactions []Reaction `json:"reactions"`
}

type Observation struct {
	SchemaVersion      int            `json:"schema_version"`
	ID                 string         `json:"id"`
	ConnectionID       string         `json:"connection_id"`
	ConversationID     string         `json:"conversation_id"`
	ExternalEventID    string         `json:"external_event_id"`
	Kind               string         `json:"kind"`
	MessageID          string         `json:"message_id"`
	Sender             Sender         `json:"sender"`
	ForwardOrigin      *ForwardOrigin `json:"forward_origin,omitempty"`
	Text               string         `json:"text"`
	Parts              []MessagePart  `json:"parts,omitempty"`
	Format             string         `json:"format,omitempty"`
	SentAt             time.Time      `json:"sent_at"`
	EditedAt           *time.Time     `json:"edited_at,omitempty"`
	ReceivedAt         time.Time      `json:"received_at"`
	ReplyID            string         `json:"reply_id,omitempty"`
	ReplySender        string         `json:"reply_sender,omitempty"`
	ReplyForwardOrigin *ForwardOrigin `json:"reply_forward_origin,omitempty"`
	ReplyPreview       string         `json:"reply_preview,omitempty"`
	ReplyQuote         *TextQuote     `json:"reply_quote,omitempty"`
	ReplyAttachments   []Attachment   `json:"reply_attachments,omitempty"`
	ReplyParts         []MessagePart  `json:"reply_parts,omitempty"`
	Attachments        []Attachment   `json:"attachments,omitempty"`
	Trigger            string         `json:"trigger,omitempty"`
	Self               bool           `json:"self"`
	Reaction           *ReactionEvent `json:"reaction,omitempty"`
}

type Outbox struct {
	Meta
	MessageOptions
	IntentKey           string       `json:"intent_key"`
	GroupID             string       `json:"group_id"`
	AgentID             string       `json:"agent_id"`
	SessionID           string       `json:"session_id"`
	BindingID           string       `json:"binding_id,omitempty"`
	ConversationID      string       `json:"conversation_id"`
	AssistantEntryID    string       `json:"assistant_entry_id,omitempty"`
	ReplyTo             string       `json:"reply_to,omitempty"`
	Parts               []OutboxPart `json:"parts,omitempty"`
	Status              string       `json:"status"`
	PlatformMessageIDs  []string     `json:"platform_message_ids,omitempty"`
	Error               string       `json:"error,omitempty"`
	RequestDigest       string       `json:"request_digest"`
	ConnectionID        string       `json:"connection_id"`
	ConnectionAccountID string       `json:"connection_account_id"`
	ExternalID          string       `json:"external_id"`
	TopicID             string       `json:"topic_id,omitempty"`
	Fragment            int          `json:"fragment"`
}

type Preferences struct {
	Timezone     string `json:"timezone,omitempty"`
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
