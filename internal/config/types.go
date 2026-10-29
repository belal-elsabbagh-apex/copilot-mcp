package config

import "github.com/belal-elsabbagh-apex/copilot-mcp/internal/shared"

// Config is the validated single-file shape: { copilot, uipath, apex?, feedback? }. Unknown
// keys (which the TS schemas passed through) are ignored. A *Config returned by Load is shared
// and immutable by convention: never mutate it.
type Config struct {
	Copilot  CopilotConfig `json:"copilot"`
	Uipath   UipathConfig  `json:"uipath"`
	Apex     *ApexFile     `json:"apex,omitempty"`
	Feedback *FeedbackFile `json:"feedback,omitempty"`
}

// EnvCreds is one direct login: batch BE base URL, email, password (+ optional FE URL).
type EnvCreds struct {
	BE       string `json:"be"`
	Email    string `json:"email"`
	Password string `json:"password"`
	FE       string `json:"fe,omitempty"`
}

// ProfileEnv is one env of one profile: EITHER a clinic pointer (support-account auth) OR a
// full per-account login. BE/FE may sit alongside ClinicUID to override the support
// account's base URL. Empty string = unset.
type ProfileEnv struct {
	ClinicUID string `json:"clinicUid,omitempty"`
	BE        string `json:"be,omitempty"`
	Email     string `json:"email,omitempty"`
	Password  string `json:"password,omitempty"`
	FE        string `json:"fe,omitempty"`
}

// CopilotConfig holds the Copilot BE credentials.
type CopilotConfig struct {
	// Support is the support account per env; only required for envs whose profiles use clinicUid.
	Support  map[Env]EnvCreds              `json:"support,omitempty"`
	Prod     *EnvCreds                     `json:"prod,omitempty"`
	PreProd  *EnvCreds                     `json:"pre_prod,omitempty"`
	Profiles map[string]map[Env]ProfileEnv `json:"profiles,omitempty"`
	// SessionCache persists sessions to disk (default true); false = in-memory only.
	SessionCache *bool `json:"sessionCache,omitempty"`
}

// UipathOAuth is the client-credentials block. TokenURL falls back to a guess derived from
// OrchestratorURL (…/identity_/connect/token) when empty.
type UipathOAuth struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// UipathConfig is the UiPath Orchestrator block. At least one of Bearer / OAuth is present
// (validated). OAuth is tried first when present; Bearer is also the runtime fallback if the
// OAuth token request fails. Empty string / nil map entry = unset.
type UipathConfig struct {
	OrchestratorURL string         `json:"orchestratorUrl"`
	Bearer          string         `json:"bearer,omitempty"`
	OAuth           *UipathOAuth   `json:"oauth,omitempty"`
	FolderPath      string         `json:"folderPath,omitempty"`
	FolderPathByEnv map[Env]string `json:"folderPathByEnv,omitempty"`
	// FolderIDByEnv: numeric UiPath folder ids (OrganizationUnitId) per env, used by the queue
	// endpoints, which the tenant addresses by org-unit id rather than folder path. Optional —
	// defaults come from reference (prod 231517 / pre_prod 434039).
	FolderIDByEnv map[Env]string `json:"folderIdByEnv,omitempty"`
	// Extra fields used ONLY by build_queue_item (optional for analyze-only setups).
	QueueURL         string         `json:"queueUrl,omitempty"`
	AddQueueItemPath string         `json:"addQueueItemPath,omitempty"`
	ServerURLByEnv   map[Env]string `json:"serverUrlByEnv,omitempty"`
}

// ApexFile is the raw apex block. Every field is optional in the file: ResolveApex fills
// apiUrl/username/password from APEX_API_URL / APEX_USER_NAME / APEX_PASSWORD.
type ApexFile struct {
	APIURL   string `json:"apiUrl,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// FolderByEnv is the folder NAME per env; resolved to the X-Folder-Id uuid at runtime.
	FolderByEnv map[Env]string `json:"folderByEnv,omitempty"`
	// ServerURLByEnv / QueueURL are the write guard: specific_content.serverURL / queueUrl must
	// equal these (fails closed when unset).
	ServerURLByEnv map[Env]string `json:"serverUrlByEnv,omitempty"`
	QueueURL       string         `json:"queueUrl,omitempty"`
}

// FeedbackFile is the raw feedback block.
type FeedbackFile struct {
	Enabled       *bool  `json:"enabled,omitempty"`
	RepositoryURL string `json:"repositoryUrl,omitempty"`
}

// ApexSource says where a resolved Apex field came from.
type ApexSource string

const (
	SourceConfig ApexSource = "config"
	SourceEnv    ApexSource = "env"
)

// ApexSources records the origin of each credential field.
type ApexSources struct {
	APIURL   ApexSource
	Username ApexSource
	Password ApexSource
}

// ApexConfig is the resolved Apex settings.
type ApexConfig struct {
	APIURL         string // no trailing slash
	Username       string
	Password       string
	FolderByEnv    map[Env]string // always has both envs
	ServerURLByEnv map[Env]string // only envs that are set
	QueueURL       string         // "" when unset
	Sources        ApexSources
}

// AuthMode tags how a profile authenticates in one env.
type AuthMode string

const (
	ModeSupport AuthMode = "support"
	ModeDirect  AuthMode = "direct"
)

// EnvAuth is how to authenticate one profile in one env. ClinicUID is set only for ModeSupport.
type EnvAuth struct {
	Mode      AuthMode
	BE        string
	Email     string
	Password  string
	ClinicUID string
}

// FeedbackConfig is the on-failure feedback setting. RepositoryURL is "" unless configured
// (the feedback layer supplies the default repo).
type FeedbackConfig struct {
	Enabled       bool
	RepositoryURL string
}

// ApexMissing names one Apex field neither the file nor the environment supplied.
type ApexMissing struct{ Field, EnvVar string }

// ApexNotConfiguredError is Expected: an unconfigured server is user state, not a bug here.
type ApexNotConfiguredError struct {
	shared.ExpectedError
	// Missing names the fields neither the file nor the environment supplied.
	Missing []string
	// Partial: some, but not all, of the fields are set (a half-finished setup, as opposed to
	// Apex simply not being used) — `doctor` flags only that case.
	Partial bool
}
