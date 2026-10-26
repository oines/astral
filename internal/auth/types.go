// Package auth owns the single administrator account and independent browser
// sessions. Desktop identity and computer pairing are separate credentials.
package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/oines/astral/internal/domain"
)

const CookieName = "astral_session"
const SecureCookieName = "__Host-astral_session"

// SessionCookieName separates HTTP and HTTPS browser storage. A Secure Cookie
// cannot block a later HTTP login on the same host by sharing its cookie name.
func SessionCookieName(secure bool) string {
	if secure {
		return SecureCookieName
	}
	return CookieName
}

type Config struct {
	DesktopToken    string
	WebOrigins      func() []string
	RequestOrigin   func(*http.Request) string
	SessionLifetime time.Duration
	Now             func() time.Time
}
type Identity struct {
	sessionTokenHash string
	Desktop          bool
	SessionID        string
}
type identityKey struct{}

func IdentityFromContext(ctx context.Context) Identity {
	id, _ := ctx.Value(identityKey{}).(Identity)
	return id
}

type Status struct {
	Configured       bool     `json:"configured"`
	Authenticated    bool     `json:"authenticated"`
	Desktop          bool     `json:"desktop"`
	Username         string   `json:"username"`
	Passwordless     bool     `json:"passwordless"`
	Methods          []string `json:"methods"`
	PasskeyAvailable bool     `json:"passkey_available"`
}
type SessionView struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	UserAgent  string    `json:"user_agent"`
	Current    bool      `json:"current"`
}
type PasskeyView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
	RPID       string    `json:"rp_id"`
}
type View struct {
	Configured             bool          `json:"configured"`
	Username               string        `json:"username"`
	Desktop                bool          `json:"desktop"`
	CurrentSessionID       string        `json:"current_session_id"`
	Passwordless           bool          `json:"passwordless"`
	TOTPEnabled            bool          `json:"totp_enabled"`
	Passkeys               []PasskeyView `json:"passkeys"`
	Sessions               []SessionView `json:"sessions"`
	RecoveryCodesRemaining int           `json:"recovery_codes_remaining"`
	ReauthUntil            time.Time     `json:"reauth_until"`
}
type LoginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type LoginResult struct {
	Authenticated bool     `json:"authenticated"`
	ChallengeID   string   `json:"challenge_id,omitempty"`
	Methods       []string `json:"methods,omitempty"`
}
type CodeInput struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}
type PasskeyBeginInput struct {
	ChallengeID string `json:"challenge_id,omitempty"`
}
type PasskeyFinishInput struct {
	ChallengeID string          `json:"challenge_id"`
	Credential  json.RawMessage `json:"credential"`
}
type Ceremony struct {
	ChallengeID string          `json:"challenge_id"`
	PublicKey   json.RawMessage `json:"public_key"`
}
type AccountInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type ReauthInput struct {
	Method   string `json:"method,omitempty" enum:"password,passkey"`
	Password string `json:"password,omitempty"`
}
type TOTPSetup struct {
	SetupID string `json:"setup_id"`
	Secret  string `json:"secret"`
	URI     string `json:"uri"`
}
type TOTPConfirm struct {
	SetupID string `json:"setup_id"`
	Code    string `json:"code"`
}
type RecoveryCodes struct {
	Codes []string `json:"codes"`
}
type PasskeyName struct {
	Name string `json:"name"`
}
type PasswordlessInput struct {
	Enabled bool `json:"enabled"`
}

type accountDocument struct {
	domain.Meta
	SecretID string `json:"secret_id"`
}
type accountState struct {
	Username       string    `json:"username"`
	PasswordHash   string    `json:"password_hash"`
	UserID         []byte    `json:"user_id"`
	Passwordless   bool      `json:"passwordless"`
	TOTPSecret     string    `json:"totp_secret"`
	TOTPLastStep   int64     `json:"totp_last_step"`
	RecoveryHashes []string  `json:"recovery_hashes"`
	Passkeys       []passkey `json:"passkeys"`
	Generation     uint64    `json:"generation"`
}
type passkey struct {
	PasskeyView
	Credential webauthn.Credential `json:"credential"`
}
type browserSession struct {
	domain.Meta
	TokenHash       string    `json:"token_hash"`
	ExpiresAt       time.Time `json:"expires_at"`
	LastUsedAt      time.Time `json:"last_used_at"`
	AuthenticatedAt time.Time `json:"authenticated_at"`
	UserAgent       string    `json:"user_agent"`
}
type challenge struct {
	PasskeyOnly bool
	ParentID    string
	Kind        string
	SessionID   string
	ExpiresAt   time.Time
	Generation  uint64
	Attempts    int
	WebAuthn    *webauthn.SessionData
	RPID        string
	Origin      string
	Name        string
	Secret      string
}

func problem(status int, code, message string) error {
	return &domain.Error{Status: status, Code: code, Message: message}
}
func unauthorized() error { return problem(401, "unauthorized", "Sign in to this Server.") }
func invalidCredentials() error {
	return problem(401, "invalid_credentials", "The credentials are not valid.")
}
