package httpapi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

const publicAuthBodyLimit int64 = 128 << 10
const publicAuthReadTimeout = 10 * time.Second

// The credential-attempt limiter runs after decoding, so separately bound the
// number of untrusted bodies being read. One peer cannot fill every slot.
type publicAuthRequests struct {
	mu    sync.Mutex
	total int
	peers map[string]int
}

func (b *publicAuthRequests) admit(peer string) (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.total >= 64 || b.peers[peer] >= 8 {
		return nil, false
	}
	if b.peers == nil {
		b.peers = map[string]int{}
	}
	b.total++
	b.peers[peer]++
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.total--
		b.peers[peer]--
		if b.peers[peer] == 0 {
			delete(b.peers, peer)
		}
	}, true
}

// AuthHandler exposes only the explicit public sign-in routes. The Server's
// management access and Host policy applies before this handler.
func (a *API) AuthHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		controller := http.NewResponseController(w)
		bodyRead := false
		publicPost := r.Method == http.MethodPost
		boundedRequest := publicPost || r.ContentLength != 0 || len(r.TransferEncoding) != 0
		if boundedRequest {
			_ = controller.SetReadDeadline(time.Now().Add(publicAuthReadTimeout))
			defer func() {
				if bodyRead {
					_ = controller.SetReadDeadline(time.Time{})
				}
			}()
		}
		reject := func(err error) {
			if boundedRequest {
				// Do not let net/http drain an untrusted body after restoring an unlimited
				// connection deadline. A failed/early-rejected body is never reusable.
				w.Header().Set("Connection", "close")
				_ = controller.SetReadDeadline(time.Now())
			}
			httpcontract.Failure(w, r, err)
		}
		if boundedRequest && !publicPost {
			reject(domain.Invalid("This authentication endpoint does not accept a request body."))
			return
		}
		if a.Config.Auth == nil {
			reject(domain.Offline("Authentication is unavailable."))
			return
		}
		if err := a.Config.Auth.CheckOrigin(r); err != nil {
			reject(err)
			return
		}
		if publicPost {
			release, allowed := a.authRequests.admit(rateKey(r))
			if !allowed {
				reject(&domain.Error{Status: 429, Code: "rate_limited", Message: "Too many authentication requests. Try again shortly."})
				return
			}
			defer release()
			if r.ContentLength > publicAuthBodyLimit {
				reject(&domain.Error{Status: 413, Code: "request_too_large", Message: "Authentication requests must not exceed 128 KiB."})
				return
			}
			original := r.Body
			limited := http.MaxBytesReader(w, original, publicAuthBodyLimit)
			raw, err := io.ReadAll(limited)
			if err != nil {
				var maxBytes *http.MaxBytesError
				var networkError net.Error
				switch {
				case errors.As(err, &maxBytes):
					reject(&domain.Error{Status: 413, Code: "request_too_large", Message: "Authentication requests must not exceed 128 KiB."})
				case errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()):
					reject(&domain.Error{Status: 408, Code: "request_timeout", Message: "The authentication request body took too long to arrive."})
				default:
					reject(domain.Invalid("The authentication request body could not be read."))
				}
				_ = limited.Close()
				return
			}
			_ = limited.Close()
			bodyRead = true
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		a.authMux.ServeHTTP(w, r)
	})
}
func authRoute[Request, Response any](a *API, method, path, id string, handler func(http.ResponseWriter, *http.Request, Request) (Response, error)) {
	httpcontract.Register(a.Contract, a.authMux, httpcontract.Operation{Method: method, Path: path, ID: id, Tag: "Authentication"}, handler)
}
func rateKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func (a *API) authRoutes() {
	a.authMux = http.NewServeMux()
	authRoute[httpcontract.Empty, auth.Status](a, "GET", "/auth/status", "authStatus", func(w http.ResponseWriter, r *http.Request, _ httpcontract.Empty) (auth.Status, error) {
		s, err := a.Config.Auth.Status(r.Context())
		if err != nil {
			return s, err
		}
		s.PasskeyAvailable = a.Config.Auth.PasskeyAvailable(r)
		if id, e := a.Config.Auth.Authenticate(r); e == nil {
			s.Authenticated = true
			s.Desktop = id.Desktop
			v, e := a.Config.Auth.View(r.Context(), id)
			if e != nil {
				return s, e
			}
			s.Username = v.Username
		}
		return s, nil
	})
	authRoute[auth.LoginInput, auth.LoginResult](a, "POST", "/auth/login", "authLogin", func(w http.ResponseWriter, r *http.Request, input auth.LoginInput) (auth.LoginResult, error) {
		result, token, err := a.Config.Auth.Login(r.Context(), input.Username, input.Password, rateKey(r), r.UserAgent())
		if err == nil {
			a.Config.Auth.SetCookie(w, r, token)
		}
		return result, err
	})
	for _, item := range []struct {
		path, id string
		recovery bool
	}{{"/auth/login/totp", "authLoginTOTP", false}, {"/auth/login/recovery", "authLoginRecovery", true}} {
		authRoute[auth.CodeInput, auth.LoginResult](a, "POST", item.path, item.id, func(w http.ResponseWriter, r *http.Request, input auth.CodeInput) (auth.LoginResult, error) {
			result, token, err := a.Config.Auth.CompleteCode(r.Context(), r, input, item.recovery)
			if err == nil {
				a.Config.Auth.SetCookie(w, r, token)
			}
			return result, err
		})
	}
	authRoute[auth.PasskeyBeginInput, auth.Ceremony](a, "POST", "/auth/login/passkey/begin", "authPasskeyLoginBegin", func(w http.ResponseWriter, r *http.Request, input auth.PasskeyBeginInput) (auth.Ceremony, error) {
		return a.Config.Auth.BeginPasskeyLogin(r.Context(), r, input.ChallengeID)
	})
	authRoute[auth.PasskeyFinishInput, auth.LoginResult](a, "POST", "/auth/login/passkey/finish", "authPasskeyLoginFinish", func(w http.ResponseWriter, r *http.Request, input auth.PasskeyFinishInput) (auth.LoginResult, error) {
		result, token, err := a.Config.Auth.FinishPasskeyLogin(r.Context(), r, input)
		if err == nil {
			a.Config.Auth.SetCookie(w, r, token)
		}
		return result, err
	})
	authRoute[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/auth/session", "authLogout", func(w http.ResponseWriter, r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		id, err := a.Config.Auth.Authenticate(r)
		if err == nil {
			if err = a.Config.Auth.Logout(r.Context(), id); err != nil {
				return httpcontract.OK{}, err
			}
		}
		a.Config.Auth.ClearCookie(w, r)
		return httpcontract.OK{OK: true}, nil
	})
	route[httpcontract.Empty, auth.View](a, "GET", "/auth", "authSettings", "Authentication", func(r *http.Request, _ httpcontract.Empty) (auth.View, error) {
		return a.Config.Auth.View(r.Context(), auth.IdentityFromContext(r.Context()))
	})
	route[auth.AccountInput, httpcontract.OK](a, "POST", "/auth/account", "authAccount", "Authentication", func(r *http.Request, input auth.AccountInput) (httpcontract.OK, error) {
		err := a.Config.Auth.SetAccount(r.Context(), auth.IdentityFromContext(r.Context()), input)
		return httpcontract.OK{OK: err == nil}, err
	})
	httpcontract.Register[auth.ReauthInput, auth.LoginResult](a.Contract, a.mux, httpcontract.Operation{Method: "POST", Path: "/api/v1/auth/reauth", ID: "authReauthenticate", Tag: "Authentication", Auth: true}, func(w http.ResponseWriter, r *http.Request, input auth.ReauthInput) (auth.LoginResult, error) {
		if input.Method == "passkey" {
			return a.Config.Auth.StartPasskeyReauthentication(r.Context(), auth.IdentityFromContext(r.Context()))
		}
		if input.Method != "" && input.Method != "password" {
			return auth.LoginResult{}, domain.Invalid("Unknown authentication method.")
		}
		result, token, err := a.Config.Auth.Reauthenticate(r.Context(), auth.IdentityFromContext(r.Context()), input.Password, rateKey(r))
		if err == nil {
			a.Config.Auth.SetCookie(w, r, token)
		}
		return result, err
	})
	route[httpcontract.Empty, auth.TOTPSetup](a, "POST", "/auth/totp/begin", "authTOTPBegin", "Authentication", func(r *http.Request, _ httpcontract.Empty) (auth.TOTPSetup, error) {
		return a.Config.Auth.BeginTOTP(r.Context(), auth.IdentityFromContext(r.Context()))
	})
	route[auth.TOTPConfirm, auth.RecoveryCodes](a, "POST", "/auth/totp/confirm", "authTOTPConfirm", "Authentication", func(r *http.Request, input auth.TOTPConfirm) (auth.RecoveryCodes, error) {
		return a.Config.Auth.ConfirmTOTP(r.Context(), auth.IdentityFromContext(r.Context()), input)
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/auth/totp", "authTOTPRemove", "Authentication", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		err := a.Config.Auth.RemoveTOTP(r.Context(), auth.IdentityFromContext(r.Context()))
		return httpcontract.OK{OK: err == nil}, err
	})
	route[auth.PasskeyName, auth.Ceremony](a, "POST", "/auth/passkeys/begin", "authPasskeyRegisterBegin", "Authentication", func(r *http.Request, input auth.PasskeyName) (auth.Ceremony, error) {
		return a.Config.Auth.BeginPasskeyRegistration(r.Context(), r, auth.IdentityFromContext(r.Context()), input.Name)
	})
	route[auth.PasskeyFinishInput, auth.PasskeyView](a, "POST", "/auth/passkeys/finish", "authPasskeyRegisterFinish", "Authentication", func(r *http.Request, input auth.PasskeyFinishInput) (auth.PasskeyView, error) {
		return a.Config.Auth.FinishPasskeyRegistration(r.Context(), r, auth.IdentityFromContext(r.Context()), input)
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/auth/passkeys/{id}", "authPasskeyRemove", "Authentication", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		err := a.Config.Auth.RemovePasskey(r.Context(), auth.IdentityFromContext(r.Context()), r.PathValue("id"))
		return httpcontract.OK{OK: err == nil}, err
	})
	route[auth.PasswordlessInput, auth.View](a, "PATCH", "/auth/passwordless", "authPasswordless", "Authentication", func(r *http.Request, input auth.PasswordlessInput) (auth.View, error) {
		id := auth.IdentityFromContext(r.Context())
		if err := a.Config.Auth.SetPasswordless(r.Context(), id, input.Enabled); err != nil {
			return auth.View{}, err
		}
		return a.Config.Auth.View(r.Context(), id)
	})
	route[httpcontract.Empty, auth.RecoveryCodes](a, "POST", "/auth/recovery-codes", "authRecoveryCodes", "Authentication", func(r *http.Request, _ httpcontract.Empty) (auth.RecoveryCodes, error) {
		return a.Config.Auth.RegenerateRecoveryCodes(r.Context(), auth.IdentityFromContext(r.Context()))
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/auth/sessions/{id}", "authSessionRevoke", "Authentication", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		err := a.Config.Auth.Revoke(r.Context(), auth.IdentityFromContext(r.Context()), r.PathValue("id"))
		return httpcontract.OK{OK: err == nil}, err
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/auth/sessions", "authSessionsRevoke", "Authentication", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		err := a.Config.Auth.RevokeAll(r.Context(), auth.IdentityFromContext(r.Context()))
		return httpcontract.OK{OK: err == nil}, err
	})
	a.Contract.Describe(httpcontract.Operation{Method: "GET", Path: "/api/v1/auth/events", ID: "authEvents", Tag: "Authentication", Auth: true, ContentType: "text/event-stream", Response: reflect.TypeFor[httpcontract.Empty]()})
	a.mux.HandleFunc("GET /api/v1/auth/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		_, _ = fmt.Fprint(w, "event: ready\ndata: {}\n\n")
		if err := controller.Flush(); err != nil {
			return
		}
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				if err := controller.Flush(); err != nil {
					return
				}
			}
		}
	})
}
