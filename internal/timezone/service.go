// Package timezone resolves the Server's global presentation and scheduling zone.
package timezone

import (
	"context"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type View struct {
	Name              string `json:"name"`
	System            string `json:"system"`
	UsingSystem       bool   `json:"using_system"`
	BrowserCompatible bool   `json:"browser_compatible"`
}

type Service struct {
	store      *persistence.Store
	once       sync.Once
	system     func() (string, *time.Location, error)
	systemName string
	systemZone *time.Location
	systemErr  error
}

func New(store *persistence.Store) *Service { return &Service{store: store, system: SystemLocation} }

// Validate accepts an IANA override, or an empty value to follow this Server's
// installation environment. "Local" is a Go process alias, not a saved zone.
func Validate(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if value == "Local" {
		return domain.Invalid("timezone must be an IANA timezone name or empty for the Server system timezone")
	}
	if _, err := time.LoadLocation(value); err != nil {
		return domain.Invalid("timezone must be an IANA timezone name or empty for the Server system timezone")
	}
	return nil
}

func (s *Service) Validate(value string) error { return Validate(value) }

func (s *Service) configured(ctx context.Context) (string, error) {
	var preferences domain.Preferences
	if err := s.store.Get(ctx, "preferences", "owner", &preferences); domain.IsMissing(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	value := strings.TrimSpace(preferences.Timezone)
	return value, Validate(value)
}

func (s *Service) installed() (string, *time.Location, error) {
	// Cache per Server instance, never by mutating time.Local or a process-wide
	// cache. Multiple Servers can have independent saved overrides in tests.
	s.once.Do(func() { s.systemName, s.systemZone, s.systemErr = s.system() })
	return s.systemName, s.systemZone, s.systemErr
}

func (s *Service) View(ctx context.Context) (View, error) {
	value, err := s.configured(ctx)
	if err != nil {
		return View{}, err
	}
	name, system, err := s.installed()
	if err != nil {
		return View{}, err
	}
	if name == "" {
		name = "System"
	}
	view := View{Name: value, System: name, UsingSystem: value == ""}
	if view.UsingSystem {
		view.Name = name
		// A fully fixed rule is provable from its unbounded zone interval,
		// unlike inferring a zone from today's UTC offset. Expose its exact
		// offset in the notation the UI handles without relying on ICU.
		at := time.Now().In(system)
		start, end := at.ZoneBounds()
		_, offset := at.Zone()
		if start.IsZero() && end.IsZero() && offset%60 == 0 {
			view.Name = fixedSystemZoneName(offset)
			view.BrowserCompatible = true
		} else if system.String() == name {
			_, err := time.LoadLocation(name)
			view.BrowserCompatible = err == nil && name != "Local"
		}
	} else {
		view.BrowserCompatible = true // An explicit override passed IANA validation.
	}
	return view, nil
}

func (s *Service) Location(ctx context.Context) (*time.Location, error) {
	value, err := s.configured(ctx)
	if err != nil {
		return nil, err
	}
	return s.Resolve(value)
}

// Resolve also lets callers use an already-read transactional preference,
// keeping schedule claims on the same side of a timezone setting update.
func (s *Service) Resolve(value string) (*time.Location, error) {
	value = strings.TrimSpace(value)
	if err := Validate(value); err != nil {
		return nil, err
	}
	if value != "" {
		return time.LoadLocation(value)
	}
	_, zone, err := s.installed()
	return zone, err
}
