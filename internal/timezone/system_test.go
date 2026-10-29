package timezone

import (
	"testing"
	"time"
)

func TestSystemTimezoneHonorsInstallationEnvironment(t *testing.T) {
	for _, name := range []string{"Asia/Singapore", "America/New_York", ""} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TZ", name)
			actual, location, err := SystemLocation()
			want := name
			if want == "" {
				want = "UTC"
			}
			if err != nil || actual != want || location == nil {
				t.Fatalf("installation TZ=%q resolved to %q %v %v", name, actual, location, err)
			}
		})
	}
	if _, _, err := systemEnvironmentLocation("not/a-real-zone"); err == nil {
		t.Fatal("invalid installation timezone was silently guessed")
	}
}

func TestWindowsIANAZoneMappingsRetainDST(t *testing.T) {
	for key, name := range windowsIANAZones {
		if _, err := time.LoadLocation(name); err != nil {
			t.Errorf("CLDR mapping %q -> %q cannot be loaded: %v", key, name, err)
		}
	}
	for key, expected := range map[string][2]int{
		"Pacific Standard Time":   {-8 * 3600, -7 * 3600},
		"Eastern Standard Time":   {-5 * 3600, -4 * 3600},
		"Singapore Standard Time": {8 * 3600, 8 * 3600},
	} {
		location, err := time.LoadLocation(windowsIANAZones[key])
		if err != nil {
			t.Fatal(err)
		}
		for index, month := range []time.Month{time.January, time.July} {
			_, offset := time.Date(2026, month, 15, 12, 0, 0, 0, time.UTC).In(location).Zone()
			if offset != expected[index] {
				t.Errorf("%q in %s uses offset=%d, want %d", key, month, offset, expected[index])
			}
		}
	}
}

func TestFixedSystemTimezoneNamesUseExactConfiguredOffsets(t *testing.T) {
	for offset, name := range map[int]string{0: "UTC", 19800: "+05:30", -18000: "-05:00", 45900: "+12:45"} {
		if actual := fixedSystemZoneName(offset); actual != name {
			t.Errorf("offset %d became %q, want %q", offset, actual, name)
		}
	}
}
