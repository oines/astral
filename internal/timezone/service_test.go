package timezone

import (
	"context"
	"testing"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

func preferences(t *testing.T, store *persistence.Store, zone string) {
	t.Helper()
	var record struct {
		domain.Meta
		domain.Preferences
	}
	if err := store.Get(context.Background(), "preferences", "owner", &record); err != nil && !domain.IsMissing(err) {
		t.Fatal(err)
	}
	record.ID, record.Timezone = "owner", zone
	if err := store.Save(context.Background(), "preferences", &record.Meta, &record); err != nil {
		t.Fatal(err)
	}
}

func timezoneFixture(t *testing.T) (*Service, *persistence.Store) {
	t.Helper()
	store, err := persistence.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return New(store), store
}

func TestServiceDefaultsToServerSystemAndOverrideUpdatesImmediately(t *testing.T) {
	s, store := timezoneFixture(t)
	calls := 0
	s.system = func() (string, *time.Location, error) {
		calls++
		zone, err := time.LoadLocation("Asia/Singapore")
		return "Asia/Singapore", zone, err
	}
	view, err := s.View(context.Background())
	if err != nil || view.Name != "Asia/Singapore" || view.System != "Asia/Singapore" || !view.UsingSystem {
		t.Fatalf("missing preferences did not use this Server's system: %+v, %v", view, err)
	}
	preferences(t, store, "America/New_York")
	view, err = s.View(context.Background())
	if err != nil || view.Name != "America/New_York" || view.System != "Asia/Singapore" || view.UsingSystem {
		t.Fatalf("saved override did not replace the effective zone: %+v, %v", view, err)
	}
	zone, err := s.Location(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, winter := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).In(zone).Zone()
	_, summer := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).In(zone).Zone()
	if winter != -5*3600 || summer != -4*3600 {
		t.Fatal("override discarded daylight-saving rules")
	}
	preferences(t, store, "")
	zone, err = s.Location(context.Background())
	if err != nil || zone.String() != "Asia/Singapore" || calls != 1 {
		t.Fatalf("reset did not restore this Service's cached installation environment: %v, %v, calls=%d", zone, err, calls)
	}
}

func TestServicePreservesUnnamedSystemRulesAndDoesNotMutateProcessLocal(t *testing.T) {
	s, store := timezoneFixture(t)
	original := time.Local
	system := time.FixedZone("System", 8*3600)
	s.system = func() (string, *time.Location, error) { return "System", system, nil }
	zone, err := s.Location(context.Background())
	if err != nil || zone != system {
		t.Fatalf("unnamed system zone was reloaded as an IANA name: %v, %v", zone, err)
	}
	other, otherStore := timezoneFixture(t)
	preferences(t, store, "Asia/Tokyo")
	preferences(t, otherStore, "Europe/London")
	first, _ := s.Location(context.Background())
	second, _ := other.Location(context.Background())
	if first.String() != "Asia/Tokyo" || second.String() != "Europe/London" || time.Local != original {
		t.Fatal("two Server instances shared their timezone override")
	}
}

func TestValidateTimezone(t *testing.T) {
	for _, value := range []string{"", "UTC", "Asia/Shanghai", "America/New_York", "Etc/GMT+8"} {
		if err := Validate(value); err != nil {
			t.Errorf("valid timezone %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"Local", "Not/AZone", "../zone", "UTC+08:00"} {
		if err := Validate(value); err == nil {
			t.Errorf("invalid timezone %q accepted", value)
		}
	}
}

func TestViewDistinguishesFixedSystemZonesFromUnknownDSTRules(t *testing.T) {
	for _, offset := range []int{0, 8 * 3600, -5*3600 - 30*60, 5*3600 + 45*60} {
		s, _ := timezoneFixture(t)
		s.system = func() (string, *time.Location, error) {
			return "Custom Windows zone", time.FixedZone("Custom Windows zone", offset), nil
		}
		view, err := s.View(context.Background())
		if err != nil || !view.BrowserCompatible || view.Name != fixedSystemZoneName(offset) || view.System != "Custom Windows zone" {
			t.Fatalf("known fixed rules cannot be displayed safely: %+v, %v", view, err)
		}
	}

	s, store := timezoneFixture(t)
	s.system = func() (string, *time.Location, error) {
		zone, err := time.LoadLocation("America/New_York")
		// This is an opaque native system rule set, not a claimed IANA mapping.
		return "Custom Windows DST zone", zone, err
	}
	view, err := s.View(context.Background())
	if err != nil || view.BrowserCompatible || view.Name != "Custom Windows DST zone" {
		t.Fatalf("unknown DST rules were mislabeled as a fixed zone: %+v, %v", view, err)
	}
	preferences(t, store, "America/New_York")
	view, err = s.View(context.Background())
	if err != nil || !view.BrowserCompatible || view.UsingSystem {
		t.Fatalf("explicit IANA override did not restore browser conversion: %+v, %v", view, err)
	}
}
