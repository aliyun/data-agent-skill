package config

import (
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// HTTP bounds remote requests and protocol sessions, not stdio operations.
type HTTP struct {
	MaxRequestBytes           int64 `yaml:"max_request_bytes"`
	MaxHeaderBytes            int   `yaml:"max_header_bytes"`
	ReadHeaderTimeoutSeconds  int   `yaml:"read_header_timeout_seconds"`
	ReadBodyTimeoutSeconds    int   `yaml:"read_body_timeout_seconds"`
	IdleTimeoutSeconds        int   `yaml:"idle_timeout_seconds"`
	MaxSessions               int   `yaml:"max_sessions"`
	MaxSessionsPerPrincipal   int   `yaml:"max_sessions_per_principal"`
	MaxInflightRequests       int   `yaml:"max_inflight_requests"`
	MaxInflightPerSession     int   `yaml:"max_inflight_per_session"`
	SessionIdleTimeoutSeconds int   `yaml:"session_idle_timeout_seconds"`
	MaxStreamLifetimeSeconds  int   `yaml:"max_stream_lifetime_seconds"`
}

func DefaultHTTP() HTTP {
	return HTTP{1048576, 65536, 5, 10, 60, 4096, 64, 256, 8, 1800, 3600}
}

func (h *HTTP) UnmarshalYAML(node *yaml.Node) error {
	if err := integerFields(node, "max_request_bytes", "max_header_bytes", "read_header_timeout_seconds", "read_body_timeout_seconds", "idle_timeout_seconds", "max_sessions", "max_sessions_per_principal", "max_inflight_requests", "max_inflight_per_session", "session_idle_timeout_seconds", "max_stream_lifetime_seconds"); err != nil {
		return err
	}
	type plain HTTP
	v := plain(DefaultHTTP())
	if err := node.Decode(&v); err != nil {
		return errors.New("invalid http configuration")
	}
	*h = HTTP(v)
	return h.validate()
}

func integerFields(node *yaml.Node, names ...string) error {
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return errors.New("invalid numeric configuration")
	}
	for _, name := range names {
		field, exists := fields[name]
		if !exists {
			continue
		}
		value := &field
		seen := map[*yaml.Node]bool{}
		for value.Kind == yaml.AliasNode && value.Alias != nil && !seen[value] {
			seen[value] = true
			value = value.Alias
		}
		if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
			return errors.New("configuration limits require integer values")
		}
	}
	return nil
}

func (i *Identity) UnmarshalYAML(node *yaml.Node) error {
	if err := integerFields(node, "max_tenants"); err != nil {
		return err
	}
	type plain Identity
	v := plain{MaxTenants: 128}
	if err := node.Decode(&v); err != nil {
		return errors.New("invalid identity configuration")
	}
	if v.MaxTenants <= 0 {
		return errors.New("identity.max_tenants must be positive")
	}
	*i = Identity(v)
	return nil
}

func (h HTTP) validate() error {
	if h.MaxRequestBytes <= 0 || h.MaxRequestBytes == math.MaxInt64 || h.MaxHeaderBytes <= 0 || h.MaxHeaderBytes > math.MaxInt-4096 || h.MaxSessions <= 0 || h.MaxSessionsPerPrincipal <= 0 || h.MaxInflightRequests <= 0 || h.MaxInflightPerSession <= 0 || h.MaxSessionsPerPrincipal > h.MaxSessions || h.MaxInflightPerSession > h.MaxInflightRequests {
		return errors.New("http limits must be positive, within supported bounds, and per-owner limits must not exceed totals")
	}
	for _, seconds := range []int{h.ReadHeaderTimeoutSeconds, h.ReadBodyTimeoutSeconds, h.IdleTimeoutSeconds, h.SessionIdleTimeoutSeconds, h.MaxStreamLifetimeSeconds} {
		if seconds <= 0 || int64(seconds) > math.MaxInt64/int64(time.Second) {
			return errors.New("http timeouts must be positive and within duration bounds")
		}
	}
	return nil
}

var namespacePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func ValidNamespace(s string) bool { return namespacePattern.MatchString(s) }

// NormalizeIdentity is shared by configuration, claim parsing and mapping.
// Trim only peripheral Unicode whitespace; reject controls before trimming.
// Preserve case, internal spaces and Unicode code points without normalization.
func NormalizeIdentity(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("invalid identity field")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errors.New("invalid identity field")
		}
	}
	return strings.TrimSpace(s), nil
}

// IdentityClaims is supplied by the authentication layer, never raw HTTP input.
type IdentityClaims struct{ AuthMode, TenantID, AgentID, UserID, Email string }

var ErrIdentityGroupConflict = errors.New("IDENTITY_GROUP_CONFLICT")
var ErrIdentityUnmapped = errors.New("identity has no role mapping")

func (c *Config) ResolveGroup(claims IdentityClaims) (string, IdentityGroup, error) {
	mode := "headers"
	if c.Identity.JWT.Enabled {
		mode = "jwt"
	}
	if claims.AuthMode != mode || (mode == "jwt" && (claims.TenantID == "" || claims.AgentID == "")) || (mode == "headers" && (claims.TenantID != "" || claims.AgentID != "")) {
		return "", IdentityGroup{}, ErrIdentityUnmapped
	}
	for _, field := range []*string{&claims.UserID, &claims.Email, &claims.TenantID, &claims.AgentID} {
		normalized, err := NormalizeIdentity(*field)
		if err != nil {
			return "", IdentityGroup{}, ErrIdentityUnmapped
		}
		*field = normalized
	}
	if (claims.UserID == "" && claims.Email == "") || mode == "jwt" && (claims.TenantID == "" || claims.AgentID == "") {
		return "", IdentityGroup{}, ErrIdentityUnmapped
	}
	matches := func(g IdentityGroup) bool { return g.TenantID == claims.TenantID && g.AgentID == claims.AgentID }
	var name string
	var selected IdentityGroup
	found := false
	for n, g := range c.Identity.Groups {
		if !matches(g) {
			continue
		}
		for _, member := range g.Users {
			u, err := NormalizeIdentity(member)
			if err != nil || u == "" {
				continue
			}
			if u == claims.UserID || u == claims.Email {
				if found {
					return "", IdentityGroup{}, ErrIdentityGroupConflict
				}
				name, selected, found = n, g, true
				break
			}
		}
	}
	if found && selected.RoleArn != "" {
		return name, selected, nil
	}
	if !found && c.Identity.Default != nil && matches(*c.Identity.Default) && c.Identity.Default.RoleArn != "" {
		return "default", *c.Identity.Default, nil
	}
	return "", IdentityGroup{}, ErrIdentityUnmapped
}

func (c *Config) validateScopes() error {
	seen := map[[3]string]string{}
	check := func(g IdentityGroup) error {
		tenant, err := NormalizeIdentity(g.TenantID)
		if err != nil || tenant != g.TenantID {
			return errors.New("identity mapping has invalid scope")
		}
		agent, err := NormalizeIdentity(g.AgentID)
		if err != nil || agent != g.AgentID {
			return errors.New("identity mapping has invalid scope")
		}
		if c.Identity.JWT.Enabled {
			if tenant == "" || agent == "" || strings.ContainsAny(tenant+agent, "*?") {
				return errors.New("JWT identity mappings require exact tenant_id and agent_id; migrate unscoped mappings")
			}
		} else if tenant != "" || agent != "" {
			return errors.New("header identity mappings must not set tenant_id or agent_id")
		}
		return nil
	}
	if c.Identity.Default != nil {
		if c.Identity.Default.RoleArn == "" {
			return errors.New("identity.default requires role_arn")
		}
		if err := check(*c.Identity.Default); err != nil {
			return err
		}
	}
	for name, g := range c.Identity.Groups {
		if g.RoleArn == "" || len(g.Users) == 0 {
			return errors.New("identity groups require role_arn and users")
		}
		if err := check(g); err != nil {
			return err
		}
		for _, member := range g.Users {
			u, err := NormalizeIdentity(member)
			if err != nil || u == "" {
				return errors.New("invalid identity group member")
			}
			key := [3]string{g.TenantID, g.AgentID, u}
			if previous, ok := seen[key]; ok && previous != name {
				return ErrIdentityGroupConflict
			}
			seen[key] = name
		}
	}
	return nil
}

// ValidateTransport must be called by every remote entry point, including
// callers that construct Config directly instead of using Load.
func (c *Config) ValidateTransport(transport string) error {
	if err := c.validate(); err != nil {
		return err
	}
	switch transport {
	case "", "stdio":
		return nil
	case "sse", "streamable-http":
	default:
		return errors.New("unsupported MCP transport")
	}
	if !c.Identity.Enabled || !c.Identity.RequireIdentity || strings.TrimSpace(c.Identity.AuthToken) == "" || !ValidNamespace(c.Identity.Namespace) {
		return errors.New("remote transport requires identity.enabled, require_identity, auth_token and a valid namespace; migrate anonymous or fallback configuration")
	}
	if c.Identity.MaxTenants <= 0 {
		return errors.New("identity.max_tenants must be positive")
	}
	return c.HTTP.validate()
}
