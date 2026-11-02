package dataagent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Keep the production clients/policies, injecting only a local TLS transport.
// Both receivers count requests independently, including method-changing redirects.
func signedRedirectFixture(t *testing.T, status int, destination string) (*Client, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var initial, redirected atomic.Int32
	targetHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		io.WriteString(w, `{"Success":true,"Data":[]}`)
	})
	var target *httptest.Server
	if destination == "downgrade" {
		target = httptest.NewServer(targetHandler)
	} else {
		target = httptest.NewTLSServer(targetHandler)
	}
	t.Cleanup(target.Close)
	cred := &Credential{AccessKeyID: "redirect-test-id", AccessKeySecret: "redirect-test-secret", SecurityToken: "redirect-test-token"}
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/" {
			targetHandler.ServeHTTP(w, req)
			return
		}
		initial.Add(1)
		if req.Method != http.MethodPost || req.TLS == nil || req.Header.Get("Authorization") == "" || req.Header.Get("x-acs-security-token") != cred.SecurityToken {
			t.Error("initial request must be signed with synthetic STS credentials over TLS")
		}
		location := "/redirect-target"
		switch destination {
		case "crosshost":
			location = strings.Replace(target.URL, "127.0.0.1", "localhost", 1) + "/redirect-target"
		case "downgrade":
			location = target.URL + "/redirect-target"
		case "malformed":
			location = "https://redirect.invalid/%zz"
		}
		location += "?Signature=synthetic-signature&SecurityToken=" + cred.SecurityToken
		w.Header().Set("Location", location)
		w.Header().Set("x-acs-request-id", "redirect-header-id")
		w.Header().Set("Content-Type", "text/event-stream; secret="+cred.SecurityToken)
		w.WriteHeader(status)
		// Neither a JSON error nor SSE-shaped body may leak or become data.
		if req.Header.Get("Accept") == "text/event-stream" {
			fmt.Fprintf(w, "event: SSE_FINISH\ndata: {\"content\":%q}\n\n", cred.SecurityToken)
		} else {
			json.NewEncoder(w).Encode(map[string]string{"Code": cred.SecurityToken, "Message": location, "RequestId": req.Header.Get("Authorization")})
		}
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "https://")
	c := NewClient(cred, "cn-shanghai", WithDataAgentEndpoint(host), WithDMSEnterpriseEndpoint(host), WithDMSUnit("test-unit"),
		WithCredentialProvider(func() *Credential { return cred })) // Tenant-style constructor options.
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	// The httptest certificate covers example.com, allowing both local hostnames.
	transport.TLSClientConfig.ServerName = "example.com"
	t.Cleanup(transport.CloseIdleConnections)
	c.http.Transport, c.sse.httpClient.Transport = transport, transport
	return c, &initial, &redirected
}

func assertSignedRedirectError(t *testing.T, err error) {
	t.Helper()
	if err == nil || IsDefiniteRejection(err) {
		t.Fatalf("redirect must return an error without claiming definite business rejection: %v", err)
	}
	for _, secret := range []string{"redirect-test-id", "redirect-test-secret", "redirect-test-token", "synthetic-signature", "Location", "Authorization", "SecurityToken", "Signature=", "https://", "http://", "ACS3-"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("redirect error leaked %q", secret)
		}
	}
}

func TestSignedAPIRedirectsRejected(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"samehost", "crosshost", "downgrade", "malformed"} {
			for _, version := range []string{"2025-04-14", "2018-11-01"} {
				t.Run(fmt.Sprintf("%d/%s/%s", status, destination, version), func(t *testing.T) {
					c, initial, redirected := signedRedirectFixture(t, status, destination)
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					ctx, ids := WithRequestIDs(ctx)
					var body map[string]interface{}
					var err error
					if version == "2018-11-01" {
						body, err = c.callDMSEnterprise(ctx, c.DMSEnterpriseEndpoint(), "ListTables", version, nil)
					} else {
						body, err = c.callAPI(ctx, c.DataAgentEndpoint(), "DescribeDataAgentSession", version, nil)
					}
					assertSignedRedirectError(t, err)
					if body != nil || initial.Load() != 1 || redirected.Load() != 0 || len(ids.Snapshot()) != 0 {
						t.Fatalf("redirect returned data, retried, or forwarded: initial=%d target=%d", initial.Load(), redirected.Load())
					}
				})
			}
		}
	}
}

func TestBuildUserAgentUsesSkillSessionID(t *testing.T) {
	sessionID := "0123456789abcdef0123456789ABCDEF"
	got := buildUserAgent(sessionID)
	want := userAgentPrefix + "/0123456789abcdef0123456789abcdef DataAgent_MCP"
	if got != want {
		t.Fatalf("buildUserAgent() = %q, want %q", got, want)
	}
}

func TestBuildUserAgentGeneratesSessionIDFallback(t *testing.T) {
	got := buildUserAgent("")
	prefix := userAgentPrefix + "/"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("buildUserAgent() = %q, want prefix %q", got, prefix)
	}
	const suffix = " DataAgent_MCP"
	if !strings.HasSuffix(got, suffix) {
		t.Fatalf("buildUserAgent() = %q, want suffix %q", got, suffix)
	}

	sessionID := strings.TrimSuffix(strings.TrimPrefix(got, prefix), suffix)
	if len(sessionID) != 32 {
		t.Fatalf("fallback session id length = %d, want 32", len(sessionID))
	}
	if _, err := hex.DecodeString(sessionID); err != nil {
		t.Fatalf("fallback session id should be hex: %v", err)
	}
}

func TestCreateSessionUserAgent(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		calls := 0
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if got := req.Header.Get("User-Agent"); got != userAgent || !strings.HasSuffix(got, " DataAgent_MCP") {
				t.Errorf("User-Agent = %q, want %q with DataAgent_MCP token", got, userAgent)
			}
			return jsonHTTPResponse(t, map[string]any{
				"Data": map[string]any{
					"SessionId": "ua-test-session",
					"AgentId":   "ua-test-agent",
				},
			}), nil
		})
		info, err := c.CreateSession(context.Background(), CreateSessionOpts{Mode: "lite", WorkspaceID: "test-workspace"})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 || info.SessionID != "ua-test-session" {
			t.Fatalf("CreateSession() made %d requests, session = %q", calls, info.SessionID)
		}
	})
}

func TestListWorkspacesScansAllPages(t *testing.T) {
	c := NewClient(
		&Credential{AccessKeyID: "ak", AccessKeySecret: "sk"},
		"cn-hangzhou",
		WithDMSUnit("cn-hangzhou"),
	)

	var pages []string
	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		page := query.Get("PageNumber")
		pages = append(pages, page)
		if got := query.Get("WorkspaceType"); got != "ALL" {
			t.Fatalf("WorkspaceType = %q, want ALL", got)
		}

		content := make([]map[string]string, 0, dataAgentListPageSize)
		switch page {
		case "1":
			for i := 0; i < dataAgentListPageSize; i++ {
				content = append(content, map[string]string{
					"WorkspaceId":   fmt.Sprintf("ws-page1-%02d", i),
					"WorkspaceName": fmt.Sprintf("workspace-page1-%02d", i),
					"Type":          "MY",
				})
			}
		case "2":
			content = append(content, map[string]string{
				"WorkspaceId":    "ws-dev",
				"WorkspaceName":  "开发环境",
				"WorkspaceType":  "SHARED",
				"UnexpectedName": "ignored",
			})
		default:
			t.Fatalf("unexpected page %q", page)
		}
		return jsonHTTPResponse(t, map[string]any{
			"Data": map[string]any{
				"Content": content,
				"Total":   dataAgentListPageSize + 1,
			},
		}), nil
	})}

	got, err := c.ListWorkspaces(context.Background(), "ALL")
	if err != nil {
		t.Fatalf("ListWorkspaces() error = %v", err)
	}
	if len(got) != dataAgentListPageSize+1 {
		t.Fatalf("ListWorkspaces() len = %d, want %d", len(got), dataAgentListPageSize+1)
	}
	dev := got[dataAgentListPageSize]
	if dev.WorkspaceID != "ws-dev" || dev.Name != "开发环境" || dev.Type != "SHARED" {
		t.Fatalf("dev workspace = %+v", dev)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages = %v, want [1 2]", pages)
	}
}

func TestListCustomAgentsStopsOnDuplicatePage(t *testing.T) {
	c := NewClient(
		&Credential{AccessKeyID: "ak", AccessKeySecret: "sk"},
		"cn-hangzhou",
		WithDMSUnit("cn-hangzhou"),
		WithWorkspaceID("ws-dev"),
	)

	var pages []string
	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		pages = append(pages, query.Get("PageNumber"))
		if got := query.Get("WorkspaceId"); got != "ws-dev" {
			t.Fatalf("WorkspaceId = %q, want ws-dev", got)
		}

		content := make([]map[string]string, 0, dataAgentListPageSize)
		for i := 0; i < dataAgentListPageSize; i++ {
			content = append(content, map[string]string{
				"CustomAgentId": "agent-duplicate",
				"Name":          "重复 Agent",
				"Status":        "RELEASED",
			})
		}
		return jsonHTTPResponse(t, map[string]any{
			"Data": map[string]any{"Content": content},
		}), nil
	})}

	got, err := c.ListCustomAgents(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListCustomAgents() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListCustomAgents() len = %d, want 1 after dedupe", len(got))
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages = %v, want [1 2]", pages)
	}
}

func TestListRemoteSessionsAddsCreateTimeRangeForSignedAuth(t *testing.T) {
	c := NewClient(
		&Credential{AccessKeyID: "ak", AccessKeySecret: "sk"},
		"cn-hangzhou",
		WithDMSUnit("cn-hangzhou"),
		WithWorkspaceID("ws-dev"),
	)

	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		if got := query.Get("WorkspaceId"); got != "ws-dev" {
			t.Fatalf("WorkspaceId = %q, want ws-dev", got)
		}
		if query.Get("CreateStartTime") == "" || query.Get("CreateEndTime") == "" {
			t.Fatalf("signed ListRemoteSessions must include CreateStartTime/CreateEndTime, query=%s", req.URL.RawQuery)
		}
		if query.Get("StartTime") != "" || query.Get("EndTime") != "" {
			t.Fatalf("signed ListRemoteSessions should not include API key time params, query=%s", req.URL.RawQuery)
		}
		return jsonHTTPResponse(t, map[string]any{
			"Data": map[string]any{
				"Content": []map[string]string{
					{
						"SessionId":   "session-1",
						"AgentId":     "agent-1",
						"Status":      "COMPLETED",
						"Mode":        "ANALYSIS",
						"WorkspaceId": "ws-dev",
					},
					{
						"SessionId": "session-2",
						"AgentId":   "agent-2",
						"Status":    "COMPLETED",
						"Mode":      "ANALYSIS",
					},
				},
			},
		}), nil
	})}

	got, err := c.ListRemoteSessions(context.Background(), "")
	if err != nil {
		t.Fatalf("ListRemoteSessions() error = %v", err)
	}
	if len(got) != 2 || got[0].SessionID != "session-1" || got[1].SessionID != "session-2" || got[0].WorkspaceID != "ws-dev" || got[1].WorkspaceID != "ws-dev" {
		t.Fatalf("ListRemoteSessions() = %+v, want both sessions in ws-dev", got)
	}
}

func TestSuccessfulAPIRequestIDs(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		type requestIDCase struct {
			name, body, acsHeader, header, want string
		}
		tests := []requestIDCase{
			{name: "acs header", body: `{}`, acsHeader: "acs-id", header: "other-id", want: "acs-id"},
			{name: "request header", body: `{}`, header: "header-id", want: "header-id"},
			{name: "missing", body: `{}`},
			{name: "empty aliases", body: `{"RequestId":"","requestId":"","request_id":"","RequestID":""}`},
			{name: "envelope", body: `{"code":"success","requestId":"outer-id","data":{"SessionId":"s"}}`, acsHeader: "header-id", want: "outer-id"},
			{name: "envelope header", body: `{"code":"success","data":{"SessionId":"s"}}`, header: "header-id", want: "header-id"},
			{name: "envelope missing", body: `{"code":"success","data":{"SessionId":"s"}}`},
			{name: "empty inner ID", body: `{"code":"success","requestId":"outer-id","data":{"RequestId":""}}`, want: "outer-id"},
		}
		for _, alias := range []string{"RequestId", "requestId", "request_id", "RequestID"} {
			tests = append(tests, requestIDCase{name: alias, body: fmt.Sprintf(`{%q:"body-id"}`, alias), acsHeader: "header-id", want: "body-id"})
			tests = append(tests, requestIDCase{name: "inner " + alias, body: fmt.Sprintf(`{"code":"success","requestId":"outer-id","data":{%q:"inner-id"}}`, alias), acsHeader: "header-id", want: "outer-id"})
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ctx, ids := WithRequestIDs(context.Background())
				useSignedTLSFixture(t, c, func(w http.ResponseWriter, req *http.Request) {
					w.Header().Set("x-acs-request-id", tt.acsHeader)
					w.Header().Set("x-request-id", tt.header)
					io.WriteString(w, tt.body)
				})
				calls := 0
				transport := c.http.Transport
				c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Context().Value(requestIDsKey{}) != ids {
						t.Error("HTTP request lost its caller context")
					}
					return transport.RoundTrip(req)
				})
				want := tt.want
				body, err := c.callAPI(ctx, c.endpoint, "DescribeDataAgentSession", "2025-04-14", nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := jsonStr(body, "RequestId"); got != want {
					t.Fatalf("normalized RequestId = %q, want %q", got, want)
				}
				created, err := c.CreateSession(ctx, CreateSessionOpts{})
				if err != nil {
					t.Fatal(err)
				}
				described, err := c.DescribeSession(ctx, "s", "explicit-workspace")
				if err != nil {
					t.Fatal(err)
				}
				if created.RequestID != want || described.RequestID != want {
					t.Fatalf("SessionInfo request IDs = %q / %q, want %q", created.RequestID, described.RequestID, want)
				}
				var expected []APIRequest
				if want != "" {
					expected = []APIRequest{
						{Action: "DescribeDataAgentSession", RequestID: want},
						{Action: "CreateDataAgentSession", RequestID: want},
						{Action: "DescribeDataAgentSession", RequestID: want},
					}
				}
				if calls != 3 || !reflect.DeepEqual(ids.Snapshot(), expected) {
					t.Fatalf("calls=%d, collected=%+v, want %+v", calls, ids.Snapshot(), expected)
				}
			})
		}
	})
}

func TestRequestIDsSnapshotAndScope(t *testing.T) {
	base := context.Background()
	ctx, ids := WithRequestIDs(base)
	recordRequestID(base, "uncollected", "uncollected-id")
	recordRequestID(ctx, "missing", "")
	recordRequestID(ctx, "first", "same-id")
	recordRequestID(ctx, "first", "same-id")
	want := []APIRequest{{Action: "first", RequestID: "same-id"}, {Action: "first", RequestID: "same-id"}}
	snapshot := ids.Snapshot()
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("snapshot=%+v, want %+v", snapshot, want)
	}
	snapshot[0].RequestID = "modified"
	if !reflect.DeepEqual(ids.Snapshot(), want) {
		t.Fatal("snapshot aliases collector storage")
	}
	child, childIDs := WithRequestIDs(ctx)
	recordRequestID(child, "child", "child-id")
	if !reflect.DeepEqual(ids.Snapshot(), want) || len(childIDs.Snapshot()) != 1 {
		t.Fatal("nested collector leaked into its parent")
	}
	recordRequestID(ctx, "later", "later-id")
	if len(snapshot) != 2 || snapshot[1] != want[1] {
		t.Fatal("later append changed an earlier snapshot")
	}
}

func TestRequestIDsConcurrentSnapshots(t *testing.T) {
	ctx, ids := WithRequestIDs(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recordRequestID(ctx, "concurrent", strconv.Itoa(i))
			snapshot := ids.Snapshot()
			if len(snapshot) > 0 {
				snapshot[0].RequestID = "snapshot-only"
			}
		}(i)
	}
	wg.Wait()
	got := ids.Snapshot()
	seen := make(map[string]bool)
	for _, request := range got {
		seen[request.RequestID] = true
	}
	if len(got) != 100 || len(seen) != 100 || seen["snapshot-only"] {
		t.Fatalf("concurrent collection lost or changed IDs: %+v", got)
	}
}

func TestRequestIDsConcurrentClientIsolation(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			id := req.URL.Query().Get("DatabaseId")
			return jsonHTTPResponse(t, map[string]any{"RequestID": id}), nil
		})
		var wg sync.WaitGroup
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ctx, ids := WithRequestIDs(context.Background())
				var want []APIRequest
				for j := 0; j < 4; j++ {
					id := fmt.Sprintf("caller-%d-request-%d", i, j)
					if _, err := c.CreateSession(ctx, CreateSessionOpts{DatabaseID: id}); err != nil {
						t.Error(err)
						return
					}
					want = append(want, APIRequest{Action: "CreateDataAgentSession", RequestID: id})
				}
				if !reflect.DeepEqual(ids.Snapshot(), want) {
					t.Errorf("caller %d collected %+v, want %+v", i, ids.Snapshot(), want)
				}
			}(i)
		}
		wg.Wait()
	})
}

func TestAPIRequestContextCancellation(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		ctx, cancel := context.WithCancel(context.Background())
		ctx, ids := WithRequestIDs(ctx)
		cancel()
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Context().Err() != context.Canceled {
				t.Error("HTTP request did not inherit cancellation")
			}
			return nil, req.Context().Err()
		})
		err := c.SendMessage(ctx, SendMessageOpts{})
		if !errors.Is(err, context.Canceled) || len(ids.Snapshot()) != 0 {
			t.Fatalf("canceled call: error=%v, requests=%+v", err, ids.Snapshot())
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func jsonHTTPResponse(t *testing.T, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func TestDMSEnterpriseEndpoint(t *testing.T) {
	c := NewClient(&Credential{}, "cn-shanghai")
	if got, want := c.DMSEnterpriseEndpoint(), "dms-enterprise.cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("default endpoint = %q, want %q", got, want)
	}

	// An explicit override wins over the region default (e.g. VPC endpoints).
	const vpc = "dms-enterprise-vpc.cn-shanghai.aliyuncs.com"
	c = NewClient(&Credential{}, "cn-shanghai", WithDMSEnterpriseEndpoint(vpc))
	if got := c.DMSEnterpriseEndpoint(); got != vpc {
		t.Errorf("overridden endpoint = %q, want %q", got, vpc)
	}

	// An empty override keeps the region default.
	c = NewClient(&Credential{}, "cn-shanghai", WithDMSEnterpriseEndpoint(""))
	if got, want := c.DMSEnterpriseEndpoint(), "dms-enterprise.cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("empty override endpoint = %q, want %q", got, want)
	}
}

// useSignedTLSFixture exercises the real HTTP transport with synthetic AK/STS
// credentials. Context and transport-failure tests use roundTripFunc directly.
func useSignedTLSFixture(t *testing.T, c *Client, handler http.HandlerFunc) {
	t.Helper()
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		cred := c.credential()
		if req.TLS == nil || req.Method != http.MethodPost || req.URL.Path != "/" ||
			!strings.HasPrefix(req.Header.Get("Authorization"), "ACS3-HMAC-SHA256 Credential="+cred.AccessKeyID+",") ||
			req.Header.Get("x-acs-security-token") != cred.SecurityToken || req.Header.Get("x-api-key") != "" {
			t.Error("expected signed TLS request")
			http.Error(w, "invalid authentication", http.StatusBadRequest)
			return
		}
		handler(w, req)
	}))
	t.Cleanup(upstream.Close)
	host := strings.TrimPrefix(upstream.URL, "https://")
	c.endpoint = host
	c.dmsEnterpriseEndpoint = host
	c.sse.endpoint = host
	c.http.Transport = upstream.Client().Transport
	c.sse.httpClient.Transport = upstream.Client().Transport
}

func testClientAuthModes(t *testing.T, run func(*testing.T, *Client)) {
	t.Helper()
	for _, auth := range []struct {
		name string
		cred *Credential
	}{
		{"signed", &Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk"}},
		{"sts", &Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk", SecurityToken: "test-sts"}},
	} {
		t.Run(auth.name, func(t *testing.T) {
			c := NewClient(auth.cred, "cn-hangzhou", WithDMSUnit("test-unit"), WithWorkspaceID("test-workspace"))
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				t.Fatalf("unexpected request: %s", req.URL.Host)
				return nil, errors.New("unexpected request")
			})
			run(t, c)
		})
	}
}

func TestSendMessageMessageType(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		for _, tt := range []struct {
			name        string
			messageType string
			want        string
		}{
			{name: "default primary", want: "primary"},
			{name: "explicit report", messageType: "report", want: "report"},
			{name: "explicit additional", messageType: "additional", want: "additional"},
		} {
			t.Run(tt.name, func(t *testing.T) {
				opts := SendMessageOpts{
					AgentID:     "message-type-agent",
					SessionID:   "message-type-session",
					Message:     "分析 revenue + costs & growth=10%\nPreserve this follow-up.",
					MessageType: tt.messageType,
					WorkspaceID: "message-type-workspace",
				}
				var calls atomic.Int32
				useSignedTLSFixture(t, c, func(w http.ResponseWriter, req *http.Request) {
					calls.Add(1)
					params := req.URL.Query()
					for key, want := range map[string]string{
						"Action":      "SendChatMessage",
						"MessageType": tt.want,
						"Message":     opts.Message,
						"SessionId":   opts.SessionID,
						"AgentId":     opts.AgentID,
						"WorkspaceId": opts.WorkspaceID,
					} {
						if got := params.Get(key); got != want {
							t.Errorf("%s = %v, want %q", key, got, want)
						}
					}
					io.WriteString(w, `{"Success":true}`)
				})
				if err := c.SendMessage(context.Background(), opts); err != nil {
					t.Fatalf("SendMessage() error = %v", err)
				}
				if calls.Load() != 1 {
					t.Fatalf("SendMessage made %d requests, want exactly one", calls.Load())
				}
			})
		}
	})
}

func TestSendMessageAPIResponses(t *testing.T) {
	const busyCode = "DMS.DataAgent.SessionInBusy"
	const busyMessage = "Current Session is processing request, cannot accept more message."
	tests := []struct {
		name    string
		body    string
		status  int
		want    *APIError
		unknown bool
	}{
		{
			name: "success true with busy error_code",
			body: `{"success":true,"error_code":"DMS.DataAgent.SessionInBusy","error_message":"Current Session is processing request, cannot accept more message.","request_id":"busy-request","credentials":{"access_key_secret":"response-secret"}}`,
			want: &APIError{Code: busyCode, Message: busyMessage, RequestID: "busy-request"},
		},
		{
			name: "success true with ErrorCode",
			body: `{"Success":true,"ErrorCode":"DMS.DataAgent.SessionInBusy","ErrorMessage":"busy","RequestId":"pascal-request"}`,
			want: &APIError{Code: busyCode, Message: "busy", RequestID: "pascal-request"},
		},
		{
			name: "Data envelope preserves outer request id",
			body: `{"RequestId":"outer-request","Data":{"success":true,"error_code":"DMS.DataAgent.SessionInBusy","error_message":"busy"}}`,
			want: &APIError{Code: busyCode, Message: "busy", RequestID: "outer-request"},
		},
		{
			name: "lowercase data envelope preserves outer request id",
			body: `{"success":true,"code":"success","requestId":"lowercase-request","data":{"success":true,"error_code":"DMS.DataAgent.SessionInBusy","error_message":"busy"}}`,
			want: &APIError{Code: busyCode, Message: "busy", RequestID: "lowercase-request"},
		},
		{
			name: "nested envelopes prefer backend request id",
			body: `{"success":true,"code":"success","requestId":"lowercase-request","data":{"Success":true,"Data":{"success":true,"ErrorCode":"DMS.DataAgent.SessionInBusy","ErrorMessage":"busy","RequestID":"backend-request"}}}`,
			want: &APIError{Code: busyCode, Message: "busy", RequestID: "backend-request"},
		},
		{
			name: "outer failure with successful data",
			body: `{"success":true,"code":"success","error_code":"DMS.DataAgent.SessionInBusy","data":{"success":true}}`,
			want: &APIError{Code: busyCode},
		},
		{
			name: "success alias does not hide explicit error",
			body: `{"success":true,"error_code":"success","ErrorCode":"DMS.DataAgent.SessionInBusy"}`,
			want: &APIError{Code: busyCode},
		},
		{
			name: "lowercase false",
			body: `{"success":false,"code":"Forbidden","msg":"no grant","requestId":"false-request"}`,
			want: &APIError{Code: "Forbidden", Message: "no grant", RequestID: "false-request"},
		},
		{
			name: "uppercase false",
			body: `{"Success":false,"Code":"Forbidden","Message":"no grant"}`,
			want: &APIError{Code: "Forbidden", Message: "no grant"},
		},
		{
			name: "nested false",
			body: `{"success":true,"code":"success","data":{"Success":false,"Message":"rejected"}}`,
			want: &APIError{Message: "rejected"},
		},
		{
			name: "bare false",
			body: `{"success":false}`,
			want: &APIError{},
		},
		{
			name: "false overrides success code and conflicting flag",
			body: `{"success":true,"Success":false,"code":"success"}`,
			want: &APIError{Code: "success"},
		},
		{
			name: "standard uppercase error",
			body: `{"Code":"Forbidden","Message":"no grant","RequestId":"standard-request"}`,
			want: &APIError{Code: "Forbidden", Message: "no grant", RequestID: "standard-request"},
		},
		{
			name: "standard lowercase error without success flag",
			body: `{"code":"Forbidden","message":"no grant"}`,
			want: &APIError{Code: "Forbidden", Message: "no grant"},
		},
		{
			name: "nested standard uppercase error",
			body: `{"success":true,"code":"success","data":{"Code":"Forbidden","Message":"no grant"}}`,
			want: &APIError{Code: "Forbidden", Message: "no grant"},
		},
		{
			name: "nested standard lowercase envelope",
			body: `{"data":{"code":"Forbidden","msg":"no grant","requestId":"inner-request"}}`,
			want: &APIError{Code: "Forbidden", Message: "no grant", RequestID: "inner-request"},
		},
		{
			name: "embedded 400",
			body: `{"HttpStatusCode":400,"Code":"InvalidParameter","Message":"bad param"}`,
			want: &APIError{Code: "InvalidParameter", Message: "bad param", StatusCode: 400},
		},
		{
			name: "embedded string 429",
			body: `{"httpStatusCode":"429","Message":"throttled"}`,
			want: &APIError{Message: "throttled", StatusCode: 429},
		},
		{
			name: "embedded 503 is unknown despite false",
			body: `{"success":false,"HttpStatusCode":503,"Code":"Unavailable","Message":"unavailable"}`,
			want: &APIError{Code: "Unavailable", Message: "unavailable", StatusCode: 503}, unknown: true,
		},
		{
			name: "nested embedded 500 is unknown",
			body: `{"success":true,"code":"success","data":{"HttpStatusCode":500,"Code":"InternalError","Message":"internal"}}`,
			want: &APIError{Code: "InternalError", Message: "internal", StatusCode: 500}, unknown: true,
		},
		{
			name: "outer false cannot hide nested 503",
			body: `{"success":false,"code":"Failed","data":{"HttpStatusCode":503,"Code":"Unavailable","Message":"unavailable"}}`,
			want: &APIError{Code: "Unavailable", Message: "unavailable", StatusCode: 503}, unknown: true,
		},
		{
			name: "embedded 400 cannot hide nested 500",
			body: `{"HttpStatusCode":400,"Data":{"HttpStatusCode":500,"Message":"internal"}}`,
			want: &APIError{Message: "internal", StatusCode: 500}, unknown: true,
		},
		{
			name: "HTTP 400 with non JSON body",
			body: `<html>credentials: response-secret</html>`, status: 400,
			want: &APIError{Message: "Bad Request"},
		},
		{
			name: "HTTP 403 retains body request id",
			body: `{"Code":"Forbidden","Message":"no grant","RequestId":"http-request","credentials":"response-secret"}`, status: 403,
			want: &APIError{Code: "Forbidden", Message: "no grant", RequestID: "http-request"},
		},
		{
			name: "HTTP 429 without body",
			body: ``, status: 429,
			want: &APIError{Message: "Too Many Requests"},
		},
		{
			name: "HTTP 500 does not leak raw credentials",
			body: `credentials=response-secret`, status: 500,
			want: &APIError{Message: "Internal Server Error"}, unknown: true,
		},
		{
			name: "HTTP 503 busy still unknown",
			body: `{"success":false,"error_code":"DMS.DataAgent.SessionInBusy","error_message":"busy"}`, status: 503,
			want: &APIError{Code: busyCode, Message: "busy"}, unknown: true,
		},
		{
			name: "HTTP 500 overrides embedded 400",
			body: `{"HttpStatusCode":400,"Code":"InvalidParameter","Message":"bad param"}`, status: 500,
			want: &APIError{Code: "InvalidParameter", Message: "bad param"}, unknown: true,
		},
		{
			name: "non 200 success status remains an error",
			body: `{"success":true}`, status: 201,
			want: &APIError{Message: "Created"}, unknown: true,
		},
		{
			name: "redirect remains unknown",
			body: `{}`, status: 302,
			want: &APIError{Code: "RedirectRejected", Message: "Signed HTTP redirects are not allowed."}, unknown: true,
		},
		{name: "plain success", body: `{"success":true}`},
		{name: "empty object", body: `{}`},
		{name: "success code with message", body: `{"code":"success","message":"accepted"}`},
		{name: "uppercase success code", body: `{"Code":"Success","Message":"accepted"}`},
		{name: "numeric success code", body: `{"code":200,"message":"accepted"}`},
		{name: "zero success code", body: `{"code":0,"message":"accepted"}`},
		{name: "OK success code", body: `{"Code":"OK","Message":"accepted"}`},
		{name: "normal lowercase envelope", body: `{"success":true,"code":"success","data":{"success":true}}`},
		{name: "normal signed envelope", body: `{"Success":true,"Data":{"MessageId":"m-1"}}`},
		{name: "null and empty error code", body: `{"success":true,"error_code":null,"ErrorCode":""}`},
		{name: "success error codes", body: `{"error_code":"0","ErrorCode":"success","Message":"accepted"}`},
		{name: "ordinary top level code", body: `{"code":"business-value"}`},
		{name: "ordinary data code", body: `{"Data":{"code":"business-value","message":"business description"}}`},
		{name: "ordinary lowercase data code", body: `{"success":true,"code":"success","data":{"code":"business-value","message":"business description"}}`},
		{name: "ordinary uppercase data code", body: `{"Data":{"Code":"business-value"}}`},
		{name: "explicit success with nonstandard code", body: `{"success":true,"code":"business-value","message":"accepted"}`},
		{name: "do not scan arbitrary business objects", body: `{"Data":{"record":{"success":false,"error_code":"business-value"},"rows":[{"code":"Forbidden","message":"business data"}]}}`},
	}

	testClientAuthModes(t, func(t *testing.T, c *Client) {
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				var calls atomic.Int32
				useSignedTLSFixture(t, c, func(w http.ResponseWriter, req *http.Request) {
					calls.Add(1)
					if req.URL.Query().Get("Action") != "SendChatMessage" || req.URL.Query().Get("SessionId") != "test-session" || req.URL.Query().Get("Message") != "hello" {
						t.Error("SendMessage lost its signed query parameters")
					}
					w.Header().Set("x-acs-request-id", "header-request")
					w.WriteHeader(status)
					io.WriteString(w, tt.body)
				})
				ctx, ids := WithRequestIDs(context.Background())
				err := c.SendMessage(ctx, SendMessageOpts{AgentID: "test-agent", SessionID: "test-session", Message: "hello"})
				if calls.Load() != 1 {
					t.Fatalf("SendMessage made %d requests, want exactly one", calls.Load())
				}
				if tt.want == nil {
					if err != nil {
						t.Fatalf("SendMessage() error = %v", err)
					}
					want := []APIRequest{{Action: "SendChatMessage", RequestID: "header-request"}}
					if !reflect.DeepEqual(ids.Snapshot(), want) {
						t.Fatalf("successful requests = %+v, want %+v", ids.Snapshot(), want)
					}
					return
				}
				if len(ids.Snapshot()) != 0 {
					t.Fatalf("failed API request was collected: %+v", ids.Snapshot())
				}
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("SendMessage() error = %v, want wrapped *APIError", err)
				}
				wantStatus := tt.want.StatusCode
				if wantStatus == 0 {
					wantStatus = status
				}
				wantID := tt.want.RequestID
				if wantID == "" {
					wantID = "header-request"
				}
				if apiErr.Code != tt.want.Code || apiErr.RequestID != wantID || apiErr.StatusCode != wantStatus {
					t.Fatalf("APIError = %+v, want code=%q requestID=%q status=%d", apiErr, tt.want.Code, wantID, wantStatus)
				}
				if apiErr.Message == "" || (tt.want.Message != "" && apiErr.Message != tt.want.Message) {
					t.Fatalf("APIError.Message = %q, want %q (or nonempty default)", apiErr.Message, tt.want.Message)
				}
				if got := IsDefiniteRejection(fmt.Errorf("caller: %w", err)); got != !tt.unknown {
					t.Fatalf("IsDefiniteRejection() = %v, want %v", got, !tt.unknown)
				}
				if !strings.Contains(err.Error(), "SendMessage") || !strings.Contains(err.Error(), "SendChatMessage") || !strings.Contains(err.Error(), wantID) {
					t.Fatalf("error missing action or request id: %v", err)
				}
				for _, secret := range []string{"response-secret", "credentials", "test-sk", "test-sts", "test-api-key"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error includes sensitive response or credential: %v", err)
					}
				}
			})
		}
	})
}

func TestSendMessageMalformedResponsesAreUnknown(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		for _, body := range []string{"", "not JSON", "null", "[]", "42", `{"success":false,"credentials":"response-secret"`, `{"success":false} trailing`} {
			t.Run(body, func(t *testing.T) {
				calls := 0
				c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"X-Request-Id": []string{"parse-request"}},
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				})
				ctx, ids := WithRequestIDs(context.Background())
				err := c.SendMessage(ctx, SendMessageOpts{SessionID: "test-session"})
				if len(ids.Snapshot()) != 0 {
					t.Fatalf("failed response was collected: %+v", ids.Snapshot())
				}
				var apiErr *APIError
				if err == nil || errors.As(err, &apiErr) || IsDefiniteRejection(err) {
					t.Fatalf("malformed response must be unknown, got %v", err)
				}
				if calls != 1 || !strings.Contains(err.Error(), "parse-request") || strings.Contains(err.Error(), "response-secret") {
					t.Fatalf("calls=%d, error=%v", calls, err)
				}
			})
		}
	})
}

func TestSendMessageTransportFailuresAreUnknown(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		for _, tt := range []struct {
			name   string
			cause  error
			status int
		}{
			{name: "network timeout", cause: context.DeadlineExceeded},
			{name: "connection reset", cause: errors.New("connection reset")},
			{name: "read timeout", cause: context.DeadlineExceeded, status: 200},
			{name: "truncated body", cause: io.ErrUnexpectedEOF, status: 200},
			{name: "4xx read timeout", cause: context.DeadlineExceeded, status: 400},
			{name: "5xx read timeout", cause: context.DeadlineExceeded, status: 503},
		} {
			t.Run(tt.name, func(t *testing.T) {
				calls := 0
				body := &failingResponseBody{Reader: strings.NewReader(`{"success":false,"credentials":"response-secret"}`), err: tt.cause}
				c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if tt.status == 0 {
						return nil, tt.cause
					}
					return &http.Response{StatusCode: tt.status, Header: http.Header{"X-Acs-Request-Id": []string{"read-request"}}, Body: body}, nil
				})
				ctx, ids := WithRequestIDs(context.Background())
				err := c.SendMessage(ctx, SendMessageOpts{SessionID: "test-session"})
				if len(ids.Snapshot()) != 0 {
					t.Fatalf("failed response was collected: %+v", ids.Snapshot())
				}
				if !errors.Is(err, tt.cause) || IsDefiniteRejection(err) || calls != 1 {
					t.Fatalf("calls=%d, error=%v, want wrapped unknown transport failure without retry", calls, err)
				}
				if tt.status != 0 && (!body.closed || !strings.Contains(err.Error(), "read-request")) {
					t.Fatalf("response closed=%v, error=%v", body.closed, err)
				}
				if strings.Contains(err.Error(), "response-secret") {
					t.Fatalf("error leaks response body: %v", err)
				}
			})
		}
	})
}

type failingResponseBody struct {
	io.Reader
	err    error
	closed bool
}

func (b *failingResponseBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		err = b.err
	}
	return n, err
}

func (b *failingResponseBody) Close() error {
	b.closed = true
	return nil
}

func TestIsDefiniteRejection(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil"},
		{name: "typed nil", err: (*APIError)(nil)},
		{name: "untyped error text", err: errors.New("HTTP 400 DMS.DataAgent.SessionInBusy")},
		{name: "unverified business code", err: &APIError{Code: "DMS.DataAgent.SessionInBusy", StatusCode: 200}},
		{name: "verified business rejection", err: &APIError{StatusCode: 200, definiteRejection: true}, want: true},
		{name: "4xx", err: &APIError{StatusCode: 403}, want: true},
		{name: "5xx", err: &APIError{StatusCode: 503, definiteRejection: true}},
		{name: "no status", err: &APIError{Code: "Forbidden"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsDefiniteRejection(tt.err); got != tt.want {
				t.Fatalf("IsDefiniteRejection() = %v, want %v", got, tt.want)
			}
			if got := IsDefiniteRejection(fmt.Errorf("wrapped: %w", tt.err)); got != tt.want {
				t.Fatalf("wrapped IsDefiniteRejection() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSignedFileUploadResponses(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		for _, dataKey := range []string{"Data", "data"} {
			t.Run(dataKey, func(t *testing.T) {
				want := &UploadSignature{
					UploadHost: "https://upload.example.invalid", UploadDir: "test-dir/",
					Policy: "test-policy", OssSignature: "test-signature", OssSignatureVersion: "OSS4-HMAC-SHA256",
					OssDate: "20261027T000000Z", OssSecurityToken: "test-oss-token", OssCredential: "test-oss-credential",
				}
				useSignedTLSFixture(t, c, func(w http.ResponseWriter, req *http.Request) {
					query := req.URL.Query()
					switch query.Get("Action") {
					case "DescribeFileUploadSignature":
						if query.Get("FileName") != "test.csv" || query.Get("FileSize") != "123" {
							t.Error("upload signature lost file metadata")
						}
						json.NewEncoder(w).Encode(map[string]any{dataKey: map[string]string{
							"UploadHost": want.UploadHost, "UploadDir": want.UploadDir,
							"Policy": want.Policy, "OssSignature": want.OssSignature, "OssSignatureVersion": want.OssSignatureVersion,
							"OssDate": want.OssDate, "OssSecurityToken": want.OssSecurityToken, "OssCredential": want.OssCredential,
						}})
					case "FileUploadCallback":
						if query.Get("Filename") != "test.csv" || query.Get("UploadLocation") != "test-dir/test.csv" || query.Get("FileSize") != "123" {
							t.Error("upload callback lost file metadata")
						}
						json.NewEncoder(w).Encode(map[string]any{dataKey: map[string]string{"FileId": "file-1"}})
					default:
						t.Errorf("unexpected action %q", query.Get("Action"))
						http.Error(w, "unexpected action", http.StatusBadRequest)
					}
				})
				got, err := c.GetFileUploadSignature(context.Background(), "test.csv", 123)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("signed upload signature = %+v, err=%v, want %+v", got, err, want)
				}
				fileID, err := c.FileUploadCallback(context.Background(), "test.csv", "test-dir/test.csv", 123)
				if err != nil || fileID != "file-1" {
					t.Fatalf("signed upload callback = %q, err=%v", fileID, err)
				}
			})
		}
	})
}

func TestAPIResponsePayloadCompatibility(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		inner := map[string]interface{}{"Content": []interface{}{map[string]interface{}{"WorkspaceId": "test-workspace"}}}
		payload := map[string]interface{}{"success": true, "code": "success", "data": inner}
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonHTTPResponse(t, payload), nil
		})
		got, err := c.callAPI(context.Background(), c.endpoint, "ListDataAgentWorkspace", "2025-04-14", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, payload) {
			t.Fatalf("response shape changed: got %#v, want %#v", got, payload)
		}
		for _, status := range []int{201, 400, 503} {
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				resp := jsonHTTPResponse(t, map[string]interface{}{"Message": "backend message", "credentials": "response-secret"})
				resp.StatusCode = status
				resp.Header.Set("x-acs-request-id", "other-request")
				return resp, nil
			})
			_, err := c.DescribeSession(context.Background(), "test-session")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("returned HTTP %d", status)) || !strings.Contains(err.Error(), "other-request") || strings.Contains(err.Error(), "response-secret") {
				t.Fatalf("DescribeSession HTTP %d error = %v", status, err)
			}
		}
	})
}
