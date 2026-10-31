package channels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/transport"
)

func TestOperationErrorsPreserveEffectClassificationAndSafeReason(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			send := &SendError{Message: "safe platform failure", Unknown: unknown}
			got := operationError(fmt.Errorf("adapter call: %w", send))
			var problem *domain.Error
			code := "platform_rejected"
			if unknown {
				code = "outcome_unknown"
			}
			if !errors.As(got, &problem) || problem.Status != http.StatusBadGateway || problem.Code != code || problem.Message != send.Message {
				t.Fatalf("operation lost its outcome or reason: %+v", got)
			}
		})
	}
}

func TestOperationErrorsLeaveOtherFailuresUnchanged(t *testing.T) {
	for _, err := range []error{
		nil,
		domain.Invalid("bad parameter"),
		fmt.Errorf("scope check: %w", domain.Forbidden("not authorized")),
		&transport.Error{Code: "file_unavailable", Message: "source unavailable"},
		&transport.Error{Code: "process_disconnected", Message: "disconnected", Outcome: "unknown"},
		context.Canceled,
		fmt.Errorf("deadline: %w", context.DeadlineExceeded),
		errors.New("unclassified failure"),
		errors.Join(context.Canceled, &SendError{Message: "joined mutation error", Unknown: true}),
	} {
		if got := operationError(err); got != err {
			t.Fatalf("replaced an existing failure: %v -> %v", err, got)
		}
	}
}

func TestOperationErrorsClassifyTelegramRejectionsAndUpstreamFailure(t *testing.T) {
	for _, test := range []struct {
		method string
		code   int
		want   string
	}{
		{"setChatTitle", 400, "platform_rejected"},
		{"getChatMember", 403, "platform_rejected"},
		{"setChatTitle", 500, "outcome_unknown"},
		{"getChatMember", 500, "platform_failed"},
		{"setChatTitle", 200, "outcome_unknown"},
		{"getChatMember", 0, "platform_failed"},
	} {
		t.Run(fmt.Sprintf("%s/%d", test.method, test.code), func(t *testing.T) {
			upstream := &telegramAPIError{Method: test.method, Code: test.code, Description: "safe [redacted] reason"}
			err := operationError(fmt.Errorf("named operation: %w", upstream))
			var problem *domain.Error
			if !errors.As(err, &problem) || problem.Status != http.StatusBadGateway || problem.Code != test.want || problem.Message != upstream.Error() {
				t.Fatalf("Telegram failure lost its safe details or effect classification: %v", err)
			}
		})
	}
}
