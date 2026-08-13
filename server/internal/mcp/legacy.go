package mcp

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
)

// Legacy per-connection credential headers (databuddy embedded mode).
//
// databuddy's gateway opens one MCP connection per IM channel and passes that
// channel's credentials in HTTP headers:
//
//	X-STS-Access-Key-Id / X-STS-Access-Key-Secret / X-STS-Security-Token
//	X-Data-Agent-Api-Key
//	X-Data-Agent-Workspace-Id / X-Data-Agent-Agent-Id / X-Data-Agent-Tenant-Id
//
// These callers run against the shared default session manager with a
// per-connection client injected through the request context, and sessions
// are isolated by a credential-fingerprint tenant key. This coexists with —
// and takes precedence over — the identity/JWT multi-tenant mode.

// ClientFactory creates a per-connection Client from legacy header
// credentials. Configured by main with the server's region/endpoint options.
type ClientFactory func(cred *dataagent.Credential) *dataagent.Client

// SetClientFactory enables the legacy credential headers on the HTTP
// transports. Without a factory the headers are ignored.
func (s *Server) SetClientFactory(f ClientFactory) { s.clientFactory = f }

type legacyConnKeyType struct{}

var legacyConnKey = legacyConnKeyType{}

type tenantKeyKeyType struct{}

var tenantKeyKey = tenantKeyKeyType{}

// legacyConn carries the per-connection client and session defaults resolved
// from the legacy databuddy headers.
type legacyConn struct {
	client  *dataagent.Client
	agentID string
}

// legacyHTTPContext extracts legacy per-connection credentials from HTTP
// headers. It also derives the tenant key used for session isolation: the
// explicit X-Data-Agent-Tenant-Id header wins, falling back to the credential
// fingerprint (stable for API Keys, empty for rotating STS credentials).
func (s *Server) legacyHTTPContext(ctx context.Context, r *http.Request) context.Context {
	var cred *dataagent.Credential

	akID := r.Header.Get("X-STS-Access-Key-Id")
	akSecret := r.Header.Get("X-STS-Access-Key-Secret")
	secToken := r.Header.Get("X-STS-Security-Token")
	if akID != "" && akSecret != "" && secToken != "" {
		cred = &dataagent.Credential{
			AccessKeyID:     akID,
			AccessKeySecret: akSecret,
			SecurityToken:   secToken,
		}
		log.Printf("connection using STS credentials (AK=%s...)", akID[:min(6, len(akID))])
	} else if apiKey := r.Header.Get("X-Data-Agent-Api-Key"); apiKey != "" {
		cred = &dataagent.Credential{APIKey: apiKey}
		log.Printf("connection using API Key (len=%d)", len(apiKey))
	}

	tenantKey := r.Header.Get("X-Data-Agent-Tenant-Id")
	if tenantKey == "" && cred != nil {
		tenantKey = cred.TenantKey()
	}
	if tenantKey != "" {
		ctx = context.WithValue(ctx, tenantKeyKey, tenantKey)
	}

	if cred == nil || s.clientFactory == nil {
		return ctx
	}

	client := s.clientFactory(cred)
	if wsID := r.Header.Get("X-Data-Agent-Workspace-Id"); wsID != "" {
		client.SetWorkspaceID(wsID)
		log.Printf("connection workspace_id=%s", wsID)
	}
	conn := &legacyConn{client: client, agentID: r.Header.Get("X-Data-Agent-Agent-Id")}
	if conn.agentID != "" {
		log.Printf("connection agent_id=%s", conn.agentID)
	}
	ctx = context.WithValue(ctx, legacyConnKey, conn)
	return session.WithClient(ctx, client)
}

// legacyFromCtx returns the legacy per-connection state, or nil for callers
// without legacy credential headers.
func legacyFromCtx(ctx context.Context) *legacyConn {
	lc, _ := ctx.Value(legacyConnKey).(*legacyConn)
	return lc
}

// tenantKeyFromCtx returns the per-connection tenant key ("" for default
// credential / stdio / identity-mode callers).
func tenantKeyFromCtx(ctx context.Context) string {
	if tk, ok := ctx.Value(tenantKeyKey).(string); ok {
		return tk
	}
	return ""
}

// visibleToTenant reports whether a session owned by ownerKey is visible to
// the caller identified by callerKey. Empty keys preserve the legacy
// single-tenant behavior: default-credential callers see everything, and
// legacy sessions without an owner remain visible to every caller.
func visibleToTenant(callerKey, ownerKey string) bool {
	return callerKey == "" || ownerKey == "" || callerKey == ownerKey
}

// checkSessionAccess enforces tenant isolation for session-scoped tools:
// callers with a per-connection identity cannot see sessions owned by a
// different tenant. Returns the snapshot on success.
func (s *Server) checkSessionAccess(ctx context.Context, sid string) (*session.StateSnapshot, error) {
	snap, err := s.mgr.GetStatus(sid)
	if err != nil {
		return nil, err
	}
	if !visibleToTenant(tenantKeyFromCtx(ctx), snap.TenantKey) {
		return nil, fmt.Errorf("session %s not found", sid)
	}
	return snap, nil
}
