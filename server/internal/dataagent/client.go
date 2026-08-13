package dataagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	userAgentPrefix            = "AlibabaCloud-Agent-Skills/alibabacloud-data-agent-skill"
	dataAgentListPageSize      = 50
	dataAgentListMaxPageScan   = 100
	dmsTagMetaPageSize         = 200
	defaultSessionLookbackDays = 7
)

var userAgent = buildUserAgent(os.Getenv("SKILL_SESSION_ID"))

func buildUserAgent(sessionID string) string {
	if !isValidSkillSessionID(sessionID) {
		sessionID = newSkillSessionID()
	}
	return userAgentPrefix + "/" + strings.ToLower(sessionID)
}

func isValidSkillSessionID(sessionID string) bool {
	if len(sessionID) != 32 {
		return false
	}
	_, err := hex.DecodeString(sessionID)
	return err == nil
}

func newSkillSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// Client is the main API client for Alibaba Cloud Data Agent.
// It uses direct HTTP calls with V3 signing (no Go SDK dependency).
type Client struct {
	cred     *Credential
	credFn   func() *Credential // optional dynamic credential provider (e.g. STS auto-refresh)
	region   string
	endpoint string // dms.{region}.aliyuncs.com  (Data Agent 2025-04-14)
	// dmsEnterpriseEndpoint overrides the dms-enterprise host (2018-11-01
	// metadata APIs). Empty means dms-enterprise.{region}.aliyuncs.com.
	dmsEnterpriseEndpoint string
	// apiKeyEndpoint overrides the API Key control-plane host. Empty means
	// dataagent-{region}.aliyuncs.com.
	apiKeyEndpoint string
	// apiKeyStreamEndpoint overrides the API Key data-plane (streaming) host.
	// Empty means dataagent-stream-{region}.aliyuncs.com.
	apiKeyStreamEndpoint string
	http                 *http.Client
	sse                  *SSEClient

	dmsUnitMu   sync.Mutex
	dmsUnit     string // cached DMSUnit from GetActiveRouteUnit
	wsMu        sync.Mutex
	workspaceID string // cached workspace ID
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithDMSUnit pre-sets the DMSUnit, skipping GetActiveRouteUnit auto-resolution.
func WithDMSUnit(unit string) ClientOption {
	return func(c *Client) { c.dmsUnit = unit }
}

// WithDataAgentEndpoint overrides the host of the AK/SK-signed Data Agent API
// (2025-04-14): session create/send/status plus the AK/SK SSE stream. Use it
// for VPC endpoints or non-public-cloud deployments. An empty value keeps the
// region-derived default dms.{region}.aliyuncs.com.
func WithDataAgentEndpoint(endpoint string) ClientOption {
	return func(c *Client) {
		if endpoint != "" {
			c.endpoint = endpoint
		}
	}
}

// WithAPIKeyEndpoint overrides the API Key control-plane host
// (default dataagent-{region}.aliyuncs.com). Only used in API Key auth mode.
func WithAPIKeyEndpoint(endpoint string) ClientOption {
	return func(c *Client) { c.apiKeyEndpoint = endpoint }
}

// WithAPIKeyStreamEndpoint overrides the API Key data-plane host used for
// streaming actions (default dataagent-stream-{region}.aliyuncs.com). Only
// used in API Key auth mode.
func WithAPIKeyStreamEndpoint(endpoint string) ClientOption {
	return func(c *Client) { c.apiKeyStreamEndpoint = endpoint }
}

// WithDMSEnterpriseEndpoint overrides the dms-enterprise endpoint used by the
// 2018-11-01 metadata APIs (database/table/instance discovery, import tagging,
// GetActiveRouteUnit). Use it for VPC endpoints
// (dms-enterprise-vpc.{region}.aliyuncs.com) or non-public-cloud deployments.
// An empty value keeps the region-derived default.
func WithDMSEnterpriseEndpoint(endpoint string) ClientOption {
	return func(c *Client) { c.dmsEnterpriseEndpoint = endpoint }
}

// WithWorkspaceID pre-sets the workspace ID, skipping InitDataAgentPersonalWorkspace auto-resolution.
func WithWorkspaceID(id string) ClientOption {
	return func(c *Client) { c.workspaceID = id }
}

// WithCredentialProvider installs a dynamic credential source. When set, every
// request pulls the current credential from fn instead of the static one, so
// rotating STS tokens (e.g. from AssumeRole auto-refresh) are picked up
// transparently. fn must return an immutable snapshot and be safe for
// concurrent use.
func WithCredentialProvider(fn func() *Credential) ClientOption {
	return func(c *Client) { c.credFn = fn }
}

// NewClient creates a new Data Agent API client.
func NewClient(cred *Credential, region string, opts ...ClientOption) *Client {
	c := &Client{
		cred:     cred,
		region:   region,
		endpoint: fmt.Sprintf("dms.%s.aliyuncs.com", region),
		http: &http.Client{
			Timeout: 60 * time.Second,
		},
		sse: NewSSEClient(cred, region),
	}
	for _, opt := range opts {
		opt(c)
	}
	c.sse.credFn = c.credFn
	c.sse.dmsUnitFn = c.ResolveDMSUnit
	// Endpoint overrides are applied by the options above, so propagate the
	// resolved hosts into the SSE client that shares this configuration.
	c.sse.endpoint = c.endpoint
	c.sse.streamEndpoint = c.APIKeyStreamEndpoint()
	return c
}

// credential returns the current credential, preferring the dynamic provider.
func (c *Client) credential() *Credential {
	if c.credFn != nil {
		if cred := c.credFn(); cred != nil {
			return cred
		}
	}
	return c.cred
}

// Region returns the configured region.
func (c *Client) Region() string { return c.region }

// DMSEnterpriseEndpoint returns the effective dms-enterprise host for the
// 2018-11-01 metadata APIs: the configured override, else the region default.
func (c *Client) DMSEnterpriseEndpoint() string {
	if c.dmsEnterpriseEndpoint != "" {
		return c.dmsEnterpriseEndpoint
	}
	return fmt.Sprintf("dms-enterprise.%s.aliyuncs.com", c.region)
}

// DataAgentEndpoint returns the host of the AK/SK-signed Data Agent API.
func (c *Client) DataAgentEndpoint() string { return c.endpoint }

// APIKeyEndpoint returns the effective API Key control-plane host.
func (c *Client) APIKeyEndpoint() string {
	if c.apiKeyEndpoint != "" {
		return c.apiKeyEndpoint
	}
	return fmt.Sprintf("dataagent-%s.aliyuncs.com", c.region)
}

// APIKeyStreamEndpoint returns the effective API Key data-plane (streaming) host.
func (c *Client) APIKeyStreamEndpoint() string {
	if c.apiKeyStreamEndpoint != "" {
		return c.apiKeyStreamEndpoint
	}
	return fmt.Sprintf("dataagent-stream-%s.aliyuncs.com", c.region)
}

// ---------- public API methods ----------

// CreateSession creates a new Data Agent session.
func (c *Client) CreateSession(opts CreateSessionOpts) (*SessionInfo, error) {
	params := map[string]string{
		"Title": "data-agent-session",
	}
	c.setDmsUnit(params)
	if opts.DatabaseID != "" {
		params["DatabaseId"] = opts.DatabaseID
	}
	if opts.FileID != "" {
		params["File"] = opts.FileID
	}
	if opts.WorkspaceID != "" {
		params["WorkspaceId"] = opts.WorkspaceID
	}

	// Build SessionConfig JSON.
	sessionCfg := map[string]interface{}{
		"Language":     "CHINESE",
		"EnableSearch": opts.EnableSearch,
	}
	if opts.Mode != "" {
		sessionCfg["Mode"] = opts.Mode
	}
	if opts.PlanMode != "" {
		sessionCfg["PlanMode"] = opts.PlanMode
	}
	if opts.CustomAgentID != "" {
		sessionCfg["CustomAgentId"] = opts.CustomAgentID
		sessionCfg["CustomAgentStage"] = "prod"
	}
	cfgBytes, _ := json.Marshal(sessionCfg)
	params["SessionConfig"] = string(cfgBytes)

	body, err := c.callAPI(c.endpoint, "CreateDataAgentSession", "2025-04-14", params)
	if err != nil {
		return nil, fmt.Errorf("CreateSession: %w", err)
	}

	data := jsonObj(body, "Data")
	if data == nil {
		data = jsonObj(body, "data")
	}
	if data == nil {
		data = body
	}

	return &SessionInfo{
		SessionID:     firstStr(data, "SessionId", "sessionId"),
		AgentID:       firstStr(data, "AgentId", "agentId"),
		AgentStatus:   firstStr(data, "AgentStatus", "agentStatus"),
		SessionStatus: firstStr(data, "SessionStatus", "sessionStatus"),
		RequestID:     firstStr(body, "RequestId", "requestId"),
		WorkspaceID:   opts.WorkspaceID,
	}, nil
}

// DescribeSession returns the current status of a session.
// An optional workspaceID can be passed to scope the query to a specific
// workspace (e.g. when the session was created in a non-default workspace).
// If empty, the globally configured workspace is used.
func (c *Client) DescribeSession(sessionID string, workspaceID ...string) (*SessionInfo, error) {
	params := map[string]string{
		"SessionId": sessionID,
	}
	c.setDmsUnit(params)
	ws := ""
	if len(workspaceID) > 0 && workspaceID[0] != "" {
		ws = workspaceID[0]
	} else {
		ws = c.ResolveWorkspaceID()
	}
	if ws != "" {
		params["WorkspaceId"] = ws
	}

	body, err := c.callAPI(c.endpoint, "DescribeDataAgentSession", "2025-04-14", params)
	if err != nil {
		return nil, fmt.Errorf("DescribeSession: %w", err)
	}

	data := jsonObj(body, "Data")
	if data == nil {
		data = jsonObj(body, "data")
	}
	if data == nil {
		data = body
	}

	return &SessionInfo{
		SessionID:     sessionID,
		AgentID:       firstStr(data, "AgentId", "agentId"),
		AgentStatus:   firstStr(data, "AgentStatus", "agentStatus"),
		SessionStatus: firstStr(data, "SessionStatus", "sessionStatus"),
		RequestID:     firstStr(body, "RequestId", "requestId"),
	}, nil
}

// SendMessage sends a chat message to an active Data Agent session.
func (c *Client) SendMessage(opts SendMessageOpts) error {
	params := map[string]string{
		"AgentId":     opts.AgentID,
		"SessionId":   opts.SessionID,
		"Message":     opts.Message,
		"MessageType": "primary",
	}
	c.setDmsUnit(params)
	if opts.WorkspaceID != "" {
		params["WorkspaceId"] = opts.WorkspaceID
	}

	// SessionConfig
	sessionCfg := map[string]interface{}{
		"Language": "CHINESE",
	}
	if opts.Mode != "" {
		sessionCfg["Mode"] = opts.Mode
	}
	if opts.PlanMode != "" {
		sessionCfg["PlanMode"] = opts.PlanMode
	}
	cfgBytes, _ := json.Marshal(sessionCfg)
	params["SessionConfig"] = string(cfgBytes)

	// DataSource
	if opts.DataSource != nil {
		dsBytes, _ := json.Marshal(opts.DataSource)
		params["DataSource"] = string(dsBytes)
	}

	_, err := c.callAPI(c.endpoint, "SendChatMessage", "2025-04-14", params)
	if err != nil {
		return fmt.Errorf("SendMessage: %w", err)
	}
	return nil
}

// SetWorkspaceID overrides the workspace this client operates in. Used by
// the MCP layer when a per-connection X-Data-Agent-Workspace-Id header
// scopes the caller to a specific workspace.
func (c *Client) SetWorkspaceID(id string) {
	c.wsMu.Lock()
	c.workspaceID = id
	c.wsMu.Unlock()
}

// ResolveWorkspaceID calls InitDataAgentPersonalWorkspace to get the user's
// personal workspace ID. Result is cached.
func (c *Client) ResolveWorkspaceID() string {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.workspaceID != "" {
		return c.workspaceID
	}
	params := map[string]string{}
	c.setDmsUnit(params)
	if c.credential().IsAPIKey() {
		// InitDataAgentPersonalWorkspace is not available in API Key mode; fall
		// back to the workspaces the user belongs to (WorkspaceType=MY) and use
		// the first one.
		wsList, err := c.ListWorkspaces("MY")
		if err == nil && len(wsList) > 0 {
			c.workspaceID = wsList[0].WorkspaceID
		}
		return c.workspaceID
	}
	body, err := c.callAPI(c.endpoint, "InitDataAgentPersonalWorkspace", "2025-04-14", params)
	if err != nil {
		log.Printf("ResolveWorkspaceID failed: %v", err)
		return ""
	}
	data := jsonObj(body, "data")
	if data == nil {
		data = jsonObj(body, "Data")
	}
	if data == nil {
		data = body
	}
	ws := firstStr(data, "WorkspaceId", "workspaceId")
	c.workspaceID = ws
	return ws
}

// listTagMetaAssetPages scans all ListTagMetaAsset pages on the dms-enterprise
// endpoint and returns the raw items. baseParams must not contain paging keys.
// Stops on an empty/short page, when TotalCount is reached, on a repeated
// page (API ignoring PageNumber), or at the page-scan safety cap.
func (c *Client) listTagMetaAssetPages(dmsEndpoint string, baseParams map[string]string) ([]map[string]interface{}, error) {
	var items []map[string]interface{}
	prevFirst := ""
	for page := 1; page <= dataAgentListMaxPageScan; page++ {
		params := map[string]string{
			"PageNumber": strconv.Itoa(page),
			"PageSize":   strconv.Itoa(dmsTagMetaPageSize),
		}
		for k, v := range baseParams {
			params[k] = v
		}

		body, err := c.callDMSEnterprise(dmsEndpoint, "ListTagMetaAsset", "2018-11-01", params)
		if err != nil {
			return nil, err
		}

		// Response format: { "Data": [...], "TotalCount": N, "Success": true }
		rawList, ok := body["Data"]
		if !ok {
			rawList, ok = body["data"]
		}
		if !ok {
			break
		}
		listSlice, ok := rawList.([]interface{})
		if !ok || len(listSlice) == 0 {
			break
		}

		// Guard against an API that ignores PageNumber and repeats page 1.
		if first, err := json.Marshal(listSlice[0]); err == nil {
			if s := string(first); s == prevFirst {
				break
			} else {
				prevFirst = s
			}
		}

		for _, item := range listSlice {
			if m, ok := item.(map[string]interface{}); ok {
				items = append(items, m)
			}
		}

		total := firstInt64(body, "TotalCount", "totalCount", "Total", "total")
		if total > 0 && int64(len(items)) >= total {
			break
		}
		if len(listSlice) < dmsTagMetaPageSize {
			break
		}
	}
	return items, nil
}

// tagMetaAttrs returns the attribute object of a ListTagMetaAsset item; fields
// are nested inside MetaEntityAttrs, with the item itself as fallback.
func tagMetaAttrs(m map[string]interface{}) map[string]interface{} {
	attrs := jsonObj(m, "MetaEntityAttrs")
	if attrs == nil {
		attrs = jsonObj(m, "metaEntityAttrs")
	}
	if attrs == nil {
		attrs = m
	}
	return attrs
}

// ListDatabases calls ListTagMetaAsset on the dms-enterprise endpoint to
// list databases imported into a workspace's Data Center.
// An empty workspaceID falls back to the configured/default workspace.
func (c *Client) ListDatabases(workspaceID string) ([]DatabaseInfo, error) {
	dmsEndpoint := c.DMSEnterpriseEndpoint()

	tid := c.resolveTid(dmsEndpoint)
	ws := workspaceID
	if ws == "" {
		ws = c.ResolveWorkspaceID()
	}

	tagName := fmt.Sprintf("sys::DMS-DA::%s::space:%s", c.region, ws)

	params := map[string]string{
		"TagName":  tagName,
		"MetaType": "META_DATABASE",
	}
	if tid != "" {
		params["Tid"] = tid
	}

	items, err := c.listTagMetaAssetPages(dmsEndpoint, params)
	if err != nil {
		return nil, fmt.Errorf("ListDatabases: %w", err)
	}

	var result []DatabaseInfo
	for _, m := range items {
		attrs := tagMetaAttrs(m)
		result = append(result, DatabaseInfo{
			DbID:               firstInt64(attrs, "DbId", "dbId"),
			InstanceID:         firstInt64(attrs, "InstanceId", "instanceId"),
			SchemaName:         firstStr(attrs, "SchemaName", "schemaName"),
			DbType:             firstStr(attrs, "DbType", "dbType"),
			InstanceResourceID: firstStr(attrs, "InstanceResourceId", "instanceResourceId"),
			CatalogName:        firstStr(attrs, "CatalogName", "catalogName"),
		})
	}

	return result, nil
}

// ListFiles returns uploaded files for a session.
func (c *Client) ListFiles(sessionID, agentID, workspaceID, category string) ([]FileInfo, error) {
	params := map[string]string{
		"SessionId": sessionID,
	}
	c.setDmsUnit(params)
	if agentID != "" {
		params["AgentId"] = agentID
	}
	if workspaceID != "" {
		params["WorkspaceId"] = workspaceID
	}
	if category != "" {
		params["FileCategory"] = category
	}

	body, err := c.callAPI(c.endpoint, "ListFileUpload", "2025-04-14", params)
	if err != nil {
		return nil, fmt.Errorf("ListFiles: %w", err)
	}

	rawList, ok := body["Data"]
	if !ok {
		rawList, ok = body["data"]
	}
	if !ok {
		return nil, nil
	}
	listSlice, ok := rawList.([]interface{})
	if !ok {
		return nil, nil
	}

	var result []FileInfo
	for _, item := range listSlice {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		dl := firstStr(m, "DownloadLink", "DownloadUrl", "downloadLink", "downloadUrl")
		result = append(result, FileInfo{
			FileID:      firstStr(m, "FileId", "fileId"),
			FileName:    firstStr(m, "FileName", "fileName"),
			FileType:    firstStr(m, "FileType", "fileType"),
			FileSize:    firstInt64(m, "FileSize", "fileSize"),
			DownloadURL: dl,
		})
	}
	return result, nil
}

// ListTables lists tables for a database in the user's Data Agent workspace.
// ListTables queries DMS Enterprise for all tables in a database.
// This returns tables from DMS directly (not workspace-scoped), suitable for
// discovering tables before importing them via import_database.
func (c *Client) ListTables(databaseID string) ([]TableInfo, error) {
	dmsEndpoint := c.DMSEnterpriseEndpoint()
	tid := c.resolveTid(dmsEndpoint)

	params := map[string]string{
		"DatabaseId": databaseID,
		"PageNumber": "1",
		"PageSize":   "200",
	}
	if tid != "" {
		params["Tid"] = tid
	}

	body, err := c.callDMSEnterprise(dmsEndpoint, "ListTables", "2018-11-01", params)
	if err != nil {
		return nil, fmt.Errorf("ListTables: %w", err)
	}

	// Response: { "TableList": { "Table": [{TableName, TableId, ...}] } }
	tableList := jsonObj(body, "TableList")
	if tableList == nil {
		return nil, nil
	}
	rawList, ok := tableList["Table"]
	if !ok {
		return nil, nil
	}
	listSlice, ok := rawList.([]interface{})
	if !ok {
		return nil, nil
	}

	var result []TableInfo
	for _, item := range listSlice {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		result = append(result, TableInfo{
			TableName: firstStr(m, "TableName", "tableName"),
			TableID:   fmt.Sprintf("%v", firstInt64(m, "TableId", "tableId")),
			Engine:    firstStr(m, "TableType", "tableType", "Engine", "engine"),
		})
	}
	return result, nil
}

// ListImportedTables queries tables already imported into a workspace via ListTagMetaAsset.
// An empty workspaceID falls back to the configured/default workspace; an empty
// databaseID lists imported tables across all databases in the workspace.
func (c *Client) ListImportedTables(databaseID, workspaceID string) ([]TableInfo, error) {
	dmsEndpoint := c.DMSEnterpriseEndpoint()
	tid := c.resolveTid(dmsEndpoint)
	ws := workspaceID
	if ws == "" {
		ws = c.ResolveWorkspaceID()
	}
	tagName := fmt.Sprintf("sys::DMS-DA::%s::space:%s", c.region, ws)

	params := map[string]string{
		"TagName":  tagName,
		"MetaType": "META_TABLE",
	}
	if databaseID != "" {
		params["MetaParentId"] = databaseID
	}
	if tid != "" {
		params["Tid"] = tid
	}

	items, err := c.listTagMetaAssetPages(dmsEndpoint, params)
	if err != nil {
		return nil, fmt.Errorf("ListImportedTables: %w", err)
	}

	var result []TableInfo
	for _, m := range items {
		attrs := tagMetaAttrs(m)
		result = append(result, TableInfo{
			TableName: firstStr(attrs, "TableName", "tableName"),
			TableID:   firstStr(attrs, "TableId", "tableId"),
			Engine:    firstStr(attrs, "Engine", "engine"),
			DbID:      firstStr(attrs, "DbId", "dbId"),
			DbName:    firstStr(attrs, "SchemaName", "schemaName"),
		})
	}
	return result, nil
}

// ImportDatabaseOpts contains the fields for importing tables into a workspace.
type ImportDatabaseOpts struct {
	DmsDbID     string   // DMS database ID (e.g. "73467990")
	Tables      []string // Table names to import (e.g. ["employees","departments"])
	WorkspaceID string   // Target workspace (default: auto-resolved)
}

// ImportDatabase tags DMS database tables into a Data Agent workspace using
// dms-enterprise TagMetaAsset. This makes the tables visible in the workspace's
// data center. Each table is tagged as a META_TABLE item with MetaId = "{dbId},{tableName}".
func (c *Client) ImportDatabase(opts ImportDatabaseOpts) error {
	wsID := opts.WorkspaceID
	if wsID == "" {
		wsID = c.ResolveWorkspaceID()
	}

	dmsEndpoint := c.DMSEnterpriseEndpoint()
	tid := c.resolveTid(dmsEndpoint)
	tagName := fmt.Sprintf("sys::DMS-DA::%s::space:%s", c.region, wsID)

	// Build metaItems: each table is "{dbId},{tableName}"
	type metaItem struct {
		MetaId string `json:"MetaId"`
	}
	var items []metaItem
	for _, t := range opts.Tables {
		items = append(items, metaItem{MetaId: opts.DmsDbID + "," + t})
	}
	metaItemsJSON, _ := json.Marshal(items)

	params := map[string]string{
		"RegionId":             c.region,
		"TagName":              tagName,
		"MetaType":             "META_TABLE",
		"MetaItems":            string(metaItemsJSON),
		"CreateTagIfNotExists": "true",
		"TagSpread":            "true",
	}
	if tid != "" {
		params["Tid"] = tid
	}

	_, err := c.callDMSEnterprise(dmsEndpoint, "TagMetaAsset", "2018-11-01", params)
	if err != nil {
		return fmt.Errorf("ImportDatabase: %w", err)
	}
	return nil
}

// ListInstances lists DMS instances with optional filters.
func (c *Client) ListInstances(searchKey, dbType string, page, size int) ([]InstanceInfo, error) {
	dmsEndpoint := c.DMSEnterpriseEndpoint()
	tid := c.resolveTid(dmsEndpoint)

	params := map[string]string{
		"PageNumber": strconv.Itoa(page),
		"PageSize":   strconv.Itoa(size),
	}
	if searchKey != "" {
		params["SearchKey"] = searchKey
	}
	if dbType != "" {
		params["DbType"] = dbType
	}
	if tid != "" {
		params["Tid"] = tid
	}

	body, err := c.callDMSEnterprise(dmsEndpoint, "ListInstances", "2018-11-01", params)
	if err != nil {
		return nil, fmt.Errorf("ListInstances: %w", err)
	}

	instList := jsonObj(body, "InstanceList")
	if instList == nil {
		return nil, nil
	}
	rawList, ok := instList["Instance"]
	if !ok {
		return nil, nil
	}
	listSlice, ok := rawList.([]interface{})
	if !ok {
		return nil, nil
	}

	var result []InstanceInfo
	for _, item := range listSlice {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// Filter out deleted instances
		state := firstStr(m, "State", "state")
		if state == "DELETED" {
			continue
		}
		// DMS ListInstances API may not return DbType in the response.
		// Infer from Host when the field is empty.
		dbType := firstStr(m, "DbType", "dbType")
		if dbType == "" {
			dbType = inferDbTypeFromHost(firstStr(m, "Host", "host"))
		}
		result = append(result, InstanceInfo{
			InstanceID:         firstStr(m, "InstanceId", "instanceId"),
			InstanceAlias:      firstStr(m, "InstanceAlias", "instanceAlias"),
			Host:               firstStr(m, "Host", "host"),
			Port:               int(firstInt64(m, "Port", "port")),
			DbType:             dbType,
			EnvType:            firstStr(m, "EnvType", "envType"),
			InstanceResourceID: firstStr(m, "EcsInstanceId", "ecsInstanceId"),
		})
	}
	return result, nil
}

// SearchDatabases searches DMS databases by keyword.
func (c *Client) SearchDatabases(searchKey string, page, size int) ([]SearchDBInfo, error) {
	dmsEndpoint := c.DMSEnterpriseEndpoint()
	tid := c.resolveTid(dmsEndpoint)

	params := map[string]string{
		"SearchKey":  searchKey,
		"PageNumber": strconv.Itoa(page),
		"PageSize":   strconv.Itoa(size),
	}
	if tid != "" {
		params["Tid"] = tid
	}

	body, err := c.callDMSEnterprise(dmsEndpoint, "SearchDatabase", "2018-11-01", params)
	if err != nil {
		return nil, fmt.Errorf("SearchDatabases: %w", err)
	}

	searchList := jsonObj(body, "SearchDatabaseList")
	if searchList == nil {
		return nil, nil
	}
	rawList, ok := searchList["SearchDatabase"]
	if !ok {
		return nil, nil
	}
	listSlice, ok := rawList.([]interface{})
	if !ok {
		return nil, nil
	}

	var result []SearchDBInfo
	for _, item := range listSlice {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// SearchDatabase API does not reliably return InstanceId.
		// Try InstanceId first; DalGroupId is a DMS routing ID (not an instance
		// ID) and must NOT be used for create_session — use
		// list_workspace_databases() for authoritative instance metadata.
		instID := firstInt64(m, "InstanceId", "instanceId")
		result = append(result, SearchDBInfo{
			DatabaseID: firstStr(m, "DatabaseId", "databaseId"),
			SchemaName: firstStr(m, "SchemaName", "schemaName"),
			Host:       firstStr(m, "Host", "host"),
			Port:       int(firstInt64(m, "Port", "port")),
			InstanceID: instID,
			DbType:     firstStr(m, "DbType", "dbType"),
			EnvType:    firstStr(m, "EnvType", "envType"),
		})
	}
	return result, nil
}

// ListWorkspaces lists Data Agent workspaces.
func (c *Client) ListWorkspaces(wsType string) ([]WorkspaceInfo, error) {
	if wsType == "" {
		wsType = "MY"
	}

	var result []WorkspaceInfo
	seen := make(map[string]struct{})
	for page := 1; page <= dataAgentListMaxPageScan; page++ {
		params := map[string]string{
			"WorkspaceType": wsType,
			"PageNumber":    strconv.Itoa(page),
			"PageSize":      strconv.Itoa(dataAgentListPageSize),
		}
		c.setDmsUnit(params)

		body, err := c.callAPI(c.endpoint, "ListDataAgentWorkspace", "2025-04-14", params)
		if err != nil {
			return nil, fmt.Errorf("ListWorkspaces: %w", err)
		}

		data, listSlice := dataAgentPageContent(body)
		if data == nil || len(listSlice) == 0 {
			break
		}

		added := 0
		for _, item := range listSlice {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			ws := WorkspaceInfo{
				WorkspaceID: firstStr(m, "WorkspaceId", "WorkspaceID", "workspaceId", "workspace_id"),
				Name:        firstStr(m, "WorkspaceName", "workspaceName", "Name", "name"),
				Type:        firstStr(m, "Type", "type", "WorkspaceType", "workspaceType"),
			}
			dedupKey := ws.WorkspaceID
			if dedupKey == "" {
				dedupKey = ws.Name + "|" + ws.Type
			}
			if dedupKey != "" {
				if _, ok := seen[dedupKey]; ok {
					continue
				}
				seen[dedupKey] = struct{}{}
			}
			result = append(result, ws)
			added++
		}

		total := firstInt64(data, "Total", "total", "TotalCount", "totalCount", "TotalElements", "totalElements", "total_elements")
		if total > 0 && int64(len(result)) >= total {
			break
		}
		if len(listSlice) < dataAgentListPageSize {
			break
		}
		// If the API ignores PageNumber, page 2 would repeat page 1 forever.
		if added == 0 && page > 1 {
			break
		}
	}
	return result, nil
}

// ListCustomAgents lists custom agents in the user's workspace.
func (c *Client) ListCustomAgents(status, workspaceID string) ([]AgentInfo, error) {
	if status == "" {
		status = "RELEASED"
	}
	if workspaceID == "" {
		workspaceID = c.ResolveWorkspaceID()
	}

	var result []AgentInfo
	seen := make(map[string]struct{})
	for page := 1; page <= dataAgentListMaxPageScan; page++ {
		params := map[string]string{
			"Status":      status,
			"PageNumber":  strconv.Itoa(page),
			"PageSize":    strconv.Itoa(dataAgentListPageSize),
			"WorkspaceId": workspaceID,
		}
		c.setDmsUnit(params)

		body, err := c.callAPI(c.endpoint, "ListCustomAgent", "2025-04-14", params)
		if err != nil {
			return nil, fmt.Errorf("ListCustomAgents: %w", err)
		}

		data, listSlice := dataAgentPageContent(body)
		if data == nil || len(listSlice) == 0 {
			break
		}

		added := 0
		for _, item := range listSlice {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			agent := AgentInfo{
				AgentID:     firstStr(m, "CustomAgentId", "CustomAgentID", "customAgentId", "custom_agent_id", "AgentId", "agentId"),
				Name:        firstStr(m, "Name", "name"),
				Description: firstStr(m, "Description", "description"),
				Status:      firstStr(m, "Status", "status"),
			}
			// 提取 ExtraInfo：透传 API 返回的 extraInfo JSON 对象，
			// DataBuddy 通过 extra_info.callbackConfig.imNotify 读取 IM 通知开关。
			if ei, ok := m["ExtraInfo"]; ok {
				if eiMap, ok := ei.(map[string]interface{}); ok {
					agent.ExtraInfo = eiMap
				}
			} else if ei, ok := m["extraInfo"]; ok {
				if eiMap, ok := ei.(map[string]interface{}); ok {
					agent.ExtraInfo = eiMap
				}
			}
			dedupKey := agent.AgentID
			if dedupKey == "" {
				dedupKey = agent.Name + "|" + agent.Status
			}
			if dedupKey != "" {
				if _, ok := seen[dedupKey]; ok {
					continue
				}
				seen[dedupKey] = struct{}{}
			}
			result = append(result, agent)
			added++
		}

		total := firstInt64(data, "Total", "total", "TotalCount", "totalCount", "TotalElements", "totalElements", "total_elements")
		if total > 0 && int64(len(result)) >= total {
			break
		}
		if len(listSlice) < dataAgentListPageSize {
			break
		}
		// If the API ignores PageNumber, page 2 would repeat page 1 forever.
		if added == 0 && page > 1 {
			break
		}
	}
	return result, nil
}

// ListRemoteSessions calls ListDataAgentSession and returns all sessions accessible
// to the current API key. Paginates until all results are fetched.
func (c *Client) ListRemoteSessions(workspaceID string) ([]RemoteSessionSummary, error) {
	if workspaceID == "" {
		workspaceID = c.ResolveWorkspaceID()
	}
	var result []RemoteSessionSummary
	listCred := c.credential()
	timeParams := sessionListTimeParams(listCred != nil && listCred.IsAPIKey())
	for page := 1; page <= dataAgentListMaxPageScan; page++ {
		params := map[string]string{
			"WorkspaceId": workspaceID,
			"PageNumber":  strconv.Itoa(page),
			"PageSize":    strconv.Itoa(dataAgentListPageSize),
		}
		c.setDmsUnit(params)
		for k, v := range timeParams {
			params[k] = v
		}
		body, err := c.callAPI(c.endpoint, "ListDataAgentSession", "2025-04-14", params)
		if err != nil {
			return nil, fmt.Errorf("ListRemoteSessions: %w", err)
		}
		_, listSlice := dataAgentPageContent(body)
		if len(listSlice) == 0 {
			break
		}
		added := 0
		for _, item := range listSlice {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			summary := RemoteSessionSummary{
				SessionID:   firstStr(m, "SessionId", "sessionId"),
				AgentID:     firstStr(m, "AgentId", "agentId"),
				Status:      firstStr(m, "Status", "status", "SessionStatus", "sessionStatus"),
				Mode:        firstStr(m, "Mode", "mode", "SessionType", "sessionType"),
				WorkspaceID: firstStr(m, "WorkspaceId", "WorkspaceID", "workspaceId", "workspace_id"),
			}
			if summary.WorkspaceID == "" {
				summary.WorkspaceID = workspaceID
			}
			result = append(result, summary)
			added++
		}
		if len(listSlice) < dataAgentListPageSize {
			break
		}
		if added == 0 && page > 1 {
			break
		}
	}
	return result, nil
}

func sessionListTimeParams(apiKeyAuth bool) map[string]string {
	days := defaultSessionLookbackDays
	if raw := strings.TrimSpace(os.Getenv("DATA_AGENT_SESSION_LOOKBACK_DAYS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			days = parsed
		}
	}
	if days > 365 {
		days = 365
	}

	end := time.Now().UTC()
	start := end.AddDate(0, 0, -days)
	if apiKeyAuth {
		return map[string]string{
			"StartTime": strconv.FormatInt(start.UnixMilli(), 10),
			"EndTime":   strconv.FormatInt(end.UnixMilli(), 10),
		}
	}
	return map[string]string{
		"CreateStartTime": start.Format(time.RFC3339),
		"CreateEndTime":   end.Format(time.RFC3339),
	}
}

// GetFileUploadSignature returns a pre-signed upload URL for a file.
func (c *Client) GetFileUploadSignature(fileName string, fileSize int64) (*UploadSignature, error) {
	params := map[string]string{
		"FileName": fileName,
		"FileSize": strconv.FormatInt(fileSize, 10),
	}
	c.setDmsUnit(params)

	body, err := c.callAPI(c.endpoint, "DescribeFileUploadSignature", "2025-04-14", params)
	if err != nil {
		return nil, fmt.Errorf("GetFileUploadSignature: %w", err)
	}

	data := jsonObj(body, "data")
	if data == nil {
		data = jsonObj(body, "Data")
	}
	if data == nil {
		// API Key gateway unwraps one envelope layer, so the signature fields
		// may already be at the top level.
		data = body
	}

	return &UploadSignature{
		UploadHost:          firstStr(data, "UploadHost", "uploadHost"),
		UploadDir:           firstStr(data, "UploadDir", "uploadDir"),
		Policy:              firstStr(data, "Policy", "policy"),
		OssSignature:        firstStr(data, "OssSignature", "ossSignature"),
		OssSignatureVersion: firstStr(data, "OssSignatureVersion", "ossSignatureVersion"),
		OssDate:             firstStr(data, "OssDate", "ossDate"),
		OssSecurityToken:    firstStr(data, "OssSecurityToken", "ossSecurityToken"),
		OssCredential:       firstStr(data, "OssCredential", "ossCredential"),
	}, nil
}

// FileUploadCallback notifies the server that a file upload is complete.
// Returns the Data Center file ID (e.g. "f-xxx") from the API response.
func (c *Client) FileUploadCallback(filename, uploadLocation string, fileSize int64) (string, error) {
	params := map[string]string{
		"Filename":       filename,
		"UploadLocation": uploadLocation,
		"FileSize":       strconv.FormatInt(fileSize, 10),
		"FileFrom":       "Skill",
	}
	c.setDmsUnit(params)

	body, err := c.callAPI(c.endpoint, "FileUploadCallback", "2025-04-14", params)
	if err != nil {
		return "", fmt.Errorf("FileUploadCallback: %w", err)
	}

	data := jsonObj(body, "Data")
	if data == nil {
		data = jsonObj(body, "data")
	}
	if data == nil {
		// API Key gateway unwraps one envelope layer, so the file ID may already
		// be at the top level.
		data = body
	}

	return firstStr(data, "FileId", "fileId"), nil
}

// ResolveDMSUnit returns the configured or auto-resolved DMSUnit.
// DMSUnit is independent from region — this method never falls back to region.
func (c *Client) ResolveDMSUnit() string {
	c.dmsUnitMu.Lock()
	defer c.dmsUnitMu.Unlock()

	if c.dmsUnit != "" {
		return c.dmsUnit
	}

	if c.credential().IsAPIKey() {
		return ""
	}

	dmsEndpoint := c.DMSEnterpriseEndpoint()
	body, err := c.callDMSEnterprise(dmsEndpoint, "GetActiveRouteUnit", "2018-11-01", nil)
	if err == nil && body != nil {
		route := jsonObj(body, "Route")
		if route == nil {
			route = jsonObj(body, "Data")
		}
		if route != nil {
			if rid := jsonStrAny(route, "RegionId"); rid != "" {
				c.dmsUnit = rid
				return c.dmsUnit
			}
		}
	}

	return ""
}

// setDmsUnit conditionally adds DmsUnit to params if configured or auto-resolved.
func (c *Client) setDmsUnit(params map[string]string) {
	if u := c.ResolveDMSUnit(); u != "" {
		params["DmsUnit"] = u
	}
}

// StreamSSE opens an SSE stream for the given agent/session and returns events
// via a channel.
func (c *Client) StreamSSE(ctx context.Context, agentID, sessionID string, checkpoint int) (<-chan SSEEvent, error) {
	return c.sse.StreamEvents(ctx, agentID, sessionID, checkpoint)
}

// ---------- internal HTTP helpers ----------

// callAPI makes a signed POST to the Data Agent endpoint (version 2025-04-14).
func (c *Client) callAPI(host, action, version string, params map[string]string) (map[string]interface{}, error) {
	if c.credential().IsAPIKey() {
		return c.doAPIKeyPost(action, version, params)
	}
	return c.doSignedPost(host, action, version, params)
}

// resolveTid calls GetUserActiveTenant to obtain the DMS tenant ID.
func (c *Client) resolveTid(dmsEndpoint string) string {
	if c.credential().IsAPIKey() {
		return ""
	}
	body, err := c.callDMSEnterprise(dmsEndpoint, "GetUserActiveTenant", "2018-11-01", nil)
	if err != nil {
		return ""
	}
	tenant := jsonObj(body, "Tenant")
	if tenant == nil {
		return ""
	}
	if tid, ok := tenant["Tid"]; ok {
		return fmt.Sprintf("%v", tid)
	}
	if tid, ok := tenant["tid"]; ok {
		return fmt.Sprintf("%v", tid)
	}
	return ""
}

// ErrAPIKeyUnsupported is returned when a DMS Enterprise API is called in API Key mode.
var ErrAPIKeyUnsupported = fmt.Errorf("this operation is not available in API Key mode (requires AK/SK credentials)")

// apiKeyDMSActions maps DMS Enterprise action names to their API Key gateway
// equivalents. Actions in this map are routed through doAPIKeyPost instead of
// being rejected in API Key mode.
var apiKeyDMSActions = map[string]string{
	"ListTagMetaAsset": "listTagMetaAsset",
}

// callDMSEnterprise makes a signed POST to a dms-enterprise endpoint.
func (c *Client) callDMSEnterprise(host, action, version string, params map[string]string) (map[string]interface{}, error) {
	if c.credential().IsAPIKey() {
		if apiKeyAction, ok := apiKeyDMSActions[action]; ok {
			return c.doAPIKeyPost(apiKeyAction, version, params)
		}
		return nil, fmt.Errorf("%s: %w", action, ErrAPIKeyUnsupported)
	}
	return c.doSignedPostVersioned(host, action, version, params)
}

// apiKeyActionRenames maps AK/SK action names to API Key gateway action names
// where casing differs between the two endpoints.
var apiKeyActionRenames = map[string]string{
	"ListDataAgentWorkspace": "ListDataAgentWorkSpace",
}

// API Key data-plane actions use the stream endpoint.
var apiKeyDataPlaneActions = map[string]bool{
	"SendChatMessage":         true,
	"GetChatContent":          true,
	"DescribeDataAgentUsage":  true,
	"UpdateDataAgentSession":  true,
	"CreateDataAgentFeedback": true,
}

// doAPIKeyPost performs an API Key authenticated POST request.
// Uses x-api-key header with no HMAC signing. Different endpoints than AK/SK.
func (c *Client) doAPIKeyPost(action, version string, params map[string]string) (map[string]interface{}, error) {
	if renamed, ok := apiKeyActionRenames[action]; ok {
		action = renamed
	}
	// Choose endpoint: control plane vs data plane.
	host := c.APIKeyEndpoint()
	if apiKeyDataPlaneActions[action] {
		host = c.APIKeyStreamEndpoint()
	}

	// Build JSON body with Action, Version, RegionId, and all params.
	// Params that are JSON strings (e.g. SessionConfig, DataSource) must be
	// sent as nested objects in the POST body, not as strings.
	bodyMap := map[string]interface{}{
		"Action":   action,
		"Version":  version,
		"RegionId": c.region,
	}
	for k, v := range params {
		if len(v) > 0 && (v[0] == '{' || v[0] == '[') {
			var nested interface{}
			if json.Unmarshal([]byte(v), &nested) == nil {
				bodyMap[k] = nested
				continue
			}
		}
		bodyMap[k] = v
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	reqURL := fmt.Sprintf("https://%s/apikey", host)
	req, err := http.NewRequest("POST", reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("build API Key request for %s: %w", action, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.credential().APIKey)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP %s: %w", action, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response for %s: %w", action, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"%s returned HTTP %d (request-id: %s): %s",
			action, resp.StatusCode, resp.Header.Get("x-acs-request-id"), truncate(string(respBody), 500),
		)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse JSON for %s: %w", action, err)
	}

	// The API Key gateway returns errors with HTTP 200 but success=false (or a
	// raw {HttpStatusCode, Code, Message} body on the stream endpoint). Surface
	// these as errors so callers don't silently see empty results. Carry the
	// gateway requestId so backend failures can be traced in support tickets.
	if success, ok := result["success"].(bool); ok && !success {
		code := firstStr(result, "code", "Code")
		msg := firstStr(result, "msg", "Message", "message")
		return nil, fmt.Errorf("%s failed (%s, request-id: %s): %s",
			action, code, apiKeyRequestID(result, resp), msg)
	}
	if httpCode := firstInt64(result, "HttpStatusCode", "httpStatusCode"); httpCode >= 400 {
		msg := firstStr(result, "Message", "message", "msg")
		return nil, fmt.Errorf("%s failed (HTTP %d, request-id: %s): %s",
			action, httpCode, apiKeyRequestID(result, resp), msg)
	}

	// The API Key gateway wraps successful responses in an envelope:
	//   {"data": <inner>, "success": true, "code": "success", "requestId": ...}
	// AK/SK callers expect the inner payload at the top level, so unwrap one
	// layer when this envelope is present. Inner payloads keep their own shape
	// (e.g. {"data": [...]} for listTagMetaAsset, {"Content": [...]} for
	// ListDataAgentWorkSpace, {"agentId": ...} for CreateDataAgentSession),
	// which the downstream parsers already handle via their data=body fallback.
	if _, hasCode := result["code"]; hasCode {
		if inner, ok := result["data"].(map[string]interface{}); ok {
			return inner, nil
		}
	}

	return result, nil
}

// doSignedPost performs a signed POST request using the default API version 2025-04-14.
func (c *Client) doSignedPost(host, action, version string, params map[string]string) (map[string]interface{}, error) {
	return c.doSignedPostVersioned(host, action, version, params)
}

// doSignedPostVersioned performs a signed POST request with the specified API version.
func (c *Client) doSignedPostVersioned(host, action, version string, params map[string]string) (map[string]interface{}, error) {
	if params == nil {
		params = map[string]string{}
	}

	// Build signed headers. We need to override the version in the signer for
	// dms-enterprise calls that use a different version.
	headers := signRequestVersioned(c.credential(), "POST", host, action, version, params, "")
	headers["User-Agent"] = userAgent

	qs := BuildSignedQueryStringVersioned(action, version, params)
	reqURL := fmt.Sprintf("https://%s/?%s", host, qs)

	req, err := http.NewRequest("POST", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", action, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP %s: %w", action, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response for %s: %w", action, err)
	}

	if resp.StatusCode != http.StatusOK {
		requestID := resp.Header.Get("x-acs-request-id")
		return nil, fmt.Errorf(
			"%s returned HTTP %d (request-id: %s): %s",
			action, resp.StatusCode, requestID, truncate(string(respBody), 500),
		)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("parse JSON for %s: %w", action, err)
	}

	return result, nil
}

// ---------- signing helpers with version support ----------

// signRequestVersioned is like SignRequest but allows specifying the API version.
func signRequestVersioned(cred *Credential, method, host, action, version string, params map[string]string, body string) map[string]string {
	if version == "2025-04-14" {
		return SignRequest(cred, method, host, action, params, body)
	}

	// For non-default versions, replicate the signing logic with the correct version.
	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	nonce := generateUUID()

	hashedPayload := emptyHash
	if body != "" {
		hashedPayload = sha256Hex(body)
	}

	httpMethod := strings.ToUpper(method)

	queryParams := map[string]string{
		"Action":  action,
		"Version": version,
	}
	for k, v := range params {
		queryParams[k] = v
	}

	canonicalQueryString := buildCanonicalQueryString(queryParams)

	headersToSign := map[string]string{
		"host":                  host,
		"x-acs-action":          action,
		"x-acs-content-sha256":  hashedPayload,
		"x-acs-date":            timestamp,
		"x-acs-signature-nonce": nonce,
		"x-acs-version":         version,
	}
	if cred.SecurityToken != "" {
		headersToSign["x-acs-security-token"] = cred.SecurityToken
	}

	headerKeys := sortedKeys(headersToSign)
	var chBuf strings.Builder
	for _, k := range headerKeys {
		fmt.Fprintf(&chBuf, "%s:%s\n", k, headersToSign[k])
	}
	canonicalHeaders := chBuf.String()
	signedHeaders := strings.Join(headerKeys, ";")

	canonicalRequest := strings.Join([]string{
		httpMethod, "/", canonicalQueryString, canonicalHeaders, signedHeaders, hashedPayload,
	}, "\n")

	algorithm := "ACS3-HMAC-SHA256"
	stringToSign := algorithm + "\n" + sha256Hex(canonicalRequest)
	signature := hexEncode(hmacSHA256([]byte(cred.AccessKeySecret), stringToSign))

	authorization := fmt.Sprintf(
		"%s Credential=%s,SignedHeaders=%s,Signature=%s",
		algorithm, cred.AccessKeyID, signedHeaders, signature,
	)

	result := map[string]string{
		"Authorization":         authorization,
		"Host":                  host,
		"x-acs-action":          action,
		"x-acs-content-sha256":  hashedPayload,
		"x-acs-date":            timestamp,
		"x-acs-signature-nonce": nonce,
		"x-acs-version":         version,
	}
	if cred.SecurityToken != "" {
		result["x-acs-security-token"] = cred.SecurityToken
	}
	return result
}

// BuildSignedQueryStringVersioned builds a sorted, percent-encoded query string.
func BuildSignedQueryStringVersioned(action, version string, params map[string]string) string {
	all := map[string]string{
		"Action":  action,
		"Version": version,
	}
	for k, v := range params {
		all[k] = v
	}
	return buildCanonicalQueryString(all)
}

// ---------- JSON helper functions ----------

func jsonStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func jsonStrAny(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		switch s := v.(type) {
		case string:
			return s
		case float64:
			return strconv.FormatFloat(s, 'f', -1, 64)
		}
	}
	return ""
}

func jsonInt64(m map[string]interface{}, key string) int64 {
	if v, ok := m[key]; ok {
		switch n := v.(type) {
		case float64:
			return int64(n)
		case json.Number:
			if i, err := n.Int64(); err == nil {
				return i
			}
		case string:
			if i, err := strconv.ParseInt(n, 10, 64); err == nil {
				return i
			}
		}
	}
	return 0
}

func jsonObj(m map[string]interface{}, key string) map[string]interface{} {
	if v, ok := m[key]; ok {
		if obj, ok := v.(map[string]interface{}); ok {
			return obj
		}
	}
	return nil
}

func dataAgentPageContent(body map[string]interface{}) (map[string]interface{}, []interface{}) {
	for _, key := range []string{"data", "Data"} {
		if rawList, ok := body[key]; ok {
			if listSlice, ok := rawList.([]interface{}); ok {
				return body, listSlice
			}
		}
	}

	data := jsonObj(body, "data")
	if data == nil {
		data = jsonObj(body, "Data")
	}
	if data == nil {
		data = body
	}
	for _, key := range []string{"Content", "content", "List", "list", "Rows", "rows"} {
		if rawList, ok := data[key]; ok {
			if listSlice, ok := rawList.([]interface{}); ok {
				return data, listSlice
			}
		}
	}
	return data, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// apiKeyRequestID extracts the backend request id from an API Key gateway
// response for error attribution: the envelope carries requestId in the body;
// fall back to the x-acs-request-id header.
func apiKeyRequestID(body map[string]interface{}, resp *http.Response) string {
	if id := firstStr(body, "requestId", "RequestId"); id != "" {
		return id
	}
	return resp.Header.Get("x-acs-request-id")
}

// firstStr tries multiple keys and returns the first non-empty string value.
func firstStr(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s := jsonStrAny(m, k); s != "" {
			return s
		}
	}
	return ""
}

// firstInt64 tries multiple keys and returns the first non-zero int64 value.
func firstInt64(m map[string]interface{}, keys ...string) int64 {
	for _, k := range keys {
		if v := jsonInt64(m, k); v != 0 {
			return v
		}
	}
	return 0
}

// ---------- small utility functions ----------

// inferDbTypeFromHost guesses the database type from a hostname pattern.
// DMS ListInstances API may not return DbType; this provides a best-effort
// fallback based on common Alibaba Cloud RDS hostname conventions.
func inferDbTypeFromHost(host string) string {
	host = strings.ToLower(host)
	switch {
	case strings.Contains(host, ".mysql.") || strings.Contains(host, ".mysql"):
		return "mysql"
	case strings.Contains(host, ".pg.") || strings.Contains(host, ".postgresql."):
		return "postgresql"
	case strings.Contains(host, ".mssql.") || strings.Contains(host, ".sqlserver."):
		return "mssql"
	case strings.Contains(host, ".mongo.") || strings.Contains(host, ".mongodb."):
		return "mongodb"
	case strings.Contains(host, ".redis.") || strings.Contains(host, ".r-kvstore."):
		return "redis"
	case strings.Contains(host, ".polardb.") || strings.HasPrefix(host, "pc-"):
		return "polardb"
	case strings.Contains(host, ".clickhouse.") || strings.HasPrefix(host, "cc-"):
		return "clickhouse"
	case strings.Contains(host, ".rds.aliyun.com") || strings.Contains(host, ".rds.aliyuncs.com"):
		// Generic RDS hostname without engine prefix — most common case is MySQL.
		return "mysql"
	default:
		return ""
	}
}

func buildCanonicalQueryString(params map[string]string) string {
	keys := sortedKeys(params)
	var parts []string
	for _, k := range keys {
		parts = append(parts, percentEncode(k)+"="+percentEncode(params[k]))
	}
	return strings.Join(parts, "&")
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	// Simple insertion sort for small slices; avoids importing sort in this file.
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}

func hexEncode(b []byte) string {
	const hextable = "0123456789abcdef"
	dst := make([]byte, len(b)*2)
	for i, v := range b {
		dst[i*2] = hextable[v>>4]
		dst[i*2+1] = hextable[v&0x0f]
	}
	return string(dst)
}

// generateUUID returns a new random UUID v4 string.
// Delegates to the uuidNew helper defined in signer.go.
func generateUUID() string {
	return uuidNew()
}
