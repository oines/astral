package channels

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/oines/astral/internal/domain"
)

const weixinDefaultURL = "https://ilinkai.weixin.qq.com"
const weixinDefaultCDN = "https://novac2c.cdn.weixin.qq.com/c2c"

// WeixinCredential is a QR-issued authorization grant. Store the complete JSON
// in the encrypted credential store, never in public Connection.Config.
// iLink has no identity introspection endpoint: BotID must come from this grant,
// rather than an account identifier supplied in editable connection settings.
type WeixinCredential struct {
	BotToken string `json:"bot_token"`
	BotID    string `json:"bot_id"`
	UserID   string `json:"user_id"`
	BaseURL  string `json:"base_url"`
}

// WeixinQR is server-owned login state. QrcodeURL is content to encode as a QR
// code, not necessarily an image URL. The owner API should retain Qrcode and
// BaseURL behind its own expiring, opaque login identifier.
type WeixinQR struct {
	Qrcode    string `json:"qrcode"`
	QrcodeURL string `json:"qrcode_url"`
	BaseURL   string `json:"base_url"`
}

type WeixinQRStatus struct {
	Status       string            `json:"status"`
	RedirectHost string            `json:"redirect_host,omitempty"`
	Credential   *WeixinCredential `json:"-"`
}

func weixinTrustedAPIURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "weixin.qq.com" || strings.HasSuffix(host, ".weixin.qq.com")
}

// WeixinStartQR begins the Tencent iLink login flow at its fixed public host.
// The caller owns login expiry, polling, confirmation, and credential storage.
func WeixinStartQR(ctx context.Context, client *http.Client) (WeixinQR, error) {
	w := weixinHTTPClient(client)
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var response struct {
		Qrcode string `json:"qrcode"`
		URL    string `json:"qrcode_img_content"`
	}
	if err := weixinRequest(ctx, w, weixinDefaultURL, "", http.MethodPost, "/ilink/bot/get_bot_qrcode?bot_type=3", map[string]any{"local_token_list": []string{}}, &response); err != nil {
		return WeixinQR{}, err
	}
	if response.Qrcode == "" || response.URL == "" {
		return WeixinQR{}, domain.Offline("Weixin did not return a usable login QR code")
	}
	return WeixinQR{Qrcode: response.Qrcode, QrcodeURL: response.URL, BaseURL: weixinDefaultURL}, nil
}

// WeixinPollQR performs one long poll. On scaned_but_redirect, retain the QR
// value and replace qr.BaseURL with "https://" + result.RedirectHost before the
// next poll. need_verifycode asks for the digits displayed on the user's phone;
// the protocol does not supply those digits or a separate verification URL.
func WeixinPollQR(ctx context.Context, qr WeixinQR, verifyCode string, client *http.Client) (WeixinQRStatus, error) {
	if !weixinTrustedAPIURL(qr.BaseURL) || qr.Qrcode == "" {
		return WeixinQRStatus{}, domain.Invalid("invalid Weixin QR login state")
	}
	query := url.Values{"qrcode": {qr.Qrcode}}
	if verifyCode != "" {
		if len(verifyCode) > 32 || strings.IndexFunc(verifyCode, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return WeixinQRStatus{}, domain.Invalid("Weixin verification code must contain digits")
		}
		query.Set("verify_code", verifyCode)
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	var response struct {
		Status       string `json:"status"`
		BotToken     string `json:"bot_token"`
		BotID        string `json:"ilink_bot_id"`
		UserID       string `json:"ilink_user_id"`
		BaseURL      string `json:"baseurl"`
		RedirectHost string `json:"redirect_host"`
	}
	err := weixinRequest(ctx, weixinHTTPClient(client), qr.BaseURL, "", http.MethodGet, "/ilink/bot/get_qrcode_status?"+query.Encode(), nil, &response)
	if err != nil {
		return WeixinQRStatus{}, err
	}
	status := WeixinQRStatus{Status: response.Status}
	switch response.Status {
	case "wait", "scaned", "expired", "need_verifycode", "verify_code_blocked", "binded_redirect":
	case "scaned_but_redirect":
		if !weixinTrustedAPIURL("https://" + response.RedirectHost) {
			return WeixinQRStatus{}, domain.Invalid("Weixin returned an untrusted QR redirect host")
		}
		status.RedirectHost = response.RedirectHost
	case "confirmed":
		base := response.BaseURL
		if base == "" {
			base = qr.BaseURL
		}
		if !weixinTrustedAPIURL(base) || response.BotToken == "" || response.BotID == "" || response.UserID == "" || strings.ContainsAny(response.BotToken, "\r\n") {
			return WeixinQRStatus{}, domain.Invalid("Weixin returned an incomplete login grant")
		}
		status.Credential = &WeixinCredential{BotToken: response.BotToken, BotID: response.BotID, UserID: response.UserID, BaseURL: strings.TrimRight(base, "/")}
	default:
		return WeixinQRStatus{}, domain.Offline("Weixin returned an unknown QR login status")
	}
	return status, nil
}
