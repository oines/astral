// Package shell supplies equivalent host-native commands for platform tests.
// It has no product dependencies, so internal package tests can use it safely.
package shell

import (
	"fmt"
	"runtime"
	"strings"
	"time"
)

func quote(value string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func Print(text string) string {
	if runtime.GOOS == "windows" {
		return "[Console]::Write(" + quote(text) + ")"
	}
	return "printf '%s' " + quote(text)
}

func WriteFile(path, text string) string {
	if runtime.GOOS == "windows" {
		return "[IO.File]::WriteAllText(" + quote(path) + ", " + quote(text) + ", [Text.UTF8Encoding]::new($false))"
	}
	return Print(text) + " > " + quote(path)
}

func ReadLine(prefix string) string {
	if runtime.GOOS == "windows" {
		return "$value = [Console]::ReadLine(); [Console]::Write(" + quote(prefix) + " + $value)"
	}
	return "IFS= read -r value; printf '%s%s' " + quote(prefix) + ` "$value"`
}

func Sleep(duration time.Duration) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("Start-Sleep -Milliseconds %d", duration.Milliseconds())
	}
	return fmt.Sprintf("sleep %.3f", duration.Seconds())
}

func CWD() string {
	if runtime.GOOS == "windows" {
		return "[Console]::Write((Get-Location).Path)"
	}
	return "pwd"
}

// HTTP uses PowerShell's built-in client on Windows; the curl alias has
// incompatible flags and PS 5.1 native argument parsing can lose JSON quotes.
func HTTP(method, url, body string) string {
	if runtime.GOOS == "windows" {
		command := "$ProgressPreference = 'SilentlyContinue'; [Console]::Write((Invoke-WebRequest -UseBasicParsing -Headers @{Authorization = 'Bearer ' + $env:ASTRAL_API_TOKEN} -Method " + quote(method) + " -Uri " + quote(url)
		if body != "" {
			command += " -ContentType 'application/json' -Body " + quote(body)
		}
		return command + ").Content)"
	}
	command := `printf 'Authorization: Bearer %s\n' "$ASTRAL_API_TOKEN" | curl --fail --silent --header @- --request ` + quote(method)
	if body != "" {
		command += " --header 'Content-Type: application/json' --data " + quote(body)
	}
	return command + " " + quote(url)
}
