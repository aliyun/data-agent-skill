//go:build !windows

package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type syntheticFileInfo struct {
	mode os.FileMode
	uid  uint32
}

func (i syntheticFileInfo) Name() string       { return "synthetic" }
func (i syntheticFileInfo) Size() int64        { return 0 }
func (i syntheticFileInfo) Mode() os.FileMode  { return i.mode }
func (i syntheticFileInfo) ModTime() time.Time { return time.Time{} }
func (i syntheticFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i syntheticFileInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

func TestLegacyReadableSecretFileBoundaries(t *testing.T) {
	uid := uint32(os.Geteuid())
	for _, owner := range []uint32{0, uid} {
		for _, mode := range []os.FileMode{0400, 0600, 0640, 0644} {
			if !legacyReadableSecretFile(syntheticFileInfo{mode: mode, uid: owner}) {
				t.Fatalf("legacy readable file rejected: %o", mode)
			}
		}
	}
	for _, mode := range []os.FileMode{0666, 0620, 0602, 0700, os.ModeNamedPipe | 0600} {
		if legacyReadableSecretFile(syntheticFileInfo{mode: mode, uid: uid}) {
			t.Fatalf("writable/executable/nonregular file accepted: %o", mode)
		}
	}
	if legacyReadableSecretFile(syntheticFileInfo{mode: 0644, uid: uid + 1}) {
		t.Fatal("untrusted file owner accepted")
	}
}

func TestLegacyReadableConfigAndDotEnv(t *testing.T) {
	for _, dotenv := range []bool{false, true} {
		for _, allow := range []string{"", "0", "1"} {
			clearEnv(t)
			t.Setenv("DOTENV_UNSAFE_FILES_ALLOW", allow)
			path := filepath.Join(t.TempDir(), "legacy-config")
			body := "identity: {auth_token: synthetic-legacy-secret}\n"
			if dotenv {
				body = "IDENTITY_AUTH_TOKEN=synthetic-legacy-secret\n"
			}
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			var err error
			if dotenv {
				t.Setenv("DATA_AGENT_ENV_FILE", path)
				err = LoadDotEnv()
			} else {
				t.Setenv("DATA_AGENT_CONFIG", path)
				_, _, err = Load()
			}
			log.SetOutput(previous)
			if (err == nil) != (allow == "1") {
				t.Fatalf("dotenv=%v allow=%q error=%v", dotenv, allow, err)
			}
			if allow == "1" && !strings.Contains(output.String(), "warning:") {
				t.Fatal("missing permission warning")
			}
			if strings.Contains(output.String(), "synthetic-legacy-secret") {
				t.Fatal("permission warning leaked secret")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0644 {
				t.Fatal("compatibility must not change file permissions")
			}
		}
	}
}

func TestDotEnvCannotEnablePermissionException(t *testing.T) {
	for _, flag := range []string{"", "0", "1"} {
		t.Run("process_flag="+flag, func(t *testing.T) {
			clearEnv(t)
			if flag == "" {
				os.Unsetenv("DOTENV_UNSAFE_FILES_ALLOW")
			} else {
				t.Setenv("DOTENV_UNSAFE_FILES_ALLOW", flag)
			}
			os.Unsetenv("DATA_AGENT_REGION")
			dir := t.TempDir()
			envPath, configPath := filepath.Join(dir, "legacy.env"), filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(envPath, []byte("DOTENV_UNSAFE_FILES_ALLOW=1\nDATA_AGENT_REGION=cn-shanghai\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(configPath, []byte("identity: {auth_token: synthetic-private-value}\n"), 0644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DATA_AGENT_ENV_FILE", envPath)
			t.Setenv("DATA_AGENT_CONFIG", configPath)
			if err := LoadDotEnv(); err != nil {
				t.Fatal(err)
			}
			if os.Getenv("DOTENV_UNSAFE_FILES_ALLOW") != flag {
				t.Fatal("dotenv changed the process-only permission exception")
			}
			if os.Getenv("DATA_AGENT_REGION") != "cn-shanghai" {
				t.Fatal("normal dotenv values were not loaded")
			}
			if _, _, err := Load(); (err == nil) != (flag == "1") {
				t.Fatalf("permission exception must depend on initial process environment: %v", err)
			}
		})
	}
}

func TestLegacyPermissionFlagDoesNotBypassIntegrity(t *testing.T) {
	clearEnv(t)
	t.Setenv("DOTENV_UNSAFE_FILES_ALLOW", "1")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("identity: {auth_token: synthetic-secret}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", path)
	for _, mode := range []os.FileMode{0666, 0620, 0700} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Load(); err == nil {
			t.Fatalf("unsafe file permissions accepted: %o", mode)
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_AGENT_CONFIG", link)
	if _, _, err := Load(); err == nil {
		t.Fatal("symlink accepted")
	}
	t.Setenv("DATA_AGENT_CONFIG", path)
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if _, _, err := Load(); err == nil {
		t.Fatal("untrusted writable directory accepted")
	}
}

func TestLoadAuthToken(t *testing.T) {
	t.Setenv("DOTENV_UNSAFE_FILES_ALLOW", "0")
	t.Setenv("IDENTITY_AUTH_TOKEN", "synthetic-server-environment-token")
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"yaml", "identity:\n  auth_token: 'synthetic-token: #quoted'\n  jwt:\n    secret: synthetic-signing-secret\n", "synthetic-token: #quoted"},
		{"json", `{"identity":{"auth_token":"synthetic-json-token"}}`, "synthetic-json-token"},
		{"missing", "identity: {}\n", ""},
		{"empty", "identity: {auth_token: ''}\n", ""},
		{"blank", "identity: {auth_token: '  '}\n", ""},
		{"null", "identity: {auth_token: null}\n", ""},
		{"other credentials", "api_key: synthetic-api-key\nidentity: {jwt: {secret: synthetic-signing-secret}}\n", ""},
		{"wrong type", "identity: {auth_token: [synthetic-hidden-value]}\n", ""},
		{"malformed", "identity: {auth_token: [synthetic-hidden-value\n", ""},
		{"multiple documents", "identity: {auth_token: synthetic-hidden-value}\n---\nidentity: {}\n", ""},
		{"cyclic alias", "identity: &loop {auth_token: *loop}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := LoadAuthToken(path)
			if (err == nil) != (tc.want != "") || got != tc.want {
				t.Fatalf("unexpected token result: error=%v", err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-") {
				t.Fatal("configuration error leaked a credential")
			}
		})
	}
}

func TestLoadAuthTokenFileSecurity(t *testing.T) {
	t.Setenv("DOTENV_UNSAFE_FILES_ALLOW", "0")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if _, err := LoadAuthToken(path); err == nil || err.Error() != "client configuration file not found" {
		t.Fatalf("missing file: %v", err)
	}
	if err := os.WriteFile(path, []byte("identity: {auth_token: synthetic-file-token}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	if token, err := LoadAuthToken("~/config.yaml"); err != nil || token != "synthetic-file-token" {
		t.Fatalf("home-relative path: %v", err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthToken(link); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthToken(path); err == nil {
		t.Fatal("publicly readable token accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if _, err := LoadAuthToken(path); err == nil {
		t.Fatal("untrusted writable directory accepted")
	}
}

func TestSecretFileOwnerChecksWithoutChangingOwnership(t *testing.T) {
	uid := uint32(os.Geteuid())
	for _, mode := range []os.FileMode{0600, 0400} {
		if !privateSecretFile(syntheticFileInfo{mode: mode, uid: uid}) {
			t.Fatal("private owned file rejected")
		}
		if privateSecretFile(syntheticFileInfo{mode: mode, uid: uid + 1}) {
			t.Fatal("different owner accepted")
		}
	}
	for _, mode := range []os.FileMode{0644, 0620, 0601, 0700, os.ModeNamedPipe | 0600} {
		if privateSecretFile(syntheticFileInfo{mode: mode, uid: uid}) {
			t.Fatal("unsafe synthetic file accepted")
		}
	}
}
