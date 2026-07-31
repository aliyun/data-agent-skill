package mcp

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

type Server struct {
	mgr      sessionManager
	client   dataAgentClient
	version  string
	mcp      *server.MCPServer
	resolve  Resolver
	defaults SessionDefaults // tenant group defaults for the current request

	// identity header names copied into the request context on HTTP/SSE
	// transports; default to the Feishu Aily convention (x-aily-*).
	hdrUser  string
	hdrEmail string
	hdrToken string
	hdrJWT   string
	// jwtIdentity is true when identity comes from a signed token, so the
	// plain identity headers must not be treated as the caller.
	jwtIdentity bool

	// uploadRoots confines data_agent_upload_file to these directories
	// (empty = no allowlist configured).
	uploadRoots []string
	// remoteCaller is true on the HTTP transports, where the caller is not
	// the local user and uploads must be confined to uploadRoots.
	remoteCaller bool
	// reqLog controls how much of each tool call reaches the log.
	reqLog RequestLogLevel
}

// SessionDefaults are group-level fallbacks applied when a tool call omits
// the corresponding argument (identity multi-tenant mode).
type SessionDefaults struct {
	Mode          string // default session mode (auto / lite / pro / ultra)
	CustomAgentID string // default custom agent
}

// Resolver maps a request context to a tenant-scoped session manager, client,
// and session defaults (identity multi-tenant mode). Returning
// (nil, nil, _, nil) selects the default server identity; returning an error
// rejects the call.
type Resolver func(ctx context.Context) (*session.Manager, *dataagent.Client, SessionDefaults, error)

type sessionManager interface {
	CreateSession(context.Context, session.CreateOpts) (*session.State, error)
	GetStatus(string) (*session.StateSnapshot, error)
	WaitForChange(context.Context, string, int, time.Duration) (*session.StateSnapshot, bool, error)
	WaitForResult(context.Context, string, time.Duration) (*session.StateSnapshot, string, error)
	WatchSession(context.Context, session.WatchOpts) (*session.StateSnapshot, error)
	SendMessage(string, string) error
	GetResult(string) (*session.StateSnapshot, error)
	ListSessions() []*session.StateSnapshot
	ListAllSessions() []*session.StateSnapshot
	StopSession(string) error
	IncrPollSeq(string) int
	SessionDir(string) string
}

type dataAgentClient interface {
	ListDatabases() ([]dataagent.DatabaseInfo, error)
	ListFiles(string, string, string, string) ([]dataagent.FileInfo, error)
	ListTables(string) ([]dataagent.TableInfo, error)
	ListImportedTables(string) ([]dataagent.TableInfo, error)
	ImportDatabase(dataagent.ImportDatabaseOpts) error
	ListInstances(string, string, int, int) ([]dataagent.InstanceInfo, error)
	SearchDatabases(string, int, int) ([]dataagent.SearchDBInfo, error)
	ListWorkspaces(string) ([]dataagent.WorkspaceInfo, error)
	ListCustomAgents(string, string) ([]dataagent.AgentInfo, error)
	GetFileUploadSignature(string, int64) (*dataagent.UploadSignature, error)
	FileUploadCallback(string, string, int64) (string, error)
	ListRemoteSessions(workspaceID string) ([]dataagent.RemoteSessionSummary, error)
}

func New(mgr *session.Manager, client *dataagent.Client, version string) *Server {
	standalone := isRemoteTransport(os.Getenv("MCP_TRANSPORT"))
	s := &Server{
		mgr: mgr, client: client, version: version,
		hdrUser: "x-aily-user", hdrEmail: "x-aily-email", hdrToken: "x-aily-token",
		hdrJWT:       tenant.DefaultJWTHeader,
		remoteCaller: standalone,
		reqLog:       defaultRequestLogLevel(standalone),
	}

	mcpServer := server.NewMCPServer(
		"data-agent-mcp-server",
		version,
		server.WithToolCapabilities(true),
	)

	mcpServer.AddTool(listWorkspaceDatabasesTool, s.withTenant((*Server).handleListDatabases))
	mcpServer.AddTool(createSessionTool, s.withTenant((*Server).handleCreateSession))
	mcpServer.AddTool(statusTool, s.withTenant((*Server).handleStatus))
	mcpServer.AddTool(waitResultTool, s.withTenant((*Server).handleWaitResult))
	mcpServer.AddTool(watchSessionTool, s.withTenant((*Server).handleWatchSession))
	mcpServer.AddTool(sendTool, s.withTenant((*Server).handleSend))
	mcpServer.AddTool(resultTool, s.withTenant((*Server).handleResult))
	mcpServer.AddTool(listSessionsTool, s.withTenant((*Server).handleListSessions))
	mcpServer.AddTool(stopSessionTool, s.withTenant((*Server).handleStopSession))
	mcpServer.AddTool(listFilesTool, s.withTenant((*Server).handleListFiles))
	mcpServer.AddTool(listTablesTool, s.withTenant((*Server).handleListTables))
	mcpServer.AddTool(listImportedTablesTool, s.withTenant((*Server).handleListImportedTables))
	mcpServer.AddTool(importDatabaseTool, s.withTenant((*Server).handleImportDatabase))
	mcpServer.AddTool(searchInstancesTool, s.withTenant((*Server).handleSearchInstances))
	mcpServer.AddTool(searchDMSDatabasesTool, s.withTenant((*Server).handleSearchDatabases))
	mcpServer.AddTool(listWorkspacesTool, s.withTenant((*Server).handleListWorkspaces))
	mcpServer.AddTool(listAgentsTool, s.withTenant((*Server).handleListAgents))
	mcpServer.AddTool(uploadFileTool, s.withTenant((*Server).handleUploadFile))

	s.mcp = mcpServer
	return s
}

// SetResolver enables per-request tenant resolution (identity multi-tenant mode).
func (s *Server) SetResolver(r Resolver) { s.resolve = r }

// SetIdentityHeaders overrides the HTTP header names carrying the end-user
// identity (defaults: x-aily-user / x-aily-email / x-aily-token).
func (s *Server) SetIdentityHeaders(user, email, token string) {
	if user != "" {
		s.hdrUser = user
	}
	if email != "" {
		s.hdrEmail = email
	}
	if token != "" {
		s.hdrToken = token
	}
}

// EnableJWTIdentity switches identity intake to the upstream-signed token and
// names the header carrying it (empty keeps the x-aily-jwt default).
func (s *Server) EnableJWTIdentity(header string) {
	s.jwtIdentity = true
	if header != "" {
		s.hdrJWT = header
	}
}

// SetUploadRoots confines data_agent_upload_file to the given directories.
// Relative entries are resolved against the working directory and symlinked
// roots are followed, so the check compares real paths. Passing no directory
// leaves stdio unrestricted and keeps the HTTP transports fail-closed.
func (s *Server) SetUploadRoots(dirs []string) {
	roots := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		abs, err := filepath.Abs(d)
		if err != nil {
			log.Printf("upload root %q ignored: %v", d, err)
			continue
		}
		real, err := filepath.EvalSymlinks(abs)
		if err != nil {
			log.Printf("upload root %q ignored: %v", d, err)
			continue
		}
		roots = append(roots, real)
	}
	s.uploadRoots = roots
	switch {
	case len(roots) > 0:
		log.Printf("file uploads confined to %v", roots)
	case s.remoteCaller:
		log.Print("file uploads disabled: upload.allowed_dirs is required on HTTP transports " +
			"(set upload.allowed_dirs or DATA_AGENT_UPLOAD_DIRS)")
	}
}

// resolveUploadPath validates a caller-supplied upload path and returns the
// real path together with its file info.
//
// The path is resolved through symlinks before the allowlist check so a link
// inside an allowed directory cannot point outside it, and only regular files
// are accepted (directories, devices, and pipes are not uploadable).
func (s *Server) resolveUploadPath(filePath string) (string, os.FileInfo, error) {
	if len(s.uploadRoots) == 0 && s.remoteCaller {
		return "", nil, fmt.Errorf("file uploads are disabled: no upload directory is allowed on this " +
			"transport; configure upload.allowed_dirs (or DATA_AGENT_UPLOAD_DIRS) on the server")
	}

	abs, err := filepath.Abs(filePath)
	if err != nil {
		return "", nil, fmt.Errorf("invalid file path: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", nil, fmt.Errorf("file not found: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", nil, fmt.Errorf("file not found: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("not a regular file: %s", filePath)
	}

	if len(s.uploadRoots) == 0 {
		return real, info, nil // stdio without allowlist: local caller, any path
	}
	for _, root := range s.uploadRoots {
		if withinRoot(root, real) {
			return real, info, nil
		}
	}
	return "", nil, fmt.Errorf("file path is outside the allowed upload directories: %s", filePath)
}

// withinRoot reports whether path is root itself or nested under it.
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	// ".." as the first segment means path escapes root; Rel already cleaned
	// the result, so a prefix check on the separator is enough.
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// withTenant wraps a tool handler so it runs against the tenant resolved from
// the request context. The Server is shallow-copied with the tenant's manager,
// client, and session defaults swapped in, keeping the handlers themselves
// tenant-agnostic.
func (s *Server) withTenant(h func(*Server, context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (res *mcp.CallToolResult, err error) {
		rec := s.startToolCall(ctx, req)
		defer func() { rec.finish(res, err) }()

		if s.resolve == nil {
			return h(s, ctx, req)
		}
		mgr, client, defaults, err := s.resolve(ctx)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if mgr == nil || client == nil {
			return h(s, ctx, req) // default server identity
		}
		scoped := *s
		scoped.mgr = mgr
		scoped.client = client
		scoped.defaults = defaults
		return h(&scoped, ctx, req)
	}
}

// identityHTTPContext copies the identity headers into the request context
// so tool handlers can resolve the per-user tenant. The JWT is passed through
// unverified; the tenant registry owns the secret and does the verification.
func (s *Server) identityHTTPContext(ctx context.Context, r *http.Request) context.Context {
	ctx = tenant.WithJWT(ctx, strings.TrimSpace(r.Header.Get(s.hdrJWT)))
	return tenant.WithIdentity(ctx,
		r.Header.Get(s.hdrUser),
		r.Header.Get(s.hdrEmail),
		r.Header.Get(s.hdrToken),
	)
}

func (s *Server) Run(_ context.Context) error {
	transport := os.Getenv("MCP_TRANSPORT")
	switch transport {
	case "sse":
		addr, err := mcpAddr()
		if err != nil {
			return err
		}
		log.Printf("starting SSE MCP server on %s", addr)
		return server.NewSSEServer(s.mcp,
			server.WithBaseURL("http://"+addr),
			server.WithSSEContextFunc(s.identityHTTPContext),
		).Start(addr)
	case "streamable-http":
		addr, err := mcpAddr()
		if err != nil {
			return err
		}
		log.Printf("starting Streamable HTTP MCP server on %s", addr)
		return server.NewStreamableHTTPServer(s.mcp,
			server.WithHTTPContextFunc(s.identityHTTPContext),
		).Start(addr)
	default:
		if transport != "" && transport != "stdio" {
			log.Printf("unknown MCP_TRANSPORT %q, falling back to stdio", transport)
		}
		stdio := server.NewStdioServer(s.mcp)
		return stdio.Listen(context.Background(), os.Stdin, os.Stdout)
	}
}

// mcpAddr returns the listen address from MCP_PORT. HTTP transports require
// the host agent/runtime to choose and pass an available port explicitly.
func mcpAddr() (string, error) {
	port := os.Getenv("MCP_PORT")
	if port == "" {
		return "", fmt.Errorf("MCP_PORT must be set for %s transport; choose an available local port in the agent runtime", os.Getenv("MCP_TRANSPORT"))
	}
	return ":" + port, nil
}

// isRemoteTransport reports whether the transport serves callers over the
// network rather than the local process on stdin/stdout.
func isRemoteTransport(transport string) bool {
	return transport == "sse" || transport == "streamable-http"
}

var listWorkspaceDatabasesTool = mcp.NewTool(
	"data_agent_list_workspace_databases",
	mcp.WithDescription("List databases imported into the current workspace's Data Agent Data Center. MANDATORY first step before data_agent_create_session — always call this in the same turn, in any auth mode (AK/SK or API Key). Never create a session from guessed or memorized database parameters."),
)

var createSessionTool = mcp.NewTool(
	"data_agent_create_session",
	mcp.WithDescription("Create a Data Agent analysis session. Supports database analysis (database_id) or file analysis (file_id from upload_file). MANDATORY: before calling this tool for database analysis, you MUST call data_agent_list_workspace_databases in this same turn and use its returned values — never guess or reuse database_id/instance_id/engine from memory or prior conversations. For pro/ultra mode with auto_confirm=true, all plan/SQL/report confirmations are handled automatically."),
	mcp.WithString("database_id", mcp.Description("DMS database ID — MUST come from a data_agent_list_workspace_databases call in this turn (required for database analysis, or use file_id for file analysis)")),
	mcp.WithString("db_name", mcp.Description("Database schema name from data_agent_list_workspace_databases (required for database analysis)")),
	mcp.WithString("tables", mcp.Description("Comma-separated table names to analyze (for database analysis)")),
	mcp.WithString("query", mcp.Required(), mcp.Description("Natural language analysis query")),
	mcp.WithString("file_id", mcp.Description("Uploaded file ID from upload_file (alternative to database_id). File analysis defaults to pro mode.")),
	mcp.WithString("file_name", mcp.Description("Original filename (e.g. sales.csv). Required when using file_id.")),
	mcp.WithString("mode", mcp.Description("Session mode tier: auto (default for database — backend decides), lite (quick Q&A, single SQL, ~seconds), pro (deep multi-step analysis with reports, minutes; default for file), ultra (most thorough multi-dimensional insights). Legacy values ASK_DATA/ANALYSIS/INSIGHT are auto-mapped to lite/pro/ultra.")),
	mcp.WithString("plan_mode", mcp.Description("Plan mode for pro/ultra sessions: 'force' (always generate an execution plan) or 'disable' (skip planning, execute directly). Empty = server default.")),
	mcp.WithBoolean("auto_confirm", mcp.Description("Auto-confirm plans/SQL/reports (default true)")),
	mcp.WithString("instance_id", mcp.Description("DMS instance ID from data_agent_list_workspace_databases, not from DMS search results")),
	mcp.WithString("instance_name", mcp.Description("Instance resource ID from data_agent_list_workspace_databases.instance_resource_id (e.g. rm-xxx)")),
	mcp.WithString("engine", mcp.Description("Database engine from data_agent_list_workspace_databases.db_type (default mysql)")),
	mcp.WithString("workspace_id", mcp.Description("Workspace ID for team collaboration")),
	mcp.WithString("custom_agent_id", mcp.Description("Custom agent ID")),
)

var statusTool = mcp.NewTool(
	"data_agent_status",
	mcp.WithDescription("Get current status of a Data Agent session including step progress, waiting state, and confirmations. Pass wait_timeout to block server-side until status changes — eliminates LLM roundtrip cost during polling."),
	mcp.WithString("session_id", mcp.Required(), mcp.Description("Session ID to check")),
	mcp.WithNumber("wait_timeout", mcp.Description("Seconds to block waiting for a status change (0=immediate snapshot). Recommended: 30. Returns changed=true when status advances, changed=false on timeout.")),
	mcp.WithString("poll_hint", mcp.Description("Caller-supplied differentiation hint to avoid identical consecutive calls. Pass the current step or an incrementing counter (e.g. check-1, check-2). The server ignores this value.")),
)

var waitResultTool = mcp.NewTool(
	"data_agent_wait_result",
	mcp.WithDescription("Block until the session needs LLM attention: completed, error, canceled, or waiting for manual input. For auto_confirm=true sessions this returns only on completion/error, eliminating all intermediate status polling. Returns reason: 'completed'|'error'|'canceled'|'waiting_input'|'timeout'."),
	mcp.WithString("session_id", mcp.Required(), mcp.Description("Session ID to wait for")),
	mcp.WithNumber("timeout", mcp.Description("Max seconds to wait (default 300). Returns reason=timeout if exceeded.")),
)

var watchSessionTool = mcp.NewTool(
	"data_agent_watch_session",
	mcp.WithDescription("Attach the MCP server to an existing remote Data Agent session and start background SSE monitoring. Returns immediately after the server-side watcher is registered."),
	mcp.WithString("session_id", mcp.Required(), mcp.Description("Existing Data Agent session ID to watch")),
	mcp.WithString("agent_id", mcp.Description("Agent ID for the session. Optional; resolved via DescribeDataAgentSession when omitted.")),
	mcp.WithString("workspace_id", mcp.Description("Workspace ID for the session. Defaults to configured workspace.")),
	mcp.WithString("mode", mcp.Description("Session mode tier (auto/lite/pro/ultra). Optional metadata for follow-up sends; legacy ASK_DATA/ANALYSIS values are auto-mapped.")),
	mcp.WithBoolean("auto_confirm", mcp.Description("Auto-confirm plans/SQL/reports while watching (default true)")),
)

var sendTool = mcp.NewTool(
	"data_agent_send",
	mcp.WithDescription("Send a message to an active Data Agent session. Use for confirming plans, answering questions, or follow-up queries."),
	mcp.WithString("session_id", mcp.Required(), mcp.Description("Target session ID")),
	mcp.WithString("message", mcp.Required(), mcp.Description("Message to send")),
)

var resultTool = mcp.NewTool(
	"data_agent_result",
	mcp.WithDescription("Get the analysis result of a Data Agent session. Returns structured JSON metadata (conclusions, artifacts, confirmations) as text, plus any chart images as ImageContent (base64). This is the primary tool for retrieving completed analysis output including visual charts."),
	mcp.WithString("session_id", mcp.Required(), mcp.Description("Session ID to get results for")),
)

var listSessionsTool = mcp.NewTool(
	"data_agent_list_sessions",
	mcp.WithDescription("List Data Agent sessions. By default returns active sessions; set include_history=true to also include completed/errored sessions from disk. Set include_remote=true to also fetch sessions from the server API (required for DataBuddy watcher to discover sessions created by Console/Web UI)."),
	mcp.WithBoolean("include_history", mcp.Description("Include historical sessions persisted on disk (default false)")),
	mcp.WithBoolean("include_remote", mcp.Description("Also fetch sessions from the server API, not just locally tracked ones (default false)")),
	mcp.WithString("workspace_id", mcp.Description("Workspace ID for remote session listing (default: configured workspace)")),
)

var stopSessionTool = mcp.NewTool(
	"data_agent_stop_session",
	mcp.WithDescription("Stop monitoring a Data Agent session and clean up resources."),
	mcp.WithString("session_id", mcp.Required(), mcp.Description("Session ID to stop")),
)

var listFilesTool = mcp.NewTool(
	"data_agent_list_files",
	mcp.WithDescription("List files and reports generated by a Data Agent session. Returns file IDs, names, types, and download URLs."),
	mcp.WithString("session_id", mcp.Required(), mcp.Description("Session ID to list files for")),
	mcp.WithString("category", mcp.Description("File category filter. Usually omit this to get all artifacts (.md/.html reports, .xlsx exports); 'WebReport' matches only interactive web-rendered reports and is often empty")),
)

var listTablesTool = mcp.NewTool(
	"data_agent_list_tables",
	mcp.WithDescription("List all tables in a DMS database (for discovering tables before import). Queries DMS directly, not workspace-scoped."),
	mcp.WithString("database_id", mcp.Required(), mcp.Description("DMS database ID")),
)

var listImportedTablesTool = mcp.NewTool(
	"data_agent_list_imported_tables",
	mcp.WithDescription("List tables already imported into the current workspace's Data Center. Only shows tables tagged for the active workspace."),
	mcp.WithString("database_id", mcp.Required(), mcp.Description("DMS database ID")),
)

var importDatabaseTool = mcp.NewTool(
	"data_agent_import_database",
	mcp.WithDescription("Import database tables into a Data Agent workspace via DMS TagMetaAsset. Makes tables visible in data_agent_list_workspace_databases for that workspace. Use data_agent_search_dms_databases and data_agent_list_tables to find database_id and table names, then call data_agent_list_workspace_databases before create_session."),
	mcp.WithString("database_id", mcp.Required(), mcp.Description("DMS database ID")),
	mcp.WithString("tables", mcp.Required(), mcp.Description("Comma-separated table names to import (each table tagged as META_TABLE)")),
	mcp.WithString("workspace_id", mcp.Description("Target workspace ID (default: configured workspace)")),
)

var searchInstancesTool = mcp.NewTool(
	"data_agent_search_instances",
	mcp.WithDescription("Search DMS-managed database instances. Use to discover databases not yet in Data Center."),
	mcp.WithString("search_key", mcp.Description("Search keyword (instance name, host, etc.)")),
	mcp.WithString("db_type", mcp.Description("Database type filter (mysql, postgresql, etc.)")),
)

var searchDMSDatabasesTool = mcp.NewTool(
	"data_agent_search_dms_databases",
	mcp.WithDescription("Search DMS-managed databases by schema name for discovery/import only. Do not use returned instance_id for create_session; call data_agent_list_workspace_databases after import and use that row."),
	mcp.WithString("search_key", mcp.Required(), mcp.Description("Database schema name to search")),
)

var listWorkspacesTool = mcp.NewTool(
	"data_agent_list_workspaces",
	mcp.WithDescription("List Data Agent workspaces (collaborative spaces) for the current account. Use this to resolve a workspace display name to its workspace_id; never pass a workspace name as workspace_id."),
	mcp.WithString("type", mcp.Description("Workspace type: MY (default, personal) or ALL (including shared)")),
)

var listAgentsTool = mcp.NewTool(
	"data_agent_list_agents",
	mcp.WithDescription("List custom Data Agent agents with specialized instructions and knowledge bases. Agents belong to a workspace; if the user gives a workspace name, first call data_agent_list_workspaces(type=\"ALL\") and pass the matched workspace_id."),
	mcp.WithString("status", mcp.Description("Agent status filter (default: RELEASED)")),
	mcp.WithString("workspace_id", mcp.Description("Workspace ID returned by data_agent_list_workspaces, not the workspace display name (default: personal workspace)")),
)

var uploadFileTool = mcp.NewTool(
	"data_agent_upload_file",
	mcp.WithDescription("Upload a local file (CSV, XLSX, XLS, JSON, TXT) for Data Agent analysis. Returns file_id for use with create_session."),
	mcp.WithString("file_path", mcp.Required(), mcp.Description("Absolute path to the local file to upload. Must be a regular file inside the server's allowed upload directories when the server configures them (always required on HTTP transports)")),
)
