package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv resets all config-related env vars so tests are hermetic.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"DATA_AGENT_CONFIG", "DATA_AGENT_ENV_FILE", "BUDDY_REGION",
		"DATA_AGENT_REGION", "DATA_AGENT_DMS_UNIT", "DATA_AGENT_WORKSPACE_ID",
		"DATA_AGENT_SESSIONS_DIR", "DATA_AGENT_API_KEY", "AILY_SHARED_SECRET",
		"DATA_AGENT_API_KEY_ENDPOINT", "DATA_AGENT_API_KEY_STREAM_ENDPOINT",
		"DATA_AGENT_ENDPOINT", "DATA_AGENT_DMS_ENTERPRISE_ENDPOINT", "DATA_AGENT_CUSTOM_AGENT_ID",
		"IDENTITY_SHARED_SECRET", "IDENTITY_AUTH_TOKEN", "IDENTITY_JWT_SECRET", "IDENTITY_JWT_SECRETS",
		"IDENTITY_SESSION_NAME_PREFIX", "DATA_AGENT_LOG_REQUESTS", "MCP_TRANSPORT",
	} {
		t.Setenv(k, "")
	}
	os.Unsetenv("IDENTITY_SESSION_NAME_PREFIX")
	t.Setenv("DATA_AGENT_CONFIG", filepath.Join(t.TempDir(), "missing-config.yaml"))
}

func TestLoadUsesBuddyRegion(t *testing.T) {
	clearEnv(t)
	t.Setenv("BUDDY_REGION", "cn-shanghai")

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "cn-shanghai" {
		t.Fatalf("Region = %q, want cn-shanghai", cfg.Region)
	}
}

func TestLoadDataAgentRegionOverridesBuddyRegion(t *testing.T) {
	clearEnv(t)
	t.Setenv("BUDDY_REGION", "cn-shanghai")
	t.Setenv("DATA_AGENT_REGION", "cn-hangzhou")

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "cn-hangzhou" {
		t.Fatalf("Region = %q, want cn-hangzhou", cfg.Region)
	}
}

func TestLoadYAMLWithLegacyAilyAlias(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yamlBody := `
region: cn-shenzhen
dms_unit: cn-hangzhou
workspace_id: ws-default
sessions_dir: ` + filepath.Join(dir, "sessions") + `
sts:
  session_expiration: 7200
aily:
  enabled: true
  require_identity: true
  shared_secret: topsecret
  session_name_prefix: prod
  default:
    role_arn: acs:ram::123:role/da-default
    workspace_id: ws-shared
    mode: ASK_DATA
  groups:
    analysts:
      role_arn: acs:ram::123:role/da-analysts
      workspace_id: ws-analysts
      custom_agent_id: ca-xyz
      mode: ANALYSIS
      users: [ou_alice, bob@example.com]
`
	if err := os.WriteFile(path, []byte(yamlBody), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", path)

	cfg, from, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if from != path {
		t.Fatalf("config path = %q, want %q", from, path)
	}
	if cfg.Region != "cn-shenzhen" || cfg.DMSUnit != "cn-hangzhou" {
		t.Fatalf("unexpected region/dms_unit: %q/%q", cfg.Region, cfg.DMSUnit)
	}
	if !cfg.Identity.Enabled || !cfg.Identity.RequireIdentity {
		t.Fatal("aily flags not parsed")
	}
	// legacy "shared_secret" yaml key must feed AuthToken.
	if cfg.Identity.AuthToken != "topsecret" || cfg.Identity.Prefix() != "prod" {
		t.Fatalf("auth_token/prefix = %q/%q", cfg.Identity.AuthToken, cfg.Identity.Prefix())
	}
	if cfg.STS.SessionExpiration != 7200 {
		t.Fatalf("sts.session_expiration = %d, want 7200", cfg.STS.SessionExpiration)
	}
	if cfg.Identity.Default == nil || cfg.Identity.Default.RoleArn != "acs:ram::123:role/da-default" ||
		cfg.Identity.Default.WorkspaceID != "ws-shared" || cfg.Identity.Default.Mode != "ASK_DATA" {
		t.Fatalf("default group = %+v", cfg.Identity.Default)
	}
	g := cfg.Identity.Groups["analysts"]
	if g.RoleArn != "acs:ram::123:role/da-analysts" || g.WorkspaceID != "ws-analysts" ||
		g.CustomAgentID != "ca-xyz" || g.Mode != "ANALYSIS" || len(g.Users) != 2 {
		t.Fatalf("analysts group = %+v", g)
	}
	// Aily-compatible header defaults are filled by ApplyDefaults.
	h := cfg.Identity.Headers
	if h.User != "x-aily-user" || h.Email != "x-aily-email" || h.Token != "x-aily-token" {
		t.Fatalf("default identity headers = %+v", h)
	}
}

func TestLoadLegacyJSONConfig(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"region": "cn-beijing", "workspace_id": "ws-json"}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", path)

	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Region != "cn-beijing" || cfg.WorkspaceID != "ws-json" {
		t.Fatalf("legacy JSON not parsed: %+v", cfg)
	}
}

func TestValidateIdentityRequiresMapping(t *testing.T) {
	cfg := Config{Identity: Identity{Enabled: true, AuthToken: "t"}}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error when aily enabled without default/groups")
	}

	// default group alone is a valid mapping source.
	cfg = Config{Identity: Identity{Enabled: true, AuthToken: "t", Default: &IdentityGroup{RoleArn: "acs:ram::1:role/da-default"}}}
	if err := cfg.validate(); err != nil {
		t.Fatalf("unexpected error with default only: %v", err)
	}

	// groups alone are valid too.
	cfg = Config{Identity: Identity{Enabled: true, AuthToken: "t", Groups: map[string]IdentityGroup{
		"g1": {RoleArn: "acs:ram::1:role/g1", Users: []string{"ou_a"}},
	}}}
	if err := cfg.validate(); err != nil {
		t.Fatalf("unexpected error with groups only: %v", err)
	}

	// group without role_arn or without users is rejected.
	cfg.Identity.Groups = map[string]IdentityGroup{"bad": {Users: []string{"ou_a"}}}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for group without role_arn")
	}
	cfg.Identity.Groups = map[string]IdentityGroup{"bad": {RoleArn: "acs:ram::1:role/x"}}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for group without users")
	}

	// a user in two groups is rejected.
	cfg.Identity.Groups = map[string]IdentityGroup{
		"g1": {RoleArn: "acs:ram::1:role/g1", Users: []string{"ou_a"}},
		"g2": {RoleArn: "acs:ram::1:role/g2", Users: []string{"ou_a"}},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error for duplicate user across groups")
	}
}

func TestLoadRejectsLegacyAuthentication(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, withAK := range []bool{false, true} {
			for _, key := range []string{"api_key", "api_key_endpoint", "api_key_stream_endpoint"} {
				for _, value := range []string{"\"\"", "null", "migration-secret", "{secret: migration-secret}"} {
					t.Run(fmt.Sprintf("yaml/%v/%v/%s/%s", enabled, withAK, key, value), func(t *testing.T) {
						clearEnv(t)
						setTestAK(t, withAK)
						body := fmt.Sprintf("agent_gateway:\n  enabled: %v\n%s: %s\n", enabled, key, value)
						writeConfig(t, body)
						_, _, err := Load()
						if !errors.Is(err, errLegacyAuthentication) || strings.Contains(err.Error(), "migration-secret") {
							t.Fatalf("expected sanitized migration error, got %v", err)
						}
					})
				}
				for _, value := range []string{"migration-secret", " "} {
					t.Run(fmt.Sprintf("env/%v/%v/%s/%s", enabled, withAK, key, value), func(t *testing.T) {
						clearEnv(t)
						setTestAK(t, withAK)
						writeConfig(t, fmt.Sprintf("agent_gateway:\n  enabled: %v\n", enabled))
						t.Setenv("DATA_AGENT_"+strings.ToUpper(key), value)
						_, _, err := Load()
						if !errors.Is(err, errLegacyAuthentication) {
							t.Fatalf("got %v", err)
						}
					})
				}
			}
		}
	}
}

func setTestAK(t *testing.T, enabled bool) {
	t.Helper()
	ak, sk := "", ""
	if enabled {
		ak, sk = "test-ak", "test-sk"
	}
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", ak)
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", sk)
}

func writeConfig(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", path)
}

func TestLegacyAuthenticationAliasesAndMerges(t *testing.T) {
	for _, body := range []string{
		"legacy: &old {api_key: secret}\n<<: *old\n",
		"legacy: &old {api_key_endpoint: null}\n<<: [*old, {region: cn-hangzhou}]\n",
		"legacy: &old {api_key_stream_endpoint: ''}\nintermediate: &middle {<<: *old}\n<<: *middle\n",
		"key: &old api_key\n*old : null\n",
		"key: &old api_key_endpoint\n*old : null\n",
		"key: &old api_key_stream_endpoint\n*old : null\n",
		"legacy: &old {api_key: secret}\n<<: *old\napi_key: null\n",
		"region: cn-hangzhou\n---\napi_key: secret\n",
		"&root {<<: *root, api_key: secret}\n",
		`{"api_key": null}`,
	} {
		t.Run(body, func(t *testing.T) {
			clearEnv(t)
			writeConfig(t, body)
			if _, _, err := Load(); !errors.Is(err, errLegacyAuthentication) {
				t.Fatalf("got %v", err)
			}
		})
	}
	clearEnv(t)
	writeConfig(t, "extension: {api_key: not-a-top-level-key}\n")
	if _, _, err := Load(); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, "&root {<<: *root}\n")
	if _, _, err := Load(); err == nil {
		t.Fatal("cyclic merge accepted")
	}
}

func TestGatewayConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, body, transport string
		invalid               bool
	}{
		{"defaults", "", "", false},
		{"enabled", "agent_gateway: {enabled: true, endpoint: dms.cn-hangzhou.aliyuncs.com}", "stdio", false},
		{"explicit limits", "agent_gateway: {enabled: true, endpoint: dms.cn-hangzhou.aliyuncs.com, request_timeout_seconds: 5, max_response_bytes: 1234}", "", false},
		{"missing endpoint", "agent_gateway: {enabled: true}", "", true},
		{"unpublished region", "region: cn-unknown\nagent_gateway: {enabled: true, endpoint: dms.cn-unknown.aliyuncs.com}", "stdio", true},
		{"zero timeout", "agent_gateway: {enabled: true, endpoint: dms.cn-hangzhou.aliyuncs.com, request_timeout_seconds: 0}", "", true},
		{"zero bytes", "agent_gateway: {enabled: true, endpoint: dms.cn-hangzhou.aliyuncs.com, max_response_bytes: 0}", "", true},
		{"disabled zero timeout", "agent_gateway: {request_timeout_seconds: 0}", "", true},
		{"disabled zero bytes", "agent_gateway: {max_response_bytes: 0}", "", true},
		{"negative timeout", "agent_gateway: {request_timeout_seconds: -1}", "", true},
		{"negative bytes", "agent_gateway: {max_response_bytes: -1}", "", true},
		{"overflow timeout", "agent_gateway: {request_timeout_seconds: 9223372037}", "", true},
		{"overflow bytes", "agent_gateway: {max_response_bytes: 9223372036854775807}", "", true},
		{"remote sse", "agent_gateway: {enabled: true, endpoint: dms.cn-hangzhou.aliyuncs.com}", "sse", true},
		{"remote http", "agent_gateway: {enabled: true, endpoint: dms.cn-hangzhou.aliyuncs.com}", "streamable-http", true},
		{"disabled remote", "agent_gateway: {enabled: false}", "sse", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("MCP_TRANSPORT", tc.transport)
			writeConfig(t, tc.body)
			cfg, _, err := Load()
			if (err != nil) != tc.invalid {
				t.Fatalf("got %v", err)
			}
			if tc.name == "defaults" && (cfg.AgentGateway.RequestTimeoutSeconds != 30 || cfg.AgentGateway.MaxResponseBytes != 4194304 || cfg.AgentGateway.Enabled || cfg.AgentGateway.DataSourcesEnabled || cfg.AgentGateway.Endpoint != "") {
				t.Fatalf("bad defaults: %+v", cfg.AgentGateway)
			}
			if tc.name == "explicit limits" && (cfg.AgentGateway.RequestTimeoutSeconds != 5 || cfg.AgentGateway.MaxResponseBytes != 1234) {
				t.Fatalf("bad limits: %+v", cfg.AgentGateway)
			}
		})
	}
}

func TestGatewayDataSourcesConfiguration(t *testing.T) {
	for _, transport := range []string{"stdio", "sse", "streamable-http"} {
		for _, tc := range []struct {
			name, flags string
			wantEnabled bool
			wantError   string
		}{
			{"omitted", "", false, ""},
			{"gateway only", "enabled: true", false, ""},
			{"explicit false", "enabled: true, data_sources_enabled: false", false, ""},
			{"both enabled", "enabled: true, data_sources_enabled: true", true, ""},
			{"both disabled", "enabled: false, data_sources_enabled: false", false, ""},
			{"parent omitted", "data_sources_enabled: true", true, "agent_gateway.data_sources_enabled requires agent_gateway.enabled"},
			{"parent disabled", "enabled: false, data_sources_enabled: true", true, "agent_gateway.data_sources_enabled requires agent_gateway.enabled"},
			{"invalid bool", "enabled: true, data_sources_enabled: not-a-bool", false, "invalid server configuration YAML"},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				clearEnv(t)
				t.Setenv("MCP_TRANSPORT", transport)
				writeConfig(t, fmt.Sprintf("agent_gateway: {endpoint: dms.cn-hangzhou.aliyuncs.com, %s}\nidentity: {enabled: true, require_identity: true, namespace: test-domain, auth_token: test, default: {role_arn: 'acs:ram::1:role/test'}}\n", tc.flags))
				cfg, _, err := Load()
				if tc.wantError != "" {
					if err == nil || err.Error() != tc.wantError {
						t.Fatalf("expected %q, got %v", tc.wantError, err)
					}
					return
				}
				if err != nil || cfg.AgentGateway.DataSourcesEnabled != tc.wantEnabled {
					t.Fatalf("data source flag not preserved: %+v, %v", cfg.AgentGateway, err)
				}
			})
		}
		if transport != "stdio" {
			t.Run(transport+"/identity required", func(t *testing.T) {
				clearEnv(t)
				t.Setenv("MCP_TRANSPORT", transport)
				writeConfig(t, "agent_gateway: {enabled: true, data_sources_enabled: true, endpoint: dms.cn-hangzhou.aliyuncs.com}\n")
				if _, _, err := Load(); err == nil || !strings.Contains(err.Error(), "remote transport requires") {
					t.Fatalf("expected remote identity requirement, got %v", err)
				}
			})
		}
	}
	cfg := Config{AgentGateway: AgentGateway{DataSourcesEnabled: true}}
	if err := cfg.validate(); err == nil || err.Error() != "agent_gateway.data_sources_enabled requires agent_gateway.enabled" {
		t.Fatalf("hand-built config bypassed parent flag validation: %v", err)
	}
}

func TestGatewayEndpointAllowlist(t *testing.T) {
	for _, endpoint := range []string{
		"https://dms.cn-hangzhou.aliyuncs.com", "dms.cn-hangzhou.aliyuncs.com/", "dms.cn-hangzhou.aliyuncs.com?x=1", "dms.cn-hangzhou.aliyuncs.com#fragment", "user@dms.cn-hangzhou.aliyuncs.com", "dms.cn-hangzhou.aliyuncs.com:443", "dms.cn-hangzhou.aliyuncs.com.evil.test", "dms.cn-shanghai.aliyuncs.com", "dms-vpc.cn-hangzhou.aliyuncs.com", "127.0.0.1", "dms.cn-hangzhou.aliyuncs.com.",
	} {
		t.Run(endpoint, func(t *testing.T) {
			clearEnv(t)
			writeConfig(t, fmt.Sprintf("agent_gateway: {enabled: true, endpoint: %q}", endpoint))
			if _, _, err := Load(); err == nil {
				t.Fatal("endpoint accepted")
			}
		})
	}
	clearEnv(t)
	t.Setenv("DATA_AGENT_REGION", "cn-shanghai")
	for _, transport := range []string{"sse", "streamable-http"} {
		t.Setenv("MCP_TRANSPORT", transport)
		writeConfig(t, "agent_gateway: {enabled: true, endpoint: dms.cn-shanghai.aliyuncs.com}\nidentity: {enabled: true, require_identity: true, namespace: test-domain, auth_token: test, default: {role_arn: 'acs:ram::1:role/test'}}\n")
		if _, _, err := Load(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveGroup(t *testing.T) {
	cfg := Config{Identity: Identity{
		Enabled: true,
		Default: &IdentityGroup{RoleArn: "acs:ram::123:role/da-default", Mode: "ASK_DATA"},
		Groups: map[string]IdentityGroup{
			"analysts": {
				RoleArn:       "acs:ram::123:role/da-analysts",
				CustomAgentID: "ca-xyz",
				Mode:          "ANALYSIS",
				Users:         []string{"ou_alice", "bob@example.com"},
			},
		},
	}}

	// Group membership by user_id.
	if name, g, err := cfg.ResolveGroup(IdentityClaims{AuthMode: "headers", UserID: "ou_alice"}); err != nil || name != "analysts" || g.Mode != "ANALYSIS" {
		t.Fatalf("user_id membership: %q %+v %v", name, g, err)
	}
	// Group membership by email.
	if name, _, err := cfg.ResolveGroup(IdentityClaims{AuthMode: "headers", UserID: "ou_unknown", Email: "bob@example.com"}); err != nil || name != "analysts" {
		t.Fatalf("email membership: %q %v", name, err)
	}
	// Everyone else lands on the default group.
	if name, g, err := cfg.ResolveGroup(IdentityClaims{AuthMode: "headers", UserID: "ou_charlie"}); err != nil || name != "default" || g.RoleArn != "acs:ram::123:role/da-default" {
		t.Fatalf("default fallback: %q %+v %v", name, g, err)
	}

	// Without a default group, unmatched users are rejected (fail-closed).
	cfg.Identity.Default = nil
	if _, _, err := cfg.ResolveGroup(IdentityClaims{AuthMode: "headers", UserID: "ou_charlie", Email: "nobody@example.com"}); err == nil {
		t.Fatal("expected fail-closed error for unmatched user")
	}
}

func TestLoadDotEnv(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "test.env")
	body := "# comment\nDATA_AGENT_REGION=cn-qingdao\nexport DATA_AGENT_WORKSPACE_ID=\"ws-env\"\nEMPTY_LINE_BELOW\n\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("DATA_AGENT_REGION")
	os.Unsetenv("DATA_AGENT_WORKSPACE_ID")
	t.Setenv("DATA_AGENT_ENV_FILE", path)

	LoadDotEnv()
	t.Cleanup(func() {
		os.Unsetenv("DATA_AGENT_REGION")
		os.Unsetenv("DATA_AGENT_WORKSPACE_ID")
	})

	if got := os.Getenv("DATA_AGENT_REGION"); got != "cn-qingdao" {
		t.Fatalf("DATA_AGENT_REGION = %q", got)
	}
	if got := os.Getenv("DATA_AGENT_WORKSPACE_ID"); got != "ws-env" {
		t.Fatalf("DATA_AGENT_WORKSPACE_ID = %q (quotes should be stripped)", got)
	}

	// .env must not override variables that are already set.
	t.Setenv("DATA_AGENT_REGION", "cn-hangzhou")
	LoadDotEnv()
	if got := os.Getenv("DATA_AGENT_REGION"); got != "cn-hangzhou" {
		t.Fatalf("DATA_AGENT_REGION overridden to %q", got)
	}
}

func TestValidateIdentityRequiresAuthToken(t *testing.T) {
	// Identity mode always falls back to the forgeable headers, so auth_token
	// is mandatory once identity is enabled.
	cfg := Config{Identity: Identity{
		Enabled: true,
		Default: &IdentityGroup{RoleArn: "acs:ram::1:role/da-default"},
	}}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "auth_token") {
		t.Fatalf("expected auth_token requirement, got %v", err)
	}

	cfg.Identity.AuthToken = "upstream-secret"
	if err := cfg.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

// The prefix must distinguish "not configured" from "configured empty", so a
// deployment can drop the historical "aily-" segment entirely.
func TestSessionNamePrefixAbsentVsExplicitlyEmpty(t *testing.T) {
	write := func(t *testing.T, body string) Config {
		t.Helper()
		clearEnv(t)
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DATA_AGENT_CONFIG", path)
		cfg, _, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return cfg
	}

	// Absent: keep the historical prefix so existing deployments are unchanged.
	cfg := write(t, "region: cn-hangzhou\nidentity:\n  enabled: false\n")
	if got := cfg.Identity.Prefix(); got != "aily" {
		t.Errorf("absent prefix = %q, want aily", got)
	}

	// Explicitly empty: no prefix.
	cfg = write(t, "region: cn-hangzhou\nidentity:\n  enabled: false\n  session_name_prefix: \"\"\n")
	if got := cfg.Identity.Prefix(); got != "" {
		t.Errorf("explicit empty prefix = %q, want no prefix", got)
	}

	// Explicit value wins.
	cfg = write(t, "region: cn-hangzhou\nidentity:\n  enabled: false\n  session_name_prefix: prod\n")
	if got := cfg.Identity.Prefix(); got != "prod" {
		t.Errorf("prefix = %q, want prod", got)
	}
}

// An empty environment override must mean "no prefix", not "unset" — otherwise
// the prefix could not be dropped without editing config.yaml.
func TestSessionNamePrefixEnvOverrideEmpty(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("region: cn-hangzhou\nidentity:\n  session_name_prefix: prod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", path)
	t.Setenv("IDENTITY_SESSION_NAME_PREFIX", "")

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Identity.Prefix(); got != "" {
		t.Errorf("empty env override = %q, want no prefix", got)
	}
}
