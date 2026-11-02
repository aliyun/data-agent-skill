package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || r.Method != http.MethodPost || r.URL.Path != "/" ||
			!strings.HasPrefix(r.Header.Get("Authorization"), "ACS3-HMAC-SHA256 Credential="+cred.AccessKeyID+",") ||
			r.Header.Get("x-acs-security-token") != cred.SecurityToken || r.Header.Get("x-api-key") != "" {
			t.Error("expected signed TLS request")
			http.Error(w, "invalid authentication", http.StatusBadRequest)
			return
		}
		handler(w, r)
	}))
	oldTransport := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
		upstream.Close()
	})
	host := strings.TrimPrefix(upstream.URL, "https://")
	return dataagent.NewClient(cred, "test",
		dataagent.WithDataAgentEndpoint(host),
		dataagent.WithDMSEnterpriseEndpoint(host),
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
		{"sts", &dataagent.Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk", SecurityToken: "test-sts"}},
	} {
		t.Run(auth.name, func(t *testing.T) {
			client := newRequestIDTestClient(t, auth.cred, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"RequestId":"list-request","Data":{"Content":[{"WorkspaceId":"test-workspace","WorkspaceName":"Test"}],"Total":1}}`)
			})
			wantAction := "ListDataAgentWorkspace"
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
	client := newRequestIDTestClient(t, &dataagent.Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk"}, func(w http.ResponseWriter, r *http.Request) {
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
	mgr, err := session.NewManager(client, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mgr.Close(); err != nil {
			t.Error(err)
		}
	})
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
	client := newRequestIDTestClient(t, &dataagent.Credential{AccessKeyID: "test-tenant-ak", AccessKeySecret: "test-tenant-sk", SecurityToken: "test-tenant-sts"}, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"RequestId":"tenant-request","Success":true,"Data":{"Content":[],"Total":0}}`)
	})
	mgr, err := session.NewManager(client, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mgr.Close(); err != nil {
			t.Error(err)
		}
	})
	s := &Server{resolve: func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
		return mgr, client, SessionDefaults{}, nil
	}}
	res, err := s.withTenant((*Server).handleListWorkspaces)(context.Background(), callReq("data_agent_list_workspaces", nil))
	if err != nil {
		t.Fatal(err)
	}
	assertUpstreamRequests(t, res, []dataagent.APIRequest{{Action: "ListDataAgentWorkspace", RequestID: "tenant-request"}})
}

// resultManager exposes only image bytes; no test or handler receives a path.
type resultManager struct {
	sessionManager
	images   []session.Image
	imageErr error
	reads    int
}

func (m *resultManager) GetResult(id string) (*session.StateSnapshot, error) {
	return &session.StateSnapshot{SessionID: id, Status: session.StatusCompleted, Conclusions: []string{"synthetic answer"}}, nil
}

func (m *resultManager) ReadImages(string) ([]session.Image, error) {
	m.reads++
	return m.images, m.imageErr
}

func TestSEC01ResultUsesRootedImageSnapshot(t *testing.T) {
	mgr := &resultManager{images: []session.Image{{Name: "img_0.png", Data: []byte("png bytes")}, {Name: "img_1.jpeg", Data: []byte("jpeg bytes")}}}
	s := &Server{mgr: mgr}
	res, err := s.handleResult(context.Background(), callReq("data_agent_result", map[string]any{"session_id": "s1"}))
	if err != nil || res.IsError || len(res.Content) != 3 || mgr.reads != 1 {
		t.Fatalf("result: %+v %v", res, err)
	}
	out := decodeResult(t, res)
	if !reflect.DeepEqual(out["images"], []any{"img_0.png", "img_1.jpeg"}) {
		t.Fatalf("image metadata: %v", out["images"])
	}
	for i, image := range mgr.images {
		content, ok := res.Content[i+1].(mcp.ImageContent)
		if !ok || content.Data != base64.StdEncoding.EncodeToString(image.Data) || content.MIMEType != inferImageMIMEType(image.Name) {
			t.Fatalf("wrong image content: %+v", res.Content[i+1])
		}
	}
	mgr.imageErr = session.ErrSessionUnavailable
	res, err = s.handleResult(context.Background(), callReq("data_agent_result", map[string]any{"session_id": "s1"}))
	if err != nil || !res.IsError || len(res.Content) != 1 || strings.Contains(resultText(res), "synthetic answer") {
		t.Fatal("unsafe image error leaked partial result")
	}
	mgr.imageErr = nil
	mgr.images = nil
	res, err = s.handleResult(context.Background(), callReq("data_agent_result", map[string]any{"session_id": "s1"}))
	if err != nil || res.IsError || len(res.Content) != 1 {
		t.Fatal("missing images broke legitimate text result")
	}
}

func TestSEC01ToolIDsRejectedBeforeWaitRegistrationOrCloud(t *testing.T) {
	// Nil dependencies deliberately panic if invalid input passes validation.
	s := &Server{}
	for name, handler := range map[string]func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error){
		"status": s.handleStatus, "wait": s.handleWaitResult, "watch": s.handleWatchSession,
		"send": s.handleSend, "result": s.handleResult, "stop": s.handleStopSession, "files": s.handleListFiles,
	} {
		for _, id := range []string{"../other", `C:\other`, "NUL", "a\nattack", strings.Repeat("x", 129)} {
			res, err := handler(context.Background(), callReq(name, map[string]any{"session_id": id, "message": "confirm", "wait_timeout": float64(5)}))
			if err != nil || !res.IsError || resultText(res) != session.ErrInvalidSessionID.Error() {
				t.Fatalf("%s invalid ID response: %+v %v", name, res, err)
			}
		}
	}
	if !errors.Is(session.ValidateSessionID("../other"), session.ErrInvalidSessionID) {
		t.Fatal("validation error contract changed")
	}
}

func TestSessionIDsRejectedBeforeTenantResolution(t *testing.T) {
	resolutions := 0
	s := &Server{waits: newWaitRegistry(), resolve: func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
		resolutions++
		return nil, nil, SessionDefaults{}, errors.New("unexpected tenant initialization")
	}}
	for name, handler := range map[string]func(*Server, context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error){
		"data_agent_status": (*Server).handleStatus, "data_agent_wait_result": (*Server).handleWaitResult,
		"data_agent_watch_session": (*Server).handleWatchSession, "data_agent_send": (*Server).handleSend,
		"data_agent_result": (*Server).handleResult, "data_agent_stop_session": (*Server).handleStopSession,
		"data_agent_list_files": (*Server).handleListFiles,
	} {
		for _, id := range []any{nil, "", "../other", `C:\other`, "NUL", "a\nattack", strings.Repeat("x", 129), float64(123)} {
			args := map[string]any{"message": "confirm", "wait_timeout": float64(5)}
			if id != nil {
				args["session_id"] = id
			}
			res, err := s.withTenant(handler)(context.Background(), callReq(name, args))
			if err != nil || res == nil || !res.IsError || !strings.Contains(resultText(res), session.ErrInvalidSessionID.Error()) {
				t.Fatalf("%s invalid ID response: %+v %v", name, res, err)
			}
		}
	}
	if resolutions != 0 || len(s.waits.inflight) != 0 {
		t.Fatalf("invalid input allocated tenant or wait state: %d %d", resolutions, len(s.waits.inflight))
	}
}
