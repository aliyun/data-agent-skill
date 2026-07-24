package dataagent

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
