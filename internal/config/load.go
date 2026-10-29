package config

// Single-file, validated configuration for the copilot MCP server.
//
// One file holds the Copilot BE credentials, the UiPath Orchestrator args and, optionally, the
// Apex orchestrator account: { copilot: {...}, uipath: {...}, apex?: {...} }. The path is
// taken from the COPILOT_MCP_CONFIG env var, else `copilot-mcp.config.json` in the working
// directory, else the older `config.local.json` name for existing setups.
//
// The config is live: Load stats the backing file(s) on every call and re-reads/re-validates
// only when they changed, so editing the file while the server is running takes effect on the
// next call — no restart needed. Subscribe via OnReload to react to that (the server turns it
// into an MCP notification).
//
// Migration fallback: if no single-file config exists, this assembles the same shape from a
// split `order-copy-credentials.json` + `uipath-config.json` found in COPILOT_MCP_LOCAL_DIR,
// so the legacy `.planning/.local` setup keeps working.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	defaultConfigName = "copilot-mcp.config.json"
	// Pre-rename default filename. Still honored (with a warning) so existing deployments
	// relying on the cwd default keep working without a restart.
	oldDefaultConfigName = "config.local.json"
)

// ReloadInfo describes a successful config reload.
type ReloadInfo struct{ Source string }

var (
	mu                       sync.Mutex
	current                  *Config
	currentKey               string
	warnedOldDefaultFilename bool
	listeners                = map[int]func(ReloadInfo){}
	nextListenerID           int
)

// errMissing marks "the file is not there" (as opposed to unreadable/invalid), which is the
// only error that lets the legacy split files be tried.
var errMissing = errors.New("not found")

// configPath is resolved per Load call so the env var is honored even when set late.
func configPath() string {
	if fromEnv := os.Getenv("COPILOT_MCP_CONFIG"); fromEnv != "" {
		return fromEnv
	}
	preferred := filepath.Join(mustCwd(), defaultConfigName)
	if exists(preferred) {
		return preferred
	}
	legacy := filepath.Join(mustCwd(), oldDefaultConfigName)
	if !exists(legacy) {
		return preferred
	}
	if !warnedOldDefaultFilename {
		warnedOldDefaultFilename = true
		slog.Warn("copilot-mcp: using legacy default config filename " + oldDefaultConfigName +
			"; rename it to " + defaultConfigName + " (the new default) when convenient.")
	}
	return legacy
}

func mustCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// legacyFile locates a split legacy file in COPILOT_MCP_LOCAL_DIR (falls back to a `.local`
// dir in the working directory for in-tree/dev use).
func legacyFile(name string) string {
	if dir := os.Getenv("COPILOT_MCP_LOCAL_DIR"); dir != "" {
		return filepath.Join(dir, name)
	}
	return filepath.Join(mustCwd(), ".local", name)
}

func readJSON(path, label string) (any, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s %w at %s", label, errMissing, path)
	}
	if err != nil {
		return nil, fmt.Errorf("Could not read %s at %s: %w", label, path, err) //nolint:staticcheck // user-visible wording kept from the TS server
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON (%s): %w", label, path, err)
	}
	return v, nil
}

// loadLegacy assembles the single-file shape from the split legacy files.
func loadLegacy() (any, error) {
	copilot, err := readJSON(legacyFile("order-copy-credentials.json"), "Copilot credentials")
	if err != nil {
		return nil, err
	}
	uipath, err := readJSON(legacyFile("uipath-config.json"), "UiPath config")
	if err != nil {
		return nil, err
	}
	return map[string]any{"copilot": copilot, "uipath": uipath}, nil
}

func mtimeKey(path string) (string, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	return fmt.Sprint(st.ModTime().UnixNano()), true
}

// statKey is the mtime of whichever file(s) currently back the config — cheap (stat only, no
// read/parse) so Load can compare against it on every call.
func statKey() string {
	path := configPath()
	if m, ok := mtimeKey(path); ok {
		return "single:" + path + ":" + m
	}
	parts := []string{"legacy"}
	for _, p := range []string{legacyFile("order-copy-credentials.json"), legacyFile("uipath-config.json")} {
		m, ok := mtimeKey(p)
		if !ok {
			m = "missing"
		}
		parts = append(parts, p+":"+m)
	}
	return strings.Join(parts, "|")
}

// OnReload subscribes to successful config reloads (fired only on changes after the first
// load, never on initial startup). A panicking listener is recovered so it can never break
// config loading. Returns an unsubscribe function.
func OnReload(fn func(ReloadInfo)) (unsubscribe func()) {
	mu.Lock()
	defer mu.Unlock()
	id := nextListenerID
	nextListenerID++
	listeners[id] = fn
	return func() {
		mu.Lock()
		defer mu.Unlock()
		delete(listeners, id)
	}
}

func notify(fns []func(ReloadInfo), info ReloadInfo) {
	for _, fn := range fns {
		func() {
			defer func() { _ = recover() }()
			fn(info)
		}()
	}
}

// Load reads and validates the config, live (see the file comment). Validation errors name
// the exact field + path so misconfig is obvious. If a previously-valid config is edited into
// something invalid, this fails closed rather than silently keeping the stale value — the
// next call retries the read. The returned *Config is shared: treat it as read-only.
func Load() (*Config, error) {
	cfg, source, isReload, fns, err := refresh()
	if err != nil {
		return nil, err
	}
	if isReload {
		notify(fns, ReloadInfo{Source: source})
	}
	return cfg, nil
}

func refresh() (cfg *Config, source string, isReload bool, fns []func(ReloadInfo), err error) {
	mu.Lock()
	defer mu.Unlock()
	key := statKey()
	if current != nil && key == currentKey {
		return current, "", false, nil, nil
	}
	parsed, source, err := readConfig()
	if err != nil {
		return nil, "", false, nil, err
	}
	cfg, err = decode(parsed, source)
	if err != nil {
		return nil, "", false, nil, err
	}
	isReload = current != nil
	current, currentKey = cfg, key
	return cfg, source, isReload, slices.Collect(maps.Values(listeners)), nil
}

// readConfig reads the single file, falling back to the legacy split pair when it is missing.
func readConfig() (parsed any, source string, err error) {
	path := configPath()
	parsed, err = readJSON(path, "config")
	if err == nil {
		return parsed, path, nil
	}
	if !errors.Is(err, errMissing) {
		return nil, "", err
	}
	parsed, err2 := loadLegacy()
	if errors.Is(err2, errMissing) {
		//nolint:staticcheck // user-visible wording kept from the TS server
		return nil, "", fmt.Errorf("No config found. Set COPILOT_MCP_CONFIG to a JSON config file, or put a %s in the working directory (%s), or set COPILOT_MCP_LOCAL_DIR to a dir with the split legacy files. Read the MCP resource copilot://reference/config-guide for the exact shape, field docs, and setup steps. Config edits are picked up automatically on the next call — no server restart needed. Verify with the doctor tool once written. Missing: %s", defaultConfigName, mustCwd(), err2.Error())
	}
	if err2 != nil {
		return nil, "", err2
	}
	source = fmt.Sprintf("legacy split config (%s + %s)", legacyFile("order-copy-credentials.json"), legacyFile("uipath-config.json"))
	slog.Warn("copilot-mcp: " + path + " not found; using " + source + ". Migrate to a single config — see copilot-mcp.config.example.json.")
	return parsed, source, nil
}

func decode(parsed any, source string) (*Config, error) {
	if issues := validate(parsed); len(issues) > 0 {
		return nil, fmt.Errorf("Config from %s is invalid:\n%s", source, strings.Join(issues, "\n"))
	}
	raw, err := json.Marshal(parsed)
	if err != nil {
		return nil, fmt.Errorf("Config from %s could not be re-encoded: %w", source, err)
	}
	cfg := &Config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("Config from %s is invalid: %w", source, err)
	}
	return cfg, nil
}

// ResetForTest clears the memoized config. Intended for tests that swap COPILOT_MCP_CONFIG.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	current, currentKey = nil, ""
}

// ConfigStatus is the result of Status. A non-empty Error carries the same actionable message
// Load would return.
type ConfigStatus struct {
	OK    bool
	Error string
}

// Status is a non-failing config probe for startup guidance (server instructions / doctor).
func Status() ConfigStatus {
	if _, err := Load(); err != nil {
		return ConfigStatus{Error: err.Error()}
	}
	return ConfigStatus{OK: true}
}
