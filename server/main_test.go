package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteStartupRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		mode os.FileMode
	}{
		{"disabled identity", "region: cn-hangzhou\n", 0600},
		{"missing namespace", "identity:\n  enabled: true\n  require_identity: true\n  auth_token: synthetic-startup-secret\n", 0600},
		{"unsafe secret file", "identity:\n  enabled: true\n  require_identity: true\n  namespace: startup-test\n  auth_token: synthetic-startup-secret\n", 0644},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DATA_AGENT_CONFIG", path)
			t.Setenv("MCP_TRANSPORT", "streamable-http")
			t.Setenv("IDENTITY_AUTH_TOKEN", "")
			t.Setenv("IDENTITY_ENABLED", "")
			t.Setenv("IDENTITY_NAMESPACE", "")
			err := run()
			if err == nil || !strings.Contains(err.Error(), "configuration error") {
				t.Fatalf("must reject before credential initialization: %v", err)
			}
			if strings.Contains(err.Error(), "synthetic-startup-secret") {
				t.Fatal("startup error exposed secret")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != tc.mode {
				t.Fatal("startup must not silently change permissions")
			}
		})
	}
}
