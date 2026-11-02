package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

// captureLog redirects the standard logger for the duration of fn.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(old)
		log.SetFlags(flags)
	})
	fn()
	return buf.String()
}

func callReq(tool string, args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = tool
	req.Params.Arguments = args
	return req
}

func TestParseRequestLogLevel(t *testing.T) {
	for in, want := range map[string]RequestLogLevel{
		"":       RequestLogBasic,
		"basic":  RequestLogBasic,
		"BASIC":  RequestLogBasic,
		" full ": RequestLogFull,
		"off":    RequestLogOff,
		"none":   RequestLogOff,
		"bogus":  RequestLogBasic, // unknown degrades to less logging, not none
	} {
		if got := ParseRequestLogLevel(in); got != want {
			t.Errorf("ParseRequestLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestRequestLogVerifiedPrincipal(t *testing.T) {
	cfg := config.Config{Identity: config.Identity{
		Enabled: true, RequireIdentity: true, Namespace: "log-tests", AuthToken: "synthetic-platform-token",
		Default: &config.IdentityGroup{RoleArn: "acs:ram::123456789012:role/synthetic"},
	}}
	registry := tenant.NewRegistry(context.Background(), cfg, nil)
	t.Cleanup(func() { _ = registry.Close() })
	ctx, err := registry.Authenticate(tenant.WithIdentity(context.Background(), "verified-user", "", "synthetic-platform-token"))
	if err != nil {
		t.Fatal(err)
	}
	principal := tenant.PrincipalFromContext(ctx)
	s := &Server{reqLog: RequestLogBasic}
	out := captureLog(t, func() {
		rec := s.startToolCall(ctx, callReq("data_agent_status", nil))
		rec.finish(mcp.NewToolResultText("ok"), nil)
	})
	if !strings.Contains(out, "caller="+quoteForLog(principal.ID)) || strings.Contains(out, "verified-user") || strings.Contains(out, "synthetic-platform-token") {
		t.Fatalf("principal log is missing or reveals raw identity: %q", out)
	}
}

func TestRequestLogLevels(t *testing.T) {
	args := map[string]any{"session_id": "sess-1", "query": "how many rows?"}

	t.Run("off", func(t *testing.T) {
		s := &Server{reqLog: RequestLogOff}
		out := captureLog(t, func() {
			rec := s.startToolCall(context.Background(), callReq("data_agent_status", args))
			rec.finish(nil, nil)
		})
		if out != "" {
			t.Fatalf("expected no output, got %q", out)
		}
	})

	t.Run("basic omits arguments", func(t *testing.T) {
		s := &Server{reqLog: RequestLogBasic}
		out := captureLog(t, func() {
			rec := s.startToolCall(context.Background(), callReq("data_agent_status", args))
			rec.finish(mcp.NewToolResultText("ok"), nil)
		})
		if !strings.Contains(out, "data_agent_status") {
			t.Errorf("tool name missing: %q", out)
		}
		if strings.Contains(out, "how many rows?") {
			t.Errorf("basic level must not log arguments: %q", out)
		}
		if !strings.Contains(out, "ok") || !strings.Contains(out, "duration=") {
			t.Errorf("completion line incomplete: %q", out)
		}
	})

	t.Run("full includes arguments", func(t *testing.T) {
		s := &Server{reqLog: RequestLogFull}
		out := captureLog(t, func() {
			rec := s.startToolCall(context.Background(), callReq("data_agent_status", args))
			rec.finish(mcp.NewToolResultText("ok"), nil)
		})
		if !strings.Contains(out, "how many rows?") {
			t.Errorf("full level should log arguments: %q", out)
		}
		if !strings.Contains(out, "sess-1") {
			t.Errorf("full level should log session_id: %q", out)
		}
	})
}

// The identity token authenticates the upstream platform; leaking it into logs
// would let anyone replaying the log impersonate that platform.
func TestRequestLogNeverContainsIdentityToken(t *testing.T) {
	const secret = "super-secret-upstream-token"
	ctx := tenant.WithIdentity(context.Background(), "ou_alice", "alice@example.com", secret)

	for _, level := range []RequestLogLevel{RequestLogBasic, RequestLogFull} {
		s := &Server{reqLog: level}
		out := captureLog(t, func() {
			rec := s.startToolCall(ctx, callReq("data_agent_status", map[string]any{"session_id": "s1"}))
			rec.finish(mcp.NewToolResultText("ok"), nil)
		})
		if strings.Contains(out, secret) {
			t.Fatalf("level %v leaked the identity token: %q", level, out)
		}
		if strings.Contains(out, "ou_alice") {
			t.Errorf("level %v must not trust unauthenticated headers: %q", level, out)
		}
	}
}

func TestRequestLogRedactsSensitiveArguments(t *testing.T) {
	s := &Server{reqLog: RequestLogFull}
	out := captureLog(t, func() {
		rec := s.startToolCall(context.Background(), callReq("probe", map[string]any{
			"api_key":       "dms-da-abcdef0123456789",
			"auth_token":    "tok-123",
			"session_token": "st-456",
			"password":      "hunter2",
			"db_name":       "chinook",
		}))
		rec.finish(nil, nil)
	})

	for _, leak := range []string{"dms-da-abcdef0123456789", "tok-123", "st-456", "hunter2"} {
		if strings.Contains(out, leak) {
			t.Errorf("sensitive value leaked: %q in %q", leak, out)
		}
	}
	if !strings.Contains(out, "<redacted>") {
		t.Errorf("expected redaction marker: %q", out)
	}
	if !strings.Contains(out, "chinook") {
		t.Errorf("non-sensitive argument should survive: %q", out)
	}
}

func TestRequestLogOutcomes(t *testing.T) {
	s := &Server{reqLog: RequestLogBasic}

	// A handler error.
	out := captureLog(t, func() {
		rec := s.startToolCall(context.Background(), callReq("t", nil))
		rec.finish(nil, errors.New("boom"))
	})
	if !strings.Contains(out, "error") || strings.Contains(out, "boom") {
		t.Errorf("handler error category missing or raw error leaked: %q", out)
	}

	// A tool-level failure, which the MCP layer returns as a result.
	out = captureLog(t, func() {
		rec := s.startToolCall(context.Background(), callReq("t", nil))
		rec.finish(mcp.NewToolResultError("db not found"), nil)
	})
	if !strings.Contains(out, "tool_error") || strings.Contains(out, "db not found") {
		t.Errorf("tool error category missing or raw error leaked: %q", out)
	}
}

// Request and completion lines must share an id so concurrent calls can be
// correlated in the log.
func TestRequestLogPairsIDs(t *testing.T) {
	s := &Server{reqLog: RequestLogBasic}
	out := captureLog(t, func() {
		rec := s.startToolCall(context.Background(), callReq("t", nil))
		rec.finish(mcp.NewToolResultText("ok"), nil)
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), out)
	}
	id := func(line string) string {
		start := strings.Index(line, "[req:")
		end := strings.Index(line, "]")
		if start < 0 || end <= start {
			return ""
		}
		return line[start+5 : end]
	}
	if a, b := id(lines[0]), id(lines[1]); a == "" || a != b {
		t.Errorf("request ids do not match: %q vs %q", a, b)
	}
}

// A standalone deployment listens on a port and owns its log, so calls are
// recorded by default. Under stdio the host agent owns the console.
func TestDefaultRequestLogLevelDependsOnTransport(t *testing.T) {
	for transport, want := range map[string]RequestLogLevel{
		"streamable-http": RequestLogBasic,
		"sse":             RequestLogBasic,
		"stdio":           RequestLogOff,
		"":                RequestLogOff,
	} {
		t.Setenv("MCP_TRANSPORT", transport)
		s := New(nil, nil, "test")
		if s.reqLog != want {
			t.Errorf("MCP_TRANSPORT=%q: default level = %v, want %v", transport, s.reqLog, want)
		}
	}
}

func TestApplyRequestLogConfig(t *testing.T) {
	t.Setenv("MCP_TRANSPORT", "streamable-http")

	// An unset value must not erase the transport default.
	s := New(nil, nil, "test")
	s.ApplyRequestLogConfig("")
	if s.reqLog != RequestLogBasic {
		t.Errorf("empty config changed the default to %v", s.reqLog)
	}
	s.ApplyRequestLogConfig("   ")
	if s.reqLog != RequestLogBasic {
		t.Errorf("blank config changed the default to %v", s.reqLog)
	}

	// An explicit value wins, including switching logging off on a
	// standalone deployment.
	s.ApplyRequestLogConfig("off")
	if s.reqLog != RequestLogOff {
		t.Errorf("explicit off ignored, got %v", s.reqLog)
	}

	// And it can raise the level under stdio, where the default is off.
	t.Setenv("MCP_TRANSPORT", "stdio")
	s = New(nil, nil, "test")
	s.ApplyRequestLogConfig("full")
	if s.reqLog != RequestLogFull {
		t.Errorf("explicit full ignored under stdio, got %v", s.reqLog)
	}
}

func TestRequestLogDoesNotTrustUnverifiedContext(t *testing.T) {
	const spoofed = "7620774801438674448"
	contexts := []context.Context{
		tenant.WithIdentity(context.Background(), spoofed, "victim@example.com", "fake-token"),
		tenant.WithJWT(context.Background(), "some.jwt.token"),
		tenant.WithClaims(context.Background(), &tenant.Claims{UserID: spoofed}),
	}
	for _, jwtMode := range []bool{false, true} {
		for _, ctx := range contexts {
			s := &Server{reqLog: RequestLogBasic, jwtIdentity: jwtMode}
			out := captureLog(t, func() {
				rec := s.startToolCall(ctx, callReq("data_agent_list_workspaces", nil))
				rec.finish(mcp.NewToolResultError("rejected"), nil)
			})
			if strings.Contains(out, spoofed) || strings.Contains(out, "fake-token") || strings.Contains(out, "some.jwt.token") {
				t.Fatalf("unverified identity or credential logged: %q", out)
			}
			if !strings.Contains(out, `caller="-"`) {
				t.Fatalf("unverified caller must be anonymous: %q", out)
			}
		}
	}
}

func TestRequestLogRecursiveRedactionAndBudget(t *testing.T) {
	args := map[string]any{
		"key\nFORGED\r\x00": map[string]any{
			"list": []any{map[string]any{"AccessKeyId": "fake-ak", "AccessKeySecret": "fake-sk", "Authorization": "fake-auth"}},
			"url":  "https://example.invalid/?Signature=fake-signature",
		},
		strings.Repeat("x", 300) + "token": "fake-long-key-secret",
	}
	for i := 0; i < 500; i++ {
		args[fmt.Sprintf("arg-%d", i)] = strings.Repeat("\n", 300)
	}
	out := formatArgs(args)
	if len(out) > maxLoggedArgsLen || strings.ContainsAny(out, "\n\r\x00") {
		t.Fatalf("log must be bounded and single-line: %d", len(out))
	}
	for _, leak := range []string{"fake-ak", "fake-sk", "fake-auth", "fake-signature", "fake-long-key-secret"} {
		if strings.Contains(out, leak) {
			t.Fatalf("secret leaked: %s", leak)
		}
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("bounded args must remain valid JSON: %q", out)
	}
	delete(args, "key\nFORGED\r\x00")
	out = formatArgs(map[string]any{"nested": []any{map[string]any{"password": "fake-secret", "safe": "kept"}}})
	if strings.Contains(out, "fake-secret") || !strings.Contains(out, "kept") {
		t.Fatalf("recursive redaction failed: %q", out)
	}
}

func TestRequestLogEscapesDynamicFieldsAndOmitsErrors(t *testing.T) {
	s := &Server{reqLog: RequestLogFull}
	out := captureLog(t, func() {
		rec := s.startToolCall(context.Background(), callReq("tool\nFORGED\r\x00", map[string]any{"key\nFORGED": "value\r\n"}))
		rec.finish(nil, errors.New("https://example.invalid/?Signature=fake-error-secret\nFORGED"))
	})
	if strings.Count(out, "\n") != 2 || strings.ContainsAny(out, "\r\x00") || strings.Contains(out, "fake-error-secret") {
		t.Fatalf("unsafe request log: %q", out)
	}
}

func TestQuoteForLogTruncatesAndFlattens(t *testing.T) {
	got := quoteForLog("line1\nline2")
	if strings.Contains(got, "\n") {
		t.Errorf("newline should be escaped: %q", got)
	}

	long := strings.Repeat("x", maxLoggedValueLen+50)
	got = quoteForLog(long)
	if !strings.Contains(got, "truncated") {
		t.Errorf("long value should be truncated: %q", got)
	}
	if len(got) > maxLoggedValueLen+40 {
		t.Errorf("truncated value still too long: %d", len(got))
	}
}

// Tool errors must carry the server-side request id so a caller-reported
// failure can be matched to the [req:...] lines in the server log.
func TestTagErrorStampsRequestID(t *testing.T) {
	s := &Server{reqLog: RequestLogBasic}

	t.Run("error result is stamped", func(t *testing.T) {
		var rec *callRecord
		captureLog(t, func() {
			rec = s.startToolCall(context.Background(), callReq("data_agent_status", nil))
		})
		res := mcp.NewToolResultError("failed to get status: boom")
		rec.tagError(res)
		if got := resultText(res); !strings.Contains(got, "[req:"+rec.id+"]") {
			t.Fatalf("error text missing request id: %q", got)
		}
	})

	t.Run("success result untouched", func(t *testing.T) {
		var rec *callRecord
		captureLog(t, func() {
			rec = s.startToolCall(context.Background(), callReq("data_agent_status", nil))
		})
		res := mcp.NewToolResultText(`{"status":"completed"}`)
		rec.tagError(res)
		if got := resultText(res); strings.Contains(got, "[req:") {
			t.Fatalf("success text must not be stamped: %q", got)
		}
	})

	t.Run("nil record is safe", func(t *testing.T) {
		var rec *callRecord
		rec.tagError(mcp.NewToolResultError("x")) // must not panic
	})
}
