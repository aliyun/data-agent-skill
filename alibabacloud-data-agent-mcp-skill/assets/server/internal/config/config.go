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

// Identity configures multi-tenant identity resolution. The upstream caller
// (e.g. Feishu Aily) forwards the end-user identity on every MCP HTTP
// request through the configured headers; the server maps that identity to a
// RAM role via STS AssumeRole and executes all calls under it.
//
// Two ways to map users to RAM roles:
//  1. Default (global sharing): identity.default carries one role plus
//     optional workspace/agent/mode defaults for every identified user.
//  2. Groups: each identity.groups.<name> lists its users and carries its
//     own role and defaults. Group membership wins over the default.
type Identity struct {
	Enabled bool `yaml:"enabled"`
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

// Config holds all server configuration.
type Config struct {
	Region      string `yaml:"region"`
	DMSUnit     string `yaml:"dms_unit"`
	WorkspaceID string `yaml:"workspace_id"`
	SessionsDir string `yaml:"sessions_dir"`
	APIKey      string `yaml:"api_key"`
	STS         STS    `yaml:"sts"`
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
	if v := os.Getenv("DATA_AGENT_WORKSPACE_ID"); v != "" {
		cfg.WorkspaceID = v
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

	cfg.SessionsDir = expandHome(cfg.SessionsDir)
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
}

func (c *Config) validate() error {
	if c.Identity.Enabled {
		if c.APIKey != "" {
			return fmt.Errorf("identity multi-tenant mode requires AK/SK base credentials for STS AssumeRole; api_key auth is not supported")
		}
		if (c.Identity.Default == nil || c.Identity.Default.RoleArn == "") && len(c.Identity.Groups) == 0 {
			return fmt.Errorf("identity.enabled requires identity.default.role_arn or at least one identity.groups entry")
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
