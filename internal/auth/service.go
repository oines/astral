package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const recentLifetime = 5 * time.Minute
const challengeLifetime = 5 * time.Minute

type rateEntry struct {
	Count int
	Since time.Time
}
type Service struct {
	mu         sync.Mutex
	store      *persistence.Store
	config     Config
	document   accountDocument
	state      accountState
	sessions   map[string]*browserSession
	challenges map[string]*challenge
	requests   map[string]map[string]context.CancelFunc
	rates      map[string]rateEntry
	dummyHash  string
}

func New(store *persistence.Store, config *Config) (*Service, error) {
	c := Config{}
	if config != nil {
		c = *config
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.SessionLifetime <= 0 {
		c.SessionLifetime = 30 * 24 * time.Hour
	}
	s := &Service{store: store, config: c, sessions: map[string]*browserSession{}, challenges: map[string]*challenge{}, requests: map[string]map[string]context.CancelFunc{}, rates: map[string]rateEntry{}}
	s.document.ID = "administrator"
	ctx := context.Background()
	if err := store.Get(ctx, "auth_account", s.document.ID, &s.document); err != nil && !domain.IsMissing(err) {
		return nil, err
	}
	if s.document.SecretID != "" {
		raw, err := store.Secret(ctx, s.document.SecretID)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &s.state); err != nil {
			return nil, err
		}
	}
	records, err := store.List(ctx, "auth_sessions")
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		var session browserSession
		if err = json.Unmarshal(record.Data, &session); err != nil {
			return nil, err
		}
		if session.ExpiresAt.After(s.now()) {
			s.sessions[session.ID] = &session
		} else {
			if err = store.Update(ctx, func(tx *persistence.Tx) error { return tx.Purge(ctx, "auth_sessions", session.ID, session.Revision) }); err != nil {
				return nil, err
			}
		}
	}
	s.dummyHash, err = passwordHash(randomToken())
	return s, err
}
func (s *Service) now() time.Time { return s.config.Now().UTC() }
func (s *Service) saveLocked(ctx context.Context, next accountState) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	doc := s.document
	old := doc.SecretID
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		var e error
		doc.SecretID, e = tx.PutSecret(ctx, string(raw))
		if e != nil {
			return e
		}
		if e = tx.Save(ctx, "auth_account", &doc.Meta, doc); e != nil {
			return e
		}
		return tx.DeleteSecret(ctx, old)
	})
	if err == nil {
		s.document = doc
		s.state = next
	}
	return err
}
func (s *Service) cloneLocked() accountState {
	raw, _ := json.Marshal(s.state)
	var next accountState
	_ = json.Unmarshal(raw, &next)
	return next
}
func (s *Service) Setup(ctx context.Context, username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Username != "" {
		return domain.Conflict("The administrator account is already configured.")
	}
	return s.resetLocked(ctx, username, password)
}

// Reset is only for the trusted machine-local control interface. It revokes all
// web access, clears factors and never touches Bots, conversations or computers.
func (s *Service) Reset(ctx context.Context, username, password string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resetLocked(ctx, username, password)
}
func (s *Service) resetLocked(ctx context.Context, username, password string) error {
	username, err := validateUsername(username)
	if err != nil {
		return err
	}
	hash, err := passwordHash(password)
	if err != nil {
		return err
	}
	next := accountState{Username: username, PasswordHash: hash, UserID: []byte(randomToken()), Generation: s.state.Generation + 1, Passkeys: []passkey{}, RecoveryHashes: []string{}}
	return s.saveAndRevokeLocked(ctx, next, "")
}
func (s *Service) Status(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Configured: s.state.Username != "", Passwordless: s.state.Passwordless, Methods: s.methodsLocked()}, nil
}
func (s *Service) methodsLocked() []string {
	out := []string{}
	if s.state.TOTPSecret != "" {
		out = append(out, "totp")
	}
	if len(s.state.Passkeys) > 0 {
		out = append(out, "passkey")
	}
	if len(s.state.RecoveryHashes) > 0 {
		out = append(out, "recovery")
	}
	return out
}
func (s *Service) View(ctx context.Context, id Identity) (View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Configured: s.state.Username != "", Username: s.state.Username, Desktop: id.Desktop, CurrentSessionID: id.SessionID, Passwordless: s.state.Passwordless, TOTPEnabled: s.state.TOTPSecret != "", Passkeys: []PasskeyView{}, Sessions: []SessionView{}, RecoveryCodesRemaining: len(s.state.RecoveryHashes)}
	for _, p := range s.state.Passkeys {
		v.Passkeys = append(v.Passkeys, p.PasskeyView)
	}
	for _, session := range s.sessions {
		if !session.ExpiresAt.After(s.now()) {
			continue
		}
		v.Sessions = append(v.Sessions, SessionView{ID: session.ID, CreatedAt: session.CreatedAt, ExpiresAt: session.ExpiresAt, LastUsedAt: session.LastUsedAt, UserAgent: session.UserAgent, Current: session.ID == id.SessionID})
		if session.ID == id.SessionID {
			v.ReauthUntil = session.AuthenticatedAt.Add(recentLifetime)
		}
	}
	sort.Slice(v.Sessions, func(i, j int) bool { return v.Sessions[i].CreatedAt.After(v.Sessions[j].CreatedAt) })
	return v, nil
}
func (s *Service) Authenticate(r *http.Request) (Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if header := r.Header.Get("Authorization"); header != "" {
		const prefix = "Desktop "
		if !strings.HasPrefix(header, prefix) || s.config.DesktopToken == "" || !loopback(r.RemoteAddr) || subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(header, prefix)), []byte(s.config.DesktopToken)) != 1 {
			return Identity{}, unauthorized()
		}
		return Identity{Desktop: true}, nil
	}
	cookie, err := r.Cookie(CookieName)
	if err != nil || cookie.Value == "" {
		return Identity{}, unauthorized()
	}
	hash := digest(cookie.Value)
	for _, session := range s.sessions {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(session.TokenHash)) == 1 {
			if !session.ExpiresAt.After(s.now()) {
				_ = s.revokeLocked(context.Background(), session.ID)
				return Identity{}, unauthorized()
			}
			if s.now().Sub(session.LastUsedAt) >= time.Minute {
				next := *session
				next.LastUsedAt = s.now()
				if err := s.store.Save(r.Context(), "auth_sessions", &next.Meta, &next); err != nil {
					return Identity{}, err
				}
				s.sessions[next.ID] = &next
			}
			return Identity{SessionID: session.ID, sessionTokenHash: session.TokenHash}, nil
		}
	}
	return Identity{}, unauthorized()
}
func loopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
func (s *Service) Bind(ctx context.Context, id Identity) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.WithValue(ctx, identityKey{}, id))
	if id.Desktop {
		return ctx, cancel
	}
	s.mu.Lock()
	session := s.sessions[id.SessionID]
	if session == nil || !session.ExpiresAt.After(s.now()) {
		s.mu.Unlock()
		cancel()
		return ctx, cancel
	}
	key := randomToken()
	if s.requests[id.SessionID] == nil {
		s.requests[id.SessionID] = map[string]context.CancelFunc{}
	}
	s.requests[id.SessionID][key] = cancel
	remaining := session.ExpiresAt.Sub(s.now())
	s.mu.Unlock()
	timer := time.AfterFunc(remaining, cancel)
	return ctx, func() {
		timer.Stop()
		cancel()
		s.mu.Lock()
		delete(s.requests[id.SessionID], key)
		if len(s.requests[id.SessionID]) == 0 {
			delete(s.requests, id.SessionID)
		}
		s.mu.Unlock()
	}
}
func (s *Service) requireRecentLocked(id Identity) error {
	if id.Desktop {
		return nil
	}
	session := s.sessions[id.SessionID]
	if session == nil || !session.ExpiresAt.After(s.now()) || id.sessionTokenHash != session.TokenHash {
		return unauthorized()
	}
	if session.AuthenticatedAt.Add(recentLifetime).Before(s.now()) {
		return problem(403, "reauth_required", "Confirm your identity to change security settings.")
	}
	return nil
}
func (s *Service) rateLocked(key string) error {
	if host, _, err := net.SplitHostPort(key); err == nil {
		key = host
	}
	now := s.now()
	if len(s.rates) > 4096 {
		for k, v := range s.rates {
			if now.Sub(v.Since) > time.Minute {
				delete(s.rates, k)
			}
		}
		if len(s.rates) > 4096 {
			return problem(429, "rate_limited", "Too many authentication attempts. Try again later.")
		}
	}
	for _, k := range []string{"global", key} {
		v := s.rates[k]
		if now.Sub(v.Since) >= time.Minute {
			v = rateEntry{Since: now}
		}
		limit := 12
		if k == "global" {
			limit = 120
		}
		if v.Count >= limit {
			return problem(429, "rate_limited", "Too many authentication attempts. Try again later.")
		}
		v.Count++
		s.rates[k] = v
	}
	return nil
}
func (s *Service) newChallengeLocked(c challenge) (string, error) {
	now := s.now()
	for id, existing := range s.challenges {
		if !existing.ExpiresAt.After(now) {
			delete(s.challenges, id)
		}
	}
	if len(s.challenges) >= 256 {
		return "", problem(429, "rate_limited", "Too many authentication attempts. Try again later.")
	}
	id := randomToken()
	c.ExpiresAt = now.Add(challengeLifetime)
	c.Generation = s.state.Generation
	s.challenges[id] = &c
	return id, nil
}
func (s *Service) challengeLocked(id, kind string) (*challenge, error) {
	c := s.challenges[id]
	if c == nil || c.Kind != kind || !c.ExpiresAt.After(s.now()) || c.Generation != s.state.Generation || c.Attempts >= 5 {
		delete(s.challenges, id)
		return nil, invalidCredentials()
	}
	return c, nil
}
func (s *Service) issueLocked(ctx context.Context, ua string) (string, error) {
	token := randomToken()
	now := s.now()
	session := &browserSession{Meta: domain.Meta{ID: domain.ID("websession")}, TokenHash: digest(token), ExpiresAt: now.Add(s.config.SessionLifetime), LastUsedAt: now, AuthenticatedAt: now, UserAgent: ua}
	if len(ua) > 512 {
		session.UserAgent = ua[:512]
	}
	if err := s.store.Save(ctx, "auth_sessions", &session.Meta, session); err != nil {
		return "", err
	}
	s.sessions[session.ID] = session
	return token, nil
}
func (s *Service) completeLocked(ctx context.Context, c *challenge, ua string) (LoginResult, string, error) {
	if c != nil && c.SessionID != "" {
		session := s.sessions[c.SessionID]
		if session == nil || !session.ExpiresAt.After(s.now()) {
			return LoginResult{}, "", unauthorized()
		}
		next := *session
		token := randomToken()
		next.TokenHash = digest(token)
		next.AuthenticatedAt = s.now()
		next.LastUsedAt = s.now()
		if err := s.store.Save(ctx, "auth_sessions", &next.Meta, &next); err != nil {
			return LoginResult{}, "", err
		}
		s.sessions[next.ID] = &next
		for key, pending := range s.challenges {
			if pending.SessionID == next.ID {
				delete(s.challenges, key)
			}
		}
		return LoginResult{Authenticated: true}, token, nil
	}
	token, err := s.issueLocked(ctx, ua)
	return LoginResult{Authenticated: err == nil}, token, err
}
func (s *Service) Login(ctx context.Context, username, password, rateKey, ua string) (LoginResult, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rateLocked(rateKey); err != nil {
		return LoginResult{}, "", err
	}
	hash := s.state.PasswordHash
	if hash == "" {
		hash = s.dummyHash
	}
	match := passwordMatches(hash, password)
	if !match || username != s.state.Username || s.state.Username == "" {
		return LoginResult{}, "", invalidCredentials()
	}
	if len(s.methodsLocked()) == 0 {
		return s.completeLocked(ctx, nil, ua)
	}
	id, err := s.newChallengeLocked(challenge{Kind: "login"})
	return LoginResult{ChallengeID: id, Methods: s.methodsLocked()}, "", err
}
func (s *Service) Reauthenticate(ctx context.Context, id Identity, password, rateKey string) (LoginResult, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id.Desktop {
		return LoginResult{Authenticated: true}, "", nil
	}
	session := s.sessions[id.SessionID]
	if session == nil || !session.ExpiresAt.After(s.now()) || id.sessionTokenHash != session.TokenHash {
		return LoginResult{}, "", unauthorized()
	}
	if err := s.rateLocked(rateKey); err != nil {
		return LoginResult{}, "", err
	}
	if !passwordMatches(s.state.PasswordHash, password) {
		return LoginResult{}, "", invalidCredentials()
	}
	if len(s.methodsLocked()) == 0 {
		return s.completeLocked(ctx, &challenge{SessionID: id.SessionID}, "")
	}
	challengeID, err := s.newChallengeLocked(challenge{Kind: "login", SessionID: id.SessionID})
	return LoginResult{ChallengeID: challengeID, Methods: s.methodsLocked()}, "", err
}
func (s *Service) verifyChallengeSessionLocked(r *http.Request, c *challenge) error {
	if c.SessionID == "" {
		return nil
	}
	cookie, err := r.Cookie(CookieName)
	session := s.sessions[c.SessionID]
	if err != nil || session == nil || !session.ExpiresAt.After(s.now()) || subtle.ConstantTimeCompare([]byte(digest(cookie.Value)), []byte(session.TokenHash)) != 1 {
		return unauthorized()
	}
	return nil
}
func (s *Service) revokeLocked(ctx context.Context, id string) error {
	session := s.sessions[id]
	if session == nil {
		return nil
	}
	if err := s.store.Update(ctx, func(tx *persistence.Tx) error { return tx.Purge(ctx, "auth_sessions", id, session.Revision) }); err != nil {
		return err
	}
	delete(s.sessions, id)
	for _, cancel := range s.requests[id] {
		cancel()
	}
	delete(s.requests, id)
	for key, c := range s.challenges {
		if c.SessionID == id {
			delete(s.challenges, key)
		}
	}
	return nil
}
func (s *Service) revokeAllLocked(ctx context.Context, keep string) error {
	if err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		for id, session := range s.sessions {
			if id != keep {
				if err := tx.Purge(ctx, "auth_sessions", id, session.Revision); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for id := range s.sessions {
		if id != keep {
			delete(s.sessions, id)
			for _, cancel := range s.requests[id] {
				cancel()
			}
			delete(s.requests, id)
			for key, c := range s.challenges {
				if c.SessionID == id {
					delete(s.challenges, key)
				}
			}
		}
	}
	return nil
}
func (s *Service) Revoke(ctx context.Context, actor Identity, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(actor); err != nil {
		return err
	}
	return s.revokeLocked(ctx, id)
}
func (s *Service) RevokeAll(ctx context.Context, actor Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(actor); err != nil {
		return err
	}
	return s.revokeAllLocked(ctx, "")
}
func (s *Service) Logout(ctx context.Context, id Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id.Desktop {
		return nil
	}
	return s.revokeLocked(ctx, id.SessionID)
}
func (s *Service) SetAccount(ctx context.Context, id Identity, input AccountInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return err
	}
	name, err := validateUsername(input.Username)
	if err != nil {
		return err
	}
	next := s.cloneLocked()
	next.Username = name
	if input.Password != "" || next.PasswordHash == "" {
		next.PasswordHash, err = passwordHash(input.Password)
		if err != nil {
			return err
		}
	}
	if len(next.UserID) == 0 {
		next.UserID = []byte(randomToken())
	}
	next.Generation++
	return s.saveAndRevokeLocked(ctx, next, "")
}

// Origin is resolved by the Server's explicit trusted-proxy policy. The auth
// package deliberately never trusts forwarding headers or the Origin header.
func (s *Service) Origin(r *http.Request) string {
	if s.config.RequestOrigin != nil {
		return s.config.RequestOrigin(r)
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
func (s *Service) CheckOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return problem(403, "cross_origin", "Cross-site management requests are not allowed.")
	}
	if origin == "" {
		return nil
	}
	if origin != s.Origin(r) {
		return problem(403, "cross_origin", "Cross-origin management requests are not allowed.")
	}
	if s.config.WebOrigins != nil {
		for _, allowed := range s.config.WebOrigins() {
			if allowed == origin {
				return nil
			}
		}
		return problem(403, "cross_origin", "This management origin is not allowed.")
	}
	return nil
}
func (s *Service) PasskeyAvailable(r *http.Request) bool {
	origin := s.Origin(r)
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || net.ParseIP(u.Hostname()) != nil {
		return false
	}
	if s.config.WebOrigins == nil {
		return false
	}
	for _, allowed := range s.config.WebOrigins() {
		if allowed == origin {
			return true
		}
	}
	return false
}
func (s *Service) SetCookie(w http.ResponseWriter, r *http.Request, token string) {
	if token == "" {
		return
	}
	// A rotated token retains the session's original absolute expiry. Do not
	// give its browser Cookie a fresh full lifetime after each reauthentication.
	s.mu.Lock()
	var expires time.Time
	hash := digest(token)
	for _, session := range s.sessions {
		if session.TokenHash == hash {
			expires = session.ExpiresAt
			break
		}
	}
	remaining := expires.Sub(s.now())
	s.mu.Unlock()
	if expires.IsZero() || remaining <= 0 {
		return
	}
	maxAge := max(1, int(remaining.Seconds()))
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Origin(r), "https://"), SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Expires: expires})
}
func (s *Service) ClearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(s.Origin(r), "https://"), SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

// Account changes and the affected session revocations commit together. Failed
// storage never leaves a changed password with accidentally live old sessions.
func (s *Service) saveAndRevokeLocked(ctx context.Context, next accountState, keep string) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	doc := s.document
	old := doc.SecretID
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		var e error
		doc.SecretID, e = tx.PutSecret(ctx, string(raw))
		if e != nil {
			return e
		}
		if e = tx.Save(ctx, "auth_account", &doc.Meta, doc); e != nil {
			return e
		}
		if e = tx.DeleteSecret(ctx, old); e != nil {
			return e
		}
		for id, session := range s.sessions {
			if id != keep {
				if e = tx.Purge(ctx, "auth_sessions", id, session.Revision); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.document = doc
	s.state = next
	s.challenges = map[string]*challenge{}
	for id := range s.sessions {
		if id != keep {
			delete(s.sessions, id)
			for _, cancel := range s.requests[id] {
				cancel()
			}
			delete(s.requests, id)
		}
	}
	return nil
}

func (s *Service) StartPasskeyReauthentication(ctx context.Context, id Identity) (LoginResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id.Desktop {
		return LoginResult{Authenticated: true}, nil
	}
	session := s.sessions[id.SessionID]
	if session == nil || !session.ExpiresAt.After(s.now()) {
		return LoginResult{}, unauthorized()
	}
	if !s.state.Passwordless {
		return LoginResult{}, problem(403, "passwordless_disabled", "Passkey passwordless login is not enabled.")
	}
	key, err := s.newChallengeLocked(challenge{Kind: "login", SessionID: id.SessionID, PasskeyOnly: true})
	return LoginResult{ChallengeID: key, Methods: []string{"passkey"}}, err
}
