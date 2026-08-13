package dataagent

import (
	"crypto/sha256"
	"encoding/hex"
)

// SessionInfo from CreateSession/DescribeSession.
type SessionInfo struct {
	SessionID     string `json:"sessionId"`
	AgentID       string `json:"agentId"`
	AgentStatus   string `json:"agentStatus"`
	SessionStatus string `json:"sessionStatus"`
	RequestID     string `json:"requestId"`
	WorkspaceID   string `json:"workspaceId,omitempty"`
}

// DataSource for SendChatMessage.
type DataSource struct {
	DataSourceType string   `json:"DataSourceType"`
	DmsDatabaseID  string   `json:"DmsDatabaseId"`
	DmsInstanceID  string   `json:"DmsInstanceId,omitempty"`
	InstanceName   string   `json:"InstanceName,omitempty"`
	DbName         string   `json:"DbName"`
	Database       string   `json:"Database"`
	Tables         []string `json:"Tables"`
	Engine         string   `json:"Engine"`
	RegionID       string   `json:"RegionId"`
	FileID         string   `json:"FileId,omitempty"`
}

// DatabaseInfo from ListTagMetaAsset.
type DatabaseInfo struct {
	DbID               int64  `json:"dbId"`
	InstanceID         int64  `json:"instanceId"`
	SchemaName         string `json:"schemaName"`
	DbType             string `json:"dbType"`
	InstanceResourceID string `json:"instanceResourceId"`
	CatalogName        string `json:"catalogName"`
}

// CreateSessionOpts options for creating a session.
type CreateSessionOpts struct {
	Mode          string // auto, lite, pro, ultra (legacy ASK_DATA/ANALYSIS/INSIGHT accepted upstream)
	PlanMode      string // "force" (always generate a plan) or "disable" (skip planning); empty = server default
	DatabaseID    string
	FileID        string // uploaded file ID (alternative to DatabaseID)
	EnableSearch  bool
	WorkspaceID   string
	CustomAgentID string
}

// SendMessageOpts options for sending a message.
type SendMessageOpts struct {
	AgentID     string
	SessionID   string
	Message     string
	DataSource  *DataSource
	WorkspaceID string
	Mode        string
	PlanMode    string // optional per-message plan mode: "force" or "disable"
}

// Credential holds Alibaba Cloud credentials.
type Credential struct {
	AccessKeyID     string
	AccessKeySecret string
	SecurityToken   string
	APIKey          string // API Key auth (alternative to AK/SK)
}

// IsAPIKey returns true if API Key auth mode is active.
func (c *Credential) IsAPIKey() bool { return c.APIKey != "" }

// TenantKey returns a stable, non-reversible fingerprint of the credential
// identity, used for per-tenant session isolation in multi-tenant
// deployments. Only stable identities yield a key: API Keys and long-term
// AK/SK pairs. STS credentials rotate on refresh, so they return "" (no
// isolation — matches the embedded single-tenant deployment behavior).
func (c *Credential) TenantKey() string {
	if c == nil {
		return ""
	}
	switch {
	case c.APIKey != "":
		return fingerprint("apikey:" + c.APIKey)
	case c.AccessKeyID != "" && c.SecurityToken == "":
		return fingerprint("ak:" + c.AccessKeyID)
	}
	return ""
}

// fingerprint returns the first 16 hex chars of the SHA-256 digest. The
// plaintext credential is never persisted.
func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:16]
}

// FileInfo from ListFileUpload.
type FileInfo struct {
	FileID      string `json:"file_id"`
	FileName    string `json:"filename"`
	FileType    string `json:"file_type"`
	FileSize    int64  `json:"file_size"`
	DownloadURL string `json:"download_url,omitempty"`
}

// TableInfo from ListTagMetaAsset (META_TABLE).
type TableInfo struct {
	TableName string `json:"table_name"`
	TableID   string `json:"table_id"`
	Engine    string `json:"engine"`
	DbID      string `json:"db_id,omitempty"`   // parent database id (set in workspace-wide listings)
	DbName    string `json:"db_name,omitempty"` // parent database name
}

// InstanceInfo from ListInstances.
type InstanceInfo struct {
	InstanceID         string `json:"instance_id"`
	InstanceAlias      string `json:"instance_alias"`
	Host               string `json:"host"`
	Port               int    `json:"port"`
	DbType             string `json:"db_type"`
	EnvType            string `json:"env_type"`
	InstanceResourceID string `json:"instance_resource_id"`
}

// SearchDBInfo from SearchDatabase.
type SearchDBInfo struct {
	DatabaseID string `json:"database_id"`
	SchemaName string `json:"schema_name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	InstanceID int64  `json:"instance_id"`
	DbType     string `json:"db_type"`
	EnvType    string `json:"env_type"`
}

// WorkspaceInfo from ListWorkspaces.
type WorkspaceInfo struct {
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
}

// AgentInfo from ListCustomAgents.
type AgentInfo struct {
	AgentID     string `json:"agent_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	// ExtraInfo 透传 ListCustomAgent API 返回的 extraInfo JSON 对象。
	// DataBuddy 通过路径 extra_info.callbackConfig.imNotify 读取 IM 通知开关。
	ExtraInfo map[string]interface{} `json:"extra_info,omitempty"`
}

// RemoteSessionSummary is a lightweight session entry returned by ListDataAgentSession API.
type RemoteSessionSummary struct {
	SessionID   string
	AgentID     string
	Status      string // server-side status string (RUNNING, STOPPED, FAILED, FINISHED, etc.)
	Mode        string
	WorkspaceID string
}

// UploadSignature from DescribeFileUploadSignature (OSS form-upload credentials).
type UploadSignature struct {
	UploadHost          string `json:"upload_host"`
	UploadDir           string `json:"upload_dir"`
	Policy              string `json:"policy"`
	OssSignature        string `json:"oss_signature"`
	OssSignatureVersion string `json:"oss_signature_version"`
	OssDate             string `json:"oss_date"`
	OssSecurityToken    string `json:"oss_security_token"`
	OssCredential       string `json:"oss_credential"`
}
