package mcp

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

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
		if !strings.Contains(out, "ou_alice") {
			t.Errorf("level %v should record the caller: %q", level, out)
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
	if !strings.Contains(out, "error") || !strings.Contains(out, "boom") {
		t.Errorf("handler error not reported: %q", out)
	}

	// A tool-level failure, which the MCP layer returns as a result.
	out = captureLog(t, func() {
		rec := s.startToolCall(context.Background(), callReq("t", nil))
		rec.finish(mcp.NewToolResultError("db not found"), nil)
	})
	if !strings.Contains(out, "tool_error") || !strings.Contains(out, "db not found") {
		t.Errorf("tool error not reported: %q", out)
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

// In JWT mode the plain headers are unverified, so a rejected request must not
// be attributed in the log to whoever the sender claimed to be.
func TestRequestLogDoesNotTrustHeadersInJWTMode(t *testing.T) {
	const spoofed = "7620774801438674448"

	s := &Server{reqLog: RequestLogBasic, jwtIdentity: true}
	ctx := tenant.WithIdentity(context.Background(), spoofed, "victim@example.com", "")
	out := captureLog(t, func() {
		rec := s.startToolCall(ctx, callReq("data_agent_list_workspaces", nil))
		rec.finish(mcp.NewToolResultError("identity request rejected"), nil)
	})
	if strings.Contains(out, spoofed) {
		t.Errorf("unverified header identity was logged as the caller: %q", out)
	}
	if !strings.Contains(out, "caller=-") {
		t.Errorf("expected an anonymous caller, got %q", out)
	}

	// A token present but not yet verified is reported as such.
	ctx = tenant.WithJWT(tenant.WithIdentity(context.Background(), spoofed, "", ""), "some.jwt.token")
	out = captureLog(t, func() {
		rec := s.startToolCall(ctx, callReq("data_agent_list_workspaces", nil))
		rec.finish(mcp.NewToolResultError("rejected"), nil)
	})
	if strings.Contains(out, spoofed) {
		t.Errorf("unverified header identity leaked: %q", out)
	}
	if !strings.Contains(out, "jwt(unverified)") {
		t.Errorf("expected jwt(unverified) caller, got %q", out)
	}

	// Verified claims are what gets attributed.
	ctx = tenant.WithClaims(context.Background(), &tenant.Claims{UserID: spoofed, Email: "real@example.com"})
	out = captureLog(t, func() {
		rec := s.startToolCall(ctx, callReq("data_agent_list_workspaces", nil))
		rec.finish(mcp.NewToolResultText("ok"), nil)
	})
	if !strings.Contains(out, spoofed+"/real@example.com") {
		t.Errorf("verified claims should be logged as the caller: %q", out)
	}

	// Header mode keeps its existing behaviour: auth_token is what vouches
	// for those headers, so they are the caller.
	s = &Server{reqLog: RequestLogBasic}
	ctx = tenant.WithIdentity(context.Background(), "ou_alice", "", "")
	out = captureLog(t, func() {
		rec := s.startToolCall(ctx, callReq("t", nil))
		rec.finish(mcp.NewToolResultText("ok"), nil)
	})
	if !strings.Contains(out, "caller=ou_alice") {
		t.Errorf("header mode should still report the header identity: %q", out)
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
