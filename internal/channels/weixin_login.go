package channels

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/oines/astral/internal/domain"
)

// QR grants are Owner-only, short-lived server records. The browser receives a
// QR payload and connection identity, never the platform credential.
type WeixinLoginInput struct {
	Name         string `json:"name"`
	ConnectionID string `json:"connection_id,omitempty"`
}
type WeixinLoginPoll struct {
	VerifyCode string `json:"verify_code,omitempty"`
}
type WeixinLoginView struct {
	ID         string             `json:"id"`
	QrcodeURL  string             `json:"qrcode_url"`
	Status     string             `json:"status"`
	Connection *domain.Connection `json:"connection,omitempty"`
}
type weixinLogin struct {
	mu         sync.Mutex
	qr         WeixinQR
	agentID    string
	input      WeixinLoginInput
	expires    time.Time
	connection *domain.Connection
	credential *WeixinCredential
}

func (s *Service) StartWeixinLogin(ctx context.Context, agentID string, input WeixinLoginInput) (WeixinLoginView, error) {
	if strings.TrimSpace(input.Name) == "" {
		return WeixinLoginView{}, domain.Invalid("account name is required")
	}
	var agent domain.Agent
	if err := s.Store.Get(ctx, "agents", agentID, &agent); err != nil {
		return WeixinLoginView{}, err
	}
	if agent.Deletion {
		return WeixinLoginView{}, domain.Missing("Bot", agentID)
	}
	if input.ConnectionID != "" {
		var c domain.Connection
		if err := s.Store.Get(ctx, "connections", input.ConnectionID, &c); err != nil {
			return WeixinLoginView{}, err
		}
		if c.AgentID != agentID || c.Adapter != "weixin.ilink" {
			return WeixinLoginView{}, domain.Forbidden("account does not belong to this Bot's Weixin connections")
		}
	}
	qr, err := WeixinStartQR(ctx, s.Client)
	if err != nil {
		return WeixinLoginView{}, err
	}
	id := domain.ID("weixin_login")
	pending := &weixinLogin{qr: qr, agentID: agentID, input: input, expires: time.Now().Add(10 * time.Minute)}
	s.mu.Lock()
	if s.deletingAgents[agentID] {
		s.mu.Unlock()
		return WeixinLoginView{}, domain.Missing("Bot", agentID)
	}
	if s.weixinLogins == nil {
		s.weixinLogins = map[string]*weixinLogin{}
	}
	for key, prior := range s.weixinLogins {
		if time.Now().After(prior.expires) {
			delete(s.weixinLogins, key)
		}
	}
	s.weixinLogins[id] = pending
	s.mu.Unlock()
	return WeixinLoginView{ID: id, QrcodeURL: qr.QrcodeURL, Status: "wait"}, nil
}
func (s *Service) PollWeixinLogin(ctx context.Context, id string, input WeixinLoginPoll) (WeixinLoginView, error) {
	s.mu.Lock()
	pending := s.weixinLogins[id]
	s.mu.Unlock()
	if pending == nil {
		return WeixinLoginView{}, domain.Invalid("QR login expired; start a new authorization")
	}
	pending.mu.Lock()
	defer pending.mu.Unlock()
	view := WeixinLoginView{ID: id, QrcodeURL: pending.qr.QrcodeURL, Status: "expired"}
	if err := s.activeAgent(ctx, pending.agentID); err != nil {
		return view, err
	}
	if time.Now().After(pending.expires) {
		return view, nil
	}
	if pending.connection != nil {
		view.Status = "confirmed"
		view.Connection = pending.connection
		return view, nil
	}
	if pending.credential == nil {
		status, err := WeixinPollQR(ctx, pending.qr, input.VerifyCode, s.Client)
		if err != nil {
			return view, err
		}
		view.Status = status.Status
		if status.RedirectHost != "" {
			pending.qr.BaseURL = "https://" + status.RedirectHost
		}
		if status.Credential == nil {
			return view, nil
		}
		pending.credential = status.Credential
	}
	view.Status = "confirmed"
	var err error
	c := domain.Connection{AgentID: pending.agentID, Name: strings.TrimSpace(pending.input.Name), Adapter: "weixin.ilink", Enabled: true, AccessMode: "specified"}
	if pending.input.ConnectionID != "" {
		if err = s.Store.Get(ctx, "connections", pending.input.ConnectionID, &c); err != nil {
			return view, err
		}
		if c.AgentID != pending.agentID || c.Adapter != "weixin.ilink" {
			return view, domain.Forbidden("account changed during authorization")
		}
	}
	// The API endpoint is bound to Tencent's confirmed QR credential. Preserve
	// independent name/access/enabled edits made while scanning.
	c.Config = encoded(map[string]string{"base_url": pending.credential.BaseURL})
	secret := string(encoded(pending.credential))
	if err = s.saveConnection(ctx, &c, &secret, true); err != nil {
		return view, err
	}
	pending.connection = &c
	pending.credential = nil
	view.Connection = &c
	return view, nil
}
