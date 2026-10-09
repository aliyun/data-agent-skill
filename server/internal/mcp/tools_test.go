package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
)

func TestMapRemoteStatusNormalizesServerStates(t *testing.T) {
	cases := map[string]session.Status{
		"RUNNING":       session.StatusRunning,
		"WAIT_INPUT":    session.StatusWaitingInput,
		"WAITING_INPUT": session.StatusWaitingInput,
		"IDLE":          session.StatusCompleted,
		"FINISHED":      session.StatusCompleted,
		"COMPLETED":     session.StatusCompleted,
		"STOPPED":       session.StatusCompleted,
		"FAILED":        session.StatusError,
		"ERROR":         session.StatusError,
		"CANCELED":      session.StatusCanceled,
	}

	for raw, want := range cases {
		if got := mapRemoteStatus(raw); got != want {
			t.Fatalf("mapRemoteStatus(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"auto":     "auto",
		"lite":     "lite",
		"pro":      "pro",
		"ultra":    "ultra",
		"PRO":      "pro",   // case-insensitive
		"ASK_DATA": "lite",  // legacy mapping
		"ANALYSIS": "pro",   // legacy mapping
		"INSIGHT":  "ultra", // legacy mapping
		"CLAW":     "CLAW",  // unknown values pass through
	}
	for in, want := range cases {
		if got := normalizeMode(in); got != want {
			t.Fatalf("normalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddUpstreamRequestsPreservesContent(t *testing.T) {
	requests := []dataagent.APIRequest{
		{Action: "CreateDataAgentSession", RequestID: "create-request"},
		{Action: "SendChatMessage", RequestID: "send-request"},
	}
	original := []mcp.Content{
		mcp.NewTextContent(`{"database_id":9007199254740993}`),
		mcp.NewImageContent("test-image", "image/png"),
	}
	res := &mcp.CallToolResult{Content: append([]mcp.Content(nil), original...)}
	addUpstreamRequests(res, requests)
	if len(res.Content) != 3 || !reflect.DeepEqual(res.Content[:2], original) {
		t.Fatalf("business content changed: %+v", res.Content)
	}
	assertUpstreamRequests(t, res, requests)

	errorResult := mcp.NewToolResultError("upstream failed")
	addUpstreamRequests(errorResult, requests)
	if len(errorResult.Content) != 1 || resultText(errorResult) != "upstream failed" {
		t.Fatalf("error result changed: %+v", errorResult)
	}
	local := mcp.NewToolResultText(`{"status":"completed"}`)
	addUpstreamRequests(local, nil)
	if len(local.Content) != 1 {
		t.Fatalf("invented request ID for local result: %+v", local)
	}
	addUpstreamRequests(nil, requests)
}

func assertUpstreamRequests(t *testing.T, res *mcp.CallToolResult, want []dataagent.APIRequest) {
	t.Helper()
	if res == nil || res.IsError || len(res.Content) < 2 {
		t.Fatalf("missing successful response metadata: %+v", res)
	}
	text, ok := res.Content[len(res.Content)-1].(mcp.TextContent)
	if !ok {
		t.Fatalf("metadata is not text: %+v", res.Content)
	}
	var metadata struct {
		RequestID        string                 `json:"request_id"`
		UpstreamRequests []dataagent.APIRequest `json:"upstream_requests"`
	}
	if err := json.Unmarshal([]byte(text.Text), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.RequestID != want[len(want)-1].RequestID || !reflect.DeepEqual(metadata.UpstreamRequests, want) {
		t.Fatalf("metadata = %+v, want %+v", metadata, want)
	}
}

func newRequestIDTestClient(t *testing.T, cred *dataagent.Credential, handler http.HandlerFunc) *dataagent.Client {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	oldTransport := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
		upstream.Close()
	})
	host := strings.TrimPrefix(upstream.URL, "https://")
	return dataagent.NewClient(cred, "test",
		dataagent.WithDataAgentEndpoint(host),
		dataagent.WithAPIKeyEndpoint(host),
		dataagent.WithAPIKeyStreamEndpoint(host),
		dataagent.WithDMSUnit("test-unit"),
		dataagent.WithWorkspaceID("test-workspace"),
	)
}

func TestToolSuccessReturnsUpstreamRequestID(t *testing.T) {
	for _, auth := range []struct {
		name string
		cred *dataagent.Credential
	}{
		{"signed", &dataagent.Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk"}},
		{"api-key", &dataagent.Credential{APIKey: "test-key"}},
	} {
		t.Run(auth.name, func(t *testing.T) {
			client := newRequestIDTestClient(t, auth.cred, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"RequestId":"list-request","Data":{"Content":[{"WorkspaceId":"test-workspace","WorkspaceName":"Test"}],"Total":1}}`)
			})
			wantAction := "ListDataAgentWorkspace"
			if auth.name == "api-key" {
				wantAction = "ListDataAgentWorkSpace"
			}
			for _, level := range []RequestLogLevel{RequestLogOff, RequestLogBasic} {
				s := &Server{client: client, reqLog: level}
				res, err := s.withTenant((*Server).handleListWorkspaces)(context.Background(), callReq("data_agent_list_workspaces", nil))
				if err != nil {
					t.Fatal(err)
				}
				assertUpstreamRequests(t, res, []dataagent.APIRequest{{Action: wantAction, RequestID: "list-request"}})
				var workspaces []dataagent.WorkspaceInfo
				if err := json.Unmarshal([]byte(resultText(res)), &workspaces); err != nil || len(workspaces) != 1 || workspaces[0].WorkspaceID != "test-workspace" {
					t.Fatalf("business array changed: %s, err=%v", resultText(res), err)
				}

				s.mgr = &waitManager{snap: session.StateSnapshot{SessionID: "test-session", Status: session.StatusCompleted}}
				local, err := s.withTenant((*Server).handleStatus)(context.Background(), callReq("data_agent_status", map[string]any{"session_id": "test-session"}))
				if err != nil || local.IsError || len(local.Content) != 1 {
					t.Fatalf("local status inherited prior API request IDs: %+v, err=%v", local, err)
				}
			}
		})
	}
}

func TestToolSuccessWithoutUpstreamRequestID(t *testing.T) {
	client := newRequestIDTestClient(t, &dataagent.Credential{APIKey: "test-key"}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Data":{"Content":[],"Total":0}}`)
	})
	s := &Server{client: client, reqLog: RequestLogBasic}
	res, err := s.withTenant((*Server).handleListWorkspaces)(context.Background(), callReq("data_agent_list_workspaces", nil))
	if err != nil || res.IsError || len(res.Content) != 1 {
		t.Fatalf("missing upstream ID was replaced with a local ID: %+v, err=%v", res, err)
	}
}

func TestToolSuccessPreservesPagedRequestIDs(t *testing.T) {
	client := newRequestIDTestClient(t, &dataagent.Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk"}, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("PageNumber")
		count := 50
		if page == "2" {
			count = 1
		}
		content := make([]map[string]string, count)
		for i := range content {
			content[i] = map[string]string{"WorkspaceId": fmt.Sprintf("page-%s-workspace-%d", page, i)}
		}
		json.NewEncoder(w).Encode(map[string]any{
			"RequestId": "page-" + page,
			"Data":      map[string]any{"Content": content, "Total": 51},
		})
	})
	s := &Server{client: client}
	res, err := s.withTenant((*Server).handleListWorkspaces)(context.Background(), callReq("data_agent_list_workspaces", nil))
	if err != nil {
		t.Fatal(err)
	}
	assertUpstreamRequests(t, res, []dataagent.APIRequest{
		{Action: "ListDataAgentWorkspace", RequestID: "page-1"},
		{Action: "ListDataAgentWorkspace", RequestID: "page-2"},
	})
}

func TestConcurrentToolRequestIDsAreIsolated(t *testing.T) {
	var entered atomic.Int32
	bothEntered := make(chan struct{})
	client := newRequestIDTestClient(t, &dataagent.Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk"}, func(w http.ResponseWriter, r *http.Request) {
		if entered.Add(1) == 2 {
			close(bothEntered)
		}
		select {
		case <-bothEntered:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"RequestId": "request-" + r.URL.Query().Get("WorkspaceType"),
			"Data":      map[string]any{"Content": []any{}, "Total": 0},
		})
	})
	s := &Server{client: client}
	type outcome struct {
		kind string
		res  *mcp.CallToolResult
		err  error
	}
	results := make(chan outcome, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, kind := range []string{"MY", "SHARED"} {
		go func() {
			res, err := s.withTenant((*Server).handleListWorkspaces)(ctx, callReq("data_agent_list_workspaces", map[string]any{"type": kind}))
			results <- outcome{kind, res, err}
		}()
	}
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		assertUpstreamRequests(t, got.res, []dataagent.APIRequest{{Action: "ListDataAgentWorkspace", RequestID: "request-" + got.kind}})
	}
}

func TestCreateAndSendToolUpstreamRequestIDs(t *testing.T) {
	var sends atomic.Int32
	client := newRequestIDTestClient(t, &dataagent.Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk"}, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("Action") {
		case "CreateDataAgentSession":
			fmt.Fprint(w, `{"RequestId":"create-request","Data":{"SessionId":"test-session","AgentId":"test-agent"}}`)
		case "DescribeDataAgentSession":
			fmt.Fprint(w, `{"RequestId":"describe-request","Data":{"SessionStatus":"RUNNING","AgentId":"test-agent"}}`)
		case "SendChatMessage":
			fmt.Fprintf(w, `{"RequestId":"send-request-%d","Success":true}`, sends.Add(1))
		case "GetChatContent":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("x-acs-request-id", "background-stream-request")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	})
	mgr := session.NewManager(client, t.TempDir())
	s := &Server{client: client, mgr: mgr}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res, err := s.withTenant((*Server).handleCreateSession)(ctx, createReq(map[string]any{
		"query": "synthetic test", "custom_agent_id": "test-custom-agent",
	}))
	if err != nil || res.IsError {
		t.Fatalf("create failed: %+v, err=%v", res, err)
	}
	t.Cleanup(func() { mgr.StopSession("test-session") })
	cancel()
	assertUpstreamRequests(t, res, []dataagent.APIRequest{
		{Action: "CreateDataAgentSession", RequestID: "create-request"},
		{Action: "DescribeDataAgentSession", RequestID: "describe-request"},
		{Action: "SendChatMessage", RequestID: "send-request-1"},
	})
	if decodeResult(t, res)["session_id"] != "test-session" {
		t.Fatalf("create payload changed: %s", resultText(res))
	}

	res, err = s.withTenant((*Server).handleSend)(context.Background(), callReq("data_agent_send", map[string]any{
		"session_id": "test-session", "message": "synthetic follow-up",
	}))
	if err != nil {
		t.Fatal(err)
	}
	assertUpstreamRequests(t, res, []dataagent.APIRequest{{Action: "SendChatMessage", RequestID: "send-request-2"}})
}

func TestTenantToolSuccessReturnsResolvedClientRequestID(t *testing.T) {
	client := newRequestIDTestClient(t, &dataagent.Credential{APIKey: "test-tenant-key"}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"requestId":"tenant-request","code":"success","data":{"Content":[],"Total":0}}`)
	})
	mgr := session.NewManager(client, t.TempDir())
	s := &Server{resolve: func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
		return mgr, client, SessionDefaults{}, nil
	}}
	res, err := s.withTenant((*Server).handleListWorkspaces)(context.Background(), callReq("data_agent_list_workspaces", nil))
	if err != nil {
		t.Fatal(err)
	}
	assertUpstreamRequests(t, res, []dataagent.APIRequest{{Action: "ListDataAgentWorkSpace", RequestID: "tenant-request"}})
}
