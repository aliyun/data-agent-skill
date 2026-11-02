package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestHTTPDefaultsAndExplicitLimits(t *testing.T) {
	clearEnv(t)
	cfg, _, err := Load()
	if err != nil || cfg.HTTP != DefaultHTTP() || cfg.Identity.MaxTenants != 128 {
		t.Fatalf("defaults: %v", err)
	}
	typ := reflect.TypeOf(HTTP{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i).Tag.Get("yaml")
		for _, value := range []string{"0", "-1", "1.5", "1.0", "'1'", "null"} {
			t.Run(field+value, func(t *testing.T) {
				clearEnv(t)
				writeConfig(t, "http: {"+field+": "+value+"}")
				if _, _, err := Load(); err == nil {
					t.Fatal("explicit nonpositive limit accepted")
				}
			})
		}
		if strings.HasSuffix(field, "_seconds") {
			t.Run(field+"overflow", func(t *testing.T) {
				clearEnv(t)
				writeConfig(t, "http: {"+field+": 9223372037}")
				if _, _, err := Load(); err == nil {
					t.Fatal("duration overflow accepted")
				}
			})
		}
	}
	for _, body := range []string{"identity: {max_tenants: 0}", "identity: {max_tenants: -1}", "http: {max_sessions: 1}", "http: {max_inflight_requests: 1}", fmt.Sprintf("http: {max_request_bytes: %d}", int64(math.MaxInt64))} {
		writeConfig(t, body)
		if _, _, err := Load(); err == nil {
			t.Fatalf("invalid numeric config accepted: %s", body)
		}
	}
	var partial Config
	if err := yaml.Unmarshal([]byte("http: {max_request_bytes: 42}\nidentity: {enabled: false}"), &partial); err != nil || partial.HTTP.MaxRequestBytes != 42 || partial.HTTP.ReadHeaderTimeoutSeconds != 5 || partial.Identity.MaxTenants != 128 {
		t.Fatalf("partial defaults: %v", err)
	}
}

func remoteConfig() Config {
	c := Config{HTTP: DefaultHTTP(), Identity: Identity{Enabled: true, RequireIdentity: true, Namespace: "test-domain", MaxTenants: 128, AuthToken: "synthetic-token", Default: &IdentityGroup{RoleArn: "role"}}}
	c.ApplyDefaults()
	return c
}

func TestRemoteRequirementsRegardlessGateway(t *testing.T) {
	for _, transport := range []string{"sse", "streamable-http"} {
		for _, gateway := range []bool{false, true} {
			base := remoteConfig()
			if gateway {
				base.Region = "cn-hangzhou"
				base.AgentGateway = AgentGateway{Enabled: true, Endpoint: "dms.cn-hangzhou.aliyuncs.com", RequestTimeoutSeconds: 30, MaxResponseBytes: 4194304}
			}
			if err := base.ValidateTransport(transport); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*Config){func(c *Config) { c.Identity.Enabled = false }, func(c *Config) { c.Identity.RequireIdentity = false }, func(c *Config) { c.Identity.AuthToken = "" }, func(c *Config) { c.Identity.Namespace = "" }, func(c *Config) { c.Identity.MaxTenants = 0 }, func(c *Config) { c.HTTP.ReadBodyTimeoutSeconds = 0 }} {
				c := base
				mutate(&c)
				if c.ValidateTransport(transport) == nil {
					t.Fatal("remote unsafe config accepted")
				}
			}
		}
	}
	if err := (&Config{}).ValidateTransport("stdio"); err != nil {
		t.Fatal("stdio remote constraints applied")
	}
	for _, unsupported := range []string{"http", "unknown", "STDIO"} {
		c := remoteConfig()
		if c.ValidateTransport(unsupported) == nil {
			t.Fatal("unsupported transport could fall back to stdio")
		}
	}
	for _, namespace := range []string{"Upper", "a.b", "_leading", "space domain", strings.Repeat("a", 65), "a\n"} {
		c := remoteConfig()
		c.Identity.Namespace = namespace
		if c.ValidateTransport("sse") == nil {
			t.Fatal("invalid namespace accepted")
		}
	}
}

func TestScopedMappingsAndCrossFieldConflict(t *testing.T) {
	c := remoteConfig()
	c.Identity.JWT = JWT{Enabled: true, Secret: "synthetic-key"}
	c.Identity.Groups = map[string]IdentityGroup{
		"a": {RoleArn: "shared", TenantID: "t1", AgentID: "a1", Users: []string{"id", "same@example.com"}},
		"b": {RoleArn: "shared", TenantID: "t2", AgentID: "a1", Users: []string{"id"}},
	}
	c.Identity.Default = &IdentityGroup{RoleArn: "default", TenantID: "t1", AgentID: "a1"}
	if err := c.ValidateTransport("sse"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ tenant, agent, user, email, want string }{
		{"t1", "a1", "id", "same@example.com", "a"},
		{"t2", "a1", "id", "", "b"},
		{"t1", "a1", "other", "", "default"},
		{"t2", "a1", "other", "", ""},
		{"t1", "a2", "id", "", ""},
		{"t3", "a1", "id", "", ""},
	} {
		name, _, err := c.ResolveGroup(IdentityClaims{AuthMode: "jwt", TenantID: tc.tenant, AgentID: tc.agent, UserID: tc.user, Email: tc.email})
		if tc.want == "" {
			if err == nil {
				t.Fatal("cross-scope default accepted")
			}
		} else if err != nil || name != tc.want {
			t.Fatalf("scope match: %q %v", name, err)
		}
	}
	c.Identity.Groups["cross"] = IdentityGroup{RoleArn: "shared", TenantID: "t1", AgentID: "a1", Users: []string{"other@example.com"}}
	for i := 0; i < 100; i++ {
		_, _, err := c.ResolveGroup(IdentityClaims{AuthMode: "jwt", TenantID: "t1", AgentID: "a1", UserID: "id", Email: "other@example.com"})
		if !errors.Is(err, ErrIdentityGroupConflict) {
			t.Fatal("cross-field ambiguity not rejected")
		}
	}
	c.Identity.Groups["cross"] = IdentityGroup{RoleArn: "shared", TenantID: "t1", AgentID: "a1", Users: []string{" id "}}
	if !errors.Is(c.ValidateTransport("sse"), ErrIdentityGroupConflict) {
		t.Fatal("normalized duplicate member accepted")
	}
}

func TestMappingScopeMigrationValidation(t *testing.T) {
	for _, jwtMode := range []bool{false, true} {
		for _, scope := range [][2]string{{"", ""}, {"tenant", ""}, {"", "agent"}, {"*", "agent"}, {"tenant", "agent"}} {
			c := remoteConfig()
			c.Identity.JWT = JWT{Enabled: jwtMode, Secret: "synthetic-key"}
			c.Identity.Default = &IdentityGroup{RoleArn: "role", TenantID: scope[0], AgentID: scope[1]}
			valid := jwtMode && scope[0] == "tenant" && scope[1] == "agent" || !jwtMode && scope == [2]string{}
			if err := c.ValidateTransport("sse"); (err == nil) != valid {
				t.Fatalf("JWT=%v scope=%v: %v", jwtMode, scope, err)
			}
		}
	}
}

func TestSecretFilePermissionsAndNoSecretDisclosure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ACL-unverified file secrets are blocked on Windows")
	}
	for _, mode := range []os.FileMode{0600, 0400, 0644} {
		for _, secret := range []bool{false, true} {
			clearEnv(t)
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			body := "region: cn-hangzhou\nidentity: {auth_token: '<your-auth-token>'}\n"
			if secret {
				body = "identity: {auth_token: synthetic-private-value}\n"
			}
			if err := os.WriteFile(path, []byte(body), mode); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DATA_AGENT_CONFIG", path)
			_, _, err := Load()
			if (err != nil) != (secret && mode == 0644) {
				t.Fatalf("mode %o secret=%v err=%v", mode, secret, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-private-value") {
				t.Fatal("secret leaked")
			}
			info, _ := os.Stat(path)
			if info.Mode().Perm() != mode {
				t.Fatal("loader changed file permissions")
			}
		}
	}
}

func TestSecretFileAliasesSymlinksAndDirectories(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	body := "value: &key synthetic-private-value\nidentity: {jwt: {secret: *key}}\n"
	if err := os.WriteFile(target, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", target)
	if _, _, err := Load(); err == nil {
		t.Fatal("alias secret bypassed checks")
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", link)
	if _, _, err := Load(); err == nil {
		t.Fatal("symlink config accepted")
	}
	t.Setenv("DATA_AGENT_CONFIG", dir)
	if _, _, err := Load(); err == nil {
		t.Fatal("directory config accepted")
	}
}

func TestSecretPlaceholderAndAliasBoundaries(t *testing.T) {
	for _, text := range []string{
		"IDENTITY_JWT_SECRETS=${KEY_A},synthetic-secret,${KEY_B}",
		"IDENTITY_AUTH_TOKEN=synthetic-secret\nIDENTITY_AUTH_TOKEN=<placeholder>",
		"IDENTITY_AUTH_TOKEN=${KEY_A}synthetic-secret${KEY_B}",
	} {
		if !dotenvContainsSecret([]byte(text)) {
			t.Fatal("placeholder wrapper hid actual secret")
		}
	}
	for _, text := range []string{"IDENTITY_JWT_SECRETS=${KEY_A},${KEY_B}", "IDENTITY_AUTH_TOKEN=<your-auth-token>"} {
		if dotenvContainsSecret([]byte(text)) {
			t.Fatal("public placeholders classified as secrets")
		}
	}
	if !yamlContainsSecret([]byte("key: &key secret\nidentity: {jwt: {*key : synthetic-secret}}")) {
		t.Fatal("aliased secret key escaped detection")
	}
}

func TestDotEnvSecretCheckBeforeEnvironmentMutation(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.env")
	body := "IDENTITY_AUTH_TOKEN=synthetic-private-dotenv\nDATA_AGENT_REGION=cn-test\n"
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_ENV_FILE", path)
	if err := LoadDotEnv(); err == nil || strings.Contains(err.Error(), "synthetic-private-dotenv") {
		t.Fatalf("dotenv rejection: %v", err)
	}
	if os.Getenv("DATA_AGENT_REGION") == "cn-test" {
		t.Fatal("unsafe dotenv partially installed")
	}
	if runtime.GOOS == "windows" {
		return
	}
	// chmod applies only to this synthetic fixture, never deployment secrets.
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("IDENTITY_AUTH_TOKEN")
	os.Unsetenv("DATA_AGENT_REGION")
	if err := LoadDotEnv(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("IDENTITY_AUTH_TOKEN") != "synthetic-private-dotenv" {
		t.Fatal("private dotenv not loaded")
	}
}
