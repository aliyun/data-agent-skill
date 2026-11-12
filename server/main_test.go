package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/logging"
)

func TestLoggingSubprocess(t *testing.T) {
	if os.Getenv("MCP_LOG_TEST_HELPER") != "1" {
		return
	}
	main()
	os.Exit(0)
}

func loggingCommand(t *testing.T, dir string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestLoggingSubprocess$")
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"MCP_LOG_TEST_HELPER=1",
		"MCP_TRANSPORT=stdio",
		"DATA_AGENT_CONFIG=" + filepath.Join(dir, "missing.yaml"),
		"DATA_AGENT_ENV_FILE=" + filepath.Join(dir, "missing.env"),
		"DATA_AGENT_SESSIONS_DIR=" + filepath.Join(dir, "sessions"),
		"DATA_AGENT_LOG_REQUESTS=basic",
		"ALIBABA_CLOUD_ACCESS_KEY_ID=synthetic-log-test-ak",
		"ALIBABA_CLOUD_ACCESS_KEY_SECRET=synthetic-log-test-sk",
	}
	return cmd
}

func TestStdioLoggingDestinationAndRotation(t *testing.T) {
	for _, tc := range []struct {
		name, flag             string
		customPath, fileOutput bool
	}{
		{"stderr default", "", false, false},
		{"custom path alone", "", true, false},
		{"disabled custom path", "0", true, false},
		{"enabled default path", "1", false, true},
		{"enabled custom path", "true", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, ".data-agent", "server.log")
			perfPath := filepath.Join(dir, "perf.log")
			cmd := loggingCommand(t, dir)
			cmd.Env = append(cmd.Env, "DATA_AGENT_LOG="+tc.flag)
			if tc.customPath {
				logPath = filepath.Join(dir, "server.log")
				cmd.Env = append(cmd.Env, "DATA_AGENT_LOG_FILE="+logPath)
			}
			if tc.fileOutput {
				cmd.Env = append(cmd.Env, "DATA_AGENT_LOG_MAX_BYTES=1024",
					"DATA_AGENT_PERF_LOG=1", "DATA_AGENT_PERF_LOG_FILE="+perfPath)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
			decoder := json.NewDecoder(stdout)
			request := func(body string) {
				t.Helper()
				if _, err := io.WriteString(stdin, body+"\n"); err != nil {
					t.Fatal(err)
				}
				var response struct {
					JSONRPC string          `json:"jsonrpc"`
					Error   json.RawMessage `json:"error"`
					Result  struct {
						IsError bool `json:"isError"`
					} `json:"result"`
				}
				if err := decoder.Decode(&response); err != nil {
					t.Fatalf("MCP stdout is not a JSON response: %v", err)
				}
				if response.JSONRPC != "2.0" || len(response.Error) != 0 || response.Result.IsError {
					t.Fatal("MCP request failed")
				}
			}
			request(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"log-test","version":"1"}}}`)
			for i := 1; i <= 16; i++ {
				request(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"data_agent_list_sessions","arguments":{}}}`, i))
			}
			_ = stdin.Close()
			if err := cmd.Wait(); err != nil {
				t.Fatalf("server exit: %v; stderr=%s", err, stderr.String())
			}
			if !tc.fileOutput {
				if !strings.Contains(stderr.String(), "data_agent_list_sessions") {
					t.Fatal("default request logs must go to stderr")
				}
				if _, err := os.Stat(logPath); !os.IsNotExist(err) {
					t.Fatal("unconfigured file log was created")
				}
				return
			}
			if stderr.Len() != 0 {
				t.Fatalf("file logs leaked to stderr: %s", stderr.String())
			}
			paths := []string{logPath}
			entries, err := os.ReadDir(filepath.Dir(logPath))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if logging.IsBackup(filepath.Base(logPath), entry.Name()) {
					paths = append(paths, filepath.Join(filepath.Dir(logPath), entry.Name()))
				}
			}
			if len(paths) < 2 || len(paths) > 8 {
				t.Fatal("expected current file and at most seven dated backups", paths)
			}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if len(data) > 1024 || !bytes.Contains(data, []byte("data_agent_list_sessions")) {
					t.Fatalf("missing request logs or invalid rotated size: %d", len(data))
				}
				if bytes.Contains(data, []byte("synthetic-log-test-")) {
					t.Fatal("log exposed credentials")
				}
			}
			data, err := os.ReadFile(perfPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
				var record map[string]any
				if err := json.Unmarshal(line, &record); err != nil || record["runtime"] == nil {
					t.Fatal("ordinary logging corrupted the independent perf log")
				}
			}
		})
	}
}

func TestFileLoggingCapturesStartupFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sessions"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, ".data-agent", "server.log")
	cmd := loggingCommand(t, dir)
	cmd.Env = append(cmd.Env, "DATA_AGENT_LOG=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("startup failure must produce a nonzero exit")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("session storage initialization failed")) || !strings.Contains(stderr.String(), "session storage initialization failed") {
		t.Fatal("startup failure must reach the log file and stderr")
	}
	if stdout.Len() != 0 {
		t.Fatal("startup diagnostics contaminated protocol stdout")
	}
}

func TestLegacyAPIKeyStartupWithoutSDKCredentials(t *testing.T) {
	dir := t.TempDir()
	cmd := loggingCommand(t, dir)
	cmd.Env = append(cmd.Env, "ALIBABA_CLOUD_ACCESS_KEY_ID=", "ALIBABA_CLOUD_ACCESS_KEY_SECRET=",
		"DATA_AGENT_API_KEY=synthetic-private-api-key", "DATA_AGENT_LOG=1", "DEBUG=credential")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("legacy API Key startup failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sessions")); err != nil {
		t.Fatal("API Key startup did not initialize the session manager")
	}
	data, err := os.ReadFile(filepath.Join(dir, ".data-agent", "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("synthetic-private-api-key")) || bytes.Contains(stderr.Bytes(), []byte("synthetic-private-api-key")) {
		t.Fatal("startup exposed API Key")
	}
	if stdout.Len() != 0 {
		t.Fatal("startup contaminated stdout")
	}
}

func TestRemoteStartupRejectsUnsafeConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		mode os.FileMode
	}{
		{"disabled identity", "region: cn-hangzhou\n", 0600},
		{"missing role mapping", "identity:\n  enabled: true\n  require_identity: true\n  auth_token: synthetic-startup-secret\n", 0600},
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
