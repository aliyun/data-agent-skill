// Package config loads the MCP server configuration from a YAML file
// combined with a .env file and process environment variables.
//
// Priority (highest wins): env vars > .env file > YAML config > defaults.
// The .env file is loaded first into the process environment (without
// overriding variables that are already set), so a single lookup through
// os.Getenv covers both sources.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// IdentityGroup is a set of users sharing one RAM role and session defaults.
// The same shape is used for the global default (identity.default) and for
// named groups (identity.groups.<name>).
type IdentityGroup struct {
	TenantID string `yaml:"tenant_id"`
	AgentID  string `yaml:"agent_id"`
	// RoleArn is the RAM role assumed for every user of this group (required).
	RoleArn string `yaml:"role_arn"`
	// WorkspaceID pins sessions of this group to a workspace (optional).
	WorkspaceID string `yaml:"workspace_id"`
	// CustomAgentID is the default custom agent applied when
	// data_agent_create_session is called without one (optional).
	CustomAgentID string `yaml:"custom_agent_id"`
	// Mode is the default session mode tier (auto / lite / pro / ultra)
	// applied when data_agent_create_session is called without one (optional).
	// Legacy values ASK_DATA/ANALYSIS/INSIGHT are auto-mapped.
	Mode string `yaml:"mode"`
	// Users lists group members by upstream user id or email. Required for
	// named groups; ignored on the default group.
	Users []string `yaml:"users"`
}

// IdentityHeaders names the HTTP headers carrying the end-user identity.
// Defaults are Feishu Aily compatible; any upstream that forwards per-user
// identity headers (gateways, bots, portals) can be plugged in by renaming.
type IdentityHeaders struct {
	User  string `yaml:"user"`  // default: x-aily-user
	Email string `yaml:"email"` // default: x-aily-email
	Token string `yaml:"token"` // default: x-aily-token
}

// JWT configures verification of the upstream-signed identity token.
//
// Every key has equivalent signing authority within identity.namespace.
// Multiple keys support rotation or mutually trusted applications, never
// independent untrusted issuers. JWT mode requires JWT on every remote request
// and never downgrades to unsigned headers.
type JWT struct {
	Enabled bool `yaml:"enabled"`
	// Secret is a single HMAC key, shown in the platform's MCP editor and used
	// as-is (no base64 decoding). Kept for the common single-agent case and as
	// the target of the IDENTITY_JWT_SECRET env override. Merged with Secrets.
	Secret string `yaml:"secret"`
	// Secrets lists additional HMAC keys for multiple upstream agents that each
	// sign with their own key. A token is accepted if any of Secret plus these
	// verifies it. Env override: IDENTITY_JWT_SECRETS (comma-separated).
	Secrets []string `yaml:"secrets"`
	// Header names the request header carrying the token.
	// Default: x-aily-jwt.
	Header string `yaml:"header"`
}

// AllSecrets returns every configured verification key, de-duplicated, with
// the single Secret first. Verification tries each until one succeeds.
func (j JWT) AllSecrets() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(j.Secret)
	for _, s := range j.Secrets {
		add(s)
	}
	return out
}

// ptr returns a pointer to v, for the optional string fields whose zero value
// is meaningful.
func ptr(v string) *string { return &v }

// Prefix returns the configured RoleSessionName prefix. An empty string means
// no prefix, so the RoleSessionName is the session-name value alone. Callers
// must not treat empty as "use the default" — ApplyDefaults has already
// resolved an absent setting to "aily".
func (i Identity) Prefix() string {
	if i.SessionNamePrefix == nil {
		return "aily" // ApplyDefaults was skipped (hand-built Config in tests)
	}
	return *i.SessionNamePrefix
}

// Identity configures multi-tenant identity resolution. The upstream caller
// (e.g. Feishu Aily) forwards the end-user identity on every MCP HTTP
// request — either as a signed JWT (identity.jwt) or through the configured
// headers — and the server maps that identity to a RAM role via STS AssumeRole
// and executes all calls under it.
//
// Two ways to map users to RAM roles:
//  1. Default: identity.default carries one role plus session defaults for
//     unmatched users (shared, or restricted by explicit tenant/agent scope).
//  2. Groups: each identity.groups.<name> lists its users and carries its
//     own role and defaults. Group membership wins over the default.
type Identity struct {
	Enabled    bool   `yaml:"enabled"`
	Namespace  string `yaml:"namespace"`
	MaxTenants int    `yaml:"max_tenants"`
	// JWT enables verification of the upstream-signed identity token.
	JWT JWT `yaml:"jwt"`
	// SessionNameClaim selects which identity field becomes the STS
	// RoleSessionName segment: user_id (default), email, enterprise_email,
	// employee_no, tenant_id or agent_id. The name shows up in ActionTrail,
	// so pick the field your audit process recognises. Group membership is
	// always matched on user_id and email, independent of this setting.
	SessionNameClaim string `yaml:"session_name_claim"`
	// RequireIdentity must be true for all remote transports. Missing identity
	// never falls back to the process-wide SDK credentials.
	RequireIdentity bool `yaml:"require_identity"`
	// AuthToken, when non-empty, must match the token header on every
	// request — it authenticates that the request really comes from the
	// trusted upstream platform (identity headers alone are spoofable).
	// Configure the same value as a custom header in the upstream MCP
	// registration. Legacy yaml name "shared_secret" is still accepted.
	AuthToken string `yaml:"auth_token"`
	// LegacySharedSecret is the deprecated alias for AuthToken.
	LegacySharedSecret string `yaml:"shared_secret"`
	// SessionNamePrefix is the leading segment of the STS RoleSessionName:
	// "<prefix>-<user_id>". A pointer so an explicitly empty value is
	// distinguishable from an absent one:
	//   absent                     -> "aily", the historical default
	//   session_name_prefix: ""    -> no prefix, RoleSessionName is the bare value
	//   session_name_prefix: prod  -> "prod-<user_id>"
	// Env override: IDENTITY_SESSION_NAME_PREFIX (set it empty to drop the prefix).
	SessionNamePrefix *string `yaml:"session_name_prefix"`
	// Headers overrides the identity header names (defaults: x-aily-*).
	Headers IdentityHeaders `yaml:"headers"`
	// Default applies to unmatched verified identities; explicit scopes restrict it.
	// Nil means reject unmatched users.
	Default *IdentityGroup `yaml:"default"`
	// Groups maps group name -> group config. A user may belong to exactly
	// one group (validated at startup).
	Groups map[string]IdentityGroup `yaml:"groups"`
}

// STS configures the AssumeRole call used for per-user credentials.
type STS struct {
	Endpoint          string `yaml:"endpoint"`           // default: sts.{region}.aliyuncs.com
	SessionExpiration int    `yaml:"session_expiration"` // seconds, default 3600
}

// DefaultLogMaxBytes is the default server log rotation size.
const DefaultLogMaxBytes = 64 << 20

// Log configures server logging to stderr or a rotating file.
type Log struct {
	Enabled  bool   `yaml:"enabled"`
	File     string `yaml:"file"`      // default ~/.data-agent/server.log
	MaxBytes int64  `yaml:"max_bytes"` // positive bytes, then rotate to <file>.1
	// Requests selects how much of every tool call is logged:
	//   basic — tool name, caller, outcome, duration (default remotely)
	//   full  — adds arguments with sensitive values redacted;
	//           arguments carry user questions, so prefer it for
	//           troubleshooting rather than steady-state operation
	//   off   — no per-call logging (default under stdio)
	Requests string `yaml:"requests"`
	// Perf periodically samples host/process/runtime/server metrics into a
	// dedicated JSON-lines file (not stderr) for post-incident analysis.
	Perf Perf `yaml:"perf"`
}

// Perf configures the periodic performance sampler.
type Perf struct {
	Enabled         bool   `yaml:"enabled"`
	File            string `yaml:"file"`             // default ~/.data-agent/perf.log
	IntervalSeconds int    `yaml:"interval_seconds"` // default 30, minimum 1
	MaxBytes        int64  `yaml:"max_bytes"`        // default 64 MiB, then rotate to <file>.1
}

// AgentGateway configures the fixed Agentic OpenAPI metadata tools.
type AgentGateway struct {
	Enabled               bool   `yaml:"enabled"`
	DataSourcesEnabled    bool   `yaml:"data_sources_enabled"`
	Endpoint              string `yaml:"endpoint"`
	RequestTimeoutSeconds int    `yaml:"request_timeout_seconds"`
	MaxResponseBytes      int64  `yaml:"max_response_bytes"`
}

// Config holds all server configuration.
type Config struct {
	Region      string `yaml:"region"`
	DMSUnit     string `yaml:"dms_unit"`
	WorkspaceID string `yaml:"workspace_id"`
	// CustomAgentID is the default custom agent for sessions that do not name
	// one, mirroring workspace_id. An identity group's custom_agent_id wins
	// over this, and an explicit tool argument wins over both.
	CustomAgentID        string       `yaml:"custom_agent_id"`
	SessionsDir          string       `yaml:"sessions_dir"`
	APIKey               string       `yaml:"api_key"`
	APIKeyEndpoint       string       `yaml:"api_key_endpoint"`
	APIKeyStreamEndpoint string       `yaml:"api_key_stream_endpoint"`
	AgentGateway         AgentGateway `yaml:"agent_gateway"`
	HTTP                 HTTP         `yaml:"http"`
	// DMSEnterpriseEndpoint overrides the dms-enterprise host used by the
	// 2018-11-01 metadata APIs (database/table/instance discovery, import
	// tagging, GetActiveRouteUnit). Empty = dms-enterprise.{region}.aliyuncs.com.
	DMSEnterpriseEndpoint string `yaml:"dms_enterprise_endpoint"`
	// DataAgentEndpoint overrides the host of the AK/SK-signed Data Agent API
	// (2025-04-14): session create/send/status and the AK/SK SSE stream.
	// Empty = dms.{region}.aliyuncs.com.
	DataAgentEndpoint string `yaml:"data_agent_endpoint"`
	STS               STS    `yaml:"sts"`
	Log               Log    `yaml:"log"`
	// Identity is the multi-tenant identity section. The legacy section
	// name "aily" is still accepted as an alias.
	Identity       Identity  `yaml:"identity"`
	LegacyIdentity *Identity `yaml:"aily"` // deprecated alias for identity
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.TempDir()
}

// LoadDotEnv loads KEY=VALUE pairs into the process environment without
// overriding variables that are already set. Search order:
// $DATA_AGENT_ENV_FILE, then ./.env. Missing files are silently skipped.
func LoadDotEnv() error {
	paths := []string{".env"}
	if p := os.Getenv("DATA_AGENT_ENV_FILE"); p != "" {
		paths = []string{p}
	}
	for _, p := range paths {
		data, err := readConfigFile(p, dotenvContainsSecret)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("dotenv: %w", errSecretFile)
		}
		for key, value := range dotenvValues(data) {
			// A loaded file must not relax checks on files read later.
			if key == "DOTENV_UNSAFE_FILES_ALLOW" {
				continue
			}
			if _, set := os.LookupEnv(key); !set {
				if err := os.Setenv(key, value); err != nil {
					return errors.New("invalid dotenv entry")
				}
			}
		}
	}
	return nil
}

// configPath resolves the YAML config file location:
// $DATA_AGENT_CONFIG > ./config.yaml > ~/.data-agent/config.yaml >
// ~/.data-agent/config.json (legacy; YAML is a superset of JSON).
func configPath() string {
	if p := os.Getenv("DATA_AGENT_CONFIG"); p != "" {
		return p
	}
	candidates := []string{
		"config.yaml",
		filepath.Join(homeDir(), ".data-agent", "config.yaml"),
		filepath.Join(homeDir(), ".data-agent", "config.json"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func decodeConfig(data []byte, cfg *Config) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documents []*yaml.Node
	for {
		var doc yaml.Node
		if err := decoder.Decode(&doc); err == io.EOF {
			break
		} else if err != nil {
			return errors.New("invalid server configuration YAML")
		}
		documents = append(documents, &doc)
	}
	if len(documents) == 0 {
		return nil
	}
	if len(documents) != 1 {
		return errors.New("server configuration must contain one YAML document")
	}
	if err := documents[0].Decode(cfg); err != nil {
		// Decoder errors may include raw scalar values, including secrets.
		return errors.New("invalid server configuration YAML")
	}
	return nil
}

// Load reads configuration with priority: env vars > .env > config file > defaults.
// Call LoadDotEnv before Load so .env values are visible as env vars.
func Load() (Config, string, error) {
	cfg := Config{
		Region:       "cn-hangzhou",
		SessionsDir:  filepath.Join(homeDir(), ".data-agent", "sessions"),
		STS:          STS{SessionExpiration: 3600},
		Log:          Log{MaxBytes: DefaultLogMaxBytes},
		AgentGateway: AgentGateway{RequestTimeoutSeconds: 30, MaxResponseBytes: 4194304},
		HTTP:         DefaultHTTP(),
		Identity:     Identity{MaxTenants: 128},
	}
	// 1. Config file (lowest priority). yaml.v3 also parses legacy JSON.
	// A missing file is not an error (matches the previous JSON loader);
	// unreadable or malformed files are.
	path := configPath()
	if path != "" {
		data, err := readConfigFile(path, yamlContainsSecret)
		switch {
		case os.IsNotExist(err):
			path = ""
		case err != nil:
			return cfg, path, fmt.Errorf("server configuration: %w", errSecretFile)
		default:
			if err := decodeConfig(data, &cfg); err != nil {
				return cfg, path, err
			}
			if cfg.Region == "" {
				cfg.Region = "cn-hangzhou"
			}
			if cfg.SessionsDir == "" {
				cfg.SessionsDir = filepath.Join(homeDir(), ".data-agent", "sessions")
			}
			if cfg.STS.SessionExpiration <= 0 {
				cfg.STS.SessionExpiration = 3600
			}
		}
	}

	// 2. Environment variables (highest priority).
	if v := os.Getenv("BUDDY_REGION"); v != "" {
		cfg.Region = v
	}
	if v := os.Getenv("DATA_AGENT_REGION"); v != "" {
		cfg.Region = v
	}
	if v := os.Getenv("DATA_AGENT_DMS_UNIT"); v != "" {
		cfg.DMSUnit = v
	}
	if v := os.Getenv("DATA_AGENT_DMS_ENTERPRISE_ENDPOINT"); v != "" {
		cfg.DMSEnterpriseEndpoint = v
	}
	if v := os.Getenv("DATA_AGENT_ENDPOINT"); v != "" {
		cfg.DataAgentEndpoint = v
	}
	if v := os.Getenv("DATA_AGENT_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("DATA_AGENT_API_KEY_ENDPOINT"); v != "" {
		cfg.APIKeyEndpoint = v
	}
	if v := os.Getenv("DATA_AGENT_API_KEY_STREAM_ENDPOINT"); v != "" {
		cfg.APIKeyStreamEndpoint = v
	}
	if v := os.Getenv("DATA_AGENT_WORKSPACE_ID"); v != "" {
		cfg.WorkspaceID = v
	}
	if v := os.Getenv("DATA_AGENT_CUSTOM_AGENT_ID"); v != "" {
		cfg.CustomAgentID = v
	}
	if v := os.Getenv("DATA_AGENT_SESSIONS_DIR"); v != "" {
		cfg.SessionsDir = v
	}
	if v := os.Getenv("AILY_SHARED_SECRET"); v != "" {
		cfg.Identity.AuthToken = v
	}
	if v := os.Getenv("IDENTITY_SHARED_SECRET"); v != "" {
		cfg.Identity.AuthToken = v
	}
	if v := os.Getenv("IDENTITY_AUTH_TOKEN"); v != "" {
		cfg.Identity.AuthToken = v
	}
	if v := os.Getenv("IDENTITY_JWT_SECRET"); v != "" {
		cfg.Identity.JWT.Secret = v
	}
	// LookupEnv, not Getenv: an explicitly empty value means "no prefix", which
	// is different from leaving the variable unset.
	if v, ok := os.LookupEnv("IDENTITY_SESSION_NAME_PREFIX"); ok {
		cfg.Identity.SessionNamePrefix = ptr(v)
	}
	if v := os.Getenv("IDENTITY_JWT_SECRETS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.Identity.JWT.Secrets = append(cfg.Identity.JWT.Secrets, s)
			}
		}
	}
	if v := os.Getenv("DATA_AGENT_LOG"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "on":
			cfg.Log.Enabled = true
		case "0", "false", "off":
			cfg.Log.Enabled = false
		default:
			return cfg, path, errors.New("DATA_AGENT_LOG must be 1/true/on or 0/false/off")
		}
	}
	if v, ok := os.LookupEnv("DATA_AGENT_LOG_FILE"); ok {
		cfg.Log.File = v
	}
	if v := os.Getenv("DATA_AGENT_LOG_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return cfg, path, errors.New("DATA_AGENT_LOG_MAX_BYTES must be a positive integer")
		}
		cfg.Log.MaxBytes = n
	}
	if v := os.Getenv("DATA_AGENT_LOG_REQUESTS"); v != "" {
		cfg.Log.Requests = v
	}
	if v := os.Getenv("DATA_AGENT_PERF_LOG"); v != "" {
		cfg.Log.Perf.Enabled = v != "0" && !strings.EqualFold(v, "false") && !strings.EqualFold(v, "off")
	}
	if v := os.Getenv("DATA_AGENT_PERF_LOG_FILE"); v != "" {
		cfg.Log.Perf.File = v
	}
	if v := os.Getenv("DATA_AGENT_PERF_LOG_INTERVAL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return cfg, path, errors.New("DATA_AGENT_PERF_LOG_INTERVAL must be a positive integer")
		}
		cfg.Log.Perf.IntervalSeconds = n
	}

	cfg.SessionsDir = expandHome(cfg.SessionsDir)
	if cfg.Log.File == "" {
		cfg.Log.File = filepath.Join(homeDir(), ".data-agent", "server.log")
	}
	cfg.Log.File = expandHome(cfg.Log.File)
	if cfg.Log.MaxBytes <= 0 {
		return cfg, path, errors.New("log.max_bytes must be a positive integer")
	}
	if cfg.Log.Perf.File == "" {
		cfg.Log.Perf.File = filepath.Join(homeDir(), ".data-agent", "perf.log")
	}
	cfg.Log.Perf.File = expandHome(cfg.Log.Perf.File)
	if cfg.Log.Perf.IntervalSeconds < 0 || cfg.Log.Perf.MaxBytes < 0 {
		return cfg, path, errors.New("log.perf interval_seconds and max_bytes must not be negative")
	}
	if cfg.Log.Enabled && cfg.Log.Perf.Enabled {
		if err := validateLogFiles(cfg.Log.File, cfg.Log.Perf.File); err != nil {
			return cfg, path, err
		}
	}
	cfg.ApplyDefaults()
	// Load starts with defaults, so zero here is an explicitly invalid limit,
	// not an absent setting. Do not silently replace it, even while disabled.
	if cfg.AgentGateway.RequestTimeoutSeconds == 0 || cfg.AgentGateway.MaxResponseBytes == 0 {
		return cfg, path, errors.New("agent_gateway limits must be positive")
	}

	if err := cfg.HTTP.validate(); err != nil {
		return cfg, path, err
	}
	if cfg.Identity.MaxTenants <= 0 {
		return cfg, path, errors.New("identity.max_tenants must be positive")
	}
	if err := cfg.ValidateTransport(os.Getenv("MCP_TRANSPORT")); err != nil {
		return cfg, path, err
	}
	return cfg, path, nil
}

func validateLogFiles(serverFile, perfFile string) error {
	paths := [4]string{serverFile, serverFile + ".1", perfFile, perfFile + ".1"}
	var resolved [4]string
	var info [4]os.FileInfo
	for i, path := range paths {
		var err error
		resolved[i], err = resolveLogPath(path)
		if err != nil {
			return errors.New("invalid log file path")
		}
		info[i], err = os.Stat(path)
		if err != nil && !os.IsNotExist(err) {
			return errors.New("invalid log file path")
		}
	}
	for i := 0; i < 2; i++ {
		for j := 2; j < 4; j++ {
			if resolved[i] == resolved[j] || (info[i] != nil && info[j] != nil && os.SameFile(info[i], info[j])) {
				return errors.New("log.file and log.perf.file must not overlap, including rotation backups")
			}
		}
	}
	return nil
}

func resolveLogPath(path string) (string, error) {
	var suffix string
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			return filepath.Abs(filepath.Join(resolved, suffix))
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		// A dangling symlink could target a log created later during startup.
		if _, statErr := os.Lstat(path); statErr == nil {
			return "", err
		}
		// Resolve the existing ancestor even when the log directory is not created yet.
		parent := filepath.Dir(path)
		if parent == path {
			return "", err
		}
		suffix = filepath.Join(filepath.Base(path), suffix)
		path = parent
	}
}

// ApplyDefaults merges the legacy "aily" alias section and fills
// Aily-compatible defaults for header names and the session-name prefix.
// Load calls it automatically; call it manually only for hand-built configs
// (e.g. in tests).
func (c *Config) ApplyDefaults() {
	if c.LegacyIdentity != nil && !c.Identity.Enabled {
		token := c.Identity.AuthToken // env override survives the alias merge
		c.Identity = *c.LegacyIdentity
		if token != "" {
			c.Identity.AuthToken = token
		}
	}
	c.LegacyIdentity = nil
	if c.Identity.Enabled && c.Identity.Namespace == "" {
		c.Identity.Namespace = "default"
	}
	// Legacy "shared_secret" key feeds AuthToken unless auth_token/env is set.
	if c.Identity.AuthToken == "" && c.Identity.LegacySharedSecret != "" {
		c.Identity.AuthToken = c.Identity.LegacySharedSecret
	}
	c.Identity.LegacySharedSecret = ""
	if c.Identity.Headers.User == "" {
		c.Identity.Headers.User = "x-aily-user"
	}
	if c.Identity.Headers.Email == "" {
		c.Identity.Headers.Email = "x-aily-email"
	}
	if c.Identity.Headers.Token == "" {
		c.Identity.Headers.Token = "x-aily-token"
	}
	if c.Identity.SessionNamePrefix == nil {
		// Absent (not an explicit ""), so keep the historical
		// "aily-<user>" RoleSessionName.
		c.Identity.SessionNamePrefix = ptr("aily")
	}
	if c.Identity.JWT.Header == "" {
		c.Identity.JWT.Header = "x-aily-jwt"
	}
	if c.Identity.SessionNameClaim == "" {
		c.Identity.SessionNameClaim = "user_id"
	}
}

// validSessionNameClaims are the identity fields that may be used as the STS
// RoleSessionName segment. Kept in sync with tenant.Claims.Field.
var validSessionNameClaims = []string{
	"user_id", "email", "enterprise_email", "employee_no", "tenant_id", "agent_id",
}

// isValidSessionNameClaim reports whether the configured claim name is usable.
// An empty name is accepted: ApplyDefaults fills it with user_id, and
// tenant.Claims.Field treats "" the same way, so validate() does not depend on
// having been called after ApplyDefaults.
func isValidSessionNameClaim(name string) bool {
	if name == "" {
		return true
	}
	for _, v := range validSessionNameClaims {
		if name == v {
			return true
		}
	}
	return false
}

func (c *Config) validate() error {
	if c.APIKey != "" && (c.Identity.Enabled || c.AgentGateway.Enabled) {
		return errors.New("identity and agent_gateway require AK/STS credentials; API Key is supported only in local single-identity mode")
	}
	if c.Identity.Namespace != "" && !ValidNamespace(c.Identity.Namespace) {
		return errors.New("invalid identity.namespace")
	}
	if c.Identity.MaxTenants < 0 {
		return errors.New("identity.max_tenants must be positive")
	}
	if c.HTTP != (HTTP{}) {
		if err := c.HTTP.validate(); err != nil {
			return err
		}
	}
	g := c.AgentGateway
	if g.DataSourcesEnabled && !g.Enabled {
		return errors.New("agent_gateway.data_sources_enabled requires agent_gateway.enabled")
	}
	if g.RequestTimeoutSeconds < 0 || int64(g.RequestTimeoutSeconds) > math.MaxInt64/int64(time.Second) ||
		g.MaxResponseBytes < 0 || g.MaxResponseBytes == math.MaxInt64 {
		return errors.New("agent_gateway limits must be positive and within supported bounds")
	}
	if g.Enabled {
		if g.RequestTimeoutSeconds == 0 || g.MaxResponseBytes == 0 {
			return errors.New("agent_gateway limits must be positive")
		}
		switch c.Region {
		case "cn-shenzhen", "cn-beijing", "cn-shanghai", "cn-hongkong",
			"ap-southeast-1", "cn-hangzhou", "us-west-1", "us-east-1":
		default:
			return errors.New("agent_gateway requires a region with a published official DMS endpoint")
		}
		if g.Endpoint != "dms."+c.Region+".aliyuncs.com" {
			return errors.New("agent_gateway.endpoint must be the explicit official DMS host for the configured region")
		}
	}
	if c.Identity.Enabled {
		if (c.Identity.Default == nil || c.Identity.Default.RoleArn == "") && len(c.Identity.Groups) == 0 {
			return fmt.Errorf("identity.enabled requires identity.default.role_arn or at least one identity.groups entry")
		}
		if c.Identity.JWT.Enabled && len(c.Identity.JWT.AllSecrets()) == 0 {
			return fmt.Errorf("identity.jwt.enabled requires identity.jwt.secret or identity.jwt.secrets (or env IDENTITY_JWT_SECRET / IDENTITY_JWT_SECRETS)")
		}
		// The platform token is required in both modes; JWT absence never
		// changes the administrator-selected authentication mode.
		if c.Identity.AuthToken == "" {
			return fmt.Errorf("identity.enabled requires identity.auth_token (or env IDENTITY_AUTH_TOKEN) to authenticate the upstream; the identity headers are otherwise forgeable")
		}
		if !isValidSessionNameClaim(c.Identity.SessionNameClaim) {
			return errors.New("invalid identity.session_name_claim; select a supported identity field")
		}
		if err := c.validateScopes(); err != nil {
			return err
		}
	}
	return nil
}

// STSEndpoint returns the effective STS endpoint for the configured region.
func (c *Config) STSEndpoint() string {
	if c.STS.Endpoint != "" {
		return c.STS.Endpoint
	}
	return fmt.Sprintf("sts.%s.aliyuncs.com", c.Region)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}
