package config

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

// DefaultApexFolders are the Apex folder names used when the file names none.
var DefaultApexFolders = map[Env]string{
	Prod:    "Authorization",
	PreProd: "Authorization Test",
}

type apexFieldSpec struct{ field, envVar string }

var apexFields = []apexFieldSpec{
	{"apiUrl", "APEX_API_URL"},
	{"username", "APEX_USER_NAME"},
	{"password", "APEX_PASSWORD"},
}

// apexField: file value first, then the env var. Read per call (never cached) so a changed
// environment — or a test — takes effect immediately. An empty env var counts as unset.
func apexField(envVar, fromFile string) (string, ApexSource) {
	if fromFile != "" {
		return fromFile, SourceConfig
	}
	if v := os.Getenv(envVar); v != "" {
		return v, SourceEnv
	}
	return "", ""
}

// NewApexNotConfiguredError names every missing field; configured is how many were supplied.
func NewApexNotConfiguredError(missing []ApexMissing, configured int) *ApexNotConfiguredError {
	fields := make([]string, len(missing))
	envVars := make([]string, len(missing))
	keys := make([]string, len(missing))
	for i, m := range missing {
		fields[i], envVars[i], keys[i] = m.Field, m.EnvVar, "apex."+m.Field
	}
	plural := "s"
	if len(missing) == 1 {
		plural = ""
	}
	e := &ApexNotConfiguredError{Missing: fields, Partial: configured > 0}
	e.Msg = fmt.Sprintf("Apex is not configured — missing %s. Set %s in the config file, or the environment variable%s %s (the file wins per field; env vars only fill what the file leaves out).",
		strings.Join(fields, ", "), strings.Join(keys, ", "), plural, strings.Join(envVars, ", "))
	return e
}

// ResolveApex returns the resolved Apex settings, or *ApexNotConfiguredError naming every
// missing field. Precedence is per field: the file wins; the environment only fills what it
// leaves out.
func ResolveApex() (ApexConfig, error) {
	cfg, err := Load()
	if err != nil {
		return ApexConfig{}, err
	}
	apex := cfg.Apex
	if apex == nil {
		apex = &ApexFile{}
	}
	fromFile := []string{apex.APIURL, apex.Username, apex.Password}
	vals := make([]string, len(apexFields))
	srcs := make([]ApexSource, len(apexFields))
	var missing []ApexMissing
	for i, f := range apexFields {
		vals[i], srcs[i] = apexField(f.envVar, fromFile[i])
		if vals[i] == "" {
			missing = append(missing, ApexMissing{f.field, f.envVar})
		}
	}
	if len(missing) > 0 {
		return ApexConfig{}, NewApexNotConfiguredError(missing, len(apexFields)-len(missing))
	}
	folders := maps.Clone(DefaultApexFolders)
	for env, name := range apex.FolderByEnv {
		if name != "" {
			folders[env] = name
		}
	}
	serverURLs := map[Env]string{}
	for _, env := range Envs {
		if u := apex.ServerURLByEnv[env]; u != "" {
			serverURLs[env] = u
		}
	}
	return ApexConfig{
		APIURL:         strings.TrimRight(vals[0], "/"),
		Username:       vals[1],
		Password:       vals[2],
		FolderByEnv:    folders,
		ServerURLByEnv: serverURLs,
		QueueURL:       apex.QueueURL,
		Sources:        ApexSources{APIURL: srcs[0], Username: srcs[1], Password: srcs[2]},
	}, nil
}

// Uipath returns the validated uipath block.
func Uipath() (UipathConfig, error) {
	cfg, err := Load()
	if err != nil {
		return UipathConfig{}, err
	}
	return cfg.Uipath, nil
}

// ListProfiles names the credential profiles defined in the config (copilot.profiles), sorted
// (TS returned file order; Go maps keep none). Never fails: returns nil when the config is
// absent or has no profiles, so it is safe from tool/prompt wiring and completions.
func ListProfiles() []string {
	cfg, err := Load()
	if err != nil {
		return nil
	}
	return slices.Sorted(maps.Keys(cfg.Copilot.Profiles))
}

// Feedback returns the on-failure feedback settings: enabled by default. It fails if the
// config is absent — callers in the error path guard against that.
func Feedback() (FeedbackConfig, error) {
	cfg, err := Load()
	if err != nil {
		return FeedbackConfig{}, err
	}
	out := FeedbackConfig{Enabled: true}
	if fb := cfg.Feedback; fb != nil {
		if fb.Enabled != nil {
			out.Enabled = *fb.Enabled
		}
		out.RepositoryURL = fb.RepositoryURL
	}
	return out, nil
}

// ResolveSupport returns the support account for one env. Absent = this env has not adopted
// support accounts.
func ResolveSupport(env Env) (EnvCreds, error) {
	cfg, err := Load()
	if err != nil {
		return EnvCreds{}, err
	}
	support, ok := cfg.Copilot.Support[env]
	if !ok {
		return EnvCreds{}, fmt.Errorf("copilot.support.%s is required in the config for support-account (clinicUid) auth — add {be, email, password}", env)
	}
	return support, nil
}

// ResolveAuth says how to authenticate one profile in one env ("" = no profile). A clinicUid
// on the env entry selects support-account auth (credentials from copilot.support[env]);
// otherwise the entry's own be/email/password are used, exactly as before support accounts
// existed. A profile carrying both wins with clinicUid — that is how an account is migrated
// env by env without deleting its old credentials.
func ResolveAuth(profile string, env Env) (EnvAuth, error) {
	cfg, err := Load()
	if err != nil {
		return EnvAuth{}, err
	}
	cp := cfg.Copilot
	if profile == "" {
		if cp.Prod == nil || cp.PreProd == nil {
			return EnvAuth{}, fmt.Errorf("copilot.prod and copilot.pre_prod are required when no profile is given")
		}
		t := cp.Prod
		if env == PreProd {
			t = cp.PreProd
		}
		return EnvAuth{Mode: ModeDirect, BE: t.BE, Email: t.Email, Password: t.Password}, nil
	}
	entry, ok := cp.Profiles[profile]
	if !ok {
		available := strings.Join(slices.Sorted(maps.Keys(cp.Profiles)), ", ")
		if available == "" {
			available = "none"
		}
		return EnvAuth{}, fmt.Errorf("unknown profile '%s' (available: %s)", profile, available)
	}
	e, ok := entry[env]
	if !ok {
		return EnvAuth{}, fmt.Errorf("profile '%s' has no %s auth — add clinicUid (support account) or be/email/password (direct login)", profile, env)
	}
	if e.ClinicUID == "" {
		return EnvAuth{Mode: ModeDirect, BE: e.BE, Email: e.Email, Password: e.Password}, nil
	}
	support, err := ResolveSupport(env)
	if err != nil {
		return EnvAuth{}, err
	}
	be := e.BE
	if be == "" {
		be = support.BE
	}
	return EnvAuth{Mode: ModeSupport, BE: be, Email: support.Email, Password: support.Password, ClinicUID: e.ClinicUID}, nil
}

// SessionCacheEnabled reports whether sessions persist to disk (default true).
func SessionCacheEnabled() (bool, error) {
	cfg, err := Load()
	if err != nil {
		return false, err
	}
	return cfg.Copilot.SessionCache == nil || *cfg.Copilot.SessionCache, nil
}
