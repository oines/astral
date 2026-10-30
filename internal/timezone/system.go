package timezone

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata"
)

// SystemLocation describes the Server installation's timezone, including an
// explicit TZ environment setting. It never uses a browser or a Computer's zone.
func SystemLocation() (string, *time.Location, error) { return systemLocation() }

func fixedSystemZoneName(offset int) string {
	if offset == 0 {
		return "UTC"
	}
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	return fmt.Sprintf("%s%02d:%02d", sign, offset/3600, offset%3600/60)
}

func systemEnvironmentLocation(value string) (string, *time.Location, error) {
	value = strings.TrimPrefix(value, ":")
	if value == "" {
		return "UTC", time.UTC, nil
	}
	if filepath.IsAbs(value) {
		return systemZoneFile(value)
	}
	location, err := time.LoadLocation(value)
	if err != nil || value == "Local" {
		return "", nil, fmt.Errorf("server TZ must name an IANA timezone or timezone file")
	}
	return value, location, nil
}

func zoneFileName(path string) string {
	path = filepath.ToSlash(path)
	_, name, ok := strings.Cut(path, "/zoneinfo/")
	if !ok {
		return ""
	}
	name = strings.TrimPrefix(strings.TrimPrefix(name, "posix/"), "right/")
	if name == "" || strings.HasPrefix(name, ".") || strings.Contains(name, "..") {
		return ""
	}
	return name
}

func systemZoneFile(path string) (string, *time.Location, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, err
	}
	if name := zoneFileName(resolved); name != "" {
		if location, err := time.LoadLocation(name); err == nil {
			return name, location, nil
		}
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", nil, err
	}
	// A standalone TZif is authoritative even without its original IANA name.
	// Read its complete transition rules instead of guessing a fixed offset.
	location, err := time.LoadLocationFromTZData("System", data)
	return "System", location, err
}
