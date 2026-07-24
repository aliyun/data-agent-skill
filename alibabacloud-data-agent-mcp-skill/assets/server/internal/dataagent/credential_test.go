package dataagent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromAliyunConfigReadsSTSToken(t *testing.T) {
	writeAliyunConfig(t, `{
  "current": "sts",
  "profiles": [
    {
      "name": "default",
      "mode": "AK",
      "access_key_id": "default-ak",
      "access_key_secret": "default-secret"
    },
    {
      "name": "sts",
      "mode": "StsToken",
      "access_key_id": "sts-ak",
      "access_key_secret": "sts-secret",
      "sts_token": "sts-token-value"
    }
  ]
}`)

	cred, ok := loadFromAliyunConfig()
	if !ok {
		t.Fatal("loadFromAliyunConfig() ok = false, want true")
	}
	if cred.AccessKeyID != "sts-ak" {
		t.Fatalf("AccessKeyID = %q, want sts-ak", cred.AccessKeyID)
	}
	if cred.AccessKeySecret != "sts-secret" {
		t.Fatalf("AccessKeySecret = %q, want sts-secret", cred.AccessKeySecret)
	}
	if cred.SecurityToken != "sts-token-value" {
		t.Fatalf("SecurityToken = %q, want sts-token-value", cred.SecurityToken)
	}
}

func TestLoadFromAliyunConfigReadsSecurityTokenAlias(t *testing.T) {
	writeAliyunConfig(t, `{
  "current": "default",
  "profiles": [
    {
      "name": "default",
      "mode": "StsToken",
      "access_key_id": "ak",
      "access_key_secret": "secret",
      "security_token": "security-token-value"
    }
  ]
}`)

	cred, ok := loadFromAliyunConfig()
	if !ok {
		t.Fatal("loadFromAliyunConfig() ok = false, want true")
	}
	if cred.SecurityToken != "security-token-value" {
		t.Fatalf("SecurityToken = %q, want security-token-value", cred.SecurityToken)
	}
}

func writeAliyunConfig(t *testing.T, content string) {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".aliyun")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
