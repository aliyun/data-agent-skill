package mcp

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestToolStatsSnapshot(t *testing.T) {
	st := newToolStats()
	st.begin("a")
	st.begin("a")
	st.end("a", 20*time.Millisecond, false, false)
	st.end("a", 40*time.Millisecond, true, false)
	st.begin("b")
	st.end("b", 5*time.Millisecond, false, true)
	st.begin("b")

	snap := st.snapshot()
	if snap["inflight"] != 1 || snap["calls"] != uint64(4) || snap["errors"] != uint64(1) || snap["tool_errors"] != uint64(1) {
		t.Fatalf("totals wrong: %v", snap)
	}
	tools := snap["tools"].(map[string]any)
	a := tools["a"].(map[string]any)
	if a["calls"] != uint64(2) || a["avg_ms"] != int64(30) || a["max_ms"] != int64(40) || a["window_max_ms"] != int64(40) {
		t.Fatalf("tool a wrong: %v", a)
	}
	b := tools["b"].(map[string]any)
	if b["inflight"] != 1 || b["tool_errors"] != uint64(1) {
		t.Fatalf("tool b wrong: %v", b)
	}
	// window max resets per snapshot while the cumulative max survives
	a = st.snapshot()["tools"].(map[string]any)["a"].(map[string]any)
	if a["window_max_ms"] != int64(0) || a["max_ms"] != int64(40) {
		t.Fatalf("window max should reset: %v", a)
	}
}

func TestPerfStatsCountsToolCallsAndNilTransport(t *testing.T) {
	s := New(nil, nil, "test")
	h := s.withTenant(func(*Server, context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultError("nope"), nil
	})
	req := mcp.CallToolRequest{}
	req.Params.Name = "data_agent_list_workspaces"
	if _, err := h(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	stats := s.PerfStats()
	calls := stats["tool_calls"].(map[string]any)
	if calls["calls"] != uint64(1) || calls["tool_errors"] != uint64(1) || calls["inflight"] != 0 {
		t.Fatalf("tool call not counted: %v", calls)
	}
	if stats["blocking_waits"] != 0 {
		t.Fatalf("no waits expected: %v", stats)
	}
	if _, ok := stats["protocol_sessions"]; ok {
		t.Fatalf("stdio server must not report protocol sessions: %v", stats)
	}
	if zero := (&Server{}).PerfStats(); len(zero["tool_calls"].(map[string]any)) != 0 || zero["blocking_waits"] != 0 {
		t.Fatalf("zero-value server must tolerate nil stats: %v", zero)
	}
}

func TestProtocolSessionsStats(t *testing.T) {
	f := newHTTPFixture(t, "streamable-http", httpLimits(), false)
	id := f.initialize(t, "A")
	stats := f.rt.sessions.stats()
	if stats["total"] != 1 || stats["ready"] != 1 || stats["principals"] != 1 || stats["inflight"] != 0 {
		t.Fatalf("one ready session expected: %v", stats)
	}
	statusHTTP(t, f.do(t, f.request(t, http.MethodDelete, "/mcp", id, "A", "")), 200)
	awaitHTTP(t, func() bool { return f.rt.sessions.stats()["total"] == 0 })
}
