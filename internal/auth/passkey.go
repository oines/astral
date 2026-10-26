package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/oines/astral/internal/domain"
)

type webUser struct {
	state accountState
	rpID  string
}

func (u webUser) WebAuthnID() []byte          { return u.state.UserID }
func (u webUser) WebAuthnName() string        { return u.state.Username }
func (u webUser) WebAuthnDisplayName() string { return u.state.Username }
func (u webUser) WebAuthnCredentials() []webauthn.Credential {
	out := []webauthn.Credential{}
	for _, p := range u.state.Passkeys {
		if p.RPID == u.rpID {
			out = append(out, p.Credential)
		}
	}
	return out
}
func (s *Service) webAuthn(r *http.Request) (*webauthn.WebAuthn, string, error) {
	if !s.PasskeyAvailable(r) {
		return nil, "", problem(400, "passkey_unavailable", "Passkeys require an allowed HTTPS address.")
	}
	origin := s.Origin(r)
	u, err := url.Parse(origin)
	if err != nil {
		return nil, "", err
	}
	rpID := strings.ToLower(u.Hostname())
	requireResident := true
	w, err := webauthn.New(&webauthn.Config{RPID: rpID, RPDisplayName: "Astral", RPOrigins: []string{origin}, AuthenticatorSelection: protocol.AuthenticatorSelection{ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: &requireResident, UserVerification: protocol.VerificationRequired}, AttestationPreference: protocol.PreferNoAttestation})
	return w, rpID, err
}
func (s *Service) BeginPasskeyRegistration(ctx context.Context, r *http.Request, id Identity, name string) (Ceremony, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return Ceremony{}, err
	}
	if s.state.Username == "" {
		return Ceremony{}, domain.Invalid("Configure a Web account first.")
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return Ceremony{}, domain.Invalid("Name this Passkey using at most 128 bytes.")
	}
	if len(s.state.Passkeys) >= 32 {
		return Ceremony{}, domain.Invalid("Remove a Passkey before adding another.")
	}
	w, rpID, err := s.webAuthn(r)
	if err != nil {
		return Ceremony{}, err
	}
	user := webUser{s.state, rpID}
	exclusions := []protocol.CredentialDescriptor{}
	for _, credential := range user.WebAuthnCredentials() {
		exclusions = append(exclusions, credential.Descriptor())
	}
	creation, session, err := w.BeginRegistration(user, webauthn.WithRegistrationOrigin(s.Origin(r)), webauthn.WithExclusions(exclusions))
	if err != nil {
		return Ceremony{}, err
	}
	key, err := s.newChallengeLocked(challenge{Kind: "passkey_registration", SessionID: actorKey(id), WebAuthn: session, RPID: rpID, Origin: s.Origin(r), Name: name})
	if err != nil {
		return Ceremony{}, err
	}
	raw, err := json.Marshal(creation.Response)
	return Ceremony{ChallengeID: key, PublicKey: raw}, err
}
func (s *Service) FinishPasskeyRegistration(ctx context.Context, r *http.Request, id Identity, input PasskeyFinishInput) (PasskeyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return PasskeyView{}, err
	}
	c, err := s.challengeLocked(input.ChallengeID, "passkey_registration")
	if err != nil {
		return PasskeyView{}, err
	}
	if c.SessionID != actorKey(id) || c.Origin != s.Origin(r) {
		return PasskeyView{}, unauthorized()
	}
	delete(s.challenges, input.ChallengeID)
	w, rpID, err := s.webAuthn(r)
	if err != nil {
		return PasskeyView{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(input.Credential)
	if err != nil {
		return PasskeyView{}, domain.Invalid("The Passkey response is not valid.")
	}
	credential, err := w.CreateCredential(webUser{s.state, rpID}, *c.WebAuthn, parsed)
	if err != nil || credential == nil || !credential.Flags.UserVerified {
		return PasskeyView{}, invalidCredentials()
	}
	for _, existing := range s.state.Passkeys {
		if bytes.Equal(existing.Credential.ID, credential.ID) {
			return PasskeyView{}, domain.Conflict("This Passkey is already registered.")
		}
	}
	next := s.cloneLocked()
	view := PasskeyView{ID: domain.ID("passkey"), Name: c.Name, CreatedAt: s.now(), RPID: rpID}
	next.Passkeys = append(next.Passkeys, passkey{PasskeyView: view, Credential: *credential})
	if err = s.factorChangedLocked(ctx, next, id.SessionID); err != nil {
		return PasskeyView{}, err
	}
	return view, nil
}
func (s *Service) BeginPasskeyLogin(ctx context.Context, r *http.Request, challengeID string) (Ceremony, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rateLocked(r.RemoteAddr); err != nil {
		return Ceremony{}, err
	}
	w, rpID, err := s.webAuthn(r)
	if err != nil {
		return Ceremony{}, err
	}
	kind := "passkey_passwordless"
	sessionID := ""
	if challengeID != "" {
		c, e := s.challengeLocked(challengeID, "login")
		if e != nil {
			return Ceremony{}, e
		}
		if e = s.verifyChallengeSessionLocked(r, c); e != nil {
			return Ceremony{}, e
		}
		sessionID = c.SessionID
		kind = "passkey_login"
	} else if !s.state.Passwordless {
		return Ceremony{}, problem(403, "passwordless_disabled", "Passkey passwordless login is not enabled.")
	}
	user := webUser{s.state, rpID}
	if len(user.WebAuthnCredentials()) == 0 {
		return Ceremony{}, problem(400, "passkey_unavailable", "No Passkey is registered for this HTTPS domain.")
	}
	var assertion *protocol.CredentialAssertion
	var data *webauthn.SessionData
	if challengeID == "" {
		assertion, data, err = w.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithLoginOrigin(s.Origin(r)))
	} else {
		assertion, data, err = w.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationRequired), webauthn.WithLoginOrigin(s.Origin(r)))
	}
	if err != nil {
		return Ceremony{}, err
	}
	key, err := s.newChallengeLocked(challenge{Kind: kind, ParentID: challengeID, SessionID: sessionID, WebAuthn: data, RPID: rpID, Origin: s.Origin(r)})
	if err != nil {
		return Ceremony{}, err
	}
	raw, err := json.Marshal(assertion.Response)
	return Ceremony{ChallengeID: key, PublicKey: raw}, err
}
func (s *Service) FinishPasskeyLogin(ctx context.Context, r *http.Request, input PasskeyFinishInput) (LoginResult, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rateLocked(r.RemoteAddr); err != nil {
		return LoginResult{}, "", err
	}
	c := s.challenges[input.ChallengeID]
	if c == nil || (c.Kind != "passkey_login" && c.Kind != "passkey_passwordless") {
		return LoginResult{}, "", invalidCredentials()
	}
	c, err := s.challengeLocked(input.ChallengeID, c.Kind)
	if err != nil {
		return LoginResult{}, "", err
	}
	if err = s.verifyChallengeSessionLocked(r, c); err != nil {
		return LoginResult{}, "", err
	}
	if c.ParentID != "" {
		if _, err = s.challengeLocked(c.ParentID, "login"); err != nil {
			return LoginResult{}, "", err
		}
	}
	delete(s.challenges, input.ChallengeID)
	if c.Origin != s.Origin(r) || (c.Kind == "passkey_passwordless" && !s.state.Passwordless) {
		return LoginResult{}, "", invalidCredentials()
	}
	w, rpID, err := s.webAuthn(r)
	if err != nil {
		return LoginResult{}, "", err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(input.Credential)
	if err != nil {
		return LoginResult{}, "", domain.Invalid("The Passkey response is not valid.")
	}
	user := webUser{s.state, rpID}
	var credential *webauthn.Credential
	if c.Kind == "passkey_passwordless" {
		credential, err = w.ValidateDiscoverableLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
			if !bytes.Equal(userHandle, s.state.UserID) {
				return nil, invalidCredentials()
			}
			for _, p := range user.WebAuthnCredentials() {
				if bytes.Equal(rawID, p.ID) {
					return user, nil
				}
			}
			return nil, invalidCredentials()
		}, *c.WebAuthn, parsed)
	} else {
		credential, err = w.ValidateLogin(user, *c.WebAuthn, parsed)
	}
	if err != nil || credential == nil {
		return LoginResult{}, "", invalidCredentials()
	}
	if credential.Authenticator.CloneWarning {
		return LoginResult{}, "", invalidCredentials()
	}
	// ValidateLogin checks the assertion's UV flag because the exact Begin session
	// required it. The stored credential's UV-initialized flag is not that proof.
	next := s.cloneLocked()
	found := false
	for i, p := range next.Passkeys {
		if p.RPID == rpID && bytes.Equal(p.Credential.ID, credential.ID) {
			next.Passkeys[i].Credential = *credential
			next.Passkeys[i].LastUsedAt = s.now()
			found = true
			break
		}
	}
	if !found {
		return LoginResult{}, "", invalidCredentials()
	}
	if err = s.saveLocked(ctx, next); err != nil {
		return LoginResult{}, "", err
	}
	if c.ParentID != "" {
		delete(s.challenges, c.ParentID)
	}
	return s.completeLocked(ctx, c, r.UserAgent())
}
