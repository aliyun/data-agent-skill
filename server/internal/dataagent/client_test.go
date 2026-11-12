package dataagent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

func TestAPIFailureLogsSafeMetadata(t *testing.T) {
	cred := &Credential{AccessKeyID: "synthetic-credential-id", AccessKeySecret: "synthetic-credential-secret", SecurityToken: "synthetic-credential-token"}
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"permission", `{"Code":"Forbidden","Message":"synthetic-private-message","RequestId":"upstream-safe-id"}`, `code="Forbidden"`, 403},
		{"business", `{"Success":false,"Code":"DMS.DataAgent.SessionInBusy","Message":"synthetic-private-message","RequestId":"upstream-safe-id"}`, `code="DMS.DataAgent.SessionInBusy"`, 200},
		{"secret code", `{"Code":"synthetic-credential-secret","Message":"synthetic-private-message","RequestId":"synthetic-credential-token"}`, `code="UpstreamError"`, 500},
		{"invalid JSON", `not-json synthetic-private-message`, `error=request_failure`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewClient(cred, "test")
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{"X-Acs-Request-Id": []string{"header-safe-id"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			ctx, ids := WithRequestIDs(context.Background())
			ids.SetLogID("deadbeef")
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			_, err := c.callAPI(ctx, c.endpoint, "SendChatMessage", "2025-04-14", map[string]string{"Message": "synthetic-private-message"})
			log.SetOutput(previous)
			text := output.String()
			if err == nil || !strings.Contains(text, "[req:deadbeef] upstream") || !strings.Contains(text, tc.want) || !strings.Contains(text, `action="SendChatMessage"`) {
				t.Fatalf("missing safe error diagnostic: %s", text)
			}
			for _, value := range []string{cred.AccessKeyID, cred.AccessKeySecret, cred.SecurityToken, "synthetic-private-message"} {
				if strings.Contains(text, value) {
					t.Fatal("upstream diagnostics leaked credentials or content")
				}
			}
		})
	}
}

func TestAPIKeyFailureLogKeepsAllowlistedCode(t *testing.T) {
	c := apiKeyTestClient(t)
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"Code":"Forbidden","Message":"synthetic-private-message","RequestId":"upstream-safe-id"}`))}, nil
	})
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	_, err := c.callAPI(context.Background(), c.endpoint, "ListDataAgentWorkspace", "2025-04-14", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "UpstreamError" {
		t.Fatal("caller error redaction changed")
	}
	text := output.String()
	if !strings.Contains(text, `code="Forbidden"`) || !strings.Contains(text, `upstream_request_id="upstream-safe-id"`) || strings.Contains(text, "synthetic-private-message") || strings.Contains(text, c.credential().APIKey) {
		t.Fatal("API Key diagnostic lost safe code or leaked content", text)
	}
}

func TestRequestLogIDsDoNotChangeResponseIDs(t *testing.T) {
	cred := &Credential{AccessKeyID: "test-id", AccessKeySecret: "synthetic-private-secret"}
	c := NewClient(cred, "test")
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"RequestId":"initial-id","Success":true}`))}, nil
	})
	ctx, ids := WithRequestIDs(context.Background())
	if _, err := c.callAPI(ctx, c.endpoint, "SendChatMessage", "2025-04-14", map[string]string{"Message": "initial"}); err != nil {
		t.Fatal(err)
	}
	if got := ids.Snapshot(); len(got) != 1 || got[0].RequestID != "initial-id" {
		t.Fatal("response request ID changed", got)
	}
	if got := ids.LogSnapshot(); len(got) != 0 {
		t.Fatal("message echo entered log metadata", got)
	}
}

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

func apiKeyTestClient(t *testing.T, opts ...ClientOption) *Client {
	t.Helper()
	c := NewClient(&Credential{APIKey: "synthetic-api-key"}, "cn-shanghai", opts...)
	blocked := roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected HTTP request (including STS or DMS routing probes)")
		return nil, errors.New("unexpected request")
	})
	c.http.Transport, c.sse.httpClient.Transport = blocked, blocked
	return c
}

func apiKeyRequestPayload(t *testing.T, req *http.Request) map[string]interface{} {
	t.Helper()
	if req.Method != http.MethodPost || req.URL.Scheme != "https" || req.URL.Path != "/apikey" || req.URL.RawQuery != "" ||
		req.Header.Get("x-api-key") != "synthetic-api-key" || req.Header.Get("Authorization") != "" ||
		req.Header.Get("Content-Type") != "application/json" || req.Header.Get("User-Agent") != userAgent {
		t.Error("invalid API Key request authentication, URL, or headers")
	}
	for name := range req.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-acs-") {
			t.Error("API Key request contains ACS signing headers")
		}
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Errorf("invalid API Key JSON request: %v", err)
	}
	return payload
}

func TestAPIKeyActionRoutesAndNestedParams(t *testing.T) {
	for _, override := range []bool{false, true} {
		for _, tc := range []struct {
			action, wireAction string
			stream, dms        bool
		}{
			{"CreateDataAgentSession", "CreateDataAgentSession", false, false},
			{"DescribeDataAgentSession", "DescribeDataAgentSession", false, false},
			{"ListDataAgentWorkspace", "ListDataAgentWorkSpace", false, false},
			{"ListCustomAgent", "ListCustomAgent", false, false},
			{"SendChatMessage", "SendChatMessage", true, false},
			{"GetChatContent", "GetChatContent", true, false},
			{"DescribeDataAgentUsage", "DescribeDataAgentUsage", true, false},
			{"UpdateDataAgentSession", "UpdateDataAgentSession", true, false},
			{"CreateDataAgentFeedback", "CreateDataAgentFeedback", true, false},
			{"ListTagMetaAsset", "listTagMetaAsset", false, true},
		} {
			t.Run(fmt.Sprintf("%t/%s", override, tc.action), func(t *testing.T) {
				c := apiKeyTestClient(t)
				if override {
					c = apiKeyTestClient(t, WithAPIKeyEndpoint("control.example.invalid"), WithAPIKeyStreamEndpoint("stream.example.invalid"))
				}
				version := "2025-04-14"
				if tc.dms {
					version = "2018-11-01"
				}
				calls := 0
				c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					payload := apiKeyRequestPayload(t, req)
					host := c.APIKeyEndpoint()
					if tc.stream {
						host = c.APIKeyStreamEndpoint()
					}
					if req.URL.Host != host || req.Header.Get("Accept") != "application/json" ||
						payload["Action"] != tc.wireAction || payload["Version"] != version || payload["RegionId"] != "cn-shanghai" {
						t.Fatal("action rename, version or endpoint selection changed")
					}
					if jsonObj(payload, "SessionConfig")["Mode"] != "pro" || jsonObj(payload, "DataSource")["DbName"] != "db" ||
						!reflect.DeepEqual(payload["MetaItems"], []interface{}{map[string]interface{}{"MetaId": "db,table"}}) ||
						payload["Literal"] != "{not-json" || payload["PageNumber"] != "1" {
						t.Fatal("nested JSON or scalar parameters changed")
					}
					return jsonHTTPResponse(t, map[string]interface{}{"success": true}), nil
				})
				params := map[string]string{"SessionConfig": `{"Mode":"pro"}`, "DataSource": `{"DbName":"db"}`,
					"MetaItems": `[{"MetaId":"db,table"}]`, "Literal": "{not-json", "PageNumber": "1"}
				var err error
				if tc.dms {
					_, err = c.callDMSEnterprise(context.Background(), c.DMSEnterpriseEndpoint(), tc.action, version, params)
				} else {
					_, err = c.callAPI(context.Background(), c.endpoint, tc.action, version, params)
				}
				if err != nil || calls != 1 {
					t.Fatalf("calls=%d, error=%v", calls, err)
				}
			})
		}
	}
}

func TestAPIKeyWorkspaceSessionsAndMetadata(t *testing.T) {
	t.Setenv("DATA_AGENT_SESSION_LOOKBACK_DAYS", "3")
	c := apiKeyTestClient(t)
	calls := make(map[string]int)
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		payload := apiKeyRequestPayload(t, req)
		action := firstStr(payload, "Action")
		calls[action]++
		if payload["DmsUnit"] != nil || payload["Tid"] != nil {
			t.Fatal("API Key mode performed DMS routing or tenant resolution")
		}
		var data interface{}
		switch action {
		case "ListDataAgentWorkSpace":
			if payload["WorkspaceType"] != "MY" || payload["PageNumber"] != "1" {
				t.Fatal("workspace lookup did not select MY workspaces")
			}
			data = map[string]interface{}{"Content": []interface{}{map[string]string{"WorkspaceId": "ws-first"}, map[string]string{"WorkspaceId": "ws-second"}}}
		case "ListDataAgentSession":
			start, err1 := strconv.ParseInt(firstStr(payload, "StartTime"), 10, 64)
			end, err2 := strconv.ParseInt(firstStr(payload, "EndTime"), 10, 64)
			if err1 != nil || err2 != nil || end-start != int64(72*time.Hour/time.Millisecond) ||
				end < time.Now().Add(-time.Minute).UnixMilli() || payload["CreateStartTime"] != nil || payload["CreateEndTime"] != nil || payload["WorkspaceId"] != "ws-first" {
				t.Fatal("API Key session lookup lost millisecond timestamps or workspace")
			}
			data = map[string]interface{}{"Content": []interface{}{map[string]string{"sessionId": "session-1", "agentId": "agent-1", "status": "FINISHED"}}}
		case "listTagMetaAsset":
			if payload["Version"] != "2018-11-01" || payload["TagName"] != "sys::DMS-DA::cn-shanghai::space:ws-first" {
				t.Fatal("metadata lookup lost workspace or version")
			}
			data = map[string]interface{}{"data": []interface{}{map[string]interface{}{"MetaEntityAttrs": map[string]interface{}{
				"DbId": 42, "SchemaName": "db", "TableId": "table-1", "TableName": "table",
			}}}, "TotalCount": 1}
		default:
			t.Fatalf("unexpected action %q; API Keys must not call STS, GetActiveRouteUnit or GetUserActiveTenant", action)
		}
		return jsonHTTPResponse(t, map[string]interface{}{"success": true, "code": "success", "data": data}), nil
	})
	if c.ResolveDMSUnit(context.Background()) != "" || c.resolveTid(context.Background(), c.DMSEnterpriseEndpoint()) != "" || len(calls) != 0 {
		t.Fatal("API Key routing resolution should be a no-op")
	}
	if c.ResolveWorkspaceID(context.Background()) != "ws-first" || c.ResolveWorkspaceID(context.Background()) != "ws-first" || calls["ListDataAgentWorkSpace"] != 1 {
		t.Fatal("first MY workspace was not cached")
	}
	sessions, err := c.ListRemoteSessions(context.Background(), "")
	if err != nil || len(sessions) != 1 || sessions[0].SessionID != "session-1" || sessions[0].WorkspaceID != "ws-first" {
		t.Fatalf("sessions=%+v error=%v", sessions, err)
	}
	dbs, err := c.ListDatabases(context.Background(), "")
	if err != nil || len(dbs) != 1 || dbs[0].DbID != 42 {
		t.Fatalf("databases=%+v error=%v", dbs, err)
	}
	tables, err := c.ListImportedTables(context.Background(), "42", "")
	if err != nil || len(tables) != 1 || tables[0].TableID != "table-1" || calls["listTagMetaAsset"] != 2 {
		t.Fatalf("tables=%+v error=%v", tables, err)
	}
}

func TestAPIKeyUnsupportedDMSOperationsDoNotSend(t *testing.T) {
	c := apiKeyTestClient(t, WithWorkspaceID("ws"))
	for _, action := range []string{"GetActiveRouteUnit", "GetUserActiveTenant", "ListTables", "ListInstances", "SearchDatabase", "TagMetaAsset", "Unknown"} {
		_, err := c.callDMSEnterprise(context.Background(), c.DMSEnterpriseEndpoint(), action, "2018-11-01", nil)
		if !errors.Is(err, ErrAPIKeyUnsupported) {
			t.Fatalf("%s error=%v", action, err)
		}
	}
	_, tablesErr := c.ListTables(context.Background(), "db")
	_, instancesErr := c.ListInstances(context.Background(), "private-query", "mysql", 1, 10)
	_, searchErr := c.SearchDatabases(context.Background(), "private-query", 1, 10)
	importErr := c.ImportDatabase(context.Background(), ImportDatabaseOpts{DmsDbID: "db", Tables: []string{"table"}})
	for _, err := range []error{tablesErr, instancesErr, searchErr, importErr} {
		if !errors.Is(err, ErrAPIKeyUnsupported) || strings.Contains(err.Error(), "private-query") {
			t.Fatalf("public DMS operation must fail safely without HTTP: %v", err)
		}
	}
}

func TestAPIKeyResponseEnvelopesAndRequestIDs(t *testing.T) {
	for _, tc := range []struct {
		name, body, want, id string
	}{
		{"plain", `{"Data":{"SessionId":"s"}}`, `{"Data":{"SessionId":"s"},"RequestId":"header-id"}`, "header-id"},
		{"outer ID", `{"success":true,"code":"success","requestId":"outer-id","data":{"agentId":"a"}}`, `{"agentId":"a","RequestId":"outer-id"}`, "outer-id"},
		{"inner ID", `{"code":"success","requestId":"outer-id","data":{"RequestID":"inner-id","Content":[]}}`, `{"Content":[],"RequestId":"inner-id"}`, "inner-id"},
		{"list", `{"code":"success","data":{"data":[{"DbId":42}],"TotalCount":1}}`, `{"data":[{"DbId":42}],"TotalCount":1,"RequestId":"header-id"}`, "header-id"},
		{"one layer", `{"code":"success","data":{"code":"success","data":{"FileId":"f"}}}`, `{"code":"success","data":{"FileId":"f"},"RequestId":"header-id"}`, "header-id"},
		{"secret ID", `{"code":"success","requestId":"outer-id","data":{"requestId":"synthetic-api-key","sessionId":"s"}}`, `{"sessionId":"s","RequestId":"outer-id"}`, "outer-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := apiKeyTestClient(t)
			ctx, ids := WithRequestIDs(context.Background())
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Context().Value(requestIDsKey{}) != ids {
					t.Fatal("API Key request lost caller context")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"header-id"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			got, err := c.callAPI(ctx, c.endpoint, "DescribeDataAgentSession", "2025-04-14", nil)
			var want map[string]interface{}
			json.Unmarshal([]byte(tc.want), &want)
			if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(ids.Snapshot(), []APIRequest{{Action: "DescribeDataAgentSession", RequestID: tc.id}}) {
				t.Fatalf("body=%v ids=%v error=%v", got, ids.Snapshot(), err)
			}
		})
	}
}

func TestAPIKeySessionAndUploadResponses(t *testing.T) {
	for _, envelope := range []string{"top", "gateway", "nested"} {
		t.Run(envelope, func(t *testing.T) {
			c := apiKeyTestClient(t)
			want := &UploadSignature{UploadHost: "https://upload.example.invalid", UploadDir: "dir/", Policy: "policy",
				OssSignature: "signature", OssSignatureVersion: "OSS4-HMAC-SHA256", OssDate: "20261027T000000Z",
				OssSecurityToken: "oss-token", OssCredential: "oss-credential"}
			c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				payload := apiKeyRequestPayload(t, req)
				var data map[string]interface{}
				switch payload["Action"] {
				case "CreateDataAgentSession":
					if payload["DatabaseId"] != "db" || jsonObj(payload, "SessionConfig")["Mode"] != "pro" {
						t.Fatal("create lost session configuration")
					}
					data = map[string]interface{}{"sessionId": "s", "agentId": "a"}
				case "DescribeDataAgentSession":
					data = map[string]interface{}{"agentId": "a", "agentStatus": "FINISHED"}
				case "SendChatMessage":
					if req.URL.Host != c.APIKeyStreamEndpoint() || payload["MessageType"] != "primary" ||
						payload["Message"] != "private-query" || jsonObj(payload, "DataSource")["DbName"] != "db" {
						t.Fatal("send lost message or nested data source")
					}
					data = map[string]interface{}{"success": true}
				case "DescribeFileUploadSignature":
					if payload["FileName"] != "test.csv" || payload["FileSize"] != "123" {
						t.Fatal("signature request lost file metadata")
					}
					data = map[string]interface{}{"uploadHost": want.UploadHost, "uploadDir": want.UploadDir, "policy": want.Policy,
						"ossSignature": want.OssSignature, "ossSignatureVersion": want.OssSignatureVersion, "ossDate": want.OssDate,
						"ossSecurityToken": want.OssSecurityToken, "ossCredential": want.OssCredential}
				case "FileUploadCallback":
					if payload["Filename"] != "test.csv" || payload["UploadLocation"] != "dir/test.csv" || payload["FileSize"] != "123" || payload["FileFrom"] != "Skill" {
						t.Fatal("callback request lost file metadata")
					}
					data = map[string]interface{}{"fileId": "file-1"}
				default:
					t.Fatal("unexpected API Key request")
				}
				if envelope == "nested" {
					data = map[string]interface{}{"Data": data}
				}
				if envelope != "top" {
					data = map[string]interface{}{"success": true, "code": "success", "data": data}
				}
				data["requestId"] = "upload-request"
				return jsonHTTPResponse(t, data), nil
			})
			ctx := context.Background()
			created, err := c.CreateSession(ctx, CreateSessionOpts{DatabaseID: "db", Mode: "pro"})
			if err != nil || created.SessionID != "s" || created.AgentID != "a" || created.RequestID != "upload-request" {
				t.Fatalf("create=%+v error=%v", created, err)
			}
			described, err := c.DescribeSession(ctx, "s", "ws")
			if err != nil || described.AgentStatus != "FINISHED" || described.RequestID != "upload-request" {
				t.Fatalf("describe=%+v error=%v", described, err)
			}
			if err := c.SendMessage(ctx, SendMessageOpts{SessionID: "s", AgentID: "a", Message: "private-query", DataSource: &DataSource{DbName: "db"}}); err != nil {
				t.Fatal(err)
			}
			got, err := c.GetFileUploadSignature(ctx, "test.csv", 123)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("signature=%+v error=%v", got, err)
			}
			fileID, err := c.FileUploadCallback(ctx, "test.csv", "dir/test.csv", 123)
			if err != nil || fileID != "file-1" {
				t.Fatalf("file=%q error=%v", fileID, err)
			}
		})
	}
}

func TestAPIKeyResponseLimit(t *testing.T) {
	for _, size := range []int{apiKeyMaxResponseBytes, apiKeyMaxResponseBytes + 10000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			c := apiKeyTestClient(t)
			payload := `{"value":"` + strings.Repeat("x", size-len(`{"value":""}`)) + `"}`
			reader := strings.NewReader(payload)
			body := &failingResponseBody{Reader: reader, err: io.EOF}
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"limit-request"}}, Body: body}, nil
			})
			ctx, ids := WithRequestIDs(context.Background())
			result, err := c.callAPI(ctx, c.endpoint, "DescribeDataAgentSession", "2025-04-14", nil)
			if size == apiKeyMaxResponseBytes {
				if err != nil || result == nil || reader.Len() != 0 {
					t.Fatalf("response at limit rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "exceeds 4 MiB") || !strings.Contains(err.Error(), "limit-request") ||
				IsDefiniteRejection(err) || result != nil || reader.Len() != size-apiKeyMaxResponseBytes-1 {
				t.Fatalf("unbounded read or unclear failure: remaining=%d error=%v", reader.Len(), err)
			}
			if !body.closed || !reflect.DeepEqual(ids.Snapshot(), []APIRequest{{Action: "DescribeDataAgentSession", RequestID: "limit-request"}}) {
				t.Fatal("response body not closed or request ID lost")
			}
		})
	}
}

func TestAPIKeyBusyErrorsPreserveSafeBusinessCode(t *testing.T) {
	const busy = "DMS.DataAgent.SessionInBusy"
	const privateMessage = "synthetic-api-key private-query"
	for _, tc := range []struct {
		name, body, code string
	}{
		{"root", `{"success":false,"error_code":"` + busy + `","error_message":"` + privateMessage + `"}`, busy},
		{"nested", `{"code":"success","data":{"success":true,"error_code":"` + busy + `","error_message":"` + privateMessage + `"}}`, busy},
		{"nested PascalCase", `{"success":true,"data":{"Data":{"Success":false,"ErrorCode":"` + busy + `","ErrorMessage":"` + privateMessage + `"}}}`, busy},
		{"not allowlisted", `{"success":false,"error_code":"` + busy + `.synthetic-api-key","error_message":"` + privateMessage + `"}`, "UpstreamError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := apiKeyTestClient(t)
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"busy-id"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			ctx, ids := WithRequestIDs(context.Background())
			err := c.SendMessage(ctx, SendMessageOpts{Message: "private-query"})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != tc.code || apiErr.RequestID != "busy-id" || !IsDefiniteRejection(err) {
				t.Fatalf("lost business failure semantics: %v", err)
			}
			if tc.code == busy && apiErr.Message != "Session is busy; wait for the current request to finish." {
				t.Fatal("busy message must be fixed and safe")
			}
			for _, secret := range []string{"synthetic-api-key", "private-query"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("busy error leaked sensitive input")
				}
			}
			if !reflect.DeepEqual(ids.Snapshot(), []APIRequest{{Action: "SendChatMessage", RequestID: "busy-id"}}) {
				t.Fatal("busy failure lost its request ID")
			}
		})
	}
}

func TestAPIKeyErrorsAreSanitized(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	const sensitive = "synthetic-api-key private-query https://example.invalid/?Signature=signed-secret"
	for _, tc := range []struct {
		name, body, id string
		status         int
		definite       bool
	}{
		{"root error", `{"success":false,"ErrorCode":"` + sensitive + `","ErrorMessage":"` + sensitive + `"}`, "header-id", 200, true},
		{"nested error", `{"code":"success","requestId":"outer-id","data":{"Data":{"Success":false,"RequestId":"inner-id","Message":"` + sensitive + `"}}}`, "inner-id", 200, true},
		{"unsafe ID", `{"success":false,"RequestId":"synthetic-api-key","Message":"` + sensitive + `"}`, "header-id", 403, true},
		{"query ID", `{"success":false,"RequestId":"private-query","Message":"` + sensitive + `"}`, "header-id", 403, true},
		{"server error", `{"Message":"` + sensitive + `"}`, "header-id", 503, false},
		{"malformed", `{"secret":"` + sensitive, "header-id", 200, false},
		{"null", `null`, "header-id", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := apiKeyTestClient(t)
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{"X-Request-Id": []string{"header-id"}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			ctx, ids := WithRequestIDs(context.Background())
			err := c.SendMessage(ctx, SendMessageOpts{Message: "private-query"})
			if err == nil || IsDefiniteRejection(err) != tc.definite || !strings.Contains(err.Error(), tc.id) {
				t.Fatalf("error classification or request ID changed: %v", err)
			}
			for _, secret := range []string{"synthetic-api-key", "private-query", "https://", "signed-secret"} {
				if strings.Contains(err.Error(), secret) || strings.Contains(logs.String(), secret) {
					t.Fatal("API Key error or log leaked sensitive data")
				}
			}
			if !reflect.DeepEqual(ids.Snapshot(), []APIRequest{{Action: "SendChatMessage", RequestID: tc.id}}) {
				t.Fatal("failed API Key request lost its safe request ID")
			}
		})
	}
	for _, readFailure := range []bool{false, true} {
		c := apiKeyTestClient(t)
		cause := fmt.Errorf("%s: %w", sensitive, context.DeadlineExceeded)
		body := &failingResponseBody{Reader: strings.NewReader(""), err: cause}
		c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			if !readFailure {
				return nil, cause
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": []string{"read-id"}}, Body: body}, nil
		})
		err := c.SendMessage(context.Background(), SendMessageOpts{Message: "private-query"})
		if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "synthetic-api-key") ||
			strings.Contains(err.Error(), "private-query") || strings.Contains(err.Error(), "https://") || IsDefiniteRejection(err) {
			t.Fatalf("unsafe or misclassified transport failure: %v", err)
		}
		if readFailure && (!body.closed || !strings.Contains(err.Error(), "read-id")) {
			t.Fatal("read failure lost body cleanup or request ID")
		}
	}
}

func TestAPIKeyRedirectsNeverForward(t *testing.T) {
	// Cover every 3xx response, including those net/http does not follow itself.
	for status := 300; status < 400; status++ {
		for _, destination := range []string{"samehost", "crosshost", "downgrade", "malformed"} {
			for _, mode := range []string{"control", "data", "metadata", "sse"} {
				t.Run(fmt.Sprintf("%d/%s/%s", status, destination, mode), func(t *testing.T) {
					c := apiKeyTestClient(t)
					initial, forwarded := 0, 0
					transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
						if req.URL.Path != "/apikey" {
							forwarded++
							return jsonHTTPResponse(t, map[string]interface{}{"success": true}), nil
						}
						initial++
						apiKeyRequestPayload(t, req)
						location := "/redirect-target"
						switch destination {
						case "crosshost":
							location = "https://other.example.invalid/redirect-target"
						case "downgrade":
							location = "http://other.example.invalid/redirect-target"
						case "malformed":
							location = "https://other.example.invalid/%zz"
						}
						location += "?Signature=signed-secret&Key=synthetic-api-key"
						return &http.Response{StatusCode: status, Header: http.Header{
							"Location": []string{location}, "X-Acs-Request-Id": []string{"synthetic-api-key"}, "X-Request-Id": []string{"redirect-id"},
						}, Body: io.NopCloser(strings.NewReader("event: SSE_FINISH\ndata: {\"content\":\"synthetic-api-key\"}\n\n"))}, nil
					})
					c.http.Transport, c.sse.httpClient.Transport = transport, transport
					var err error
					switch mode {
					case "control":
						_, err = c.CreateSession(context.Background(), CreateSessionOpts{})
					case "data":
						err = c.SendMessage(context.Background(), SendMessageOpts{Message: "private-query"})
					case "metadata":
						_, err = c.ListDatabases(context.Background(), "ws")
					case "sse":
						events := make(chan SSEEvent, 4)
						var finished bool
						finished, err = c.sse.doStream(context.Background(), "a", "s", 2, events)
						if finished || len(events) != 0 {
							t.Fatal("SSE consumed a redirect body")
						}
					}
					if err == nil || IsDefiniteRejection(err) || initial != 1 || forwarded != 0 {
						t.Fatalf("redirect sent, retried, or accepted: initial=%d forwarded=%d error=%v", initial, forwarded, err)
					}
					for _, secret := range []string{"synthetic-api-key", "signed-secret", "private-query", "https://", "http://"} {
						if strings.Contains(err.Error(), secret) {
							t.Fatal("redirect error leaked sensitive data")
						}
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
	recordRequestID(base, "uncollected", "uncollected-id", "uncollected-id")
	recordRequestID(ctx, "missing", "", "")
	recordRequestID(ctx, "first", "same-id", "same-id")
	recordRequestID(ctx, "first", "same-id", "same-id")
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
	recordRequestID(child, "child", "child-id", "child-id")
	if !reflect.DeepEqual(ids.Snapshot(), want) || len(childIDs.Snapshot()) != 1 {
		t.Fatal("nested collector leaked into its parent")
	}
	recordRequestID(ctx, "later", "later-id", "later-id")
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
			recordRequestID(ctx, "concurrent", strconv.Itoa(i), strconv.Itoa(i))
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

func TestSendMessageWorkMode(t *testing.T) {
	for _, auth := range []string{"signed", "apikey"} {
		for _, tc := range []struct {
			name, workMode, wireMode string
		}{
			{name: "omitted"},
			{name: "analysis", workMode: "ANALYSIS", wireMode: "analysis"},
			{name: "chat", workMode: "CHAT", wireMode: "chat"},
			{name: "lowercase", workMode: "chat", wireMode: "chat"},
		} {
			t.Run(auth+"/"+tc.name, func(t *testing.T) {
				c := NewClient(&Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk"}, "cn-shanghai", WithDMSUnit("test-unit"))
				if auth == "apikey" {
					c = apiKeyTestClient(t)
				}
				opts := SendMessageOpts{
					AgentID: "test-agent", SessionID: "test-session", Message: "initial query",
					Mode: "pro", PlanMode: "force", WorkMode: tc.workMode,
				}
				calls := 0
				c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					var sessionCfg map[string]interface{}
					if auth == "apikey" {
						payload := apiKeyRequestPayload(t, req)
						if payload["Action"] != "SendChatMessage" || req.URL.Host != c.APIKeyStreamEndpoint() {
							t.Fatal("incorrect API Key send action or endpoint")
						}
						if _, ok := payload["WorkMode"]; ok {
							t.Fatal("WorkMode must be nested in SessionConfig")
						}
						var ok bool
						sessionCfg, ok = payload["SessionConfig"].(map[string]interface{})
						if !ok {
							t.Fatal("API Key SessionConfig must be a nested JSON object")
						}
					} else {
						params := req.URL.Query()
						if params.Get("Action") != "SendChatMessage" || params.Has("WorkMode") {
							t.Fatal("incorrect signed send action or top-level WorkMode")
						}
						if err := json.Unmarshal([]byte(params.Get("SessionConfig")), &sessionCfg); err != nil {
							t.Fatalf("decode signed SessionConfig query: %v", err)
						}
					}
					want := map[string]interface{}{"Language": "CHINESE", "Mode": opts.Mode, "PlanMode": opts.PlanMode}
					if tc.workMode != "" {
						want["WorkMode"] = tc.wireMode
					} else if _, ok := sessionCfg["WorkMode"]; ok {
						t.Fatal("omitted WorkMode must not send an empty or default value")
					}
					if !reflect.DeepEqual(sessionCfg, want) {
						t.Fatalf("SessionConfig = %#v, want %#v", sessionCfg, want)
					}
					return jsonHTTPResponse(t, map[string]interface{}{"Success": true}), nil
				})
				if err := c.SendMessage(context.Background(), opts); err != nil {
					t.Fatalf("SendMessage() error = %v", err)
				}
				if calls != 1 {
					t.Fatalf("SendMessage made %d requests, want exactly one", calls)
				}
			})
		}
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
