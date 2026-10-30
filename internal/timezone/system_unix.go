//go:build !windows

package timezone

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func systemLocation() (string, *time.Location, error) {
	if value, exists := os.LookupEnv("TZ"); exists {
		return systemEnvironmentLocation(value)
	}
	return unixSystemLocation("/etc/localtime", []string{"/etc/timezone", "/etc/sysconfig/clock", "/etc/conf.d/clock"}, []string{
		"/usr/share/zoneinfo", "/usr/share/lib/zoneinfo", "/usr/lib/zoneinfo", "/var/db/timezone/zoneinfo", "/etc/zoneinfo",
	})
}

func unixSystemLocation(localtime string, configurations, roots []string) (string, *time.Location, error) {
	if resolved, err := filepath.EvalSymlinks(localtime); err == nil {
		if name := zoneFileName(resolved); name != "" {
			if location, err := time.LoadLocation(name); err == nil {
				return name, location, nil
			}
		}
	}
	for _, path := range configurations {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
			if _, value, found := strings.Cut(line, "="); found {
				key, _, _ := strings.Cut(line, "=")
				if strings.TrimSpace(key) != "ZONE" && strings.TrimSpace(key) != "TIMEZONE" {
					continue
				}
				line = strings.Trim(strings.TrimSpace(value), "\"'")
			}
			if line != "" && line != "Local" {
				if location, err := time.LoadLocation(line); err == nil {
					return line, location, nil
				}
			}
		}
	}
	if data, err := os.ReadFile(localtime); err == nil {
		// Some Linux installations copy /etc/localtime rather than linking it.
		// Exact TZif equality identifies complete rules; an offset/DST sample
		// cannot safely identify a zone and is deliberately not used.
		for _, root := range roots {
			name := ""
			_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil || entry.IsDir() {
					return nil
				}
				relative, err := filepath.Rel(root, path)
				if err != nil || strings.HasPrefix(relative, "posix/") || strings.HasPrefix(relative, "right/") || strings.HasPrefix(relative, ".") {
					return nil
				}
				info, err := entry.Info()
				if err != nil || info.Size() != int64(len(data)) {
					return nil
				}
				candidate, err := os.ReadFile(path)
				if err == nil && bytes.Equal(candidate, data) {
					if _, err := time.LoadLocation(filepath.ToSlash(relative)); err == nil {
						name = filepath.ToSlash(relative)
						return fs.SkipAll
					}
				}
				return nil
			})
			if name != "" {
				location, err := time.LoadLocation(name)
				return name, location, err
			}
		}
		location, err := time.LoadLocationFromTZData("System", data)
		if err == nil {
			return "System", location, nil
		}
	}
	// Go's native Local retains the OS rules when the installation has no
	// inspectable named zone. No offset-to-zone inference is performed.
	name := time.Local.String()
	if name == "Local" {
		name = "System"
	}
	return name, time.Local, nil
}
