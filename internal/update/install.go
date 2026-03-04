package update

import (
	"path/filepath"
	"strings"
)

// IsHomebrewPath returns true if the binary path indicates a Homebrew installation.
// Fully resolves symlink chains (e.g. /usr/local/bin/shan → /usr/local/Cellar/shan/…).
func IsHomebrewPath(path string) bool {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return strings.Contains(path, "/Cellar/") ||
		strings.HasPrefix(path, "/opt/homebrew/")
}
