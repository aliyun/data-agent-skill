package dataagent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestBuildUserAgentUsesSkillSessionID(t *testing.T) {
	sessionID := "0123456789abcdef0123456789ABCDEF"
	got := buildUserAgent(sessionID)
	want := userAgentPrefix + "/0123456789abcdef0123456789abcdef"
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

	sessionID := strings.TrimPrefix(got, prefix)
	if len(sessionID) != 32 {
		t.Fatalf("fallback session id length = %d, want 32", len(sessionID))
	}
	if _, err := hex.DecodeString(sessionID); err != nil {
		t.Fatalf("fallback session id should be hex: %v", err)
	}
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

	got, err := c.ListWorkspaces("ALL")
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

	got, err := c.ListCustomAgents("", "")
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
				},
			},
		}), nil
	})}

	got, err := c.ListRemoteSessions("")
	if err != nil {
		t.Fatalf("ListRemoteSessions() error = %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "session-1" {
		t.Fatalf("ListRemoteSessions() = %+v, want session-1", got)
	}
}

func TestListRemoteSessionsAddsTimeRangeForAPIKeyAuth(t *testing.T) {
	c := NewClient(
		&Credential{APIKey: "api-key"},
		"cn-hangzhou",
		WithDMSUnit("cn-hangzhou"),
		WithWorkspaceID("ws-dev"),
	)

	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("parse request body: %v", err)
		}
		if got := payload["WorkspaceId"]; got != "ws-dev" {
			t.Fatalf("WorkspaceId = %v, want ws-dev", got)
		}
		if payload["StartTime"] == "" || payload["EndTime"] == "" {
			t.Fatalf("API key ListRemoteSessions must include StartTime/EndTime, payload=%v", payload)
		}
		for _, key := range []string{"StartTime", "EndTime"} {
			value, ok := payload[key].(string)
			if !ok {
				t.Fatalf("%s = %T(%v), want millisecond timestamp string", key, payload[key], payload[key])
			}
			ms, err := strconv.ParseInt(value, 10, 64)
			if err != nil || ms < 1_000_000_000_000 {
				t.Fatalf("%s = %q, want millisecond timestamp", key, value)
			}
		}
		if payload["CreateStartTime"] != nil || payload["CreateEndTime"] != nil {
			t.Fatalf("API key ListRemoteSessions should not include signed time params, payload=%v", payload)
		}
		return jsonHTTPResponse(t, map[string]any{
			"success": true,
			"code":    "success",
			"data": map[string]any{
				"Content": []map[string]string{
					{
						"SessionId": "session-api-key",
						"AgentId":   "agent-1",
						"Status":    "COMPLETED",
						"Mode":      "ANALYSIS",
					},
				},
			},
		}), nil
	})}

	got, err := c.ListRemoteSessions("")
	if err != nil {
		t.Fatalf("ListRemoteSessions() error = %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "session-api-key" || got[0].WorkspaceID != "ws-dev" {
		t.Fatalf("ListRemoteSessions() = %+v, want session-api-key in ws-dev", got)
	}
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

func testClientAuthModes(t *testing.T, run func(*testing.T, *Client)) {
	t.Helper()
	for _, auth := range []struct {
		name string
		cred *Credential
	}{
		{"signed", &Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk", SecurityToken: "test-sts"}},
		{"api-key", &Credential{APIKey: "test-api-key"}},
	} {
		t.Run(auth.name, func(t *testing.T) {
			c := NewClient(auth.cred, "cn-hangzhou", WithDMSUnit("test-unit"), WithWorkspaceID("test-workspace"))
			c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				t.Fatalf("unexpected request: %s", req.URL.Host)
				return nil, errors.New("unexpected request")
			})}
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
				calls := 0
				c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodPost {
						t.Fatalf("method = %s, want POST", req.Method)
					}
					params := make(map[string]any)
					if c.cred.IsAPIKey() {
						if req.URL.Host != c.APIKeyStreamEndpoint() || req.URL.Path != "/apikey" || req.Header.Get("x-api-key") != c.cred.APIKey {
							t.Fatal("SendMessage did not use API Key data plane authentication")
						}
						if err := json.NewDecoder(req.Body).Decode(&params); err != nil {
							t.Fatalf("decode API Key request body: %v", err)
						}
					} else {
						if req.Header.Get("Authorization") == "" || req.Header.Get("x-acs-security-token") != c.cred.SecurityToken {
							t.Fatal("SendMessage did not use signed authentication")
						}
						for key, values := range req.URL.Query() {
							params[key] = values[0]
						}
					}
					for key, want := range map[string]string{
						"Action":      "SendChatMessage",
						"MessageType": tt.want,
						"Message":     opts.Message,
						"SessionId":   opts.SessionID,
						"AgentId":     opts.AgentID,
						"WorkspaceId": opts.WorkspaceID,
					} {
						if got := params[key]; got != want {
							t.Errorf("%s = %v, want %q", key, got, want)
						}
					}
					return jsonHTTPResponse(t, map[string]any{"success": true}), nil
				})
				if err := c.SendMessage(opts); err != nil {
					t.Fatalf("SendMessage() error = %v", err)
				}
				if calls != 1 {
					t.Fatalf("SendMessage made %d requests, want exactly one", calls)
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
			name: "gateway data envelope preserves outer request id",
			body: `{"success":true,"code":"success","requestId":"gateway-request","data":{"success":true,"error_code":"DMS.DataAgent.SessionInBusy","error_message":"busy"}}`,
			want: &APIError{Code: busyCode, Message: "busy", RequestID: "gateway-request"},
		},
		{
			name: "nested envelopes prefer backend request id",
			body: `{"success":true,"code":"success","requestId":"gateway-request","data":{"Success":true,"Data":{"success":true,"ErrorCode":"DMS.DataAgent.SessionInBusy","ErrorMessage":"busy","RequestID":"backend-request"}}}`,
			want: &APIError{Code: busyCode, Message: "busy", RequestID: "backend-request"},
		},
		{
			name: "outer failure before unwrap",
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
			want: &APIError{Message: "Found"}, unknown: true,
		},
		{name: "plain success", body: `{"success":true}`},
		{name: "empty object", body: `{}`},
		{name: "success code with message", body: `{"code":"success","message":"accepted"}`},
		{name: "uppercase success code", body: `{"Code":"Success","Message":"accepted"}`},
		{name: "numeric success code", body: `{"code":200,"message":"accepted"}`},
		{name: "zero success code", body: `{"code":0,"message":"accepted"}`},
		{name: "OK success code", body: `{"Code":"OK","Message":"accepted"}`},
		{name: "normal gateway envelope", body: `{"success":true,"code":"success","data":{"success":true}}`},
		{name: "normal signed envelope", body: `{"Success":true,"Data":{"MessageId":"m-1"}}`},
		{name: "null and empty error code", body: `{"success":true,"error_code":null,"ErrorCode":""}`},
		{name: "success error codes", body: `{"error_code":"0","ErrorCode":"success","Message":"accepted"}`},
		{name: "ordinary top level code", body: `{"code":"business-value"}`},
		{name: "ordinary data code", body: `{"Data":{"code":"business-value","message":"business description"}}`},
		{name: "ordinary code after unwrap", body: `{"success":true,"code":"success","data":{"code":"business-value","message":"business description"}}`},
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
				calls := 0
				c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodPost {
						t.Fatalf("method = %s, want POST", req.Method)
					}
					if c.cred.IsAPIKey() {
						if req.URL.Host != c.APIKeyStreamEndpoint() || req.URL.Path != "/apikey" || req.Header.Get("x-api-key") != c.cred.APIKey {
							t.Fatal("SendMessage did not use API Key data plane authentication")
						}
						var payload map[string]interface{}
						if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
							t.Fatal(err)
						}
						if payload["Action"] != "SendChatMessage" || payload["SessionId"] != "test-session" || payload["Message"] != "hello" {
							t.Fatalf("unexpected SendMessage payload: %v", payload)
						}
					} else {
						if req.URL.Query().Get("Action") != "SendChatMessage" || req.URL.Query().Get("Message") != "hello" || req.Header.Get("Authorization") == "" || req.Header.Get("x-acs-security-token") != c.cred.SecurityToken {
							t.Fatal("SendMessage did not use signed authentication")
						}
					}
					return &http.Response{
						StatusCode: status,
						Header:     http.Header{"X-Acs-Request-Id": []string{"header-request"}},
						Body:       io.NopCloser(strings.NewReader(tt.body)),
					}, nil
				})
				err := c.SendMessage(SendMessageOpts{AgentID: "test-agent", SessionID: "test-session", Message: "hello"})
				if calls != 1 {
					t.Fatalf("SendMessage made %d requests, want exactly one", calls)
				}
				if tt.want == nil {
					if err != nil {
						t.Fatalf("SendMessage() error = %v", err)
					}
					return
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
				err := c.SendMessage(SendMessageOpts{SessionID: "test-session"})
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
				err := c.SendMessage(SendMessageOpts{SessionID: "test-session"})
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

func TestAPIResponsePayloadCompatibility(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		inner := map[string]interface{}{"Content": []interface{}{map[string]interface{}{"WorkspaceId": "test-workspace"}}}
		payload := map[string]interface{}{"success": true, "code": "success", "data": inner}
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonHTTPResponse(t, payload), nil
		})
		got, err := c.callAPI(c.endpoint, "ListDataAgentWorkspace", "2025-04-14", nil)
		if err != nil {
			t.Fatal(err)
		}
		want := payload
		if c.cred.IsAPIKey() {
			want = inner
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("response shape changed: got %#v, want %#v", got, want)
		}
		for _, status := range []int{201, 400, 503} {
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				resp := jsonHTTPResponse(t, map[string]interface{}{"Message": "backend message", "credentials": "response-secret"})
				resp.StatusCode = status
				resp.Header.Set("x-acs-request-id", "other-request")
				return resp, nil
			})
			_, err := c.DescribeSession("test-session")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("returned HTTP %d", status)) || !strings.Contains(err.Error(), "other-request") || strings.Contains(err.Error(), "response-secret") {
				t.Fatalf("DescribeSession HTTP %d error = %v", status, err)
			}
		}
	})
}
