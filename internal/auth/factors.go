package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func actorKey(id Identity) string {
	if id.Desktop {
		return "desktop"
	}
	return id.SessionID
}
func (s *Service) factorChangedLocked(ctx context.Context, next accountState, keep string) error {
	next.Generation++
	return s.saveAndRevokeLocked(ctx, next, keep)
}
func newRecoveryCodes() ([]string, []string, error) {
	codes := make([]string, 10)
	hashes := make([]string, 10)
	for i := range codes {
		raw := make([]byte, 12)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		v := hex.EncodeToString(raw)
		codes[i] = v[:6] + "-" + v[6:12] + "-" + v[12:18] + "-" + v[18:]
		hashes[i] = digest(v)
	}
	return codes, hashes, nil
}
func normalizedCode(code string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}
func totpStep(secret, code string, now time.Time, last int64) (int64, bool) {
	step := now.Unix() / 30
	for _, offset := range []int64{0, -1, 1} {
		candidate := step + offset
		if candidate <= last {
			continue
		}
		expected, err := totp.GenerateCodeCustom(secret, time.Unix(candidate*30, 0), totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return candidate, true
		}
	}
	return 0, false
}
func (s *Service) BeginTOTP(ctx context.Context, id Identity) (TOTPSetup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return TOTPSetup{}, err
	}
	if s.state.Username == "" {
		return TOTPSetup{}, domain.Invalid("Configure a Web account first.")
	}
	if s.state.TOTPSecret != "" {
		return TOTPSetup{}, domain.Conflict("An authenticator is already configured.")
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "Astral", AccountName: s.state.Username, SecretSize: 32, Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		return TOTPSetup{}, err
	}
	setupID, err := s.newChallengeLocked(challenge{Kind: "totp_setup", SessionID: actorKey(id), Secret: key.Secret()})
	return TOTPSetup{SetupID: setupID, Secret: key.Secret(), URI: key.URL()}, err
}
func (s *Service) ConfirmTOTP(ctx context.Context, id Identity, input TOTPConfirm) (RecoveryCodes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return RecoveryCodes{}, err
	}
	c, err := s.challengeLocked(input.SetupID, "totp_setup")
	if err != nil {
		return RecoveryCodes{}, err
	}
	if c.SessionID != actorKey(id) {
		return RecoveryCodes{}, unauthorized()
	}
	c.Attempts++
	step, valid := totpStep(c.Secret, input.Code, s.now(), 0)
	if !valid {
		return RecoveryCodes{}, invalidCredentials()
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return RecoveryCodes{}, err
	}
	next := s.cloneLocked()
	next.TOTPSecret = c.Secret
	next.TOTPLastStep = step
	next.RecoveryHashes = hashes
	if err = s.factorChangedLocked(ctx, next, id.SessionID); err != nil {
		return RecoveryCodes{}, err
	}
	return RecoveryCodes{Codes: codes}, nil
}
func (s *Service) RemoveTOTP(ctx context.Context, id Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return err
	}
	next := s.cloneLocked()
	next.TOTPSecret = ""
	next.TOTPLastStep = 0
	if len(next.Passkeys) == 0 {
		next.RecoveryHashes = []string{}
	}
	return s.factorChangedLocked(ctx, next, id.SessionID)
}
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, id Identity) (RecoveryCodes, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return RecoveryCodes{}, err
	}
	if s.state.TOTPSecret == "" && len(s.state.Passkeys) == 0 {
		return RecoveryCodes{}, domain.Invalid("Add an authenticator or Passkey first.")
	}
	codes, hashes, err := newRecoveryCodes()
	if err != nil {
		return RecoveryCodes{}, err
	}
	next := s.cloneLocked()
	next.RecoveryHashes = hashes
	if err = s.factorChangedLocked(ctx, next, id.SessionID); err != nil {
		return RecoveryCodes{}, err
	}
	return RecoveryCodes{Codes: codes}, nil
}
func (s *Service) CompleteCode(ctx context.Context, r *http.Request, input CodeInput, recovery bool) (LoginResult, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rateLocked(r.RemoteAddr); err != nil {
		return LoginResult{}, "", err
	}
	c, err := s.challengeLocked(input.ChallengeID, "login")
	if err != nil {
		return LoginResult{}, "", err
	}
	if c.PasskeyOnly {
		return LoginResult{}, "", invalidCredentials()
	}
	if err = s.verifyChallengeSessionLocked(r, c); err != nil {
		return LoginResult{}, "", err
	}
	c.Attempts++
	next := s.cloneLocked()
	valid := false
	if recovery {
		hash := digest(normalizedCode(input.Code))
		for i, value := range next.RecoveryHashes {
			if subtle.ConstantTimeCompare([]byte(hash), []byte(value)) == 1 {
				next.RecoveryHashes = append(next.RecoveryHashes[:i], next.RecoveryHashes[i+1:]...)
				valid = true
				break
			}
		}
	} else if next.TOTPSecret != "" {
		next.TOTPLastStep, valid = totpStep(next.TOTPSecret, input.Code, s.now(), next.TOTPLastStep)
	}
	if !valid {
		return LoginResult{}, "", invalidCredentials()
	}
	delete(s.challenges, input.ChallengeID)
	if err = s.saveLocked(ctx, next); err != nil {
		return LoginResult{}, "", err
	}
	return s.completeLocked(ctx, c, r.UserAgent())
}
func (s *Service) RemovePasskey(ctx context.Context, id Identity, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return err
	}
	next := s.cloneLocked()
	found := false
	for i, p := range next.Passkeys {
		if p.ID == keyID {
			next.Passkeys = append(next.Passkeys[:i], next.Passkeys[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return domain.Missing("Passkey", keyID)
	}
	if len(next.Passkeys) == 0 {
		next.Passwordless = false
		if next.TOTPSecret == "" {
			next.RecoveryHashes = []string{}
		}
	}
	return s.factorChangedLocked(ctx, next, id.SessionID)
}
func (s *Service) SetPasswordless(ctx context.Context, id Identity, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireRecentLocked(id); err != nil {
		return err
	}
	if enabled && len(s.state.Passkeys) == 0 {
		return domain.Invalid("Add a Passkey before enabling passwordless login.")
	}
	next := s.cloneLocked()
	next.Passwordless = enabled
	return s.factorChangedLocked(ctx, next, id.SessionID)
}
