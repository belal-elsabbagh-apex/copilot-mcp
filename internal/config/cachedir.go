package config

import (
	"os"
	"path/filepath"
)

// CacheDir is the directory every on-disk cache lives under: the directory of COPILOT_MCP_CACHE
// when set (that variable names the session file), else $XDG_CACHE_HOME/copilot-mcp, else
// ~/.cache/copilot-mcp. XDG Base Directory spec: $XDG_CACHE_HOME must be an absolute path; a
// relative or empty value is invalid and must be treated as unset.
func CacheDir() string {
	if p := os.Getenv("COPILOT_MCP_CACHE"); p != "" {
		return filepath.Dir(p)
	}
	base := os.Getenv("XDG_CACHE_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "copilot-mcp")
}

// DiskCacheEnabled is the single "anything on disk" switch: copilot.sessionCache (default true).
// A config that cannot be loaded counts as off — fail closed.
func DiskCacheEnabled() bool {
	on, err := SessionCacheEnabled()
	return err == nil && on
}
