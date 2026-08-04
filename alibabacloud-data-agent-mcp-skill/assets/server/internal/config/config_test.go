package config

import (
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
	} {
		t.Setenv(k, "")
	}
	os.Unsetenv("DATA_AGENT_CONFIG")
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
	if cfg.Identity.AuthToken != "topsecret" || cfg.Identity.SessionNamePrefix != "prod" {
		t.Fatalf("auth_token/prefix = %q/%q", cfg.Identity.AuthToken, cfg.Identity.SessionNamePrefix)
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

func TestValidateIdentityRejectsAPIKey(t *testing.T) {
	cfg := Config{
		APIKey:   "dms-da-xxx",
		Identity: Identity{Enabled: true, Default: &IdentityGroup{RoleArn: "acs:ram::1:role/da-default"}},
	}
	if err := cfg.validate(); err == nil {
		t.Fatal("expected error when aily enabled with api_key auth")
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
	if name, g, err := cfg.ResolveGroup("ou_alice", ""); err != nil || name != "analysts" || g.Mode != "ANALYSIS" {
		t.Fatalf("user_id membership: %q %+v %v", name, g, err)
	}
	// Group membership by email.
	if name, _, err := cfg.ResolveGroup("ou_unknown", "bob@example.com"); err != nil || name != "analysts" {
		t.Fatalf("email membership: %q %v", name, err)
	}
	// Everyone else lands on the default group.
	if name, g, err := cfg.ResolveGroup("ou_charlie", ""); err != nil || name != "default" || g.RoleArn != "acs:ram::123:role/da-default" {
		t.Fatalf("default fallback: %q %+v %v", name, g, err)
	}

	// Without a default group, unmatched users are rejected (fail-closed).
	cfg.Identity.Default = nil
	if _, _, err := cfg.ResolveGroup("ou_charlie", "nobody@example.com"); err == nil {
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
