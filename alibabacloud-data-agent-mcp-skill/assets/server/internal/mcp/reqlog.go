package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

// RequestLogLevel controls how much of every tool call is written to the log.
type RequestLogLevel int

const (
	// RequestLogOff disables per-call logging.
	RequestLogOff RequestLogLevel = iota
	// RequestLogBasic logs the tool name, caller identity, outcome and
	// duration — no arguments, so no business data reaches the log.
	RequestLogBasic
	// RequestLogFull adds the call arguments with sensitive values redacted.
	// Arguments carry user questions and table names, so keep it for
	// troubleshooting rather than steady-state operation.
	RequestLogFull
)

// ParseRequestLogLevel maps a config/env value to a level. Unknown values fall
// back to basic so a typo degrades to less logging, never to none. An empty
// value also yields basic; callers holding a possibly-unset config value should
// use ApplyRequestLogConfig so the transport default survives.
func ParseRequestLogLevel(v string) RequestLogLevel {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "none", "false", "0":
		return RequestLogOff
	case "full", "args", "debug":
		return RequestLogFull
	case "", "basic", "true", "1":
		return RequestLogBasic
	default:
		log.Printf("unknown request log level %q, using basic", v)
		return RequestLogBasic
	}
}

// defaultRequestLogLevel picks the level for an unconfigured server.
//
// A standalone deployment listens on a port and its stderr goes to a log file
// or the process supervisor, so per-call logging is what makes the service
// operable. Under stdio the server is a child of the host agent, which owns
// the console and already records the calls it makes, so staying quiet avoids
// duplicating its transcript.
func defaultRequestLogLevel(standalone bool) RequestLogLevel {
	if standalone {
		return RequestLogBasic
	}
	return RequestLogOff
}

// SetRequestLogLevel selects how much of each tool call is logged.
func (s *Server) SetRequestLogLevel(l RequestLogLevel) { s.reqLog = l }

// ApplyRequestLogConfig applies a configured level, keeping the
// transport-derived default when the value is unset.
func (s *Server) ApplyRequestLogConfig(configured string) {
	if strings.TrimSpace(configured) == "" {
		return
	}
	s.reqLog = ParseRequestLogLevel(configured)
}

// sensitiveArgKeys are argument names whose values are never logged. The
// identity token is not an argument and is never logged at all.
var sensitiveArgKeys = map[string]bool{
	"api_key":    true,
	"apikey":     true,
	"token":      true,
	"auth_token": true,
	"secret":     true,
	"password":   true,
	"credential": true,
}

const maxLoggedValueLen = 160

// callRecord tracks one tool call so the completion line can report the
// outcome and how long the call took.
type callRecord struct {
	srv    *Server
	id     string
	tool   string
	caller string
	start  time.Time
}

// startToolCall emits the request line and returns a record used to emit the
// matching completion line. A nil record means logging is disabled.
func (s *Server) startToolCall(ctx context.Context, req mcp.CallToolRequest) *callRecord {
	if s.reqLog == RequestLogOff {
		return nil
	}

	rec := &callRecord{
		srv:    s,
		id:     newRequestID(),
		tool:   req.Params.Name,
		caller: callerFromContext(ctx),
		start:  time.Now(),
	}

	if s.reqLog >= RequestLogFull {
		log.Printf("[req:%s] -> %s caller=%s args=%s", rec.id, rec.tool, rec.caller, formatArgs(req.GetArguments()))
	} else {
		log.Printf("[req:%s] -> %s caller=%s", rec.id, rec.tool, rec.caller)
	}
	return rec
}

// finish emits the completion line. Tool-level failures are returned as a
// result with IsError set rather than as an error, so both are inspected.
func (r *callRecord) finish(res *mcp.CallToolResult, err error) {
	if r == nil {
		return
	}
	outcome := "ok"
	detail := ""
	switch {
	case err != nil:
		outcome = "error"
		detail = " detail=" + quoteForLog(err.Error())
	case res != nil && res.IsError:
		outcome = "tool_error"
		detail = " detail=" + quoteForLog(resultText(res))
	}
	log.Printf("[req:%s] <- %s %s duration=%s%s",
		r.id, r.tool, outcome, time.Since(r.start).Round(time.Millisecond), detail)
}

// callerFromContext describes who issued the call. On the HTTP transports the
// identity headers are in the context; stdio has no identity, so the caller is
// the local process. The identity token is deliberately never included.
func callerFromContext(ctx context.Context) string {
	user, email, _ := tenant.IdentityFromContext(ctx)
	switch {
	case user != "" && email != "":
		return user + "/" + email
	case user != "":
		return user
	case email != "":
		return email
	default:
		return "-"
	}
}

// formatArgs renders arguments in a stable order with sensitive values
// replaced and long values truncated.
func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		if isSensitiveArg(k) {
			b.WriteString("<redacted>")
			continue
		}
		b.WriteString(quoteForLog(fmt.Sprintf("%v", args[k])))
	}
	b.WriteByte('}')
	return b.String()
}

func isSensitiveArg(key string) bool {
	k := strings.ToLower(key)
	if sensitiveArgKeys[k] {
		return true
	}
	// Catch composed names such as "x_api_key" or "session_token".
	for s := range sensitiveArgKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// quoteForLog collapses newlines and truncates so one call stays on one line.
func quoteForLog(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > maxLoggedValueLen {
		s = s[:maxLoggedValueLen] + "...(truncated)"
	}
	return strconv.Quote(s)
}

// resultText extracts the textual payload of a tool result for the log line.
func resultText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

func newRequestID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}
