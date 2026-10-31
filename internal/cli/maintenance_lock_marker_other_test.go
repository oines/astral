//go:build !windows

package cli

func maintenanceMarkerReadRetry(error) bool { return false }
