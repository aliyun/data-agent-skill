package mcp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

const initMessage = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"local-security-test","version":"1"}}}`
const pingMessage = `{"jsonrpc":"2.0","id":2,"method":"ping"}`
const testHTTPSecret = "synthetic-local-jwt-secret-not-a-real-credential"

type httpFixture struct {
	s        *Server
	rt       *remoteTransport
	ts       *httptest.Server
	jwt      bool
	resolves atomic.Int64
}

func httpLimits() config.HTTP {
	l := config.DefaultHTTP()
	l.MaxSessions, l.MaxSessionsPerPrincipal = 8, 4
	l.MaxInflightRequests, l.MaxInflightPerSession = 4, 2
	return l
}
func newHTTPFixture(t *testing.T, kind string, limits config.HTTP, jwtMode bool) *httpFixture {
	t.Helper()
	cfg := config.Config{HTTP: limits, Identity: config.Identity{Enabled: true, RequireIdentity: true, Namespace: "transport-tests", AuthToken: "synthetic-platform-token", Groups: map[string]config.IdentityGroup{}}}
	group := config.IdentityGroup{RoleArn: "acs:ram::123456789012:role/synthetic", Users: []string{"A", "B"}}
	if jwtMode {
		cfg.Identity.JWT = config.JWT{Enabled: true, Secret: testHTTPSecret, Secrets: []string{"synthetic-rotation-secret"}}
		group.TenantID, group.AgentID = "tenant", "agent"
	}
	cfg.Identity.Groups["shared-role"] = group
	return newConfiguredHTTPFixture(t, kind, cfg)
}
func newConfiguredHTTPFixture(t *testing.T, kind string, cfg config.Config) *httpFixture {
	t.Helper()
	registry := tenant.NewRegistry(context.Background(), cfg, nil)
	t.Cleanup(func() { _ = registry.Close() })
	f := &httpFixture{s: New(nil, nil, "test"), jwt: cfg.Identity.JWT.Enabled}
	if f.jwt {
		f.s.EnableJWTIdentity(cfg.Identity.JWT.Header)
	}
	f.s.SetResolver(func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
		f.resolves.Add(1)
		return nil, nil, SessionDefaults{}, errors.New("unexpected resolver")
	})
	if err := f.s.ConfigureHTTP(cfg.HTTP, registry.Authenticate); err != nil {
		t.Fatal(err)
	}
	var err error
	f.rt, err = f.s.newRemoteTransport(context.Background(), kind)
	if err != nil {
		t.Fatal(err)
	}
	f.ts = httptest.NewUnstartedServer(f.rt)
	f.ts.Config = f.s.ownedHTTPServer("", f.rt)
	f.ts.Start()
	t.Cleanup(func() { f.rt.close(); f.ts.Close() })
	return f
}
func newSharedJWTHTTPFixture(t *testing.T, kind string, groups bool) *httpFixture {
	t.Helper()
	cfg := config.Config{HTTP: httpLimits(), Identity: config.Identity{
		Enabled: true, RequireIdentity: true, AuthToken: "synthetic-platform-token", MaxTenants: 128,
		SessionNameClaim: "email",
		JWT:              config.JWT{Enabled: true, Secret: testHTTPSecret},
		Default:          &config.IdentityGroup{RoleArn: "shared-role"},
	}}
	if groups {
		cfg.Identity.Default = nil
		cfg.Identity.Groups = map[string]config.IdentityGroup{
			"shared-role": {RoleArn: "shared-role", Users: []string{"A", "B", "a@example.test"}},
		}
	}
	cfg.ApplyDefaults()
	if err := cfg.ValidateTransport(kind); err != nil {
		t.Fatal(err)
	}
	return newConfiguredHTTPFixture(t, kind, cfg)
}
func signHTTPToken(t *testing.T, user, secret string, expires time.Time) string {
	t.Helper()
	return signHTTPClaims(t, jwt.MapClaims{"user_id": user, "tenant_id": "tenant", "agent_id": "agent", "exp": expires.Unix()}, secret)
}
func signHTTPClaims(t *testing.T, claims jwt.MapClaims, secret string) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func (f *httpFixture) request(t *testing.T, method, path, id, user, body string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		r.Header.Set("x-aily-token", "synthetic-platform-token")
		if f.jwt {
			r.Header.Set("x-aily-jwt", signHTTPToken(t, user, testHTTPSecret, time.Now().Add(time.Hour)))
		} else {
			r.Header.Set("x-aily-user", user)
		}
	}
	if id != "" {
		r.Header.Set("Mcp-Session-Id", id)
	}
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}
func (f *httpFixture) do(t *testing.T, r *http.Request) *http.Response {
	t.Helper()
	client := &http.Client{Timeout: 65 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func statusHTTP(t *testing.T, response *http.Response, want int) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("status=%d, want=%d, body=%s", response.StatusCode, want, body)
	}
	return string(body)
}
func (f *httpFixture) initialize(t *testing.T, user string) string {
	t.Helper()
	response := f.do(t, f.request(t, "POST", "/mcp", "", user, initMessage))
	id := response.Header.Get("Mcp-Session-Id")
	body := statusHTTP(t, response, 200)
	if id == "" || !strings.Contains(body, "protocolVersion") {
		t.Fatalf("missing initialized session: %s", body)
	}
	return id
}
func awaitHTTP(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("bounded wait timed out")
		}
	}
}
func (f *httpFixture) counts() (int, int) {
	f.rt.sessions.mu.Lock()
	defer f.rt.sessions.mu.Unlock()
	return len(f.rt.sessions.all), f.rt.sessions.inflight
}

type sseConnection struct {
	response *http.Response
	events   chan string
	id       string
}

func (f *httpFixture) connectSSE(t *testing.T, user string) *sseConnection {
	t.Helper()
	return f.connectSSERequest(t, f.request(t, "GET", "/sse", "", user, ""))
}
func (f *httpFixture) connectSSERequest(t *testing.T, request *http.Request) *sseConnection {
	t.Helper()
	resp := f.do(t, request)
	if resp.StatusCode != 200 {
		statusHTTP(t, resp, 200)
	}
	c := &sseConnection{response: resp, events: make(chan string, 16)}
	go func() {
		defer close(c.events)
		scan := bufio.NewScanner(resp.Body)
		for scan.Scan() {
			if strings.HasPrefix(scan.Text(), "data: ") {
				select {
				case c.events <- strings.TrimSpace(strings.TrimPrefix(scan.Text(), "data: ")):
				default:
					return
				}
			}
		}
	}()
	endpoint := c.next(t)
	u, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	c.id = u.Query().Get("sessionId")
	if c.id == "" {
		t.Fatalf("no issued SSE ID: %s", endpoint)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return c
}
func (c *sseConnection) next(t *testing.T) string {
	t.Helper()
	select {
	case event, ok := <-c.events:
		if !ok {
			t.Fatal("SSE closed before event")
		}
		return event
	case <-time.After(3 * time.Second):
		t.Fatal("SSE event timed out")
		return ""
	}
}
func (f *httpFixture) ssePost(t *testing.T, c *sseConnection, user, body string, want int) {
	t.Helper()
	statusHTTP(t, f.do(t, f.request(t, "POST", "/message?sessionId="+c.id, "", user, body)), want)
}
func (f *httpFixture) initializeSSE(t *testing.T, c *sseConnection) {
	t.Helper()
	f.ssePost(t, c, "A", initMessage, 202)
	if event := c.next(t); !strings.Contains(event, "protocolVersion") {
		t.Fatalf("initialize failed: %s", event)
	}
	awaitHTTP(t, func() bool { _, n := f.counts(); return n == 0 })
}

func TestLegacyJWTMappingDoesNotShareProtocolSessions(t *testing.T) {
	for _, kind := range []string{"streamable-http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			cfg := config.Config{HTTP: httpLimits(), Identity: config.Identity{
				Enabled: true, RequireIdentity: true, AuthToken: "synthetic-platform-token", MaxTenants: 128,
				JWT:     config.JWT{Enabled: true, Secret: testHTTPSecret},
				Default: &config.IdentityGroup{RoleArn: "shared-role"},
			}}
			cfg.ApplyDefaults()
			if err := cfg.ValidateTransport(kind); err != nil {
				t.Fatal(err)
			}
			f := newConfiguredHTTPFixture(t, kind, cfg)
			path, id, status := "/mcp", "", 200
			var stream *sseConnection
			if kind == "sse" {
				stream = f.connectSSE(t, "A")
				f.initializeSSE(t, stream)
				path, status = "/message?sessionId="+stream.id, 202
			} else {
				id = f.initialize(t, "A")
			}
			for _, scope := range [][3]string{{"other-tenant", "agent", "A"}, {"tenant", "other-agent", "A"}, {"tenant", "agent", "B"}} {
				token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
					"tenant_id": scope[0], "agent_id": scope[1], "user_id": scope[2], "exp": time.Now().Add(time.Hour).Unix(),
				}).SignedString([]byte(testHTTPSecret))
				if err != nil {
					t.Fatal(err)
				}
				req := f.request(t, "POST", path, id, "A", pingMessage)
				req.Header.Set("x-aily-jwt", token)
				statusHTTP(t, f.do(t, req), 404)
			}
			req := f.request(t, "POST", path, id, "A", pingMessage)
			req.Header.Del("x-aily-jwt")
			statusHTTP(t, f.do(t, req), 401)
			req = f.request(t, "POST", path, id, "A", pingMessage)
			req.Header.Del("x-aily-jwt")
			req.Header.Set("x-aily-user", "A")
			statusHTTP(t, f.do(t, req), 404)
			req = f.request(t, "POST", path, id, "A", pingMessage)
			req.Header.Set("x-aily-token", "incorrect-platform-token")
			statusHTTP(t, f.do(t, req), 401)
			statusHTTP(t, f.do(t, f.request(t, "POST", path, id, "A", pingMessage)), status)
			if stream != nil {
				if response := stream.next(t); !strings.Contains(response, `"id":2`) {
					t.Fatal("owner could not use its SSE session")
				}
			}
		})
	}
}

func TestSharedJWTHTTPLifecycleAndPrincipalIsolation(t *testing.T) {
	for _, kind := range []string{"streamable-http", "sse"} {
		for _, groups := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/groups=%t", kind, groups), func(t *testing.T) {
				f := newSharedJWTHTTPFixture(t, kind, groups)
				type caller struct{ name, mode, user, email, tenantID, agentID string }
				callers := []caller{
					{"header-user-email-fallback", "headers", "A", "", "", ""},
					{"header-email-only", "headers", "", "a@example.test", "", ""},
					{"jwt-no-scope", "jwt", "A", "a@example.test", "", ""},
					{"jwt-tenant-only", "jwt", "A", "a@example.test", "tenant", ""},
					{"jwt-agent-only", "jwt", "A", "a@example.test", "", "agent"},
					{"jwt-full-scope", "jwt", "A", "a@example.test", "tenant", "agent"},
					{"jwt-other-user", "jwt", "B", "b@example.test", "tenant", "agent"},
				}
				request := func(c caller, method, path, id, body string) *http.Request {
					r := f.request(t, method, path, id, "", body)
					r.Header.Set("x-aily-token", "synthetic-platform-token")
					if c.mode == "headers" {
						if c.user != "" {
							r.Header.Set("x-aily-user", c.user)
						}
						if c.email != "" {
							r.Header.Set("x-aily-email", c.email)
						}
					} else {
						claims := jwt.MapClaims{"user_id": c.user, "email": c.email, "exp": time.Now().Add(time.Hour).Unix()}
						if c.tenantID != "" {
							claims["tenant_id"] = c.tenantID
						}
						if c.agentID != "" {
							claims["agent_id"] = c.agentID
						}
						r.Header.Set("x-aily-jwt", signHTTPClaims(t, claims, testHTTPSecret))
						// Mixed plain headers must never replace either signed field.
						r.Header.Set("x-aily-user", "spoofed-user")
						r.Header.Set("x-aily-email", "spoofed@example.test")
					}
					return r
				}
				// Exercise the real tenant-dispatch wrapper with a wholly local resolver
				// and handler: neither the empty manager nor client performs any I/O.
				observed := make(chan *tenant.Principal, len(callers)+1)
				f.s.SetResolver(func(ctx context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
					f.resolves.Add(1)
					if tenant.PrincipalFromContext(ctx) == nil {
						return nil, nil, SessionDefaults{}, errors.New("missing verified principal")
					}
					return &session.Manager{}, &dataagent.Client{}, SessionDefaults{}, nil
				})
				f.s.mcp.AddTool(mcp.NewTool("local_identity_probe"), f.s.withTenant(func(_ *Server, ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
					select {
					case observed <- tenant.PrincipalFromContext(ctx):
						return mcp.NewToolResultText("local-dispatch-ok"), nil
					default:
						return nil, errors.New("unexpected additional local dispatch")
					}
				}))
				type protocolSession struct {
					owner    caller
					path, id string
					stream   *sseConnection
				}
				post := func(p *protocolSession, body string, notification bool) string {
					resp := f.do(t, request(p.owner, "POST", p.path, p.id, body))
					want := 200
					if kind == "sse" || notification {
						want = 202
					}
					if kind != "sse" && p.id == "" {
						p.id = resp.Header.Get("Mcp-Session-Id")
					}
					result := statusHTTP(t, resp, want)
					if p.stream != nil && !notification {
						result = p.stream.next(t)
					}
					awaitHTTP(t, func() bool { _, in := f.counts(); return in == 0 })
					return result
				}
				const list = `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`
				const call = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"local_identity_probe"}}`
				var sessions []*protocolSession
				for _, c := range callers {
					p := &protocolSession{owner: c, path: "/mcp"}
					if kind == "sse" {
						p.stream = f.connectSSERequest(t, request(c, "GET", "/sse", "", ""))
						p.path = "/message?sessionId=" + p.stream.id
					}
					if result := httpRPCResult(t, post(p, initMessage, false), 1); !strings.Contains(string(result), "protocolVersion") {
						t.Fatalf("%s: initialize failed: %s", c.name, result)
					}
					if p.stream == nil && p.id == "" {
						t.Fatalf("%s: initialize did not issue a session", c.name)
					}
					post(p, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, true)
					if result := httpRPCResult(t, post(p, list, false), 3); !strings.Contains(string(result), `"local_identity_probe"`) {
						t.Fatalf("%s: local tool not listed: %s", c.name, result)
					}
					result := httpRPCResult(t, post(p, call, false), 4)
					if !strings.Contains(string(result), "local-dispatch-ok") || strings.Contains(string(result), `"isError":true`) {
						t.Fatalf("%s: tool dispatch failed: %s", c.name, result)
					}
					var principal *tenant.Principal
					select {
					case principal = <-observed:
					default:
						t.Fatalf("%s: tool handler was not dispatched", c.name)
					}
					subjectKind, subject := "user_id", c.user
					if subject == "" {
						subjectKind, subject = "email", c.email
					}
					canonical, err := json.Marshal([7]string{"principal-v2", "default", c.mode, c.tenantID, c.agentID, subjectKind, subject})
					if err != nil {
						t.Fatal(err)
					}
					if principal == nil || principal.ID != fmt.Sprintf("%x", sha256.Sum256(canonical)) || principal.Namespace != "default" || principal.AuthMode != c.mode || principal.UserID != c.user || principal.Email != c.email || principal.TenantID != c.tenantID || principal.AgentID != c.agentID || principal.SubjectKind != subjectKind || principal.SubjectValue != subject {
						t.Fatalf("%s: incorrect verified principal: %+v", c.name, principal)
					}
					sessions = append(sessions, p)
				}
				// Every direction is checked, including full/partial/absent JWT scopes
				// and Header/JWT identities with the same user or email.
				for _, p := range sessions {
					for _, other := range callers {
						if other.name == p.owner.name {
							continue
						}
						methods := []string{"POST"}
						if kind != "sse" {
							methods = append(methods, "GET", "DELETE")
						}
						for _, method := range methods {
							body := ""
							if method == "POST" {
								body = call
							}
							r := request(other, method, p.path, p.id, body)
							if other.mode == "jwt" {
								r.Header.Set("x-aily-user", p.owner.user)
								r.Header.Set("x-aily-email", p.owner.email)
							}
							t.Run(p.owner.name+"/reject/"+other.name+"/"+method, func(t *testing.T) {
								f.rejectBeforeDispatch(t, r, 404)
							})
						}
					}
					// Removing spoofed headers, or supplying email after Header fallback,
					// must leave the legitimate owner's protocol principal unchanged.
					r := request(p.owner, "POST", p.path, p.id, pingMessage)
					if p.owner.mode == "jwt" {
						r.Header.Del("x-aily-user")
						r.Header.Del("x-aily-email")
					} else if p.owner.user != "" {
						r.Header.Set("x-aily-email", "a@example.test")
					}
					want := 200
					if p.stream != nil {
						want = 202
					}
					result := statusHTTP(t, f.do(t, r), want)
					if p.stream != nil {
						result = p.stream.next(t)
					}
					httpRPCResult(t, result, 2)
					awaitHTTP(t, func() bool { _, in := f.counts(); return in == 0 })
				}
				if got := f.resolves.Load(); got != int64(len(callers)) {
					t.Fatalf("resolver calls=%d, want=%d", got, len(callers))
				}
				for _, p := range sessions {
					if p.stream != nil {
						_ = p.stream.response.Body.Close()
					} else {
						statusHTTP(t, f.do(t, request(p.owner, "DELETE", p.path, p.id, "")), 200)
					}
				}
				awaitHTTP(t, func() bool { n, in := f.counts(); return n == 0 && in == 0 })
				for _, p := range sessions {
					f.rejectBeforeDispatch(t, request(p.owner, "POST", p.path, p.id, call), 404)
				}
			})
		}
	}
}

func httpRPCResult(t *testing.T, body string, id int) json.RawMessage {
	t.Helper()
	var response struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil || response.ID != id || len(response.Error) != 0 || len(response.Result) == 0 {
		t.Fatalf("unexpected RPC response for id=%d: %s (decode error: %v)", id, body, err)
	}
	return response.Result
}

func TestHTTPRemoteRequiresConfiguration(t *testing.T) {
	s := New(nil, nil, "test")
	for _, kind := range []string{"sse", "streamable-http"} {
		if _, err := s.newRemoteTransport(context.Background(), kind); err == nil {
			t.Fatal("unconfigured remote transport launched")
		}
		t.Setenv("MCP_TRANSPORT", kind)
		if err := s.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "authentication") {
			t.Fatalf("missing runtime auth gate: %v", err)
		}
	}
	if err := s.ConfigureHTTP(config.HTTP{}, func(ctx context.Context) (context.Context, error) { return ctx, nil }); err == nil {
		t.Fatal("accepted zero bounds")
	}
	// No HTTP auth hook is applied to local stdio dispatch.
	if result := s.mcp.HandleMessage(context.Background(), json.RawMessage(pingMessage)); result == nil {
		t.Fatal("local dispatch changed")
	}
}

type httpAuthRoute struct{ method, path, body string }

func httpAuthenticationRoutes(kind string) []httpAuthRoute {
	paths := []httpAuthRoute{
		{"POST", "/mcp", initMessage},
		{"POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
		{"POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"data_agent_list_workspaces"}}`},
		{"POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{"GET", "/mcp", ""},
		{"DELETE", "/mcp", ""},
	}
	if kind == "sse" {
		paths = paths[:4]
		for i := range paths {
			paths[i].path = "/message?sessionId=synthetic-unknown"
		}
		paths = append(paths, httpAuthRoute{"GET", "/sse", ""})
	}
	return paths
}

func TestSharedJWTHTTPAuthenticationCannotDowngrade(t *testing.T) {
	for _, kind := range []string{"streamable-http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			f := newSharedJWTHTTPFixture(t, kind, false)
			request := func(method, path, id, body string) *http.Request {
				r := f.request(t, method, path, id, "", body)
				r.Header.Set("x-aily-token", "synthetic-platform-token")
				r.Header.Set("x-aily-user", "A")
				r.Header.Set("x-aily-email", "a@example.test")
				return r
			}
			path, id, want := "/mcp", "", 200
			var stream *sseConnection
			if kind == "sse" {
				stream = f.connectSSERequest(t, request("GET", "/sse", "", ""))
				path, want = "/message?sessionId="+stream.id, 202
			}
			resp := f.do(t, request("POST", path, "", initMessage))
			if kind != "sse" {
				id = resp.Header.Get("Mcp-Session-Id")
				if id == "" {
					statusHTTP(t, resp, 200)
					t.Fatal("Header initialization did not issue a session")
				}
			}
			body := statusHTTP(t, resp, want)
			if stream != nil {
				body = stream.next(t)
			}
			httpRPCResult(t, body, 1)
			awaitHTTP(t, func() bool { _, in := f.counts(); return in == 0 })
			claims := jwt.MapClaims{"user_id": "A", "email": "a@example.test", "exp": time.Now().Add(time.Hour).Unix()}
			valid := signHTTPClaims(t, claims, testHTTPSecret)
			badSignature := signHTTPClaims(t, claims, "synthetic-wrong-signing-key")
			claims["exp"] = time.Now().Add(-time.Minute).Unix()
			expired := signHTTPClaims(t, claims, testHTTPSecret)
			claims["exp"] = time.Now().Add(time.Hour).Unix()
			delete(claims, "email")
			missingEmail := signHTTPClaims(t, claims, testHTTPSecret)
			claims["email"] = " "
			blankEmail := signHTTPClaims(t, claims, testHTTPSecret)
			for _, route := range httpAuthenticationRoutes(kind) {
				if route.method == "POST" {
					route.path = path
				}
				for _, invalid := range []struct{ name, token string }{
					{"empty", ""}, {"whitespace", " \t "}, {"malformed", "bad"},
					{"bad-signature", badSignature}, {"expired", expired},
					{"missing-selected-email", missingEmail}, {"blank-selected-email", blankEmail},
				} {
					t.Run(route.method+"/"+route.body+"/jwt-"+invalid.name, func(t *testing.T) {
						r := request(route.method, route.path, id, route.body)
						// Set, not Del: even a present-but-empty JWT forbids Header fallback.
						r.Header.Set("x-aily-jwt", invalid.token)
						f.rejectBeforeDispatch(t, r, 401)
					})
				}
				for _, jwtPresent := range []bool{false, true} {
					for _, token := range []string{"", "wrong-platform-token"} {
						t.Run(fmt.Sprintf("%s/%s/jwt=%t/platform=%q", route.method, route.body, jwtPresent, token), func(t *testing.T) {
							r := request(route.method, route.path, id, route.body)
							if jwtPresent {
								r.Header.Set("x-aily-jwt", valid)
							}
							if token == "" {
								r.Header.Del("x-aily-token")
							} else {
								r.Header.Set("x-aily-token", token)
							}
							f.rejectBeforeDispatch(t, r, 401)
						})
					}
				}
				r := request(route.method, route.path, id, route.body)
				r.Header.Del("x-aily-user")
				r.Header.Del("x-aily-email")
				f.rejectBeforeDispatch(t, r, 401)
			}
			if f.resolves.Load() != 0 {
				t.Fatal("rejected credentials reached tenant/STS resolver")
			}
			if n, in := f.counts(); n != 1 || in != 0 {
				t.Fatalf("rejected requests changed live reservations: %d/%d", n, in)
			}
			body = statusHTTP(t, f.do(t, request("POST", path, id, pingMessage)), want)
			if stream != nil {
				body = stream.next(t)
			}
			httpRPCResult(t, body, 2)
		})
	}
}

func TestHTTPDisabledJWTRejectsHeaderPresence(t *testing.T) {
	for _, kind := range []string{"streamable-http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			f := newHTTPFixture(t, kind, httpLimits(), false)
			for _, route := range httpAuthenticationRoutes(kind) {
				for _, token := range []string{"", " \t ", "bad", signHTTPToken(t, "A", testHTTPSecret, time.Now().Add(time.Hour))} {
					r := f.request(t, route.method, route.path, "", "A", route.body)
					r.Header.Set("x-aily-jwt", token)
					f.rejectBeforeDispatch(t, r, 400)
				}
			}
			if n, in := f.counts(); n != 0 || in != 0 {
				t.Fatalf("disabled JWT reserved sessions: %d/%d", n, in)
			}
		})
	}
}

func TestHTTPAuthenticationBeforeBodyAndDispatch(t *testing.T) {
	for _, kind := range []string{"sse", "streamable-http"} {
		for _, gateway := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gateway=%t", kind, gateway), func(t *testing.T) {
				f := newHTTPFixture(t, kind, httpLimits(), true)
				f.s.EnableGateway(gateway, false, true)
				paths := httpAuthenticationRoutes(kind)
				for _, route := range paths {
					for _, variant := range []string{"missing-platform", "wrong-platform", "missing-jwt", "bad-jwt", "expired", "missing-scope", "missing-tenant", "missing-agent", "header-unmapped", "unmapped", "invalid-jwt-with-headers", "unmapped-jwt-with-headers"} {
						r := f.request(t, route.method, route.path, "", "A", route.body)
						want := 401
						switch variant {
						case "missing-platform":
							r.Header.Del("x-aily-token")
						case "wrong-platform":
							r.Header.Set("x-aily-token", "wrong")
						case "missing-jwt":
							r.Header.Del("x-aily-jwt")
						case "bad-jwt":
							r.Header.Set("x-aily-jwt", "bad")
						case "expired":
							r.Header.Set("x-aily-jwt", signHTTPToken(t, "A", testHTTPSecret, time.Now().Add(-time.Second)))
						case "missing-scope", "missing-tenant", "missing-agent":
							claims := jwt.MapClaims{"user_id": "A", "exp": time.Now().Add(time.Hour).Unix()}
							if variant == "missing-tenant" {
								claims["agent_id"] = "agent"
							}
							if variant == "missing-agent" {
								claims["tenant_id"] = "tenant"
							}
							r.Header.Set("x-aily-jwt", signHTTPClaims(t, claims, testHTTPSecret))
							want = 403
						case "header-unmapped":
							r.Header.Del("x-aily-jwt")
							r.Header.Set("x-aily-user", "A")
							r.Header.Set("x-aily-email", "a@example.test")
							want = 403
						case "unmapped", "unmapped-jwt-with-headers":
							r.Header.Set("x-aily-jwt", signHTTPToken(t, "C", testHTTPSecret, time.Now().Add(time.Hour)))
							if variant == "unmapped-jwt-with-headers" {
								r.Header.Set("x-aily-user", "A")
								r.Header.Set("x-aily-email", "a@example.test")
							}
							want = 403
						case "invalid-jwt-with-headers":
							r.Header.Set("x-aily-jwt", "bad")
							r.Header.Set("x-aily-user", "A")
							r.Header.Set("x-aily-email", "a@example.test")
						}
						t.Run(route.method+"/"+route.path+"/"+variant, func(t *testing.T) {
							f.rejectBeforeDispatch(t, r, want)
						})
					}
				}
				if f.resolves.Load() != 0 {
					t.Fatal("preauth rejection reached tenant/STS resolver")
				}
				if n, in := f.counts(); n != 0 || in != 0 {
					t.Fatalf("unauthenticated reservations=%d/%d", n, in)
				}
				// A hostile body must not be read even once by the application.
				r := f.request(t, "POST", paths[0].path, "", "", "")
				r.Body = &neverReadBody{t: t}
				r.ContentLength = -1
				w := httptest.NewRecorder()
				f.rt.ServeHTTP(w, r)
				if w.Code != 401 {
					t.Fatalf("preauth status %d", w.Code)
				}
			})
		}
	}
}

func (f *httpFixture) rejectBeforeDispatch(t *testing.T, request *http.Request, want int) {
	t.Helper()
	before := f.resolves.Load()
	statusHTTP(t, f.do(t, request), want)
	if request.Method == http.MethodPost {
		probe := request.Clone(context.Background())
		probe.Body = &neverReadBody{t: t}
		probe.ContentLength = -1
		w := httptest.NewRecorder()
		f.rt.ServeHTTP(w, probe)
		if w.Code != want {
			t.Fatalf("rejection with unread body: status=%d, want=%d", w.Code, want)
		}
	}
	if f.resolves.Load() != before {
		t.Fatal("rejected request reached tenant/STS resolver")
	}
}

type neverReadBody struct{ t *testing.T }

func (b *neverReadBody) Read([]byte) (int, error) {
	b.t.Error("read body before authentication")
	return 0, io.EOF
}
func (*neverReadBody) Close() error { return nil }

func TestHTTPBoundsAndAmbiguousEnvelopes(t *testing.T) {
	limits := httpLimits()
	limits.MaxRequestBytes = 256
	limits.MaxHeaderBytes = 2048
	f := newHTTPFixture(t, "streamable-http", limits, false)
	for _, size := range []int{255, 256, 257} {
		for _, unknownLength := range []bool{false, true} {
			body := initMessage + strings.Repeat(" ", size-len(initMessage))
			r := f.request(t, "POST", "/mcp", "", "A", body)
			if unknownLength {
				r.ContentLength = -1
			}
			want := 200
			if size > 256 {
				want = 413
			}
			resp := f.do(t, r)
			id := resp.Header.Get("Mcp-Session-Id")
			statusHTTP(t, resp, want)
			if id != "" {
				statusHTTP(t, f.do(t, f.request(t, "DELETE", "/mcp", id, "A", "")), 200)
			}
		}
	}
	for _, body := range []string{`[]`, initMessage + ` {}`, `{"jsonrpc":"2.0","method":"ping","method":"initialize","id":1}`, `{"jsonrpc":"2.0","method":"initialize","id":1,"\u0069d":2}`, `{"jsonrpc":"2.0","method":"ping","Method":"initialize","id":1}`, `{"jsonrpc":"2.0","method":"initialize"}`, `{"jsonrpc":"2.0","method":"initialize","id":null}`, `{"jsonrpc":"2.0","method":"initialize","id":1,"result":{}}`, `{"jsonrpc":"1.0","method":"initialize","id":1}`} {
		statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", "", "A", body)), 400)
	}
	for _, header := range []string{"x-aily-user", "x-aily-token", "Mcp-Session-Id", "Content-Type"} {
		r := f.request(t, "POST", "/mcp", "", "A", initMessage)
		r.Header.Add(header, "x")
		r.Header.Add(header, "y")
		statusHTTP(t, f.do(t, r), 400)
	}
	r := f.request(t, "POST", "/mcp", "", "A", initMessage)
	r.Header.Set("Content-Encoding", "gzip")
	statusHTTP(t, f.do(t, r), 415)
	r = f.request(t, "POST", "/mcp", "", "A", initMessage)
	r.Header.Set("X-Filler", strings.Repeat("a", 3000))
	statusHTTP(t, f.do(t, r), 431)
	if n, in := f.counts(); n != 0 || in != 0 {
		t.Fatalf("failed body leaked reservations %d/%d", n, in)
	}
	deep := `{"jsonrpc":"2.0","method":"initialize","id":1,"params":` + strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65) + "}"
	if _, err := inspectMessage([]byte(deep)); err == nil {
		t.Fatal("accepted excessive nesting")
	}
}

func TestHTTPOwnerIssuedIDsInitializeFailureAndDelete(t *testing.T) {
	f := newHTTPFixture(t, "streamable-http", httpLimits(), true)
	id := f.initialize(t, "A")
	for _, other := range []string{"B", ""} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			body := ""
			if method == "POST" {
				body = pingMessage
			}
			want := 404
			if other == "" {
				want = 401
			}
			statusHTTP(t, f.do(t, f.request(t, method, "/mcp", id, other, body)), want)
		}
	}
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", id, "A", initMessage)), 400)
	for _, method := range []string{"GET", "POST", "DELETE"} {
		body := ""
		if method == "POST" {
			body = pingMessage
		}
		statusHTTP(t, f.do(t, f.request(t, method, "/mcp", "mcp-session-12345678-1234-1234-1234-123456789012", "A", body)), 404)
	}
	// Same canonical principal, different accepted signing key.
	r := f.request(t, "POST", "/mcp", id, "A", pingMessage)
	r.Header.Set("x-aily-jwt", signHTTPToken(t, "A", "synthetic-rotation-secret", time.Now().Add(2*time.Hour)))
	statusHTTP(t, f.do(t, r), 200)
	get := f.do(t, f.request(t, "GET", "/mcp", id, "A", ""))
	if get.StatusCode != 202 {
		t.Fatalf("GET status %d", get.StatusCode)
	}
	statusHTTP(t, f.do(t, f.request(t, "GET", "/mcp", id, "A", "")), 409)
	_ = get.Body.Close()
	awaitHTTP(t, func() bool {
		f.rt.sessions.mu.Lock()
		defer f.rt.sessions.mu.Unlock()
		return !f.rt.sessions.ids[id].stream
	})
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", id, "A", pingMessage)), 200)
	statusHTTP(t, f.do(t, f.request(t, "DELETE", "/mcp", id, "A", "")), 200)
	statusHTTP(t, f.do(t, f.request(t, "GET", "/mcp", id, "A", "")), 404)
	// Struct decoding fails AFTER an ID was generated/bound. It must not be
	// published or retained merely because HTTP status happens to be 200.
	bad := strings.Replace(initMessage, `"params":{`, `"params":{"protocolVersion":{},`, 1)
	resp := f.do(t, f.request(t, "POST", "/mcp", "", "A", bad))
	if resp.Header.Get("Mcp-Session-Id") != "" {
		t.Fatal("failed initialization published an ID")
	}
	statusHTTP(t, resp, 200)
	awaitHTTP(t, func() bool { n, in := f.counts(); return n == 0 && in == 0 })
}

func TestSSE202ActualCompletionAndDisconnect(t *testing.T) {
	limits := httpLimits()
	limits.MaxInflightRequests = 1
	limits.MaxInflightPerSession = 1
	f := newHTTPFixture(t, "sse", limits, false)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.s.mcp.AddTool(mcp.NewTool("blocked"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-time.After(3 * time.Second):
			return nil, errors.New("cancellation timeout")
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		return mcp.NewToolResultText("done"), nil
	})
	c := f.connectSSE(t, "A")
	f.initializeSSE(t, c)
	f.ssePost(t, c, "B", pingMessage, 404)
	f.ssePost(t, c, "", pingMessage, 401)
	select {
	case event := <-c.events:
		t.Fatalf("cross-owner event: %s", event)
	default:
	}
	f.ssePost(t, c, "A", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"blocked"}}`, 202)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	select {
	case <-cancelled:
		t.Fatal("normal POST completion canceled accepted SSE task")
	default:
	}
	if _, in := f.counts(); in != 1 {
		t.Fatalf("202 prematurely released quota: %d", in)
	}
	f.ssePost(t, c, "A", pingMessage, 429)
	_ = c.response.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("SSE disconnect failed to cancel actual handler")
	}
	if n, in := f.counts(); n != 1 || in != 1 {
		t.Fatalf("cancellation released running task: %d/%d", n, in)
	}
	close(release)
	awaitHTTP(t, func() bool { n, in := f.counts(); return n == 0 && in == 0 })
	f.ssePost(t, c, "A", pingMessage, 404)
}

func TestSSECompletionAllProtocolBranches(t *testing.T) {
	f := newHTTPFixture(t, "sse", httpLimits(), false)
	f.s.mcp.AddTool(mcp.NewTool("panic"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) { panic("synthetic") })
	c := f.connectSSE(t, "A")
	f.initializeSSE(t, c)
	for _, body := range []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":42}}`, `{"jsonrpc":"2.0","method":"unknown/notification"}`, `{"jsonrpc":"2.0","id":4,"method":"unknown/method"}`, `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":{}}}`, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"missing"}}`, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"panic"}}`, pingMessage} {
		f.ssePost(t, c, "A", body, 202)
		awaitHTTP(t, func() bool { _, in := f.counts(); return in == 0 })
		if strings.Contains(body, `"id":`) {
			c.next(t)
		}
	}
	f.ssePost(t, c, "A", `{"jsonrpc":"2.0","id":1,"method":"ping","id":2}`, 400)
	f.ssePost(t, c, "A", initMessage, 400)
}

func TestHTTPDeleteCancelsButRetainsActualQuota(t *testing.T) {
	f := newHTTPFixture(t, "streamable-http", httpLimits(), false)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.s.mcp.AddTool(mcp.NewTool("blocked"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-time.After(3 * time.Second):
			return nil, errors.New("cancellation timeout")
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		return mcp.NewToolResultText("done"), nil
	})
	id := f.initialize(t, "A")
	result := make(chan struct{})
	r := f.request(t, "POST", "/mcp", id, "A", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"blocked"}}`)
	go func() {
		defer close(result)
		resp, err := http.DefaultClient.Do(r)
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	statusHTTP(t, f.do(t, f.request(t, "DELETE", "/mcp", id, "A", "")), 200)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("DELETE did not cancel")
	}
	if n, in := f.counts(); n != 1 || in != 1 {
		t.Fatalf("DELETE freed running work %d/%d", n, in)
	}
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", id, "A", pingMessage)), 404)
	close(release)
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("handler did not finish")
	}
	awaitHTTP(t, func() bool { n, in := f.counts(); return n == 0 && in == 0 })
}

func TestHTTPCapacityIdleAndStreamLease(t *testing.T) {
	limits := httpLimits()
	limits.MaxSessions = 2
	limits.MaxSessionsPerPrincipal = 1
	limits.SessionIdleTimeoutSeconds = 1
	limits.MaxStreamLifetimeSeconds = 1
	f := newHTTPFixture(t, "streamable-http", limits, false)
	id := f.initialize(t, "A")
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", "", "A", initMessage)), 429)
	other := f.initialize(t, "B")
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", "", "B", initMessage)), 429)
	get := f.do(t, f.request(t, "GET", "/mcp", id, "A", ""))
	if get.StatusCode != 202 {
		t.Fatalf("GET status %d", get.StatusCode)
	}
	if _, err := io.ReadAll(get.Body); err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Fatal(err)
	}
	get.Body.Close()
	// Header-mode HTTP stream expiry closes only the stream, not the session.
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", id, "A", pingMessage)), 200)
	f.rt.sessions.sweep(time.Now().Add(2 * time.Second))
	for _, sid := range []string{id, other} {
		statusHTTP(t, f.do(t, f.request(t, "GET", "/mcp", sid, "A", "")), 404)
	}
	awaitHTTP(t, func() bool { n, _ := f.counts(); return n == 0 })
}

func TestHTTPReadTimeoutAndHeaderParserBudget(t *testing.T) {
	limits := httpLimits()
	limits.ReadBodyTimeoutSeconds = 1
	limits.ReadHeaderTimeoutSeconds = 1
	limits.MaxHeaderBytes = 512
	f := newHTTPFixture(t, "streamable-http", limits, false)
	connect := func() net.Conn {
		t.Helper()
		c, err := net.DialTimeout("tcp", f.ts.Listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(4 * time.Second))
		t.Cleanup(func() { c.Close() })
		return c
	}
	c := connect()
	fmt.Fprintf(c, "POST /mcp HTTP/1.1\r\nHost: local\r\nx-aily-token: synthetic-platform-token\r\nx-aily-user: A\r\nContent-Type: application/json\r\nContent-Length: 99\r\n\r\n{")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	statusHTTP(t, resp, 408)
	c = connect()
	fmt.Fprintf(c, "POST /mcp HTTP/1.1\r\nHost: local\r\nX-Slow: ")
	if _, err := bufio.NewReader(c).ReadByte(); err == nil {
		t.Fatal("slow headers unexpectedly accepted")
	}
	// net/http adds a fixed 4096-byte slop to MaxHeaderBytes. The outer
	// handler enforces the configured budget within that parser allowance.
	c = connect()
	fmt.Fprintf(c, "GET /mcp HTTP/1.1\r\nHost: local\r\nX-Filler: %s\r\n\r\n", strings.Repeat("a", 512+4096+128))
	resp, err = http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	statusHTTP(t, resp, 431)
	h := f.ts.Config
	if h.ReadTimeout != 0 || h.WriteTimeout != 0 {
		t.Fatal("common timeout breaks long MCP waits")
	}
}

func FuzzHTTPMessageBoundary(f *testing.F) {
	for _, seed := range []string{initMessage, pingMessage, `[]`, `{"jsonrpc":"2.0","method":"initialize","id":1,"\\u0069d":2}`, `{"jsonrpc":"2.0","method":"initialize","id":1,"reſult":{}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		parsed, err := inspectMessage([]byte(body))
		if err != nil {
			return
		}
		var dependencyEnvelope struct {
			JSONRPC, Method string
			ID              any
			Result          any
		}
		if json.Unmarshal([]byte(body), &dependencyEnvelope) != nil || dependencyEnvelope.JSONRPC != "2.0" || dependencyEnvelope.Method != parsed.method || dependencyEnvelope.Result != nil {
			t.Fatal("boundary and dependency disagree")
		}
		if parsed.method == "initialize" && dependencyEnvelope.ID == nil {
			t.Fatal("notification classified as initialization")
		}
	})
}

func TestMCP032HooksDoNotCoverCompletion(t *testing.T) {
	var starts, successes, failures int
	h := &server.Hooks{}
	h.AddOnRequestInitialization(func(context.Context, any, any) error { starts++; return nil })
	h.AddOnSuccess(func(context.Context, any, mcp.MCPMethod, any, any) { successes++ })
	h.AddOnError(func(context.Context, any, mcp.MCPMethod, any, error) { failures++ })
	s := server.NewMCPServer("hooks", "test", server.WithHooks(h))
	for _, body := range []string{`{`, `{"jsonrpc":"1.0","id":1,"method":"ping"}`, `{"jsonrpc":"2.0","method":"unknown/notification"}`, `{"jsonrpc":"2.0","id":1,"result":{}}`} {
		s.HandleMessage(context.Background(), json.RawMessage(body))
	}
	if starts != 0 || successes != 0 || failures != 0 {
		t.Fatalf("unexpected early hooks %d/%d/%d", starts, successes, failures)
	}
	s.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"unknown/method"}`))
	if starts != 1 || successes != 0 || failures != 0 {
		t.Fatal("unknown method lacks a terminal hook; adapter assumption changed")
	}
	h.AddOnRequestInitialization(func(context.Context, any, any) error { return errors.New("rejected") })
	s.HandleMessage(context.Background(), json.RawMessage(pingMessage))
	if successes != 0 || failures != 0 {
		t.Fatal("initialization rejection terminal hook assumption changed")
	}
}
