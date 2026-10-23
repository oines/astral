package computers

import "strings"

// The Server validates the target's path syntax, never the Server OS's syntax.
// Existence, normalization, symlinks and actual access are resolved by the Daemon.
func absoluteTargetPath(system, path string) bool {
	if path == "" || strings.ContainsRune(path, 0) {
		return false
	}
	if system != "windows" {
		return strings.HasPrefix(path, "/")
	}
	separator := func(b byte) bool { return b == '/' || b == '\\' }
	if len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && separator(path[2]) {
		return true
	}
	normalized := strings.ReplaceAll(path, "/", "\\")
	if strings.HasPrefix(normalized, `\\?\UNC\`) {
		normalized = `\\` + normalized[8:]
	}
	if strings.HasPrefix(normalized, `\\?\`) {
		return absoluteTargetPath(system, normalized[4:])
	}
	if !strings.HasPrefix(normalized, `\\`) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(normalized, `\\`), `\`)
	return len(parts) >= 2 && parts[0] != "" && parts[0] != "." && parts[0] != "?" && parts[1] != ""
}
