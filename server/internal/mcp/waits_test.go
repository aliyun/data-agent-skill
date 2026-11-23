package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

// waitManager fakes the blocking wait paths so the handlers can be exercised
// without a live watcher: it records the effective timeout and returns a
// canned snapshot.
type waitManager struct {
	sessionManager
	waitTimeout   time.Duration // captured by WaitForResult
	changeTimeout time.Duration // captured by WaitForChange
	reason        string
	snap          session.StateSnapshot
	block         chan struct{} // when set, WaitForResult blocks until closed
	pollSeq       int
}

func (m *waitManager) GetStatus(string) (*session.StateSnapshot, error) {
	s := m.snap
	return &s, nil
}

func (m *waitManager) WaitForResult(_ context.Context, _ string, timeout time.Duration) (*session.StateSnapshot, string, error) {
	m.waitTimeout = timeout
	if m.block != nil {
		<-m.block
	}
	s := m.snap
	return &s, m.reason, nil
}

func (m *waitManager) WaitForChange(_ context.Context, _ string, _ int, timeout time.Duration) (*session.StateSnapshot, bool, error) {
	m.changeTimeout = timeout
	s := m.snap
	return &s, false, nil
}

func (m *waitManager) IncrPollSeq(string) int {
	m.pollSeq++
	return m.pollSeq
}

func waitReq(name string, args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

func decodeResult(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &out); err != nil {
		t.Fatalf("result is not JSON: %v (%s)", err, resultText(res))
	}
	return out
}

func TestCapWaitClampsToDefault(t *testing.T) {
	s := &Server{} // zero-value server falls back to defaultWaitCap
	if got := s.capWait(480 * time.Second); got != defaultWaitCap {
		t.Errorf("capWait(480s) = %v, want %v", got, defaultWaitCap)
	}
	if got := s.capWait(30 * time.Second); got != 30*time.Second {
		t.Errorf("capWait(30s) = %v, want 30s", got)
	}
	if got := s.capWait(0); got != 0 {
		t.Errorf("capWait(0) = %v, want 0", got)
	}
}

// A requested timeout above the cap must be clamped before reaching the
// manager: transport timeouts (~120s) would otherwise cancel the call.
func TestWaitResultClampsTimeout(t *testing.T) {
	mgr := &waitManager{reason: "timeout", snap: session.StateSnapshot{SessionID: "s1", Status: session.StatusRunning}}
	s := &Server{mgr: mgr, waits: newWaitRegistry()}

	res, err := s.handleWaitResult(context.Background(), waitReq("data_agent_wait_result", map[string]any{
		"session_id": "s1", "timeout": float64(480),
	}))
	if err != nil || res.IsError {
		t.Fatalf("unexpected error: %v %v", err, resultText(res))
	}
	if mgr.waitTimeout != defaultWaitCap {
		t.Errorf("effective timeout = %v, want clamped %v", mgr.waitTimeout, defaultWaitCap)
	}
}

// On timeout the response must teach the LLM how to continue: progress delta
// plus an explicit next_action.
func TestWaitResultTimeoutCarriesProgress(t *testing.T) {
	mgr := &waitManager{
		reason: "timeout",
		snap: session.StateSnapshot{
			SessionID: "s1", Status: session.StatusRunning,
			Checkpoint:  42,
			Conclusions: []string{"c1", "c2"},
		},
	}
	s := &Server{mgr: mgr, waits: newWaitRegistry()}

	res, _ := s.handleWaitResult(context.Background(), waitReq("data_agent_wait_result", map[string]any{"session_id": "s1"}))
	out := decodeResult(t, res)
	if out["reason"] != "timeout" {
		t.Fatalf("reason = %v, want timeout", out["reason"])
	}
	if _, ok := out["next_action"]; !ok {
		t.Error("timeout response missing next_action")
	}
	if _, ok := out["checkpoint_delta"]; !ok {
		t.Error("timeout response missing checkpoint_delta")
	}
}

// A second wait_result issued while the first is still blocking must degrade
// to an immediate snapshot instead of stacking another multi-minute block.
func TestWaitResultDuplicateDegrades(t *testing.T) {
	block := make(chan struct{})
	mgr := &waitManager{
		reason: "completed", block: block,
		snap: session.StateSnapshot{SessionID: "s1", Status: session.StatusRunning},
	}
	s := &Server{mgr: mgr, waits: newWaitRegistry()}

	first := make(chan struct{})
	go func() {
		defer close(first)
		s.handleWaitResult(context.Background(), waitReq("data_agent_wait_result", map[string]any{"session_id": "s1"}))
	}()

	// Wait until the first call is registered as in flight.
	deadline := time.After(2 * time.Second)
	for {
		s.waits.mu.Lock()
		_, inflight := s.waits.inflight[blockingWaitKey(context.Background(), "s1")]
		s.waits.mu.Unlock()
		if inflight {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first wait never registered")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	res, _ := s.handleWaitResult(context.Background(), waitReq("data_agent_wait_result", map[string]any{"session_id": "s1"}))
	out := decodeResult(t, res)
	if out["reason"] != "duplicate_wait" {
		t.Errorf("reason = %v, want duplicate_wait", out["reason"])
	}
	if w, _ := out["warning"].(string); !strings.Contains(w, "in flight") {
		t.Errorf("expected duplicate warning, got %v", out["warning"])
	}

	close(block)
	<-first

	// The registry must be clean afterwards so later waits can block again.
	if !s.waits.enter(context.Background(), "s1") {
		t.Error("registry still marks s1 in flight after the wait returned")
	}
}

// Long-poll status calls are the recommended pattern; they must neither count
// toward the anti-loop warning nor exceed the wait cap.
func TestStatusLongPollSkipsPollSeqAndClamps(t *testing.T) {
	mgr := &waitManager{snap: session.StateSnapshot{SessionID: "s1", Status: session.StatusRunning}}
	s := &Server{mgr: mgr, waits: newWaitRegistry()}

	res, _ := s.handleStatus(context.Background(), waitReq("data_agent_status", map[string]any{
		"session_id": "s1", "wait_timeout": float64(600),
	}))
	out := decodeResult(t, res)
	if _, ok := out["poll_seq"]; ok {
		t.Error("long-poll status must not report poll_seq")
	}
	if _, ok := out["changed"]; !ok {
		t.Error("long-poll status must report changed")
	}
	if mgr.changeTimeout != defaultWaitCap {
		t.Errorf("long-poll timeout = %v, want clamped %v", mgr.changeTimeout, defaultWaitCap)
	}
	if mgr.pollSeq != 0 {
		t.Errorf("IncrPollSeq was called %d times during a long-poll", mgr.pollSeq)
	}

	// A bare snapshot still counts.
	res, _ = s.handleStatus(context.Background(), waitReq("data_agent_status", map[string]any{"session_id": "s1"}))
	out = decodeResult(t, res)
	if out["poll_seq"] == nil {
		t.Error("bare status must report poll_seq")
	}
}

func TestBlockingWaitsArePrincipalScoped(t *testing.T) {
	cfg := config.Config{Identity: config.Identity{
		Enabled: true, RequireIdentity: true, Namespace: "wait-tests", AuthToken: "synthetic-token",
		Groups: map[string]config.IdentityGroup{
			"shared-role": {RoleArn: "acs:ram::123456789012:role/synthetic", Users: []string{"A", "B"}},
		},
	}}
	registry := tenant.NewRegistry(context.Background(), cfg, nil)
	t.Cleanup(func() { _ = registry.Close() })
	a, err := registry.Authenticate(tenant.WithIdentity(context.Background(), "A", "", "synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := registry.Authenticate(tenant.WithIdentity(context.Background(), "B", "", "synthetic-token"))
	if err != nil {
		t.Fatal(err)
	}
	waits := newWaitRegistry()
	if !waits.enter(a, "shared-session") {
		t.Fatal("first wait rejected")
	}
	mgr := &waitManager{reason: "timeout", snap: session.StateSnapshot{SessionID: "shared-session", Status: session.StatusRunning}}
	s := &Server{mgr: mgr, waits: waits}
	request := waitReq("data_agent_wait_result", map[string]any{"session_id": "shared-session"})
	res, err := s.handleWaitResult(b, request)
	if err != nil || res.IsError || decodeResult(t, res)["reason"] != "timeout" {
		t.Fatalf("B wait was affected by A: %v %v", err, res)
	}
	res, err = s.handleWaitResult(a, request)
	if err != nil || res.IsError || decodeResult(t, res)["reason"] != "duplicate_wait" {
		t.Fatalf("same-principal duplicate not blocked: %v %v", err, res)
	}
	res, err = s.handleStatus(b, waitReq("data_agent_status", map[string]any{"session_id": "shared-session", "wait_timeout": float64(30)}))
	if err != nil || res.IsError || mgr.changeTimeout != 30*time.Second {
		t.Fatalf("B status wait was affected by A: %v %v", err, res)
	}
	waits.exit(a, "shared-session")
	if len(waits.inflight) != 0 {
		t.Fatal("completed waits retained entries")
	}
	if blockingWaitKey(tenant.WithIdentity(context.Background(), "spoofed", "", ""), "shared-session") != blockingWaitKey(context.Background(), "shared-session") {
		t.Fatal("unverified header influenced wait ownership")
	}
}
