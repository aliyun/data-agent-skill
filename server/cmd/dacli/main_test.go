package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPrettyPreservesJSONNumbers(t *testing.T) {
	input := `{"data":[{"ID":9007199254740993,"Amount":0.123456789012345678901,"Exponent":1e+30,"Description":null}]}`
	output := pretty(input)
	if !json.Valid([]byte(output)) || !strings.Contains(output, "\n") {
		t.Fatalf("expected formatted JSON: %q", output)
	}
	for _, literal := range []string{"9007199254740993", "0.123456789012345678901", "1e+30", "null"} {
		if !strings.Contains(output, literal) {
			t.Fatalf("lost literal %s: %s", literal, output)
		}
	}
}

func TestPrettyPreservesNonJSONText(t *testing.T) {
	for _, input := range []string{"not JSON", "", `{"unfinished":`, `{} {}`} {
		if got := pretty(input); got != input {
			t.Fatalf("pretty(%q) = %q", input, got)
		}
	}
}

func TestAuthenticatedProtocolLifecycle(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-aily-token") != "synthetic-token" || r.Header.Get("x-aily-jwt") != "synthetic-jwt" {
			t.Error("all protocol requests require credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("x-aily-user") != "" || r.Header.Get("x-aily-email") != "" {
			t.Error("JWT mode must not also send conflicting plain identity headers")
		}
		if r.Method == http.MethodDelete {
			methods = append(methods, "DELETE")
			if r.Header.Get("Mcp-Session-Id") != "synthetic-session" {
				t.Error("DELETE missing issued session")
			}
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		method, _ := body["method"].(string)
		methods = append(methods, method)
		if method == "initialize" {
			if r.Header.Get("Mcp-Session-Id") != "" {
				t.Error("initialize must not reuse an issued ID")
			}
			w.Header().Set("Mcp-Session-Id", "synthetic-session")
		} else if r.Header.Get("Mcp-Session-Id") != "synthetic-session" {
			t.Error("missing issued session")
		}
		if method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{}}`, body["id"])
	}))
	defer srv.Close()
	c := &client{url: srv.URL, user: "ignored-plain-user", email: "ignored@example.invalid", token: "synthetic-token", jwt: "synthetic-jwt", http: srv.Client()}
	if err := c.connect(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.rpc("tools/list", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(methods, ","); got != "initialize,notifications/initialized,tools/list,DELETE" {
		t.Fatal(got)
	}
	if c.sid != "" {
		t.Fatal("deleted session retained")
	}
}

func TestDacliProcess(t *testing.T) {
	if os.Getenv("DACLI_TEST_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"dacli"}, os.Args[i+1:]...)
			flag.CommandLine = flag.NewFlagSet("dacli", flag.ExitOnError)
			main()
			os.Exit(0)
		}
	}
	t.Fatal("missing dacli arguments")
}

func TestDacliConfigToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secret files require verified Windows ACLs")
	}
	t.Setenv("DOTENV_UNSAFE_FILES_ALLOW", "0")
	t.Setenv("AILY_JWT", "synthetic-client-jwt")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	emptyPath := filepath.Join(dir, "empty.yaml")
	invalidPath := filepath.Join(dir, "invalid.yaml")
	for path, body := range map[string]string{
		configPath:  "identity: {auth_token: synthetic-file-token, jwt: {secret: synthetic-signing-secret}}\n",
		emptyPath:   "identity: {}\n",
		invalidPath: "identity: {auth_token: [synthetic-hidden-value]}\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		path    string
		env     string
		flag    string
		want    string
		wantErr string
	}{
		{name: "file", path: configPath, want: "synthetic-file-token"},
		{name: "environment", path: configPath, env: "synthetic-environment-token", want: "synthetic-environment-token"},
		{name: "flag", path: configPath, env: "synthetic-environment-token", flag: "synthetic-flag-token", want: "synthetic-flag-token"},
		{name: "explicit token skips file", path: filepath.Join(dir, "missing.yaml"), env: "synthetic-environment-token", want: "synthetic-environment-token"},
		{name: "no implicit file"},
		{name: "missing file", path: filepath.Join(dir, "missing.yaml"), wantErr: "client configuration file not found"},
		{name: "missing token", path: emptyPath, wantErr: "identity.auth_token is missing or empty"},
		{name: "invalid file", path: invalidPath, wantErr: "invalid server configuration YAML"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AILY_TOKEN", tc.env)
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("x-aily-token") != tc.want || r.Header.Get("x-aily-jwt") != "synthetic-client-jwt" {
					t.Error("unexpected token or JWT on protocol request")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.Method == http.MethodDelete {
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["method"] == "initialize" {
					w.Header().Set("Mcp-Session-Id", "synthetic-session")
				}
				if body["method"] == "notifications/initialized" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{"tools":[{"name":"synthetic-tool"}]}}`, body["id"])
			}))
			defer srv.Close()
			args := []string{"-test.run=^TestDacliProcess$", "--", "--url", srv.URL}
			if tc.path != "" {
				args = append(args, "--config", tc.path)
			}
			if tc.flag != "" {
				args = append(args, "--token", tc.flag)
			}
			args = append(args, "tools")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "DACLI_TEST_PROCESS=1")
			output, err := cmd.CombinedOutput()
			text := string(output)
			for _, secret := range []string{"synthetic-file-token", "synthetic-environment-token", "synthetic-flag-token", "synthetic-signing-secret", "synthetic-client-jwt", "synthetic-hidden-value"} {
				if strings.Contains(text, secret) {
					t.Fatal("client output leaked a credential")
				}
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(text, tc.wantErr) || requests.Load() != 0 {
					t.Fatalf("expected local configuration rejection: %v, %s", err, text)
				}
				return
			}
			if err != nil || !strings.Contains(text, "1 tools:") || requests.Load() != 4 {
				t.Fatalf("protocol lifecycle failed: %v, %s", err, text)
			}
		})
	}
}

func TestDecodeHTTPRejectionDoesNotEchoBody(t *testing.T) {
	for _, status := range []int{301, 401, 403, 404, 413, 429} {
		_, err := decode(&http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("synthetic-secret"))})
		if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("unsafe rejection: %v", err)
		}
	}
}
