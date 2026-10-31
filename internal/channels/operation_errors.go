package channels

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

// Named mutations bypass Outbox, so their errors must retain the adapter's
// effect classification at the HTTP boundary rather than becoming a generic
// server failure. Adapter SendError messages already contain safe diagnostics.
func operationError(err error) error {
	var problem *domain.Error
	var remote *transport.Error
	if errors.As(err, &problem) || errors.As(err, &remote) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var telegram *telegramAPIError
	if errors.As(err, &telegram) {
		code := "platform_failed"
		if telegram.Code >= 400 && telegram.Code < 500 {
			code = "platform_rejected"
		} else if !strings.HasPrefix(telegram.Method, "get") {
			code = "outcome_unknown"
		}
		// Telegram's request layer has already removed the Bot token and
		// bounded this description. Do not return the raw response envelope.
		return &domain.Error{Status: http.StatusBadGateway, Code: code, Message: telegram.Error()}
	}
	var send *SendError
	if !errors.As(err, &send) {
		return err
	}
	code := "platform_rejected"
	if send.Unknown {
		code = "outcome_unknown"
	}
	return &domain.Error{Status: http.StatusBadGateway, Code: code, Message: send.Message}
}
