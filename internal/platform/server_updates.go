package platform

import (
	"time"

	"github.com/oines/astral/internal/serverupdate"
)

// Desktop owns its own metadata timer. A managed headless Server checks after
// launch and periodically, including when no browser is open.
func (s *Server) startUpdateChecks() {
	if s.Config.InstallationRoot == "" || s.Config.DesktopUpdateURL != "" {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		timer := time.NewTimer(30 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-timer.C:
				_, _ = serverupdate.CheckAutomatic(s.ctx, s.updates)
				timer.Reset(6 * time.Hour)
			}
		}
	}()
}
