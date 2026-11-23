package dataagent

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// tagMetaTablePage builds a ListTagMetaAsset table item.
func tagMetaTableItem(dbID string, i int) map[string]any {
	return map[string]any{
		"MetaType": "META_TABLE",
		"MetaEntityAttrs": map[string]any{
			"TableName":  fmt.Sprintf("t_%s_%03d", dbID, i),
			"TableId":    fmt.Sprintf("%s%05d", dbID, i),
			"DbId":       dbID,
			"SchemaName": "db_" + dbID,
		},
	}
}

// Workspaces above one page of imported tables must be scanned fully instead
// of silently truncating at the first page.
func TestListImportedTablesScansAllPages(t *testing.T) {
	c := NewClient(
		&Credential{AccessKeyID: "ak", AccessKeySecret: "sk"},
		"cn-hangzhou",
		WithWorkspaceID("ws-1"),
	)

	total := dmsTagMetaPageSize + 30 // 230 tables → 2 pages
	var pages []string
	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		if query.Get("Action") == "GetUserActiveTenant" {
			return jsonHTTPResponse(t, map[string]any{"Tenant": map[string]any{"Tid": "42"}}), nil
		}
		if got := query.Get("Action"); got != "ListTagMetaAsset" {
			t.Fatalf("unexpected action %q", got)
		}
		if got := query.Get("MetaType"); got != "META_TABLE" {
			t.Fatalf("MetaType = %q, want META_TABLE", got)
		}

		page := query.Get("PageNumber")
		pages = append(pages, page)
		var items []map[string]any
		switch page {
		case "1":
			for i := 0; i < dmsTagMetaPageSize; i++ {
				items = append(items, tagMetaTableItem("100", i))
			}
		case "2":
			for i := 0; i < total-dmsTagMetaPageSize; i++ {
				items = append(items, tagMetaTableItem("200", i))
			}
		default:
			t.Fatalf("unexpected page %q", page)
		}
		return jsonHTTPResponse(t, map[string]any{
			"Data":       items,
			"TotalCount": total,
			"Success":    true,
		}), nil
	})}

	got, err := c.ListImportedTables(context.Background(), "", "")
	if err != nil {
		t.Fatalf("ListImportedTables() error = %v", err)
	}
	if len(got) != total {
		t.Fatalf("ListImportedTables() len = %d, want %d", len(got), total)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages = %v, want [1 2]", pages)
	}
	// Ownership annotations survive pagination.
	last := got[total-1]
	if last.DbID != "200" || last.DbName != "db_200" {
		t.Fatalf("last table ownership = %+v", last)
	}
}

// An API that ignores PageNumber replays page 1 forever; the scanner must
// detect the repeated page and stop rather than loop to the scan cap.
func TestListImportedTablesStopsOnRepeatedPage(t *testing.T) {
	c := NewClient(
		&Credential{AccessKeyID: "ak", AccessKeySecret: "sk"},
		"cn-hangzhou",
		WithWorkspaceID("ws-1"),
	)

	calls := 0
	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		if query.Get("Action") == "GetUserActiveTenant" {
			return jsonHTTPResponse(t, map[string]any{"Tenant": map[string]any{"Tid": "42"}}), nil
		}
		calls++
		var items []map[string]any
		for i := 0; i < dmsTagMetaPageSize; i++ { // full page, no TotalCount
			items = append(items, tagMetaTableItem("100", i))
		}
		return jsonHTTPResponse(t, map[string]any{"Data": items, "Success": true}), nil
	})}

	got, err := c.ListImportedTables(context.Background(), "100", "")
	if err != nil {
		t.Fatalf("ListImportedTables() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("ListTagMetaAsset called %d times, want 2 (page 1 + repeated page detection)", calls)
	}
	if len(got) != dmsTagMetaPageSize {
		t.Fatalf("len = %d, want %d (repeated page must not be appended)", len(got), dmsTagMetaPageSize)
	}
}

// Databases share the same pager; a short first page must finish in one call.
func TestListDatabasesSinglePageStops(t *testing.T) {
	c := NewClient(
		&Credential{AccessKeyID: "ak", AccessKeySecret: "sk"},
		"cn-hangzhou",
		WithWorkspaceID("ws-1"),
	)

	calls := 0
	c.http = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		query := req.URL.Query()
		if query.Get("Action") == "GetUserActiveTenant" {
			return jsonHTTPResponse(t, map[string]any{"Tenant": map[string]any{"Tid": "42"}}), nil
		}
		calls++
		return jsonHTTPResponse(t, map[string]any{
			"Data": []map[string]any{{
				"MetaEntityAttrs": map[string]any{
					"DbId":       float64(123),
					"SchemaName": "sales",
					"DbType":     "mysql",
				},
			}},
			"TotalCount": 1,
			"Success":    true,
		}), nil
	})}

	got, err := c.ListDatabases(context.Background(), "")
	if err != nil {
		t.Fatalf("ListDatabases() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("ListTagMetaAsset called %d times, want 1", calls)
	}
	if len(got) != 1 || got[0].SchemaName != "sales" || got[0].DbID != 123 {
		t.Fatalf("databases = %+v", got)
	}
}

func TestRequestIDsIncludeResolversAndPages(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, configured *Client) {
		c := NewClient(configured.cred, configured.region)
		ctx, ids := WithRequestIDs(context.Background())
		c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Context().Value(requestIDsKey{}) != ids {
				t.Error("resolver or page request lost caller context")
			}
			params := make(map[string]any)
			for key, values := range req.URL.Query() {
				params[key] = values[0]
			}
			action := firstStr(params, "Action")
			var payload map[string]any
			switch action {
			case "GetUserActiveTenant":
				payload = map[string]any{"Tenant": map[string]any{"Tid": "42"}}
			case "GetActiveRouteUnit":
				payload = map[string]any{"Route": map[string]any{"RegionId": "unit-1"}}
			case "InitDataAgentPersonalWorkspace":
				payload = map[string]any{"Data": map[string]any{"WorkspaceId": "ws-1"}}
			case "ListTagMetaAsset":
				if got := firstStr(params, "TagName"); !strings.HasSuffix(got, "::space:ws-1") {
					t.Errorf("resolved workspace not used: TagName=%q", got)
				}
				count, start := dmsTagMetaPageSize, 0
				if firstStr(params, "PageNumber") == "2" {
					count, start = 1, dmsTagMetaPageSize
				}
				var items []map[string]any
				for i := 0; i < count; i++ {
					items = append(items, map[string]any{"DbId": start + i + 1, "SchemaName": fmt.Sprintf("db-%d", start+i)})
				}
				payload = map[string]any{"Data": items, "TotalCount": dmsTagMetaPageSize + 1}
			default:
				t.Fatalf("unexpected action %q", action)
			}
			payload["RequestID"] = action + "-id"
			return jsonHTTPResponse(t, payload), nil
		})
		got, err := c.ListDatabases(ctx, "")
		if err != nil || len(got) != dmsTagMetaPageSize+1 {
			t.Fatalf("ListDatabases: count=%d, error=%v", len(got), err)
		}
		actions := []string{"GetUserActiveTenant", "GetActiveRouteUnit", "InitDataAgentPersonalWorkspace", "ListTagMetaAsset", "ListTagMetaAsset"}
		var want []APIRequest
		for _, action := range actions {
			want = append(want, APIRequest{Action: action, RequestID: action + "-id"})
		}
		if !reflect.DeepEqual(ids.Snapshot(), want) {
			t.Fatalf("requests=%+v, want %+v", ids.Snapshot(), want)
		}
		otherCtx, otherIDs := WithRequestIDs(context.Background())
		if c.ResolveWorkspaceID(otherCtx) != "ws-1" || len(otherIDs.Snapshot()) != 0 {
			t.Fatal("cached resolution must not replay an earlier request ID")
		}
	})
}

// Signed API errors must carry the backend request id in all three failure
// shapes so tool errors are traceable to backend support tickets.
func TestSignedErrorsCarryRequestID(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		for _, tt := range []struct {
			name, body, header, want string
			status                   int
		}{
			{
				name: "http error carries header request id", status: http.StatusForbidden,
				body: `{"Message":"denied"}`, header: "REQ-HTTP-403", want: "REQ-HTTP-403",
			},
			{
				name: "success=false carries body requestId", status: http.StatusOK,
				body: `{"success":false,"code":"Forbidden","msg":"no grant","requestId":"REQ-BODY-1"}`, want: "REQ-BODY-1",
			},
			{
				name: "HttpStatusCode>=400 falls back to header", status: http.StatusOK,
				body: `{"HttpStatusCode":400,"Message":"bad param"}`, header: "REQ-HDR-400", want: "REQ-HDR-400",
			},
		} {
			t.Run(tt.name, func(t *testing.T) {
				useSignedTLSFixture(t, c, func(w http.ResponseWriter, req *http.Request) {
					switch req.URL.Query().Get("Action") {
					case "GetUserActiveTenant":
						fmt.Fprint(w, `{"Tenant":{"Tid":"42"}}`)
					case "ListTagMetaAsset":
						if req.URL.Query().Get("Tid") != "42" || req.URL.Query().Get("Version") != "2018-11-01" {
							t.Error("missing signed DMS tenant or API version")
						}
						w.Header().Set("x-acs-request-id", tt.header)
						w.WriteHeader(tt.status)
						fmt.Fprint(w, tt.body)
					default:
						t.Errorf("unexpected action %q", req.URL.Query().Get("Action"))
						http.Error(w, "unexpected action", http.StatusBadRequest)
					}
				})
				_, err := c.ListDatabases(context.Background(), "")
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error missing request id %q: %v", tt.want, err)
				}
			})
		}
	})
}
