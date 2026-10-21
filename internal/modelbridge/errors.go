package modelbridge

import (
	"context"
	"errors"
	"fmt"

	models "github.com/oines/astral-models"
)

type providerError struct {
	cause   error
	message string
}

func (e *providerError) Error() string { return e.message }
func (e *providerError) Unwrap() error { return e.cause }

// Keep native classifications/Retry-After in the in-memory error chain while
// preventing provider-controlled error text from copying credentials to rollout.
func safeProviderError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	message := "Model request failed; check the model settings and provider connection."
	if models.IsEncryptedReasoningError(err) {
		return &providerError{cause: err, message: "Provider could not verify its encrypted reasoning."}
	}
	var native *models.APIError
	if errors.As(err, &native) {
		switch native.Kind {
		case models.KindAuthentication:
			message = "Model provider rejected its credentials."
		case models.KindPermissionDenied:
			message = "Model provider denied access to this model."
		case models.KindQuotaExhausted:
			message = "Model provider quota is exhausted."
		case models.KindRateLimited:
			message = "Model provider rate limit was reached."
		case models.KindServerError:
			message = "Model provider reported a server error."
		}
		if native.StatusCode > 0 {
			message = fmt.Sprintf("%s HTTP %d.", message, native.StatusCode)
		}
	}
	return &providerError{cause: err, message: message}
}
