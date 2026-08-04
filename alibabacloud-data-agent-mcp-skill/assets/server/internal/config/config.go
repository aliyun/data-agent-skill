// Package config loads the MCP server configuration from a YAML file
// combined with a .env file and process environment variables.
//
// Priority (highest wins): env vars > .env file > YAML config > defaults.
// The .env file is loaded first into the process environment (without
// overriding variables that are already set), so a single lookup through
// os.Getenv covers both sources.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// IdentityGroup is a set of users sharing one RAM role and session defaults.
// The same shape is used for the global default (identity.default) and for
// named groups (identity.groups.<name>).
type IdentityGroup struct {
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
	// named groups; ignored on the default group (which catches everyone).
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
// Feishu Aily signs the end-user identity with a platform-generated HS256
// secret and sends it as a header on every MCP request. Several upstream
// agents can share one MCP Server while each signs with its own secret, so
// more than one key may be accepted — a token is valid if any configured
// secret verifies it.
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

// Identity configures multi-tenant identity resolution. The upstream caller
// (e.g. Feishu Aily) forwards the end-user identity on every MCP HTTP
// request — either as a signed JWT (identity.jwt) or through the configured
// headers — and the server maps that identity to a RAM role via STS AssumeRole
// and executes all calls under it.
//
// Two ways to map users to RAM roles:
//  1. Default (global sharing): identity.default carries one role plus
//     optional workspace/agent/mode defaults for every identified user.
//  2. Groups: each identity.groups.<name> lists its users and carries its
//     own role and defaults. Group membership wins over the default.
type Identity struct {
	Enabled bool `yaml:"enabled"`
	// JWT enables verification of the upstream-signed identity token.
	JWT JWT `yaml:"jwt"`
	// SessionNameClaim selects which identity field becomes the STS
	// RoleSessionName segment: user_id (default), email, enterprise_email,
	// employee_no, tenant_id or agent_id. The name shows up in ActionTrail,
	// so pick the field your audit process recognises. Group membership is
	// always matched on user_id and email, independent of this setting.
	SessionNameClaim string `yaml:"session_name_claim"`
	// RequireIdentity rejects requests without identity headers instead of
	// falling back to the default (server) identity.
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
	// "<prefix>-<user_id>". Default "aily" keeps the historical
	// "aily-<user_id>" form; set e.g. "prod" for "prod-<user_id>".
	SessionNamePrefix string `yaml:"session_name_prefix"`
	// Headers overrides the identity header names (defaults: x-aily-*).
	Headers IdentityHeaders `yaml:"headers"`
	// Default is the global-sharing group: identified users that belong to
	// no named group fall back to it. Nil/empty role means unmatched users
	// are rejected (fail-closed).
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

// Upload restricts which local files data_agent_upload_file may read.
//
// The tool reads a server-side path chosen by the caller, so on the HTTP
// transports (where the caller is remote) an unrestricted path would let any
// client exfiltrate arbitrary server files. AllowedDirs confines uploads to
// an explicit set of directories; HTTP transports refuse every upload while
// it is empty (fail-closed). The stdio transport, where client and server
// share one trust domain and the caller is the local user, stays
// unrestricted unless AllowedDirs is set.
type Upload struct {
	AllowedDirs []string `yaml:"allowed_dirs"`
}

// Log configures server logging. Everything goes to stderr, so on stdio the
// host agent captures it and on the HTTP transports the process supervisor
// (systemd, container runtime) does.
type Log struct {
	// Requests selects how much of every tool call is logged:
	//   basic (default) — tool name, caller, outcome, duration
	//   full            — adds arguments with sensitive values redacted;
	//                     arguments carry user questions, so prefer it for
	//                     troubleshooting rather than steady-state operation
	//   off             — no per-call logging
	Requests string `yaml:"requests"`
}

// Config holds all server configuration.
type Config struct {
	Region      string `yaml:"region"`
	DMSUnit     string `yaml:"dms_unit"`
	WorkspaceID string `yaml:"workspace_id"`
	// CustomAgentID is the default custom agent for sessions that do not name
	// one, mirroring workspace_id. An identity group's custom_agent_id wins
	// over this, and an explicit tool argument wins over both.
	CustomAgentID string `yaml:"custom_agent_id"`
	SessionsDir   string `yaml:"sessions_dir"`
	APIKey        string `yaml:"api_key"`
	// DMSEnterpriseEndpoint overrides the dms-enterprise host used by the
	// 2018-11-01 metadata APIs (database/table/instance discovery, import
	// tagging, GetActiveRouteUnit). Empty = dms-enterprise.{region}.aliyuncs.com.
	DMSEnterpriseEndpoint string `yaml:"dms_enterprise_endpoint"`
	// DataAgentEndpoint overrides the host of the AK/SK-signed Data Agent API
	// (2025-04-14): session create/send/status and the AK/SK SSE stream.
	// Empty = dms.{region}.aliyuncs.com.
	DataAgentEndpoint string `yaml:"data_agent_endpoint"`
	// APIKeyEndpoint overrides the API Key control-plane host.
	// Empty = dataagent-{region}.aliyuncs.com. Only used with api_key auth.
	APIKeyEndpoint string `yaml:"api_key_endpoint"`
	// APIKeyStreamEndpoint overrides the API Key data-plane (streaming) host.
	// Empty = dataagent-stream-{region}.aliyuncs.com. Only used with api_key auth.
	APIKeyStreamEndpoint string `yaml:"api_key_stream_endpoint"`
	STS                  STS    `yaml:"sts"`
	Upload               Upload `yaml:"upload"`
	Log                  Log    `yaml:"log"`
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
func LoadDotEnv() {
	paths := []string{}
	if p := os.Getenv("DATA_AGENT_ENV_FILE"); p != "" {
		paths = append(paths, p)
	}
	paths = append(paths, ".env")

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			value = strings.TrimSpace(value)
			// Strip optional surrounding quotes.
			if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' ||
				value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
			if key == "" || os.Getenv(key) != "" {
				continue
			}
			os.Setenv(key, value)
		}
	}
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

// Load reads configuration with priority: env vars > .env > config file > defaults.
// Call LoadDotEnv before Load so .env values are visible as env vars.
func Load() (Config, string, error) {
	cfg := Config{
		Region:      "cn-hangzhou",
		SessionsDir: filepath.Join(homeDir(), ".data-agent", "sessions"),
		STS:         STS{SessionExpiration: 3600},
	}

	// 1. Config file (lowest priority). yaml.v3 also parses legacy JSON.
	// A missing file is not an error (matches the previous JSON loader);
	// unreadable or malformed files are.
	path := configPath()
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case os.IsNotExist(err):
			path = ""
		case err != nil:
			return cfg, path, fmt.Errorf("read config %s: %w", path, err)
		default:
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, path, fmt.Errorf("parse config %s: %w", path, err)
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
	if v := os.Getenv("DATA_AGENT_API_KEY"); v != "" {
		cfg.APIKey = v
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
	if v := os.Getenv("IDENTITY_JWT_SECRETS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.Identity.JWT.Secrets = append(cfg.Identity.JWT.Secrets, s)
			}
		}
	}
	// Colon-separated on Unix, semicolon-separated on Windows (os.PathListSeparator).
	if v := os.Getenv("DATA_AGENT_UPLOAD_DIRS"); v != "" {
		cfg.Upload.AllowedDirs = filepath.SplitList(v)
	}
	if v := os.Getenv("DATA_AGENT_LOG_REQUESTS"); v != "" {
		cfg.Log.Requests = v
	}

	cfg.SessionsDir = expandHome(cfg.SessionsDir)
	for i, d := range cfg.Upload.AllowedDirs {
		cfg.Upload.AllowedDirs[i] = expandHome(strings.TrimSpace(d))
	}
	cfg.ApplyDefaults()

	if err := cfg.validate(); err != nil {
		return cfg, path, err
	}
	return cfg, path, nil
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
	if c.Identity.SessionNamePrefix == "" {
		c.Identity.SessionNamePrefix = "aily" // keeps historical "aily-<user>" RoleSessionName
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
	if c.Identity.Enabled {
		if c.APIKey != "" {
			return fmt.Errorf("identity multi-tenant mode requires AK/SK base credentials for STS AssumeRole; api_key auth is not supported")
		}
		if (c.Identity.Default == nil || c.Identity.Default.RoleArn == "") && len(c.Identity.Groups) == 0 {
			return fmt.Errorf("identity.enabled requires identity.default.role_arn or at least one identity.groups entry")
		}
		if c.Identity.JWT.Enabled && len(c.Identity.JWT.AllSecrets()) == 0 {
			return fmt.Errorf("identity.jwt.enabled requires identity.jwt.secret or identity.jwt.secrets (or env IDENTITY_JWT_SECRET / IDENTITY_JWT_SECRETS)")
		}
		// A request without a JWT falls back to the forgeable identity headers,
		// so auth_token is what keeps a caller from skipping the token to reach
		// that path. Require it whenever identity mode is on.
		if c.Identity.AuthToken == "" {
			return fmt.Errorf("identity.enabled requires identity.auth_token (or env IDENTITY_AUTH_TOKEN) to authenticate the upstream; the identity headers are otherwise forgeable")
		}
		if !isValidSessionNameClaim(c.Identity.SessionNameClaim) {
			return fmt.Errorf("identity.session_name_claim %q is not one of %s",
				c.Identity.SessionNameClaim, strings.Join(validSessionNameClaims, ", "))
		}
		seen := map[string]string{}
		for name, g := range c.Identity.Groups {
			if g.RoleArn == "" {
				return fmt.Errorf("identity.groups.%s: role_arn is required", name)
			}
			if len(g.Users) == 0 {
				return fmt.Errorf("identity.groups.%s: users is required (use identity.default for the catch-all group)", name)
			}
			for _, u := range g.Users {
				if prev, dup := seen[u]; dup {
					return fmt.Errorf("identity user %q belongs to both group %q and %q; a user may belong to exactly one group", u, prev, name)
				}
				seen[u] = name
			}
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

// ResolveGroup maps an upstream user id/email to its group configuration.
// Named-group membership (matched by user id or email) wins over the global
// default group. Returns the group name ("default" for the catch-all) or an
// error when nothing matches (fail-closed).
func (c *Config) ResolveGroup(userID, email string) (string, IdentityGroup, error) {
	for name, g := range c.Identity.Groups {
		for _, u := range g.Users {
			if (userID != "" && u == userID) || (email != "" && u == email) {
				return name, g, nil
			}
		}
	}
	if c.Identity.Default != nil && c.Identity.Default.RoleArn != "" {
		return "default", *c.Identity.Default, nil
	}
	return "", IdentityGroup{}, fmt.Errorf("no identity group for user %q (email %q) and no identity.default configured", userID, email)
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}
