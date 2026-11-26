package acp

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
	"time"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const tokenKind = "acp_tokens"

// TokenInfo omits the credential and its hash.
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

func (record tokenRecord) public() TokenInfo {
	return TokenInfo{ID: record.ID, Name: record.Name, CreatedAt: record.CreatedAt, LastUsedAt: record.LastUsedAt}
}

type recordReader interface {
	Get(context.Context, string, string, any) error
}

func availableBot(ctx context.Context, store recordReader, id string) error {
	var bot domain.Agent
	if err := store.Get(ctx, "agents", id, &bot); err != nil {
		return err
	}
	if bot.Deletion {
		return domain.Missing("Bot", id)
	}
	return nil
}

func (s *Service) CreateToken(ctx context.Context, botID, name string) (CreatedToken, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 80 {
		return CreatedToken{}, domain.Invalid("ACP token name must contain 1 to 80 characters")
	}
	id := domain.ID("acp")
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return CreatedToken{}, err
	}
	token := id + "." + base64.RawURLEncoding.EncodeToString(secret[:])
	hash := sha256.Sum256([]byte(token))
	record := tokenRecord{Meta: domain.Meta{ID: id}, BotID: botID, Name: name, Hash: hex.EncodeToString(hash[:])}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.harness.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := availableBot(ctx, tx, botID); err != nil {
			return err
		}
		return tx.Save(ctx, tokenKind, &record.Meta, record)
	})
	if err != nil {
		return CreatedToken{}, err
	}
	return CreatedToken{TokenInfo: record.public(), Token: token}, nil
}

func (s *Service) ListTokens(ctx context.Context, botID string) ([]TokenInfo, error) {
	if err := availableBot(ctx, s.harness.Store, botID); err != nil {
		return nil, err
	}
	rows, err := s.harness.Store.List(ctx, tokenKind)
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
			tokens = append(tokens, record.public())
		}
	}
	slices.SortStableFunc(tokens, func(a, b TokenInfo) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return tokens, nil
}

func (s *Service) RevokeToken(ctx context.Context, botID, tokenID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.harness.Store.Update(ctx, func(tx *persistence.Tx) error {
		var record tokenRecord
		if err := tx.Get(ctx, tokenKind, tokenID, &record); err != nil {
			return err
		}
		if record.BotID != botID {
			return domain.Missing("ACP token", tokenID)
		}
		return tx.Purge(ctx, tokenKind, tokenID, record.Revision)
	})
	if err != nil {
		return err
	}
	for conn := range s.connections {
		if conn.tokenID == tokenID {
			conn.cancel()
			s.harness.ReleaseACPMounts(conn.id)
		}
	}
	return nil
}

var errUnauthorized = errors.New("invalid ACP token")

func (s *Service) authenticate(ctx context.Context, header string) (tokenRecord, error) {
	scheme, token, valid := strings.Cut(header, " ")
	id, secret, separated := strings.Cut(token, ".")
	if !valid || !strings.EqualFold(scheme, "Bearer") || !separated || !strings.HasPrefix(id, "acp_") || len(id) > 64 || len(secret) != base64.RawURLEncoding.EncodedLen(32) {
		return tokenRecord{}, errUnauthorized
	}
	var record tokenRecord
	if err := s.harness.Store.Get(ctx, tokenKind, id, &record); err != nil {
		if domain.IsMissing(err) {
			return tokenRecord{}, errUnauthorized
		}
		return tokenRecord{}, err
	}
	hash := sha256.Sum256([]byte(token))
	storedHash, err := hex.DecodeString(record.Hash)
	if err != nil || subtle.ConstantTimeCompare(hash[:], storedHash) != 1 {
		return tokenRecord{}, errUnauthorized
	}
	return record, availableBot(ctx, s.harness.Store, record.BotID)
}
