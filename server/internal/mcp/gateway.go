package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

type gatewayClient interface {
	ListAgenticCatalog(context.Context, dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error)
	SearchAgenticCatalog(context.Context, string, dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error)
	ListAgenticDatabase(context.Context, string, dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error)
	ListAgenticTableBaseInfo(context.Context, string, dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error)
	GetAgenticTableColumn(context.Context, string, string) (*dataagent.GatewayResult, error)
	GetAgenticTableIndex(context.Context, string, string) (*dataagent.GatewayResult, error)
	GetAgenticCatalog(context.Context, string) (*dataagent.GatewayResult, error)
	GetAgenticDatabase(context.Context, string) (*dataagent.GatewayResult, error)
	SearchAgenticDatabase(context.Context, string, dataagent.GatewayDatabaseSearchOptions, dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error)
	ListAgenticDataSources(context.Context, string, dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error)
	GetAgenticDataSource(context.Context, string) (*dataagent.GatewayResult, error)
}

type gatewayToolSpec struct {
	name        string
	action      string
	description string
	required    []string
	optional    []string
	paginated   bool
}

var gatewayTools = []gatewayToolSpec{
	{"data_gateway_list_catalogs", "ListAgenticCatalog", "List one page of authorized AI data gateway catalogs. Metadata only; does not execute SQL.", nil, nil, true},
	{"data_gateway_search_catalogs", "SearchAgenticCatalog", "Search gateway catalogs by name, alias, or connection-address prefix. Returns one page of metadata, not SQL results.", []string{"search_key"}, nil, true},
	{"data_gateway_list_databases", "ListAgenticDatabase", "List one page of databases in a gateway catalog. CatalogUuid must come from gateway catalog discovery. Gateway UUIDs are not Data Agent database IDs.", []string{"catalog_uuid"}, nil, true},
	{"data_gateway_list_tables", "ListAgenticTableBaseInfo", "List one page of tables in a gateway database. Use DatabaseUuid from gateway database discovery; retain each table's QualifiedName unchanged.", []string{"database_uuid"}, nil, true},
	{"data_gateway_get_table_columns", "GetAgenticTableColumn", "Get gateway table column metadata using the exact DatabaseUuid and QualifiedName returned by gateway discovery. No pagination.", []string{"database_uuid", "table_qualified_name"}, nil, false},
	{"data_gateway_get_table_indexes", "GetAgenticTableIndex", "Get gateway table index metadata using the exact DatabaseUuid and QualifiedName returned by gateway discovery. No pagination.", []string{"database_uuid", "table_qualified_name"}, nil, false},
	{"data_gateway_get_catalog", "GetAgenticCatalog", "Get a gateway catalog metadata object using the exact CatalogUuid returned by gateway discovery. No pagination; does not execute SQL.", []string{"catalog_uuid"}, nil, false},
	{"data_gateway_get_database", "GetAgenticDatabase", "Get a gateway database metadata object using the exact DatabaseUuid returned by gateway discovery, not a Data Agent database ID. No pagination.", []string{"database_uuid"}, nil, false},
	{"data_gateway_search_databases", "SearchAgenticDatabase", "Search one page of authorized gateway databases. Omit catalog_uuid to search across authorized catalogs. Keep identifiers, uppercase db_type, filters, and cursor unchanged; does not execute SQL.", []string{"search_key"}, []string{"catalog_uuid", "db_type", "env_type"}, true},
}

var gatewayDataSourceTools = []gatewayToolSpec{
	{"data_gateway_list_data_sources", "ListAgenticDataSources", "List one page of data source control-plane summaries visible to the current upstream principal, not the chat user's database permissions. Optionally filter by uppercase db_type; omit it for no type filter. Keep cursor and filter unchanged. Returns an array, not null; metadata only, does not execute SQL.", nil, []string{"db_type"}, true},
	{"data_gateway_get_data_source", "GetAgenticDataSource", "Get a data source control-plane summary visible to the current upstream principal, not the chat user's database permissions. Keep the DataSourceUuid from discovery unchanged as data_source_uuid. Returns an object; missing sources remain errors, not successful null results. No pagination; metadata only, does not execute SQL.", []string{"data_source_uuid"}, nil, false},
}

func (spec gatewayToolSpec) tool() mcp.Tool {
	properties := map[string]any{}
	for _, names := range [][]string{spec.required, spec.optional} {
		for _, name := range names {
			property := map[string]any{"type": "string", "minLength": 1, "pattern": `\S`}
			switch name {
			case "db_type":
				property["pattern"] = `^[A-Z][A-Z0-9_]*$`
			case "env_type":
				property["enum"] = []string{"product", "pre", "dev", "test", "sit", "uat", "pet", "stag"}
			}
			properties[name] = property
		}
	}
	if spec.paginated {
		properties["page_size"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "default": 50}
		properties["next_token"] = map[string]any{"type": "string", "description": "Copy the previous page's next_token unchanged, using the same identity, target, and filters."}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(spec.required) != 0 {
		schema["required"] = spec.required
	}
	raw, _ := json.Marshal(schema)
	tool := mcp.NewToolWithRawSchema(spec.name, spec.description, raw)
	tool.Annotations = mcp.ToolAnnotation{
		ReadOnlyHint: mcp.ToBoolPtr(true), DestructiveHint: mcp.ToBoolPtr(false),
		IdempotentHint: mcp.ToBoolPtr(true), OpenWorldHint: mcp.ToBoolPtr(true),
	}
	return tool
}

func (s *Server) EnableGateway(enabled, dataSourcesEnabled, remote bool) {
	s.gatewayRemote = remote
	if !enabled {
		return
	}
	for _, spec := range gatewayTools {
		s.mcp.AddTool(spec.tool(), s.withGateway(spec))
	}
	if dataSourcesEnabled {
		for _, spec := range gatewayDataSourceTools {
			s.mcp.AddTool(spec.tool(), s.withGateway(spec))
		}
	}
}

type gatewayPagination struct {
	HasMore   bool   `json:"has_more"`
	NextToken string `json:"next_token,omitempty"`
}

type gatewayFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type gatewayEnvelope struct {
	Status            string             `json:"status"`
	Data              json.RawMessage    `json:"data,omitempty"`
	Pagination        *gatewayPagination `json:"pagination,omitempty"`
	RequestID         string             `json:"request_id"`
	UpstreamRequestID string             `json:"upstream_request_id,omitempty"`
	Error             *gatewayFailure    `json:"error,omitempty"`
}

func (s *Server) withGateway(spec gatewayToolSpec) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		started := time.Now()
		envelope := gatewayEnvelope{Status: "ERROR", RequestID: newRequestID()}
		var caller *tenant.GatewayCaller
		defer func() { s.logGatewayCall(spec, caller, envelope, started) }()
		fail := func(err error, code, message string) (*mcp.CallToolResult, error) {
			failure := &gatewayFailure{Code: code, Message: message}
			var upstream *dataagent.GatewayError
			if errors.As(err, &upstream) {
				failure.Code, failure.Message, failure.Retryable = upstream.Code, upstream.Message, upstream.Retryable
				envelope.UpstreamRequestID = upstream.RequestID
			}
			envelope.Error = failure
			result, marshalErr := jsonResult(envelope)
			if result != nil {
				result.IsError = true
			}
			return result, marshalErr
		}

		args, page, valid := spec.arguments(req)
		if !valid {
			return fail(nil, "GATEWAY_INVALID_ARGUMENT", "Arguments must match the gateway tool schema.")
		}
		client := s.gatewayClient
		if s.gatewayRemote {
			if s.resolve == nil {
				return fail(nil, "GATEWAY_IDENTITY_REJECTED", "Verified gateway identity is required.")
			}
			ctx, caller = tenant.WithGatewayIdentity(ctx)
			_, scoped, _, err := s.resolve(ctx)
			if err != nil {
				return fail(err, "GATEWAY_IDENTITY_REJECTED", "Gateway identity could not be resolved.")
			}
			if scoped == nil || (caller.UserID == "" && caller.Email == "") || caller.Group == "" || caller.RoleArn == "" {
				return fail(nil, "GATEWAY_IDENTITY_REJECTED", "Verified gateway identity is required.")
			}
			client = scoped
		}
		if client == nil {
			return fail(nil, "GATEWAY_CREDENTIAL_ERROR", "Gateway credentials are unavailable.")
		}
		result, err := invokeGateway(ctx, client, spec.action, args, page)
		if err != nil {
			return fail(err, "GATEWAY_UNAVAILABLE", "Gateway request failed.")
		}
		envelope.Status = "OK"
		envelope.Data = result.Data
		envelope.UpstreamRequestID = result.RequestID
		if spec.paginated {
			envelope.Pagination = &gatewayPagination{HasMore: result.NextToken != "", NextToken: result.NextToken}
		}
		return jsonResult(envelope)
	}
}

func (spec gatewayToolSpec) arguments(req mcp.CallToolRequest) (map[string]any, dataagent.GatewayPageOptions, bool) {
	page := dataagent.GatewayPageOptions{PageSize: 50}
	args := req.GetArguments()
	if req.Params.Arguments != nil {
		if _, ok := req.Params.Arguments.(map[string]any); !ok {
			return nil, page, false
		}
	}
	for key, value := range args {
		allowed := false
		for _, names := range [][]string{spec.required, spec.optional} {
			for _, name := range names {
				if key == name {
					allowed = true
					break
				}
			}
		}
		if spec.paginated && key == "page_size" {
			var number float64
			switch n := value.(type) {
			case float64:
				number = n
			case int:
				number = float64(n)
			case json.Number:
				var err error
				number, err = n.Float64()
				if err != nil {
					return nil, page, false
				}
			default:
				return nil, page, false
			}
			if math.IsNaN(number) || number < 1 || number > 100 || math.Trunc(number) != number {
				return nil, page, false
			}
			page.PageSize = int(number)
			continue
		}
		if spec.paginated && key == "next_token" {
			value, ok := value.(string)
			if !ok {
				return nil, page, false
			}
			page.NextToken = value
			continue
		}
		if !allowed {
			return nil, page, false
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, page, false
		}
		switch key {
		case "db_type":
			if !dataagent.GatewayDatabaseTypeValid(text) {
				return nil, page, false
			}
		case "env_type":
			if !dataagent.GatewayEnvironmentTypeValid(text) {
				return nil, page, false
			}
		}
	}
	for _, key := range spec.required {
		if _, ok := args[key]; !ok {
			return nil, page, false
		}
	}
	return args, page, true
}

func invokeGateway(ctx context.Context, client gatewayClient, action string, args map[string]any, page dataagent.GatewayPageOptions) (*dataagent.GatewayResult, error) {
	switch action {
	case "ListAgenticCatalog":
		return client.ListAgenticCatalog(ctx, page)
	case "SearchAgenticCatalog":
		return client.SearchAgenticCatalog(ctx, args["search_key"].(string), page)
	case "ListAgenticDatabase":
		return client.ListAgenticDatabase(ctx, args["catalog_uuid"].(string), page)
	case "ListAgenticTableBaseInfo":
		return client.ListAgenticTableBaseInfo(ctx, args["database_uuid"].(string), page)
	case "GetAgenticTableColumn":
		return client.GetAgenticTableColumn(ctx, args["database_uuid"].(string), args["table_qualified_name"].(string))
	case "GetAgenticTableIndex":
		return client.GetAgenticTableIndex(ctx, args["database_uuid"].(string), args["table_qualified_name"].(string))
	case "GetAgenticCatalog":
		return client.GetAgenticCatalog(ctx, args["catalog_uuid"].(string))
	case "GetAgenticDatabase":
		return client.GetAgenticDatabase(ctx, args["database_uuid"].(string))
	case "SearchAgenticDatabase":
		catalogUUID, _ := args["catalog_uuid"].(string)
		dbType, _ := args["db_type"].(string)
		envType, _ := args["env_type"].(string)
		filters := dataagent.GatewayDatabaseSearchOptions{CatalogUUID: catalogUUID, DbType: dbType, EnvType: envType}
		return client.SearchAgenticDatabase(ctx, args["search_key"].(string), filters, page)
	case "ListAgenticDataSources":
		dbType, _ := args["db_type"].(string)
		return client.ListAgenticDataSources(ctx, dbType, page)
	case "GetAgenticDataSource":
		return client.GetAgenticDataSource(ctx, args["data_source_uuid"].(string))
	default:
		return nil, errors.New("unsupported gateway action")
	}
}
