package timezone

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This is the Win32 DYNAMIC_TIME_ZONE_INFORMATION layout, including the stable
// registry key (the StandardName and DaylightName fields are localized).
type dynamicTimezoneInformation struct {
	Bias                        int32
	StandardName                [32]uint16
	StandardDate                windows.Systemtime
	StandardBias                int32
	DaylightName                [32]uint16
	DaylightDate                windows.Systemtime
	DaylightBias                int32
	TimeZoneKeyName             [128]uint16
	DynamicDaylightTimeDisabled uint8
}

func systemLocation() (string, *time.Location, error) {
	if value, exists := os.LookupEnv("TZ"); exists {
		return systemEnvironmentLocation(value)
	}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDynamicTimeZoneInformation")
	var info dynamicTimezoneInformation
	result, _, callErr := proc.Call(uintptr(unsafe.Pointer(&info)))
	if uint32(result) == 0xffffffff {
		if callErr == syscall.Errno(0) {
			callErr = syscall.EINVAL
		}
		return "", nil, fmt.Errorf("read Windows system timezone: %w", callErr)
	}
	key := windows.UTF16ToString(info.TimeZoneKeyName[:])
	if info.DynamicDaylightTimeDisabled != 0 {
		if info.StandardDate.Month == 0 && info.DaylightDate.Month == 0 {
			// With both transition dates cleared, Windows explicitly disables
			// DST (StandardBias is ignored). Name the exact configured fixed
			// zone; do not infer a geographical zone from today's offset.
			offset := -int(info.Bias) * 60
			name := fixedSystemZoneName(offset)
			return name, time.FixedZone(name, offset), nil
		}
		// The flag alone only disables *dynamic* year-specific transition
		// updates; supplied fixed transition dates may still enable DST.
		// Native Local preserves those custom rules instead of guessing.
		return key, time.Local, nil
	}
	if name, ok := windowsIANAZones[key]; ok {
		location, err := time.LoadLocation(name)
		return name, location, err
	}
	// Custom Windows zones have no authoritative CLDR mapping. Native Local
	// still preserves the system's configured transition rules.
	if key == "" {
		key = "System"
	}
	return key, time.Local, nil
}
