package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	credential "github.com/aliyun/credentials-go/credentials"
	"github.com/golang-jwt/jwt/v5"
	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

type fakeGatewayClient struct {
	calls          int
	action         string
	first          string
	second         string
	filters        dataagent.GatewayDatabaseSearchOptions
	dbType         string
	dataSourceUUID string
	page           dataagent.GatewayPageOptions
	result         *dataagent.GatewayResult
	err            error
}

func (f *fakeGatewayClient) record(action, first, second string, page dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error) {
	f.calls++
	f.action, f.first, f.second, f.page = action, first, second, page
	return f.result, f.err
}

func (f *fakeGatewayClient) ListAgenticCatalog(_ context.Context, page dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error) {
	return f.record("ListAgenticCatalog", "", "", page)
}

func (f *fakeGatewayClient) SearchAgenticCatalog(_ context.Context, key string, page dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error) {
	return f.record("SearchAgenticCatalog", key, "", page)
}

func (f *fakeGatewayClient) ListAgenticDatabase(_ context.Context, id string, page dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error) {
	return f.record("ListAgenticDatabase", id, "", page)
}

func (f *fakeGatewayClient) ListAgenticTableBaseInfo(_ context.Context, id string, page dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error) {
	return f.record("ListAgenticTableBaseInfo", id, "", page)
}

func (f *fakeGatewayClient) GetAgenticTableColumn(_ context.Context, id, name string) (*dataagent.GatewayResult, error) {
	return f.record("GetAgenticTableColumn", id, name, dataagent.GatewayPageOptions{})
}

func (f *fakeGatewayClient) GetAgenticTableIndex(_ context.Context, id, name string) (*dataagent.GatewayResult, error) {
	return f.record("GetAgenticTableIndex", id, name, dataagent.GatewayPageOptions{})
}

func (f *fakeGatewayClient) GetAgenticCatalog(_ context.Context, id string) (*dataagent.GatewayResult, error) {
	return f.record("GetAgenticCatalog", id, "", dataagent.GatewayPageOptions{})
}

func (f *fakeGatewayClient) GetAgenticDatabase(_ context.Context, id string) (*dataagent.GatewayResult, error) {
	return f.record("GetAgenticDatabase", id, "", dataagent.GatewayPageOptions{})
}

func (f *fakeGatewayClient) SearchAgenticDatabase(_ context.Context, key string, filters dataagent.GatewayDatabaseSearchOptions, page dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error) {
	f.filters = filters
	return f.record("SearchAgenticDatabase", key, "", page)
}

func (f *fakeGatewayClient) ListAgenticDataSources(_ context.Context, dbType string, page dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error) {
	f.dbType = dbType
	return f.record("ListAgenticDataSources", "", "", page)
}

func (f *fakeGatewayClient) GetAgenticDataSource(_ context.Context, id string) (*dataagent.GatewayResult, error) {
	f.dataSourceUUID = id
	return f.record("GetAgenticDataSource", id, "", dataagent.GatewayPageOptions{})
}

func allGatewayTestTools() []gatewayToolSpec {
	return append(append([]gatewayToolSpec{}, gatewayTools...), gatewayDataSourceTools...)
}

func decodeGatewayResult(t *testing.T, result *mcp.CallToolResult, err error) gatewayEnvelope {
	t.Helper()
	if err != nil || result == nil || len(result.Content) != 1 {
		t.Fatalf("expected one text result, got %+v, %v", result, err)
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", result.Content[0])
	}
	var envelope gatewayEnvelope
	if err := json.Unmarshal([]byte(text.Text), &envelope); err != nil {
		t.Fatalf("invalid JSON result: %v: %s", err, text.Text)
	}
	if envelope.RequestID == "" || result.IsError != (envelope.Status == "ERROR") {
		t.Fatalf("inconsistent envelope: %+v, isError=%v", envelope, result.IsError)
	}
	return envelope
}

func TestGatewayRegistrationAndSchemas(t *testing.T) {
	for _, transport := range []string{"stdio", "sse", "streamable-http"} {
		for _, tc := range []struct {
			name                        string
			enabled, dataSourcesEnabled bool
			want                        int
		}{
			{"disabled", false, false, 19},
			{"gateway only", true, false, 28},
			{"data sources", true, true, 30},
			{"parent disabled", false, true, 19},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				t.Setenv("MCP_TRANSPORT", transport)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				s := New(nil, nil, "test")
				s.EnableGateway(tc.enabled, tc.dataSourcesEnabled, isRemoteTransport(transport))
				s.SetRequestLogLevel(RequestLogOff)
				f := &fakeGatewayClient{}
				s.gatewayClient = f
				s.SetResolver(func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
					t.Error("disabled tool reached identity/credential resolution")
					return nil, nil, SessionDefaults{}, errors.New("unexpected resolver call")
				})
				client := newGatewayProtocolClient(t, ctx, s, transport)
				tools, err := client.ListTools(ctx, mcp.ListToolsRequest{})
				if err != nil || len(tools.Tools) != tc.want {
					t.Fatalf("want %d tools over %s, got %+v, %v", tc.want, transport, tools, err)
				}
				names := map[string]bool{}
				for _, tool := range tools.Tools {
					names[tool.Name] = true
				}
				for group, specs := range [][]gatewayToolSpec{gatewayTools, gatewayDataSourceTools} {
					for _, spec := range specs {
						wantEnabled := tc.enabled && (group == 0 || tc.dataSourcesEnabled)
						if names[spec.name] != wantEnabled {
							t.Fatalf("tool %s registered=%v, want %v", spec.name, names[spec.name], wantEnabled)
						}
						if !wantEnabled {
							args := map[string]any{}
							for _, key := range spec.required {
								args[key] = "opaque-value"
							}
							result, err := client.CallTool(ctx, callReq(spec.name, args))
							if err == nil && (result == nil || !result.IsError) {
								t.Fatalf("disabled tool %s is callable: %+v", spec.name, result)
							}
						}
					}
				}
				if f.calls != 0 {
					t.Fatalf("disabled tools reached gateway: %d calls", f.calls)
				}
				message := s.mcp.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
				raw, err := json.Marshal(message)
				if err != nil {
					t.Fatal(err)
				}
				var response struct {
					Result struct {
						Tools []struct {
							Name   string         `json:"name"`
							Schema map[string]any `json:"inputSchema"`
						} `json:"tools"`
					} `json:"result"`
				}
				if err := json.Unmarshal(raw, &response); err != nil {
					t.Fatal(err)
				}
				want := tc.want
				if len(response.Result.Tools) != want {
					t.Fatalf("want %d tools, response=%s", want, raw)
				}
				count := 0
				for _, tool := range response.Result.Tools {
					if !strings.HasPrefix(tool.Name, "data_gateway_") {
						continue
					}
					count++
					if tool.Schema["additionalProperties"] != false {
						t.Errorf("%s permits unknown arguments", tool.Name)
					}
					if strings.Contains(tool.Name, "sql") || strings.Contains(tool.Name, "wiki") || strings.Contains(tool.Name, "acl") {
						t.Errorf("unexpected gateway tool %s", tool.Name)
					}
				}
				if count != want-19 {
					t.Errorf("gateway count=%d", count)
				}
			})
		}
	}
}

func newGatewayProtocolClient(t *testing.T, ctx context.Context, s *Server, transport string) *mcpclient.Client {
	t.Helper()
	var client *mcpclient.Client
	var err error
	switch transport {
	case "stdio":
		serverConn, clientConn := net.Pipe()
		finished := make(chan error, 1)
		go func() { finished <- mcpserver.NewStdioServer(s.mcp).Listen(ctx, serverConn, serverConn) }()
		t.Cleanup(func() {
			clientConn.Close()
			serverConn.Close()
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Error("stdio server did not stop")
			}
		})
		client = mcpclient.NewClient(mcptransport.NewIO(clientConn, clientConn, io.NopCloser(strings.NewReader(""))))
	case "sse":
		endpoint := httptest.NewServer(mcpserver.NewSSEServer(s.mcp, mcpserver.WithSSEContextFunc(s.identityHTTPContext)))
		t.Cleanup(endpoint.Close)
		client, err = mcpclient.NewSSEMCPClient(endpoint.URL + "/sse")
	case "streamable-http":
		endpoint := httptest.NewServer(mcpserver.NewStreamableHTTPServer(s.mcp, mcpserver.WithHTTPContextFunc(s.identityHTTPContext)))
		t.Cleanup(endpoint.Close)
		client, err = mcpclient.NewStreamableHttpClient(endpoint.URL + "/mcp")
	default:
		t.Fatalf("unexpected transport %q", transport)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var initialize mcp.InitializeRequest
	initialize.Params.ProtocolVersion = "2024-11-05"
	initialize.Params.ClientInfo = mcp.Implementation{Name: "gateway-test", Version: "1"}
	if _, err := client.Initialize(ctx, initialize); err != nil {
		t.Fatal(err)
	}
	return client
}

func TestGatewayToolSchemaContracts(t *testing.T) {
	contracts := map[string]struct {
		required, optional []string
		paginated          bool
	}{
		"data_gateway_list_catalogs":     {nil, nil, true},
		"data_gateway_search_catalogs":   {[]string{"search_key"}, nil, true},
		"data_gateway_list_databases":    {[]string{"catalog_uuid"}, nil, true},
		"data_gateway_list_tables":       {[]string{"database_uuid"}, nil, true},
		"data_gateway_get_table_columns": {[]string{"database_uuid", "table_qualified_name"}, nil, false},
		"data_gateway_get_table_indexes": {[]string{"database_uuid", "table_qualified_name"}, nil, false},
		"data_gateway_get_catalog":       {[]string{"catalog_uuid"}, nil, false},
		"data_gateway_get_database":      {[]string{"database_uuid"}, nil, false},
		"data_gateway_search_databases":  {[]string{"search_key"}, []string{"catalog_uuid", "db_type", "env_type"}, true},
		"data_gateway_list_data_sources": {nil, []string{"db_type"}, true},
		"data_gateway_get_data_source":   {[]string{"data_source_uuid"}, nil, false},
	}
	seen := map[string]bool{}
	for _, spec := range allGatewayTestTools() {
		t.Run(spec.name, func(t *testing.T) {
			want, ok := contracts[spec.name]
			if !ok || seen[spec.name] {
				t.Fatalf("unexpected or duplicate tool %s", spec.name)
			}
			seen[spec.name] = true
			var schema struct {
				Type                 string                    `json:"type"`
				Properties           map[string]map[string]any `json:"properties"`
				Required             []string                  `json:"required"`
				AdditionalProperties *bool                     `json:"additionalProperties"`
			}
			if err := json.Unmarshal(spec.tool().RawInputSchema, &schema); err != nil {
				t.Fatal(err)
			}
			if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties || !reflect.DeepEqual(schema.Required, want.required) {
				t.Fatalf("unexpected schema: %+v", schema)
			}
			count := len(want.required) + len(want.optional)
			if want.paginated {
				count += 2
				page := schema.Properties["page_size"]
				if page["type"] != "integer" || page["minimum"] != float64(1) || page["maximum"] != float64(100) || page["default"] != float64(50) || schema.Properties["next_token"]["type"] != "string" {
					t.Fatalf("unexpected pagination schema: %+v", schema.Properties)
				}
			}
			if len(schema.Properties) != count {
				t.Fatalf("unexpected properties: %+v", schema.Properties)
			}
			for _, names := range [][]string{want.required, want.optional} {
				for _, name := range names {
					property := schema.Properties[name]
					pattern := `\S`
					if name == "db_type" {
						pattern = `^[A-Z][A-Z0-9_]*$`
					}
					if property["type"] != "string" || property["minLength"] != float64(1) || property["pattern"] != pattern {
						t.Errorf("%s must be a nonblank string with pattern %q: %+v", name, pattern, property)
					}
					if name == "env_type" && !reflect.DeepEqual(property["enum"], []any{"product", "pre", "dev", "test", "sit", "uat", "pet", "stag"}) {
						t.Errorf("unexpected environment enum: %+v", property)
					}
				}
			}
		})
	}
	if len(seen) != len(contracts) {
		t.Fatalf("missing gateway tools: registered=%v", seen)
	}
}

func TestGatewayToolGroupsAndOrder(t *testing.T) {
	for _, tc := range []struct {
		name    string
		specs   []gatewayToolSpec
		actions []string
	}{
		{"base", gatewayTools, []string{"ListAgenticCatalog", "SearchAgenticCatalog", "ListAgenticDatabase", "ListAgenticTableBaseInfo", "GetAgenticTableColumn", "GetAgenticTableIndex", "GetAgenticCatalog", "GetAgenticDatabase", "SearchAgenticDatabase"}},
		{"data sources", gatewayDataSourceTools, []string{"ListAgenticDataSources", "GetAgenticDataSource"}},
	} {
		var actions []string
		for _, spec := range tc.specs {
			actions = append(actions, spec.action)
		}
		if !reflect.DeepEqual(actions, tc.actions) {
			t.Fatalf("%s tool group changed: %v", tc.name, actions)
		}
	}
	for _, spec := range gatewayDataSourceTools {
		for _, phrase := range []string{"control-plane", "current upstream principal", "not the chat user's database permissions"} {
			if !strings.Contains(spec.description, phrase) {
				t.Errorf("%s description must explain %q", spec.name, phrase)
			}
		}
	}
	if !strings.Contains(gatewayDataSourceTools[1].description, "missing sources remain errors") {
		t.Error("data source detail description must explain missing-source errors")
	}
}

func TestGatewayHandlersPreserveArgumentsAndResults(t *testing.T) {
	for _, spec := range allGatewayTestTools() {
		t.Run(spec.name, func(t *testing.T) {
			f := &fakeGatewayClient{result: &dataagent.GatewayResult{
				Data:      json.RawMessage(`[{"Position":9007199254740993,"Description":null,"Size":1.00000000000000001}]`),
				NextToken: " opaque+/= ", RequestID: "upstream-123",
			}}
			switch spec.action {
			case "GetAgenticCatalog":
				f.result.Data = json.RawMessage(`{"CatalogUuid":" exact/value+名 ","Description":null,"State":9007199254740993}`)
			case "GetAgenticDatabase":
				f.result.Data = json.RawMessage(`{"DatabaseUuid":" exact/value+名 ","Description":null,"EngineMeta":{"StorageCapacity":1.00000000000000001}}`)
			case "GetAgenticDataSource":
				f.result.Data = json.RawMessage(`{"DataSourceUuid":" exact/value+名 ","Alias":null,"State":-2}`)
			}
			s := &Server{gatewayClient: f}
			args := map[string]any{}
			for _, name := range spec.required {
				args[name] = " exact/value+名 "
			}
			if spec.paginated {
				args["page_size"], args["next_token"] = float64(100), " previous+/= "
			}
			result, err := s.withGateway(spec)(context.Background(), callReq(spec.name, args))
			envelope := decodeGatewayResult(t, result, err)
			if f.calls != 1 || f.action != spec.action || envelope.Status != "OK" || envelope.UpstreamRequestID != "upstream-123" {
				t.Fatalf("unexpected action/envelope: %s %+v; calls=%d", f.action, envelope, f.calls)
			}
			if len(spec.required) > 0 && f.first != " exact/value+名 " {
				t.Fatalf("first identifier altered: %q", f.first)
			}
			if len(spec.required) > 1 && f.second != " exact/value+名 " {
				t.Fatalf("qualified name altered: %q", f.second)
			}
			if spec.action == "GetAgenticDataSource" && f.dataSourceUUID != " exact/value+名 " {
				t.Fatalf("data source UUID altered: %q", f.dataSourceUUID)
			}
			if string(envelope.Data) != string(f.result.Data) {
				t.Fatalf("precision/null lost: %s", envelope.Data)
			}
			if spec.paginated {
				if f.page.PageSize != 100 || f.page.NextToken != " previous+/= " || envelope.Pagination == nil || !envelope.Pagination.HasMore || envelope.Pagination.NextToken != " opaque+/= " {
					t.Fatalf("pagination changed: %+v %+v", f.page, envelope.Pagination)
				}
			} else if envelope.Pagination != nil || f.page != (dataagent.GatewayPageOptions{}) || strings.Contains(resultText(result), `"pagination"`) {
				t.Fatal("detail/columns/indexes tools must not paginate")
			}
		})
	}
}

func TestGatewayRejectsInvalidArgumentsBeforeResolving(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec int
		args any
	}{
		{"endpoint", 0, map[string]any{"endpoint": "evil.invalid"}},
		{"action", 0, map[string]any{"action": "ExecuteScript"}},
		{"role", 0, map[string]any{"role_arn": "admin"}},
		{"credential", 0, map[string]any{"access_key": "secret"}},
		{"confirmation", 0, map[string]any{"confirm": true}},
		{"fraction", 0, map[string]any{"page_size": 1.5}},
		{"nan", 0, map[string]any{"page_size": math.NaN()}},
		{"infinity", 0, map[string]any{"page_size": math.Inf(1)}},
		{"small", 0, map[string]any{"page_size": 0}},
		{"large", 0, map[string]any{"page_size": 101}},
		{"string page", 0, map[string]any{"page_size": "50"}},
		{"null page", 0, map[string]any{"page_size": nil}},
		{"numeric token", 0, map[string]any{"next_token": 1}},
		{"array arguments", 0, []any{}},
		{"missing search", 1, nil},
		{"blank search", 1, map[string]any{"search_key": " \t\n"}},
		{"numeric uuid", 2, map[string]any{"catalog_uuid": 123}},
		{"missing qualified name", 4, map[string]any{"database_uuid": "db"}},
		{"columns page", 4, map[string]any{"database_uuid": "db", "table_qualified_name": "table", "page_size": 1}},
		{"indexes token", 5, map[string]any{"database_uuid": "db", "table_qualified_name": "table", "next_token": "next"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGatewayClient{}
			s := &Server{gatewayClient: f, gatewayRemote: true, resolve: func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
				t.Fatal("invalid arguments reached resolver")
				return nil, nil, SessionDefaults{}, nil
			}}
			req := callReq(gatewayTools[tc.spec].name, nil)
			req.Params.Arguments = tc.args
			result, err := s.withGateway(gatewayTools[tc.spec])(context.Background(), req)
			envelope := decodeGatewayResult(t, result, err)
			if envelope.Error == nil || envelope.Error.Code != "GATEWAY_INVALID_ARGUMENT" || f.calls != 0 {
				t.Fatalf("unexpected result %+v; calls=%d", envelope, f.calls)
			}
		})
	}
}

func TestGatewayStringArgumentsAndUnknownFields(t *testing.T) {
	for _, spec := range allGatewayTestTools() {
		t.Run(spec.name, func(t *testing.T) {
			base := map[string]any{}
			for _, key := range spec.required {
				base[key] = "exact-value"
			}
			reject := func(name, key string, value any, omit bool) {
				t.Helper()
				t.Run(name, func(t *testing.T) {
					args := map[string]any{}
					for k, v := range base {
						args[k] = v
					}
					if omit {
						delete(args, key)
					} else {
						args[key] = value
					}
					f := &fakeGatewayClient{}
					s := &Server{gatewayClient: f, gatewayRemote: true, resolve: func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
						t.Fatal("invalid arguments reached resolver")
						return nil, nil, SessionDefaults{}, nil
					}}
					result, err := s.withGateway(spec)(context.Background(), callReq(spec.name, args))
					envelope := decodeGatewayResult(t, result, err)
					if envelope.Error == nil || envelope.Error.Code != "GATEWAY_INVALID_ARGUMENT" || f.calls != 0 {
						t.Fatalf("unexpected result %+v; calls=%d", envelope, f.calls)
					}
				})
			}
			for _, key := range spec.required {
				reject(key+"/missing", key, nil, true)
			}
			for _, keys := range [][]string{spec.required, spec.optional} {
				for _, key := range keys {
					for _, invalid := range []struct {
						name  string
						value any
					}{
						{"null", nil}, {"empty", ""}, {"blank", " \t\n"}, {"unicode blank", "\u3000\u00a0"},
						{"number", float64(1)}, {"boolean", true}, {"array", []any{"value"}}, {"object", map[string]any{"value": "value"}},
					} {
						reject(key+"/"+invalid.name, key, invalid.value, false)
					}
				}
			}
			for _, key := range []string{"endpoint", "action", "role", "role_arn", "RoleArn", "access_key", "confirm", "unexpected", "Host", "host", "State", "state", "DbType", "DataSourceUuid"} {
				reject("unknown/"+key, key, "forbidden", false)
			}
			if spec.action == "ListAgenticDataSources" || spec.action == "GetAgenticDataSource" {
				for _, key := range []string{"search_key", "catalog_uuid", "database_uuid", "env_type", "SearchKey", "EnvType"} {
					reject("unexposed filter/"+key, key, "forbidden", false)
				}
				if spec.paginated {
					reject("unexposed filter/data_source_uuid", "data_source_uuid", "opaque", false)
				} else {
					reject("unexposed filter/db_type", "db_type", "MYSQL", false)
				}
			}
			if !spec.paginated {
				reject("no pagination/size", "page_size", 1, false)
				reject("no pagination/token", "next_token", "next", false)
			}
			if spec.action == "SearchAgenticDatabase" || spec.action == "ListAgenticDataSources" {
				for _, value := range []string{"mysql", "MySQL", " MYSQL", "MYSQL ", "MYSQL\n", "1MYSQL", "_MYSQL", "MY-SQL", "MY.SQL", "数据库"} {
					reject("db_type/"+value, "db_type", value, false)
				}
			}
			if spec.action == "SearchAgenticDatabase" {
				for _, value := range []string{"prod", "staging", "DEV", " dev", "dev ", "dev\n"} {
					reject("env_type/"+value, "env_type", value, false)
				}
				reject("unexposed filter", "database_uuid", "db", false)
			}
			if spec.paginated {
				for _, invalid := range []struct {
					name  string
					value any
				}{
					{"null", nil}, {"string", "50"}, {"boolean", true}, {"fraction", 1.5}, {"zero", 0}, {"negative", -1}, {"large", 101},
					{"nan", math.NaN()}, {"infinity", math.Inf(1)}, {"invalid number", json.Number("invalid")},
				} {
					reject("page_size/"+invalid.name, "page_size", invalid.value, false)
				}
				reject("next_token/null", "next_token", nil, false)
				reject("next_token/number", "next_token", 1, false)
			}
		})
	}
}

func TestGatewayDatabaseSearchFilterMapping(t *testing.T) {
	spec := gatewayTools[8]
	for _, tc := range []struct {
		name    string
		filters dataagent.GatewayDatabaseSearchOptions
	}{
		{"omitted", dataagent.GatewayDatabaseSearchOptions{}},
		{"catalog only", dataagent.GatewayDatabaseSearchOptions{CatalogUUID: " catalog/value+名 "}},
		{"type only", dataagent.GatewayDatabaseSearchOptions{DbType: "MYSQL"}},
		{"environment only", dataagent.GatewayDatabaseSearchOptions{EnvType: "dev"}},
		{"product", dataagent.GatewayDatabaseSearchOptions{CatalogUUID: " catalog/value+名 ", DbType: "POLARDB_2", EnvType: "product"}},
		{"pre", dataagent.GatewayDatabaseSearchOptions{EnvType: "pre"}},
		{"test", dataagent.GatewayDatabaseSearchOptions{EnvType: "test"}},
		{"sit", dataagent.GatewayDatabaseSearchOptions{EnvType: "sit"}},
		{"uat", dataagent.GatewayDatabaseSearchOptions{EnvType: "uat"}},
		{"pet", dataagent.GatewayDatabaseSearchOptions{EnvType: "pet"}},
		{"stag", dataagent.GatewayDatabaseSearchOptions{EnvType: "stag"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"search_key": " search/value+名 ", "page_size": 100, "next_token": " previous+/= "}
			for key, value := range map[string]string{"catalog_uuid": tc.filters.CatalogUUID, "db_type": tc.filters.DbType, "env_type": tc.filters.EnvType} {
				if value != "" {
					args[key] = value
				}
			}
			f := &fakeGatewayClient{result: &dataagent.GatewayResult{
				Data: json.RawMessage(`[{"DatabaseUuid":"db","Description":null,"EngineMeta":{"StorageCapacity":9007199254740993}}]`), NextToken: " next+/= ",
			}}
			s := &Server{gatewayClient: f}
			result, err := s.withGateway(spec)(context.Background(), callReq(spec.name, args))
			envelope := decodeGatewayResult(t, result, err)
			if envelope.Status != "OK" || f.calls != 1 || f.action != "SearchAgenticDatabase" || f.first != args["search_key"] || f.filters != tc.filters {
				t.Fatalf("search mapping changed: %+v, envelope=%+v", f, envelope)
			}
			if f.page.PageSize != 100 || f.page.NextToken != " previous+/= " || envelope.Pagination == nil || !envelope.Pagination.HasMore || envelope.Pagination.NextToken != " next+/= " || string(envelope.Data) != string(f.result.Data) {
				t.Fatalf("search data/pagination changed: %+v, page=%+v", envelope, f.page)
			}
		})
	}
}

func TestGatewayDataSourceFilterMapping(t *testing.T) {
	spec := gatewayDataSourceTools[0]
	for _, tc := range []struct {
		name, dbType string
		args         map[string]any
		page         dataagent.GatewayPageOptions
	}{
		{"no arguments", "", nil, dataagent.GatewayPageOptions{PageSize: 50}},
		{"empty arguments", "", map[string]any{}, dataagent.GatewayPageOptions{PageSize: 50}},
		{"omitted filter", "", map[string]any{"page_size": 1, "next_token": " previous+/= 名 "}, dataagent.GatewayPageOptions{PageSize: 1, NextToken: " previous+/= 名 "}},
		{"mysql", "MYSQL", map[string]any{"db_type": "MYSQL"}, dataagent.GatewayPageOptions{PageSize: 50}},
		{"opaque cursor", "POLARDB_2", map[string]any{"db_type": "POLARDB_2", "page_size": 100, "next_token": " previous+/= 名 "}, dataagent.GatewayPageOptions{PageSize: 100, NextToken: " previous+/= 名 "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGatewayClient{result: &dataagent.GatewayResult{
				Data: json.RawMessage(`[{"DataSourceUuid":"opaque/value+名","DbType":"MYSQL","Description":null}]`), NextToken: " next+/= 名 ", RequestID: "data-source-upstream",
			}}
			s := &Server{gatewayClient: f}
			result, err := s.withGateway(spec)(context.Background(), callReq(spec.name, tc.args))
			envelope := decodeGatewayResult(t, result, err)
			if envelope.Status != "OK" || f.calls != 1 || f.action != "ListAgenticDataSources" || f.dbType != tc.dbType || f.page != tc.page {
				t.Fatalf("list mapping changed: %+v, envelope=%+v", f, envelope)
			}
			if string(envelope.Data) != string(f.result.Data) || envelope.UpstreamRequestID != "data-source-upstream" || envelope.Pagination == nil || !envelope.Pagination.HasMore || envelope.Pagination.NextToken != " next+/= 名 " {
				t.Fatalf("list envelope changed: %+v", envelope)
			}
		})
	}
}

func TestGatewayDataSourceDetailProtocolEnvelope(t *testing.T) {
	t.Setenv("MCP_TRANSPORT", "stdio")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s := New(nil, nil, "test")
	s.EnableGateway(true, true, false)
	s.SetRequestLogLevel(RequestLogFull)
	f := &fakeGatewayClient{result: &dataagent.GatewayResult{Data: json.RawMessage(`{"DataSourceUuid":" opaque/value+名 ","Alias":null,"State":-2}`), RequestID: "data-source-upstream"}}
	s.gatewayClient = f
	client := newGatewayProtocolClient(t, ctx, s, "stdio")
	result, err := client.CallTool(ctx, callReq("data_gateway_get_data_source", map[string]any{"data_source_uuid": " opaque/value+名 "}))
	envelope := decodeGatewayResult(t, result, err)
	if envelope.Status != "OK" || envelope.Error != nil || string(envelope.Data) != string(f.result.Data) || envelope.Pagination != nil || envelope.UpstreamRequestID != "data-source-upstream" {
		t.Fatalf("detail object changed: %+v", envelope)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resultText(result)), &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["data"]) != string(f.result.Data) || len(fields) != 4 {
		t.Fatalf("detail must remain an object in a single JSON envelope: %s", resultText(result))
	}
	if f.calls != 1 || f.action != "GetAgenticDataSource" || f.dataSourceUUID != " opaque/value+名 " || f.page != (dataagent.GatewayPageOptions{}) {
		t.Fatalf("detail mapping changed: %+v", f)
	}
}

func TestGatewayPaginationDefaultsAndEmptyPage(t *testing.T) {
	for _, spec := range allGatewayTestTools() {
		if !spec.paginated {
			continue
		}
		t.Run(spec.name, func(t *testing.T) {
			f := &fakeGatewayClient{result: &dataagent.GatewayResult{Data: json.RawMessage(`[]`), NextToken: "next"}}
			s := &Server{gatewayClient: f}
			args := map[string]any{}
			for _, name := range spec.required {
				args[name] = " exact/value+名 "
			}
			for _, size := range []any{nil, json.Number("1e1")} {
				want := 50
				if size != nil {
					args["page_size"], want = size, 10
				}
				result, err := s.withGateway(spec)(context.Background(), callReq(spec.name, args))
				envelope := decodeGatewayResult(t, result, err)
				if f.page.PageSize != want || envelope.Pagination == nil || !envelope.Pagination.HasMore || envelope.Pagination.NextToken != "next" || string(envelope.Data) != `[]` || envelope.UpstreamRequestID != "" {
					t.Fatalf("unexpected pagination or invented upstream ID: %+v", envelope)
				}
			}
			f.result.NextToken = ""
			result, err := s.withGateway(spec)(context.Background(), callReq(spec.name, args))
			envelope := decodeGatewayResult(t, result, err)
			if envelope.Pagination == nil || envelope.Pagination.HasMore || strings.Contains(resultText(result), "next_token") {
				t.Fatalf("unexpected next page: %+v", envelope)
			}
		})
	}
}

func TestGatewayRemoteNeverUsesGlobalClient(t *testing.T) {
	for _, spec := range allGatewayTestTools() {
		t.Run(spec.name, func(t *testing.T) {
			args := map[string]any{}
			for _, key := range spec.required {
				args[key] = "exact-value"
			}
			for _, resolver := range []Resolver{
				nil,
				func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
					return nil, nil, SessionDefaults{}, nil
				},
				func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
					return nil, nil, SessionDefaults{}, errors.New("raw secret in resolver")
				},
			} {
				f := &fakeGatewayClient{}
				s := &Server{gatewayClient: f, gatewayRemote: true, resolve: resolver, reqLog: RequestLogFull}
				out := captureLog(t, func() {
					ctx := tenant.WithIdentity(context.Background(), "forged-user", "forged-email", "secret-token")
					result, err := s.withGateway(spec)(ctx, callReq(spec.name, args))
					envelope := decodeGatewayResult(t, result, err)
					if envelope.Error == nil || envelope.Error.Code != "GATEWAY_IDENTITY_REJECTED" || f.calls != 0 {
						t.Fatalf("unexpected identity result: %+v; calls=%d", envelope, f.calls)
					}
					if strings.Contains(resultText(result), "raw secret") {
						t.Fatal("resolver error leaked to model")
					}
				})
				for _, forbidden := range []string{"forged-user", "forged-email", "secret-token", "raw secret"} {
					if strings.Contains(out, forbidden) {
						t.Fatalf("unverified or secret value logged: %q", out)
					}
				}
			}
		})
	}
}

func TestGatewayStdioDoesNotResolveUsers(t *testing.T) {
	f := &fakeGatewayClient{result: &dataagent.GatewayResult{Data: json.RawMessage(`[]`)}}
	s := &Server{gatewayClient: f, resolve: func(context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
		t.Fatal("stdio must use process identity")
		return nil, nil, SessionDefaults{}, nil
	}}
	result, err := s.withGateway(gatewayTools[0])(context.Background(), callReq(gatewayTools[0].name, nil))
	if envelope := decodeGatewayResult(t, result, err); envelope.Status != "OK" {
		t.Fatalf("unexpected result %+v", envelope)
	}
}

func TestGatewayLogsNeverContainArgumentsOrResults(t *testing.T) {
	for _, spec := range allGatewayTestTools() {
		t.Run(spec.name, func(t *testing.T) {
			args := map[string]any{}
			for _, key := range spec.required {
				args[key] = "secret-search"
			}
			if spec.action == "SearchAgenticDatabase" {
				args["catalog_uuid"], args["db_type"], args["env_type"] = "secret-catalog", "SECRET_DB_TYPE", "pet"
			}
			if spec.action == "ListAgenticDataSources" {
				args["db_type"] = "SECRET_DB_TYPE"
			}
			if spec.paginated {
				args["next_token"] = "secret-cursor"
			}
			for _, level := range []RequestLogLevel{RequestLogOff, RequestLogBasic, RequestLogFull} {
				for _, failure := range []error{nil, errors.New("https://example.invalid/?Signature=secret-result"), &dataagent.GatewayError{Code: "GATEWAY_ACCESS_DENIED", Message: "Access denied.", RequestID: "real-upstream-id"}} {
					f := &fakeGatewayClient{result: &dataagent.GatewayResult{Data: json.RawMessage(`[{"Description":"secret-result"}]`), RequestID: "real-upstream-id"}, err: failure}
					if spec.action == "GetAgenticCatalog" || spec.action == "GetAgenticDatabase" || spec.action == "GetAgenticDataSource" {
						f.result.Data = json.RawMessage(`{"Description":"secret-result"}`)
					}
					s := &Server{gatewayClient: f, reqLog: level}
					var envelope gatewayEnvelope
					out := captureLog(t, func() {
						result, err := s.withGateway(spec)(context.Background(), callReq(spec.name, args))
						envelope = decodeGatewayResult(t, result, err)
						if failure != nil && strings.Contains(resultText(result), "secret-result") {
							t.Fatal("raw error leaked")
						}
					})
					if f.calls != 1 {
						t.Fatalf("expected upstream call, got %+v", envelope)
					}
					for _, forbidden := range []string{"secret-search", "secret-result", "secret-catalog", "SECRET_DB_TYPE", "secret-cursor", "pet"} {
						if strings.Contains(out, forbidden) {
							t.Fatalf("gateway leaked arguments or result at level %v: %q", level, out)
						}
					}
					if level == RequestLogOff && out != "" {
						t.Fatalf("off logged %q", out)
					}
					if level != RequestLogOff && (!strings.Contains(out, envelope.RequestID) || !strings.Contains(out, spec.action)) {
						t.Fatalf("missing request correlation %q", out)
					}
				}
			}
		})
	}
}

func TestGatewayProtocolCallReturnsSingleErrorJSON(t *testing.T) {
	s := New(nil, nil, "test")
	s.EnableGateway(true, false, false)
	s.SetRequestLogLevel(RequestLogFull)
	s.gatewayClient = &fakeGatewayClient{err: &dataagent.GatewayError{Code: "GATEWAY_RATE_LIMITED", Message: "Rate limited.", Retryable: true, RequestID: "upstream-error"}}
	response := s.mcp.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"data_gateway_list_catalogs","arguments":{}}}`))
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Result.IsError || len(result.Result.Content) != 1 {
		t.Fatalf("invalid protocol response %s", raw)
	}
	var envelope gatewayEnvelope
	if err := json.Unmarshal([]byte(result.Result.Content[0].Text), &envelope); err != nil {
		t.Fatalf("JSON was corrupted by request tagging: %v", err)
	}
	if envelope.Error == nil || !envelope.Error.Retryable || envelope.UpstreamRequestID != "upstream-error" {
		t.Fatalf("lost failure details %+v", envelope)
	}
}

func TestGatewayStdioDiscoveryChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f := &fakeGatewayClient{}
	s := New(nil, nil, "test")
	s.gatewayClient = f
	s.EnableGateway(true, false, false)
	s.SetRequestLogLevel(RequestLogOff)
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	if err := clientConn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- mcpserver.NewStdioServer(s.mcp).Listen(ctx, serverConn, serverConn) }()
	decoder, encoder := json.NewDecoder(clientConn), json.NewEncoder(clientConn)
	id := 0
	rpc := func(method string, params any) json.RawMessage {
		t.Helper()
		id++
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.ID != id || len(response.Error) != 0 {
			t.Fatalf("invalid protocol response %+v", response)
		}
		return response.Result
	}
	rpc("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "gateway-test", "version": "1"}})
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		t.Fatal(err)
	}
	catalog, database, qualified := "", "", ""
	for _, step := range []struct{ tool, data string }{
		{"data_gateway_list_catalogs", `[{"CatalogUuid":"catalog-uuid"}]`},
		{"data_gateway_list_databases", `[{"DatabaseUuid":"database-uuid"}]`},
		{"data_gateway_list_tables", `[{"QualifiedName":"catalog.schema.table"}]`},
		{"data_gateway_get_table_columns", `[{"ColumnName":"id","Position":1}]`},
		{"data_gateway_get_table_indexes", `[]`},
	} {
		f.result = &dataagent.GatewayResult{Data: json.RawMessage(step.data)}
		args := map[string]any{}
		switch step.tool {
		case "data_gateway_list_databases":
			args["catalog_uuid"] = catalog
		case "data_gateway_list_tables":
			args["database_uuid"] = database
		case "data_gateway_get_table_columns", "data_gateway_get_table_indexes":
			args["database_uuid"], args["table_qualified_name"] = database, qualified
		}
		raw := rpc("tools/call", map[string]any{"name": step.tool, "arguments": args})
		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || result.IsError || len(result.Content) != 1 {
			t.Fatalf("discovery %s failed: %s, %v", step.tool, raw, err)
		}
		var envelope gatewayEnvelope
		if err := json.Unmarshal([]byte(result.Content[0].Text), &envelope); err != nil || envelope.Status != "OK" {
			t.Fatalf("invalid discovery envelope %s: %v", result.Content[0].Text, err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(envelope.Data, &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			if v, ok := rows[0]["CatalogUuid"].(string); ok {
				catalog = v
			}
			if v, ok := rows[0]["DatabaseUuid"].(string); ok {
				database = v
			}
			if v, ok := rows[0]["QualifiedName"].(string); ok {
				qualified = v
			}
		}
	}
	if f.calls != 5 || f.first != database || f.second != qualified || catalog == "" || database == "" || qualified == "" {
		t.Fatalf("discovery chain did not retain identifiers: %+v", f)
	}
	clientConn.Close()
	select {
	case err := <-finished:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("stdio server did not stop")
	}
}

type gatewayTestBaseProvider struct {
	credential.Credential
	model *credential.CredentialModel
	calls atomic.Int32
	fail  atomic.Bool
}

func (p *gatewayTestBaseProvider) GetCredential() (*credential.CredentialModel, error) {
	p.calls.Add(1)
	if p.model != nil && !p.fail.Load() {
		return p.model, nil
	}
	return nil, errors.New("base-provider-secret")
}

func TestGatewayRemoteTransportsUseStrictRegistry(t *testing.T) {
	t.Setenv("DEBUG", "")
	const platformToken, jwtSecret = "platform-test-token", "test-jwt-signing-secret"
	sign := func(user string, expiry time.Time) string {
		t.Helper()
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": user, "tenant_id": "test-tenant", "agent_id": "test-agent", "exp": expiry.Unix()}).SignedString([]byte(jwtSecret))
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	validJWT := sign("alice", time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name, user, token, jwt, code string
		jwtEnabled, defaultRole      bool
		providerCalls                int32
	}{
		{name: "anonymous", code: "GATEWAY_IDENTITY_REJECTED"},
		{name: "forged identity", user: "alice", code: "GATEWAY_IDENTITY_REJECTED"},
		{name: "wrong platform token", user: "alice", token: "wrong", code: "GATEWAY_IDENTITY_REJECTED"},
		{name: "identity required", token: platformToken, code: "GATEWAY_IDENTITY_REJECTED"},
		{name: "jwt cannot downgrade", user: "alice", token: platformToken, jwtEnabled: true, code: "GATEWAY_IDENTITY_REJECTED"},
		{name: "invalid jwt", user: "alice", token: platformToken, jwtEnabled: true, jwt: "invalid-secret-jwt", code: "GATEWAY_IDENTITY_REJECTED"},
		{name: "expired jwt", user: "alice", token: platformToken, jwtEnabled: true, jwt: sign("alice", time.Now().Add(-time.Hour)), code: "GATEWAY_IDENTITY_REJECTED"},
		{name: "jwt needs platform token", jwtEnabled: true, jwt: validJWT, code: "GATEWAY_IDENTITY_REJECTED"},
		{name: "unmapped headers", user: "unmapped", token: platformToken, code: "GATEWAY_IDENTITY_UNMAPPED"},
		{name: "jwt overrides headers", user: "alice", token: platformToken, jwtEnabled: true, jwt: sign("unmapped", time.Now().Add(time.Hour)), code: "GATEWAY_IDENTITY_UNMAPPED"},
		{name: "mapped header credential failure", user: "alice", token: platformToken, code: "GATEWAY_CREDENTIAL_ERROR", providerCalls: 1},
		{name: "mapped jwt credential failure", user: "unmapped", token: platformToken, jwtEnabled: true, jwt: validJWT, code: "GATEWAY_CREDENTIAL_ERROR", providerCalls: 1},
		{name: "mapped header success", user: "alice", token: platformToken, providerCalls: 2},
		{name: "mapped jwt success", user: "unmapped", token: platformToken, jwtEnabled: true, jwt: validJWT, providerCalls: 2},
		{name: "explicit default success", user: "bob", token: platformToken, defaultRole: true, providerCalls: 2},
	} {
		for _, transport := range []string{"sse", "streamable-http"} {
			t.Run(tc.name+"/"+transport, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cfg := config.Config{Region: "cn-hangzhou", SessionsDir: t.TempDir(), Identity: config.Identity{
					Enabled: true, AuthToken: platformToken, RequireIdentity: true, Namespace: "gateway-test",
					JWT:     config.JWT{Enabled: tc.jwtEnabled, Secret: jwtSecret},
					Headers: config.IdentityHeaders{User: "x-test-user", Email: "x-test-email", Token: "x-test-token"},
					Groups:  map[string]config.IdentityGroup{"analysts": {RoleArn: "acs:ram::123:role/analyst", Users: []string{"alice"}}},
				}}
				cfg.Identity.JWT.Header = "x-test-jwt"
				if tc.jwtEnabled {
					group := cfg.Identity.Groups["analysts"]
					group.TenantID, group.AgentID = "test-tenant", "test-agent"
					cfg.Identity.Groups["analysts"] = group
				}
				if tc.defaultRole {
					cfg.Identity.Default = &config.IdentityGroup{RoleArn: "acs:ram::123:role/shared"}
				}
				cfg.ApplyDefaults()
				base := &gatewayTestBaseProvider{}
				var stsCalls, gatewayCalls atomic.Int32
				if tc.code == "" {
					ak, sk, token := "base-ak", "base-sk", "base-sts"
					base.model = &credential.CredentialModel{AccessKeyId: &ak, AccessKeySecret: &sk, SecurityToken: &token}
					expectedUser, expectedRole := tc.user, cfg.Identity.Groups["analysts"].RoleArn
					if tc.jwtEnabled {
						expectedUser = "alice"
					}
					if tc.defaultRole {
						expectedRole = cfg.Identity.Default.RoleArn
					}
					upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						switch r.URL.Query().Get("Action") {
						case "AssumeRole":
							stsCalls.Add(1)
							if r.FormValue("RoleArn") != expectedRole || r.FormValue("RoleSessionName") != "aily-"+expectedUser || r.FormValue("AccessKeyId") != "base-ak" || r.FormValue("SecurityToken") != "base-sts" || r.FormValue("Signature") == "" {
								t.Error("wrong SDK AssumeRole identity or signature")
							}
							select {
							case <-time.After(40 * time.Millisecond):
							case <-r.Context().Done():
								return
							}
							json.NewEncoder(w).Encode(map[string]any{"Credentials": map[string]string{
								"AccessKeyId": "role-ak", "AccessKeySecret": "role-sk", "SecurityToken": "role-sts",
								"Expiration": time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05Z"),
							}})
						case "ListAgenticCatalog":
							gatewayCalls.Add(1)
							if !strings.Contains(r.Header.Get("Authorization"), "Credential=role-ak") || r.Header.Get("x-acs-security-token") != "role-sts" {
								t.Error("gateway did not sign with the assumed role")
							}
							json.NewEncoder(w).Encode(map[string]any{"Success": true, "Data": []map[string]string{{"CatalogUuid": "catalog-uuid"}}, "RequestId": "gateway-upstream"})
						default:
							t.Error("unexpected upstream action")
							http.Error(w, "unexpected action", http.StatusBadRequest)
						}
					}))
					t.Cleanup(upstream.Close)
					cfg.STS.Endpoint = strings.TrimPrefix(upstream.URL, "https://")
					cfg.STS.SessionExpiration = 3600
					cfg.AgentGateway.Endpoint = "dms.cn-hangzhou.aliyuncs.com"
					localTransport := upstream.Client().Transport.(*http.Transport).Clone()
					localTransport.TLSClientConfig.ServerName = "127.0.0.1"
					localTransport.Proxy = nil
					localTransport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
						if address == "dms.cn-hangzhou.aliyuncs.com:443" {
							address = cfg.STS.Endpoint
						} else if !strings.HasPrefix(address, "127.0.0.1:") {
							return nil, errors.New("nonlocal test request rejected")
						}
						return (&net.Dialer{}).DialContext(ctx, network, address)
					}
					previous := http.DefaultTransport
					http.DefaultTransport = localTransport
					t.Cleanup(func() {
						http.DefaultTransport = previous
						localTransport.CloseIdleConnections()
					})
				}
				registry := tenant.NewRegistry(ctx, cfg, base)
				defer registry.Close()
				s := New(nil, nil, "test")
				s.EnableGateway(true, false, true)
				s.SetRequestLogLevel(RequestLogOff)
				s.SetIdentityHeaders(cfg.Identity.Headers.User, cfg.Identity.Headers.Email, cfg.Identity.Headers.Token)
				if tc.jwtEnabled {
					s.EnableJWTIdentity(cfg.Identity.JWT.Header)
				}
				s.SetResolver(func(ctx context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error) {
					tn, err := registry.Resolve(ctx)
					if err != nil || tn == nil {
						return nil, nil, SessionDefaults{}, err
					}
					return tn.Manager, tn.Client, SessionDefaults{}, nil
				})
				global := &fakeGatewayClient{}
				s.gatewayClient = global
				headers := map[string]string{"x-test-user": tc.user, "x-test-token": tc.token, "x-test-jwt": tc.jwt}
				if tc.jwtEnabled {
					delete(headers, "x-test-user")
				}
				if err := s.ConfigureHTTP(config.DefaultHTTP(), registry.Authenticate); err != nil {
					t.Fatal(err)
				}
				remote, err := s.newRemoteTransport(ctx, transport)
				if err != nil {
					t.Fatal(err)
				}
				defer remote.close()
				endpoint := httptest.NewServer(remote)
				defer endpoint.Close()
				if tc.code == "GATEWAY_IDENTITY_REJECTED" || tc.code == "GATEWAY_IDENTITY_UNMAPPED" {
					method, path := http.MethodPost, "/mcp"
					if transport == "sse" {
						method, path = http.MethodGet, "/sse"
					}
					req, _ := http.NewRequestWithContext(ctx, method, endpoint.URL+path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
					req.Header.Set("Content-Type", "application/json")
					if method == http.MethodGet {
						req.Body, req.ContentLength = nil, 0
					}
					for key, value := range headers {
						if value != "" {
							req.Header.Set(key, value)
						}
					}
					resp, err := endpoint.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()
					status := http.StatusUnauthorized
					if tc.code == "GATEWAY_IDENTITY_UNMAPPED" {
						status = http.StatusForbidden
					}
					if resp.StatusCode != status || base.calls.Load() != 0 || global.calls != 0 || stsCalls.Load() != 0 || gatewayCalls.Load() != 0 {
						t.Fatalf("rejection must occur before dispatch/credentials: status=%d provider=%d", resp.StatusCode, base.calls.Load())
					}
					return
				}
				var client *mcpclient.Client
				if transport == "sse" {
					client, err = mcpclient.NewSSEMCPClient(endpoint.URL+"/sse", mcpclient.WithHeaders(headers))
				} else {
					client, err = mcpclient.NewStreamableHttpClient(endpoint.URL+"/mcp", mcptransport.WithHTTPHeaders(headers))
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := client.Start(ctx); err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				var initialize mcp.InitializeRequest
				initialize.Params.ProtocolVersion = "2024-11-05"
				initialize.Params.ClientInfo = mcp.Implementation{Name: "gateway-test", Version: "1"}
				if _, err := client.Initialize(ctx, initialize); err != nil {
					t.Fatal(err)
				}
				tools, err := client.ListTools(ctx, mcp.ListToolsRequest{})
				if err != nil || len(tools.Tools) != 28 {
					t.Fatalf("tools/list: %+v %v", tools, err)
				}
				result, err := client.CallTool(ctx, callReq(gatewayTools[0].name, nil))
				envelope := decodeGatewayResult(t, result, err)
				if tc.code == "" {
					if envelope.Status != "OK" || envelope.Error != nil || envelope.UpstreamRequestID != "gateway-upstream" || string(envelope.Data) != `[{"CatalogUuid":"catalog-uuid"}]` || stsCalls.Load() != 1 || gatewayCalls.Load() != 1 {
						t.Fatalf("scoped gateway request failed: %+v", envelope)
					}
				} else if envelope.Error == nil || envelope.Error.Code != tc.code {
					t.Fatalf("wrong strict identity result: %+v", envelope)
				}
				if base.calls.Load() != tc.providerCalls || global.calls != 0 {
					t.Fatalf("unexpected credential/global fallback: provider=%d global=%d", base.calls.Load(), global.calls)
				}
				if tc.code == "" {
					base.fail.Store(true)
					result, err = client.CallTool(ctx, callReq(gatewayTools[0].name, nil))
					envelope = decodeGatewayResult(t, result, err)
					if envelope.Error == nil || envelope.Error.Code != "GATEWAY_CREDENTIAL_ERROR" || stsCalls.Load() != 1 || gatewayCalls.Load() != 1 || global.calls != 0 {
						t.Fatalf("refresh failure reused a cached role or global identity: %+v", envelope)
					}
				}
				if strings.Contains(resultText(result), "secret") {
					t.Fatal("raw identity or credential error leaked")
				}
			})
		}
	}
}
