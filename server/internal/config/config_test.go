package config

import (
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
		"DATA_AGENT_CONFIG", "DATA_AGENT_ENV_FILE", "BUDDY_REGION", "DOTENV_UNSAFE_FILES_ALLOW",
		"DATA_AGENT_REGION", "DATA_AGENT_DMS_UNIT", "DATA_AGENT_WORKSPACE_ID",
		"DATA_AGENT_SESSIONS_DIR", "DATA_AGENT_API_KEY", "AILY_SHARED_SECRET",
		"DATA_AGENT_API_KEY_ENDPOINT", "DATA_AGENT_API_KEY_STREAM_ENDPOINT",
		"DATA_AGENT_ENDPOINT", "DATA_AGENT_DMS_ENTERPRISE_ENDPOINT", "DATA_AGENT_CUSTOM_AGENT_ID",
		"IDENTITY_SHARED_SECRET", "IDENTITY_AUTH_TOKEN", "IDENTITY_JWT_SECRET", "IDENTITY_JWT_SECRETS",
		"IDENTITY_SESSION_NAME_PREFIX", "DATA_AGENT_LOG_REQUESTS", "MCP_TRANSPORT",
		"DATA_AGENT_LOG", "DATA_AGENT_LOG_FILE", "DATA_AGENT_LOG_MAX_BYTES",
		"DATA_AGENT_PERF_LOG", "DATA_AGENT_PERF_LOG_FILE", "DATA_AGENT_PERF_LOG_INTERVAL",
	} {
		t.Setenv(k, "")
	}
	os.Unsetenv("IDENTITY_SESSION_NAME_PREFIX")
	os.Unsetenv("DATA_AGENT_LOG_FILE")
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

func TestLogConfig(t *testing.T) {
	clearEnv(t)
	defaultFile := filepath.Join(homeDir(), ".data-agent", "server.log")
	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Enabled || cfg.Log.File != defaultFile || cfg.Log.MaxBytes != 64<<20 {
		t.Fatalf("unexpected log defaults: %+v", cfg.Log)
	}

	t.Setenv("DATA_AGENT_LOG", "1")
	cfg, _, err = Load()
	if err != nil || !cfg.Log.Enabled || cfg.Log.File != defaultFile || cfg.Log.MaxBytes != 64<<20 {
		t.Fatalf("flag alone must enable default file logging: %+v, %v", cfg.Log, err)
	}
	t.Setenv("DATA_AGENT_LOG", "")
	writeConfig(t, "log:\n  enabled: true\n  file: ~/server-from-yaml.log\n  max_bytes: 1024\n")
	cfg, _, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Log.Enabled || cfg.Log.File != filepath.Join(homeDir(), "server-from-yaml.log") || cfg.Log.MaxBytes != 1024 {
		t.Fatalf("yaml log section not applied: %+v", cfg.Log)
	}

	t.Setenv("DATA_AGENT_LOG", "0")
	t.Setenv("DATA_AGENT_LOG_FILE", "~/server-from-env.log")
	t.Setenv("DATA_AGENT_LOG_MAX_BYTES", "2048")
	cfg, _, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Enabled || cfg.Log.File != filepath.Join(homeDir(), "server-from-env.log") || cfg.Log.MaxBytes != 2048 {
		t.Fatalf("env override not applied: %+v", cfg.Log)
	}

	t.Setenv("DATA_AGENT_LOG", "1")
	t.Setenv("DATA_AGENT_LOG_FILE", "")
	cfg, _, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Log.Enabled || cfg.Log.File != defaultFile || cfg.Log.MaxBytes != 2048 {
		t.Fatalf("empty file must use the default path without disabling logging: %+v", cfg.Log)
	}
}

func TestLogEnabledEnvironment(t *testing.T) {
	for _, tc := range []struct {
		value   string
		enabled bool
	}{
		{"1", true}, {"true", true}, {"ON", true}, {" True ", true},
		{"0", false}, {"false", false}, {"OFF", false}, {" False ", false},
	} {
		t.Run(tc.value, func(t *testing.T) {
			clearEnv(t)
			writeConfig(t, fmt.Sprintf("log:\n  enabled: %t\n", !tc.enabled))
			t.Setenv("DATA_AGENT_LOG", tc.value)
			cfg, _, err := Load()
			if err != nil || cfg.Log.Enabled != tc.enabled {
				t.Fatalf("flag override: enabled=%t error=%v", cfg.Log.Enabled, err)
			}
		})
	}
	for _, value := range []string{"2", "invalid-sensitive-flag", "   "} {
		t.Run("invalid/"+value, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("DATA_AGENT_LOG", value)
			if _, _, err := Load(); err == nil || strings.Contains(err.Error(), value) {
				t.Fatalf("expected sanitized flag error, got %v", err)
			}
		})
	}
}

func TestLogFileDoesNotEnableLogging(t *testing.T) {
	clearEnv(t)
	writeConfig(t, "log:\n  file: ~/custom.log\n")
	cfg, _, err := Load()
	if err != nil || cfg.Log.Enabled {
		t.Fatalf("path alone must not enable file logging: %+v, %v", cfg.Log, err)
	}
	t.Setenv("DATA_AGENT_LOG_FILE", "~/custom-env.log")
	cfg, _, err = Load()
	if err != nil || cfg.Log.Enabled {
		t.Fatalf("environment path alone must not enable file logging: %+v, %v", cfg.Log, err)
	}
}

func TestLogMaxBytesValidation(t *testing.T) {
	for _, source := range []string{"yaml", "env"} {
		for _, tc := range []struct {
			name, value string
			want        int64
		}{
			{"zero", "0", 0},
			{"negative", "-1", 0},
			{"string", "sensitive-invalid-limit", 0},
			{"quoted integer", "\"1024\"", 0},
			{"overflow", "9223372036854775808", 0},
			{"underflow", "-9223372036854775809", 0},
			{"minimum", "1", 1},
			{"maximum", "9223372036854775807", 1<<63 - 1},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				clearEnv(t)
				if source == "yaml" {
					writeConfig(t, "log:\n  max_bytes: "+tc.value+"\n")
				} else {
					writeConfig(t, "log:\n  max_bytes: 1024\n")
					t.Setenv("DATA_AGENT_LOG_MAX_BYTES", tc.value)
				}
				cfg, _, err := Load()
				if tc.want == 0 {
					if err == nil || strings.Contains(err.Error(), strings.Trim(tc.value, "\"")) {
						t.Fatalf("expected sanitized invalid limit error, got %v", err)
					}
				} else if err != nil || cfg.Log.MaxBytes != tc.want {
					t.Fatalf("max_bytes = %d, want %d; error: %v", cfg.Log.MaxBytes, tc.want, err)
				}
			})
		}
	}
}

func TestLogPerfPathConflicts(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sensitive-log-path.log")
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(dir, file)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, file, perfFile string
		enabled, conflict    bool
	}{
		{"same file", file, file, true, true},
		{"normalized file", file + "/../sensitive-log-path.log", file, true, true},
		{"relative file", relative, file, true, true},
		{"server backup", file, file + ".1", true, true},
		{"perf backup", file + ".1", file, true, true},
		{"normalized server backup", relative, file + "/../sensitive-log-path.log.1", true, true},
		{"normalized perf backup", file + "/../sensitive-log-path.log.1", relative, true, true},
		{"expanded home", "~/sensitive-log-path.log", filepath.Join(homeDir(), "sensitive-log-path.log"), true, true},
		{"default perf file", "~/.data-agent/perf.log", "", true, true},
		{"different files", file, file + ".perf", true, false},
		{"same file disabled", file, file, false, false},
		{"backup disabled", file + ".1", file, false, false},
		{"default server file", "", file, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			writeConfig(t, fmt.Sprintf("log:\n  enabled: true\n  file: %q\n  perf:\n    enabled: %v\n    file: %q\n", tc.file, tc.enabled, tc.perfFile))
			cfg, _, err := Load()
			if tc.conflict {
				if err == nil || err.Error() != "log.file and log.perf.file must not overlap, including rotation backups" {
					t.Fatalf("expected sanitized path conflict error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			} else {
				wantFile := expandHome(tc.file)
				if wantFile == "" {
					wantFile = filepath.Join(homeDir(), ".data-agent", "server.log")
				}
				if !cfg.Log.Enabled || cfg.Log.File != wantFile || cfg.Log.Perf.Enabled != tc.enabled || cfg.Log.MaxBytes != DefaultLogMaxBytes {
					t.Fatalf("unexpected log configuration: %+v", cfg.Log)
				}
			}
		})
	}
}

func TestLogAndPerfSwitchesAreIndependent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, perfEnabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("log=%t/perf=%t", enabled, perfEnabled), func(t *testing.T) {
				clearEnv(t)
				writeConfig(t, fmt.Sprintf("log:\n  enabled: %t\n  perf:\n    enabled: %t\n    file: ~/.data-agent/server.log\n", enabled, perfEnabled))
				cfg, _, err := Load()
				if enabled && perfEnabled {
					if err == nil || !strings.Contains(err.Error(), "must not overlap") {
						t.Fatalf("enabled default log path must be protected: %v", err)
					}
				} else if err != nil || cfg.Log.Enabled != enabled || cfg.Log.Perf.Enabled != perfEnabled {
					t.Fatalf("disabled output must not conflict: %+v, %v", cfg.Log, err)
				}
			})
		}
	}
}

func TestLogPerfHardLinkConflicts(t *testing.T) {
	for _, tc := range []struct{ serverSuffix, perfSuffix string }{{"", ""}, {".1", ""}, {"", ".1"}, {".1", ".1"}} {
		t.Run(tc.serverSuffix+"/"+tc.perfSuffix, func(t *testing.T) {
			clearEnv(t)
			dir := t.TempDir()
			serverFile, perfFile := filepath.Join(dir, "server.log"), filepath.Join(dir, "perf.log")
			if err := os.WriteFile(serverFile+tc.serverSuffix, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(serverFile+tc.serverSuffix, perfFile+tc.perfSuffix); err != nil {
				t.Fatal(err)
			}
			writeConfig(t, fmt.Sprintf("log:\n  enabled: true\n  file: %q\n  perf:\n    enabled: true\n    file: %q\n", serverFile, perfFile))
			if _, _, err := Load(); err == nil || err.Error() != "log.file and log.perf.file must not overlap, including rotation backups" {
				t.Fatalf("hard-linked logs must be rejected: %v", err)
			}
			data, err := os.ReadFile(serverFile + tc.serverSuffix)
			if err != nil || string(data) != "unchanged" {
				t.Fatal("validation modified the existing log")
			}
		})
	}
}

func TestLogPerfDirectoryAliasConflicts(t *testing.T) {
	for _, tc := range []struct{ subdir, suffix string }{{"", ""}, {"future/nested", ""}, {"future/nested", ".1"}} {
		t.Run(tc.subdir+tc.suffix, func(t *testing.T) {
			clearEnv(t)
			dir := t.TempDir()
			real, alias := filepath.Join(dir, "real"), filepath.Join(dir, "alias")
			if err := os.Mkdir(real, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, alias); err != nil {
				if os.IsPermission(err) {
					t.Skip("symlink creation not permitted")
				}
				t.Fatal(err)
			}
			serverFile := filepath.Join(real, tc.subdir, "server.log")
			perfFile := filepath.Join(alias, tc.subdir, "server.log") + tc.suffix
			writeConfig(t, fmt.Sprintf("log:\n  enabled: true\n  file: %q\n  perf:\n    enabled: true\n    file: %q\n", serverFile, perfFile))
			if _, _, err := Load(); err == nil || err.Error() != "log.file and log.perf.file must not overlap, including rotation backups" {
				t.Fatalf("aliased log paths must be rejected: %v", err)
			}
		})
	}
}

func TestLogPerfDanglingAliasRejected(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	serverFile, perfFile := filepath.Join(dir, "server.log"), filepath.Join(dir, "perf.log")
	if err := os.Symlink(serverFile, perfFile); err != nil {
		if os.IsPermission(err) {
			t.Skip("symlink creation not permitted")
		}
		t.Fatal(err)
	}
	writeConfig(t, fmt.Sprintf("log:\n  enabled: true\n  file: %q\n  perf:\n    enabled: true\n    file: %q\n", serverFile, perfFile))
	if _, _, err := Load(); err == nil || err.Error() != "invalid log file path" {
		t.Fatalf("dangling log alias must be rejected: %v", err)
	}
}

func TestPerfLogConfig(t *testing.T) {
	clearEnv(t)
	cfg, _, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Perf.Enabled || !strings.HasSuffix(cfg.Log.Perf.File, filepath.Join(".data-agent", "perf.log")) {
		t.Fatalf("perf log should be off with a default path: %+v", cfg.Log.Perf)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("log:\n  perf:\n    enabled: true\n    file: ~/perf-from-yaml.log\n    interval_seconds: 5\n    max_bytes: 1024\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", path)
	cfg, _, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Log.Perf.Enabled || cfg.Log.Perf.IntervalSeconds != 5 || cfg.Log.Perf.MaxBytes != 1024 || strings.HasPrefix(cfg.Log.Perf.File, "~") {
		t.Fatalf("yaml perf section not applied: %+v", cfg.Log.Perf)
	}

	t.Setenv("DATA_AGENT_PERF_LOG", "false")
	t.Setenv("DATA_AGENT_PERF_LOG_FILE", filepath.Join(dir, "env.log"))
	t.Setenv("DATA_AGENT_PERF_LOG_INTERVAL", "7")
	cfg, _, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Log.Perf.Enabled || cfg.Log.Perf.File != filepath.Join(dir, "env.log") || cfg.Log.Perf.IntervalSeconds != 7 {
		t.Fatalf("env override not applied: %+v", cfg.Log.Perf)
	}

	t.Setenv("DATA_AGENT_PERF_LOG_INTERVAL", "0")
	if _, _, err := Load(); err == nil {
		t.Fatal("non-positive interval must be rejected")
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

func TestLoadLegacyAuthentication(t *testing.T) {
	for _, withAK := range []bool{false, true} {
		for _, source := range []string{"yaml", "env"} {
			t.Run(fmt.Sprintf("%s/ak=%t", source, withAK), func(t *testing.T) {
				clearEnv(t)
				setTestAK(t, withAK)
				writeConfig(t, "api_key: synthetic-yaml-key\napi_key_endpoint: control.example\napi_key_stream_endpoint: stream.example\n")
				want := "synthetic-yaml-key"
				if source == "env" {
					want = "synthetic-env-key"
					t.Setenv("DATA_AGENT_API_KEY", want)
					t.Setenv("DATA_AGENT_API_KEY_ENDPOINT", "env-control.example")
					t.Setenv("DATA_AGENT_API_KEY_STREAM_ENDPOINT", "env-stream.example")
				}
				cfg, _, err := Load()
				if err != nil || cfg.APIKey != want {
					t.Fatal("legacy API Key not loaded")
				}
				if source == "env" && (cfg.APIKeyEndpoint != "env-control.example" || cfg.APIKeyStreamEndpoint != "env-stream.example") {
					t.Fatal("endpoint environment override not applied")
				}
			})
		}
	}
	for _, body := range []string{
		"api_key: ''\napi_key_endpoint: null\napi_key_stream_endpoint: old-stream.example\n",
		"api_key: null\napi_key_endpoint: old-control.example\n",
	} {
		clearEnv(t)
		writeConfig(t, body+"identity:\n  enabled: true\n  auth_token: synthetic-platform\n  default: {role_arn: synthetic-role}\n")
		t.Setenv("MCP_TRANSPORT", "streamable-http")
		cfg, _, err := Load()
		if err != nil || cfg.APIKey != "" {
			t.Fatalf("unused legacy configuration must not prevent AK/STS startup: %v", err)
		}
	}
}

func TestAPIKeyDoesNotDowngradeIdentityOrGateway(t *testing.T) {
	for _, body := range []string{
		"identity: {enabled: true, auth_token: synthetic-platform, default: {role_arn: synthetic-role}}\n",
		"agent_gateway: {enabled: true, endpoint: dms.cn-hangzhou.aliyuncs.com}\n",
	} {
		clearEnv(t)
		writeConfig(t, "api_key: synthetic-sensitive-api-key\n"+body)
		if _, _, err := Load(); err == nil || strings.Contains(err.Error(), "synthetic-sensitive-api-key") {
			t.Fatalf("expected sanitized incompatible credential error, got %v", err)
		}
	}
	for _, transport := range []string{"sse", "streamable-http"} {
		clearEnv(t)
		writeConfig(t, "api_key: synthetic-sensitive-api-key\n")
		t.Setenv("MCP_TRANSPORT", transport)
		if _, _, err := Load(); err == nil || strings.Contains(err.Error(), "synthetic-sensitive-api-key") {
			t.Fatalf("remote API Key must not bypass identity: %v", err)
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
	for _, tc := range []struct{ body, key string }{
		{"legacy: &old {api_key: synthetic-secret}\n<<: *old\n", "synthetic-secret"},
		{"legacy: &old {api_key_endpoint: null}\n<<: [*old, {region: cn-hangzhou}]\n", ""},
		{"legacy: &old {api_key_stream_endpoint: ''}\nintermediate: &middle {<<: *old}\n<<: *middle\n", ""},
		{"key: &old api_key\n*old : null\n", ""},
		{"legacy: &old {api_key: synthetic-secret}\n<<: *old\napi_key: null\n", ""},
		{`{"api_key": null}`, ""},
		{"extension: {api_key: not-a-top-level-key}\n", ""},
	} {
		clearEnv(t)
		writeConfig(t, tc.body)
		cfg, _, err := Load()
		if err != nil || cfg.APIKey != tc.key {
			t.Fatalf("legacy YAML merge not decoded: %v", err)
		}
	}
	for _, body := range []string{
		"region: cn-hangzhou\n---\napi_key: synthetic-secret\n",
		"&root {<<: *root, api_key: synthetic-secret}\n",
		"api_key: {secret: synthetic-secret}\n",
		"api_key: [synthetic-secret]\n",
	} {
		clearEnv(t)
		writeConfig(t, body)
		if _, _, err := Load(); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("expected sanitized malformed config error, got %v", err)
		}
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
