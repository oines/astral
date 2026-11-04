package harness

import (
	"context"
	"time"
)

type imSessionResumer interface {
	ResumeIMSession(context.Context, string) error
}

type imDeliveryErrorReader interface {
	IMDeliveryError(context.Context, string) (string, error)
}

func (s *Service) imDeliveryError(ctx context.Context, id string) (string, error) {
	if reader, ok := s.IM.(imDeliveryErrorReader); ok {
		return reader.IMDeliveryError(ctx, id)
	}
	return "", nil
}

// Only explicit Owner operations call this, after releasing runtime locks.
// Passive delivery and internal continuation must not retry a paused IM inbox.
func (s *Service) resumeIMSession(ctx context.Context, id string) error {
	resumer, ok := s.IM.(imSessionResumer)
	if !ok {
		return nil
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return resumer.ResumeIMSession(persist, id)
}
