package mcp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	mcpsdk "github.com/mark3labs/mcp-go/mcp"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
)

type (
	mcpCallToolRequest = mcpsdk.CallToolRequest
	mcpCallToolResult  = mcpsdk.CallToolResult
)

func legacyReq(t *testing.T, headers map[string]string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "http://localhost/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func newLegacyServer() *Server {
	s := &Server{}
	s.clientFactory = func(cred *dataagent.Credential) *dataagent.Client {
		return dataagent.NewClient(cred, "cn-hangzhou")
	}
	return s
}

// API Key headers must yield a per-connection client, a stable fingerprint
// tenant key, and the workspace/agent defaults from the companion headers.
func TestLegacyHTTPContextAPIKey(t *testing.T) {
	s := newLegacyServer()
	ctx := s.legacyHTTPContext(context.Background(), legacyReq(t, map[string]string{
		"X-Data-Agent-Api-Key":      "dms-da-test",
		"X-Data-Agent-Workspace-Id": "ws-1",
		"X-Data-Agent-Agent-Id":     "agent-1",
	}))

	lc := legacyFromCtx(ctx)
	if lc == nil || lc.client == nil {
		t.Fatal("expected legacy connection with client")
	}
	if lc.agentID != "agent-1" {
		t.Errorf("agentID = %q, want agent-1", lc.agentID)
	}
	want := (&dataagent.Credential{APIKey: "dms-da-test"}).TenantKey()
	if got := tenantKeyFromCtx(ctx); got != want {
		t.Errorf("tenant key = %q, want fingerprint %q", got, want)
	}
}

// An explicit X-Data-Agent-Tenant-Id wins over the credential fingerprint.
func TestLegacyHTTPContextExplicitTenantWins(t *testing.T) {
	s := newLegacyServer()
	ctx := s.legacyHTTPContext(context.Background(), legacyReq(t, map[string]string{
		"X-Data-Agent-Api-Key":   "dms-da-test",
		"X-Data-Agent-Tenant-Id": "channel-42",
	}))
	if got := tenantKeyFromCtx(ctx); got != "channel-42" {
		t.Errorf("tenant key = %q, want channel-42", got)
	}
}

// STS headers build a client; rotating STS credentials yield no tenant key
// (single-tenant legacy behavior).
func TestLegacyHTTPContextSTSNoTenantKey(t *testing.T) {
	s := newLegacyServer()
	ctx := s.legacyHTTPContext(context.Background(), legacyReq(t, map[string]string{
		"X-STS-Access-Key-Id":     "STS.abcdef",
		"X-STS-Access-Key-Secret": "secret",
		"X-STS-Security-Token":    "token",
	}))
	if legacyFromCtx(ctx) == nil {
		t.Fatal("expected legacy connection for STS headers")
	}
	if got := tenantKeyFromCtx(ctx); got != "" {
		t.Errorf("tenant key = %q, want empty for rotating STS", got)
	}
}

// Without credential headers (or without a factory) the context is unchanged.
func TestLegacyHTTPContextAbsent(t *testing.T) {
	s := newLegacyServer()
	ctx := s.legacyHTTPContext(context.Background(), legacyReq(t, nil))
	if legacyFromCtx(ctx) != nil {
		t.Error("expected no legacy connection without headers")
	}

	noFactory := &Server{}
	ctx = noFactory.legacyHTTPContext(context.Background(), legacyReq(t, map[string]string{
		"X-Data-Agent-Api-Key": "dms-da-test",
	}))
	if legacyFromCtx(ctx) != nil {
		t.Error("expected no legacy connection without a client factory")
	}
}

// The withTenant wrapper must route legacy connections to a scoped server
// carrying the per-connection client and the header agent default.
func TestWithTenantLegacyBranch(t *testing.T) {
	s := newLegacyServer()
	ctx := s.legacyHTTPContext(context.Background(), legacyReq(t, map[string]string{
		"X-Data-Agent-Api-Key":  "dms-da-test",
		"X-Data-Agent-Agent-Id": "agent-9",
	}))

	var got *Server
	h := s.withTenant(func(scoped *Server, _ context.Context, _ mcpCallToolRequest) (*mcpCallToolResult, error) {
		got = scoped
		return nil, nil
	})
	if _, err := h(ctx, mcpCallToolRequest{}); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("handler not invoked")
	}
	if got == s {
		t.Error("expected a scoped copy, got the shared server")
	}
	if got.defaults.CustomAgentID != "agent-9" {
		t.Errorf("defaults.CustomAgentID = %q, want agent-9", got.defaults.CustomAgentID)
	}
	if got.client == s.client {
		t.Error("expected the per-connection client on the scoped server")
	}
}

func TestVisibleToTenant(t *testing.T) {
	cases := []struct {
		caller, owner string
		want          bool
	}{
		{"", "", true},
		{"", "t1", true},   // default-credential caller sees everything
		{"t1", "", true},   // legacy unowned session visible to everyone
		{"t1", "t1", true},
		{"t1", "t2", false},
	}
	for _, c := range cases {
		if got := visibleToTenant(c.caller, c.owner); got != c.want {
			t.Errorf("visibleToTenant(%q, %q) = %v, want %v", c.caller, c.owner, got, c.want)
		}
	}
}

// A caller with a tenant key must not see sessions owned by another tenant.
func TestCheckSessionAccessTenantIsolation(t *testing.T) {
	mgr := &waitManager{snap: session.StateSnapshot{SessionID: "s1", TenantKey: "owner-a"}}
	s := &Server{mgr: mgr}

	foreign := context.WithValue(context.Background(), tenantKeyKey, "other")
	if _, err := s.checkSessionAccess(foreign, "s1"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not-found for foreign tenant, got %v", err)
	}

	owner := context.WithValue(context.Background(), tenantKeyKey, "owner-a")
	if _, err := s.checkSessionAccess(owner, "s1"); err != nil {
		t.Errorf("owner must access its session: %v", err)
	}
	if _, err := s.checkSessionAccess(context.Background(), "s1"); err != nil {
		t.Errorf("default caller must access every session: %v", err)
	}
}
