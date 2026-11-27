package botaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const tokenKind = "bot_api_tokens"
const tokenPrefix = "bapi"

var ErrUnauthorized = errors.New("invalid Bot API token")

type TokenInfo struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type CreatedToken struct {
	TokenInfo
	Token string `json:"token"`
}

type tokenRecord struct {
	domain.Meta
	BotID      string     `json:"agent_id"`
	Name       string     `json:"name"`
	Hash       string     `json:"hash"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

func (r tokenRecord) info() TokenInfo {
	return TokenInfo{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, LastUsedAt: r.LastUsedAt}
}

type Service struct {
	store *persistence.Store

	mu     sync.Mutex
	active map[string]*tokenLease
}

type tokenLease struct {
	ctx      context.Context
	cancel   context.CancelFunc
	requests map[context.Context]context.CancelFunc
}

func New(store *persistence.Store) *Service {
	if store == nil {
		return nil
	}
	return &Service{store: store, active: make(map[string]*tokenLease)}
}

func (s *Service) CreateToken(ctx context.Context, botID, name string) (CreatedToken, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 80 {
		return CreatedToken{}, domain.Invalid("Bot API token name must contain 1 to 80 characters.")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return CreatedToken{}, err
	}
	id := domain.ID(tokenPrefix)
	token := id + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	digest := sha256.Sum256([]byte(token))
	record := tokenRecord{Meta: domain.Meta{ID: id}, BotID: botID, Name: name, Hash: hex.EncodeToString(digest[:])}
	if err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.RequireActiveAgent(ctx, botID); err != nil {
			return err
		}
		return tx.Save(ctx, tokenKind, &record.Meta, record)
	}); err != nil {
		return CreatedToken{}, err
	}
	return CreatedToken{TokenInfo: record.info(), Token: token}, nil
}

func (s *Service) ListTokens(ctx context.Context, botID string) ([]TokenInfo, error) {
	if err := s.requireBot(ctx, botID); err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, tokenKind)
	if err != nil {
		return nil, err
	}
	tokens := []TokenInfo{}
	for _, row := range rows {
		var record tokenRecord
		if err = json.Unmarshal(row.Data, &record); err != nil {
			return nil, err
		}
		if record.BotID == botID {
			tokens = append(tokens, record.info())
		}
	}
	slices.SortStableFunc(tokens, func(a, b TokenInfo) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return tokens, nil
}

func (s *Service) RevokeToken(ctx context.Context, botID, tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		var record tokenRecord
		if err := tx.Get(ctx, tokenKind, tokenID, &record); err != nil {
			return err
		}
		if record.BotID != botID {
			return domain.Missing("Bot API token", tokenID)
		}
		return tx.Purge(ctx, tokenKind, tokenID, record.Revision)
	}); err != nil {
		return err
	}
	s.cancelLeaseLocked(tokenID)
	return nil
}

// Authorize binds a request to one Bot until release, revocation or Bot deletion.
func (s *Service) Authorize(ctx context.Context, header string) (string, context.Context, func(), error) {
	scheme, token, valid := strings.Cut(header, " ")
	token = strings.TrimLeft(token, " ")
	id, secret, separated := strings.Cut(token, ".")
	if !valid || !strings.EqualFold(scheme, "Bearer") || !separated || !strings.HasPrefix(id, tokenPrefix+"_") || len(id) > 64 || len(secret) != base64.RawURLEncoding.EncodedLen(32) {
		return "", nil, nil, ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	lease := s.active[id]
	var watcher *persistence.Watcher
	var changed <-chan struct{}
	if lease == nil {
		// Subscribe before checking stored state so concurrent deletion cannot be
		// missed between reading the credential and registering the first request.
		watcher = s.store.Watch(persistence.ChangeKey{Kind: tokenKind, ID: id}, persistence.ChangeKey{Kind: "agents"})
		changed = watcher.Changes()
		defer func() {
			if watcher != nil {
				watcher.Close()
			}
		}()
	}
	var record tokenRecord
	if err := s.store.Get(ctx, tokenKind, id, &record); err != nil {
		if domain.IsMissing(err) {
			err = ErrUnauthorized
		}
		return "", nil, nil, err
	}
	storedHash, err := hex.DecodeString(record.Hash)
	if err != nil || subtle.ConstantTimeCompare(digest[:], storedHash) != 1 {
		return "", nil, nil, ErrUnauthorized
	}
	if err := s.requireBot(ctx, record.BotID); err != nil {
		if domain.IsMissing(err) {
			err = domain.Forbidden("This Bot is unavailable.")
		}
		return "", nil, nil, err
	}
	if lease == nil {
		watchCtx, cancel := context.WithCancel(context.Background())
		lease = &tokenLease{ctx: watchCtx, cancel: cancel, requests: make(map[context.Context]context.CancelFunc)}
		s.active[id] = lease
		go s.watchLease(record, lease, watcher, changed)
		watcher = nil
	}
	scoped, cancel := context.WithCancel(ctx)
	lease.requests[scoped] = cancel
	s.touchToken(ctx, record)
	return record.BotID, scoped, func() {
		cancel()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active[id] == lease {
			delete(lease.requests, scoped)
			if len(lease.requests) == 0 {
				s.cancelLeaseLocked(id)
			}
		}
	}, nil
}

func (s *Service) requireBot(ctx context.Context, id string) error {
	var bot domain.Agent
	if err := s.store.Get(ctx, "agents", id, &bot); err != nil {
		return err
	}
	if bot.Deletion {
		return domain.Missing("Bot", id)
	}
	return nil
}

func (s *Service) touchToken(ctx context.Context, record tokenRecord) {
	now := time.Now().UTC()
	if record.LastUsedAt != nil && now.Sub(*record.LastUsedAt) < time.Minute {
		return
	}
	record.LastUsedAt = &now
	_ = s.store.Save(ctx, tokenKind, &record.Meta, record)
}

func (s *Service) cancelLeaseLocked(id string) {
	if lease := s.active[id]; lease != nil {
		delete(s.active, id)
		lease.cancel()
		for _, cancel := range lease.requests {
			cancel()
		}
	}
}

func (s *Service) watchLease(token tokenRecord, lease *tokenLease, watcher *persistence.Watcher, changed <-chan struct{}) {
	defer watcher.Close()
	for {
		select {
		case <-lease.ctx.Done():
			return
		case <-changed:
			changed = watcher.Changes()
			var current tokenRecord
			if err := s.store.Get(lease.ctx, tokenKind, token.ID, &current); err == nil && current.BotID == token.BotID && current.Hash == token.Hash && s.requireBot(lease.ctx, token.BotID) == nil {
				continue
			}
			s.mu.Lock()
			if s.active[token.ID] == lease {
				s.cancelLeaseLocked(token.ID)
			}
			s.mu.Unlock()
			return
		}
	}
}
