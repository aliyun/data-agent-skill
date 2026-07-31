package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
)

func argStr(req mcp.CallToolRequest, key string) string {
	return req.GetString(key, "")
}

func argBool(req mcp.CallToolRequest, key string, def bool) bool {
	return req.GetBool(key, def)
}

// normalizeMode maps a session mode to the current tier system
// (auto / lite / pro / ultra) and translates legacy mode names so older
// callers and configs keep working:
//
//	ASK_DATA → lite    (quick Q&A, single SQL)
//	ANALYSIS → pro     (multi-step deep analysis with reports)
//	INSIGHT  → ultra   (most thorough, multi-dimensional insights)
func normalizeMode(mode string) string {
	switch strings.ToUpper(strings.TrimSpace(mode)) {
	case "":
		return ""
	case "ASK_DATA":
		return "lite"
	case "ANALYSIS":
		return "pro"
	case "INSIGHT":
		return "ultra"
	case "AUTO", "LITE", "PRO", "ULTRA":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		// Unknown values pass through unchanged (forward compatibility).
		return strings.TrimSpace(mode)
	}
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

func (s *Server) handleListDatabases(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbs, err := s.client.ListDatabases()
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list databases: %v", err)), nil
	}
	type dbEntry struct {
		DbID               int64  `json:"db_id"`
		SchemaName         string `json:"db_name"`
		DbType             string `json:"db_type"`
		InstanceID         int64  `json:"instance_id"`
		InstanceResourceID string `json:"instance_resource_id"`
		CatalogName        string `json:"catalog_name"`
	}
	out := make([]dbEntry, len(dbs))
	for i, d := range dbs {
		out[i] = dbEntry{
			DbID:               d.DbID,
			SchemaName:         d.SchemaName,
			DbType:             d.DbType,
			InstanceID:         d.InstanceID,
			InstanceResourceID: d.InstanceResourceID,
			CatalogName:        d.CatalogName,
		}
	}
	return jsonResult(out)
}

func (s *Server) handleCreateSession(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbID := argStr(req, "database_id")
	dbName := argStr(req, "db_name")
	tablesStr := argStr(req, "tables")
	query := argStr(req, "query")
	fileID := argStr(req, "file_id")
	fileName := argStr(req, "file_name")

	if query == "" {
		return mcp.NewToolResultError("query is required"), nil
	}
	if dbID == "" && fileID == "" {
		return mcp.NewToolResultError("either database_id or file_id is required"), nil
	}
	if dbID != "" && fileID != "" {
		return mcp.NewToolResultError("database_id and file_id are mutually exclusive"), nil
	}
	if dbID != "" && dbName == "" {
		return mcp.NewToolResultError("db_name is required for database analysis"), nil
	}
	if dbID != "" && tablesStr == "" {
		return mcp.NewToolResultError("tables is required for database analysis; call data_agent_list_imported_tables first to get available table names"), nil
	}
	if fileID != "" && fileName == "" {
		return mcp.NewToolResultError("file_name is required for file analysis"), nil
	}

	var tables []string
	if tablesStr != "" {
		for _, t := range strings.Split(tablesStr, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tables = append(tables, t)
			}
		}
	}

	mode := normalizeMode(argStr(req, "mode"))
	if mode == "" {
		mode = normalizeMode(s.defaults.Mode) // identity group default, if any
	}
	if mode == "" {
		if fileID != "" {
			mode = "pro" // file analysis defaults to deep analysis
		} else {
			mode = "auto" // backend decides based on query complexity
		}
	}

	autoConfirm := argBool(req, "auto_confirm", true)

	engine := argStr(req, "engine")
	if engine == "" {
		engine = "mysql"
	}

	customAgentID := s.resolveCustomAgentID(argStr(req, "custom_agent_id"))

	planMode := strings.ToLower(strings.TrimSpace(argStr(req, "plan_mode")))
	if planMode != "" && planMode != "force" && planMode != "disable" {
		return mcp.NewToolResultError("plan_mode must be 'force' or 'disable'"), nil
	}

	opts := session.CreateOpts{
		DatabaseID:    dbID,
		DbName:        dbName,
		Tables:        tables,
		Query:         query,
		Mode:          mode,
		PlanMode:      planMode,
		AutoConfirm:   autoConfirm,
		InstanceID:    argStr(req, "instance_id"),
		InstanceName:  argStr(req, "instance_name"),
		Engine:        engine,
		WorkspaceID:   argStr(req, "workspace_id"),
		CustomAgentID: customAgentID,
		FileID:        fileID,
		FileName:      fileName,
	}

	state, err := s.mgr.CreateSession(ctx, opts)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create session: %v", err)), nil
	}

	return jsonResult(map[string]any{
		"session_id":   state.SessionID,
		"status":       state.Status,
		"mode":         state.Mode,
		"auto_confirm": state.AutoConfirm,
	})
}

func (s *Server) handleStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sid := argStr(req, "session_id")
	if sid == "" {
		return mcp.NewToolResultError("session_id is required"), nil
	}

	waitTimeout := 0
	if wt, ok := req.GetArguments()["wait_timeout"]; ok && wt != nil {
		if f, ok2 := wt.(float64); ok2 {
			waitTimeout = int(f)
		}
	}

	var state *session.StateSnapshot
	changed := true
	var err error

	if waitTimeout > 0 {
		var cur *session.StateSnapshot
		cur, err = s.mgr.GetStatus(sid)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to get status: %v", err)), nil
		}
		state, changed, err = s.mgr.WaitForChange(ctx, sid, cur.Checkpoint, time.Duration(waitTimeout)*time.Second)
	} else {
		state, err = s.mgr.GetStatus(sid)
	}
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to get status: %v", err)), nil
	}

	pollSeq := s.mgr.IncrPollSeq(sid)

	result := map[string]any{
		"session_id":    state.SessionID,
		"status":        state.Status,
		"mode":          state.Mode,
		"auto_confirm":  state.AutoConfirm,
		"current_step":  state.CurrentStep,
		"total_steps":   state.TotalSteps,
		"step_name":     state.StepName,
		"waiting_for":   state.WaitingFor,
		"checkpoint":    state.Checkpoint,
		"conclusions":   len(state.Conclusions),
		"artifacts":     state.Artifacts,
		"error_message": state.ErrorMessage,
		"updated_at":    state.UpdatedAt,
		"poll_seq":      pollSeq,
	}
	if waitTimeout > 0 {
		result["changed"] = changed
	}
	if pollSeq > 2 {
		result["warning"] = "You have polled status " + strconv.Itoa(pollSeq) + " times. STOP polling. Use data_agent_wait_result instead of calling data_agent_status repeatedly. Repeated status calls will trigger 'Repetitive tool calls detected' errors."
	}
	return jsonResult(result)
}

func (s *Server) handleWaitResult(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sid := argStr(req, "session_id")
	if sid == "" {
		return mcp.NewToolResultError("session_id is required"), nil
	}

	timeout := 300 * time.Second
	if t, ok := req.GetArguments()["timeout"]; ok && t != nil {
		if f, ok2 := t.(float64); ok2 && f > 0 {
			timeout = time.Duration(f) * time.Second
		}
	}

	snap, reason, err := s.mgr.WaitForResult(ctx, sid, timeout)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to wait: %v", err)), nil
	}

	result := map[string]any{
		"session_id":     snap.SessionID,
		"status":         snap.Status,
		"reason":         reason,
		"mode":           snap.Mode,
		"auto_confirm":   snap.AutoConfirm,
		"current_step":   snap.CurrentStep,
		"total_steps":    snap.TotalSteps,
		"step_name":      snap.StepName,
		"waiting_for":    snap.WaitingFor,
		"waiting_detail": snap.WaitingDetail,
		"checkpoint":     snap.Checkpoint,
		"conclusions":    len(snap.Conclusions),
		"artifacts":      snap.Artifacts,
		"error_message":  snap.ErrorMessage,
		"updated_at":     snap.UpdatedAt,
	}
	return jsonResult(result)
}

func (s *Server) handleWatchSession(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sid := argStr(req, "session_id")
	if sid == "" {
		return mcp.NewToolResultError("session_id is required"), nil
	}

	snap, err := s.mgr.WatchSession(ctx, session.WatchOpts{
		SessionID:   sid,
		AgentID:     argStr(req, "agent_id"),
		WorkspaceID: argStr(req, "workspace_id"),
		Mode:        normalizeMode(argStr(req, "mode")),
		AutoConfirm: argBool(req, "auto_confirm", true),
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to watch session: %v", err)), nil
	}

	return jsonResult(map[string]any{
		"session_id":    snap.SessionID,
		"agent_id":      snap.AgentID,
		"status":        snap.Status,
		"mode":          snap.Mode,
		"auto_confirm":  snap.AutoConfirm,
		"current_step":  snap.CurrentStep,
		"total_steps":   snap.TotalSteps,
		"step_name":     snap.StepName,
		"waiting_for":   snap.WaitingFor,
		"checkpoint":    snap.Checkpoint,
		"workspace_id":  snap.WorkspaceID,
		"error_message": snap.ErrorMessage,
		"updated_at":    snap.UpdatedAt,
	})
}

func (s *Server) handleSend(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sid := argStr(req, "session_id")
	msg := argStr(req, "message")
	if sid == "" || msg == "" {
		return mcp.NewToolResultError("session_id and message are required"), nil
	}

	if err := s.mgr.SendMessage(sid, msg); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to send message: %v", err)), nil
	}

	state, _ := s.mgr.GetStatus(sid)
	status := "unknown"
	if state != nil {
		status = string(state.Status)
	}

	return jsonResult(map[string]any{
		"ok":     true,
		"status": status,
	})
}

func (s *Server) handleResult(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sid := argStr(req, "session_id")
	if sid == "" {
		return mcp.NewToolResultError("session_id is required"), nil
	}

	state, err := s.mgr.GetResult(sid)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to get result: %v", err)), nil
	}

	sessDir := s.mgr.SessionDir(sid)
	imageFilenames := getImageFilenames(sessDir)

	// Build multi-content result
	var contents []mcp.Content

	// 1. JSON metadata as TextContent
	jsonData, _ := json.Marshal(map[string]any{
		"session_id":            state.SessionID,
		"status":                state.Status,
		"conclusions":           state.Conclusions,
		"artifacts":             state.Artifacts,
		"images":                imageFilenames,
		"confirmations":         state.Confirmations,
		"recommended_questions": state.RecommendedQuestions,
		"error_message":         state.ErrorMessage,
	})
	contents = append(contents, mcp.NewTextContent(string(jsonData)))

	// 2. Append images as ImageContent
	imgDir := filepath.Join(sessDir, "images")
	entries, err := os.ReadDir(imgDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			filePath := filepath.Join(imgDir, entry.Name())
			data, err := os.ReadFile(filePath)
			if err != nil {
				continue
			}
			b64Data := base64.StdEncoding.EncodeToString(data)
			mimeType := inferImageMIMEType(entry.Name())
			contents = append(contents, mcp.NewImageContent(b64Data, mimeType))
		}
	}

	return &mcp.CallToolResult{
		Content: contents,
	}, nil
}

func (s *Server) handleListSessions(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	includeHistory := argBool(req, "include_history", false)
	includeRemote := argBool(req, "include_remote", false)

	var localSessions []*session.StateSnapshot
	if includeHistory {
		localSessions = s.mgr.ListAllSessions()
	} else {
		localSessions = s.mgr.ListSessions()
	}

	type entry struct {
		SessionID   string         `json:"session_id"`
		AgentID     string         `json:"agent_id,omitempty"`
		Status      session.Status `json:"status"`
		Mode        string         `json:"mode"`
		CurrentStep int            `json:"current_step"`
		TotalSteps  int            `json:"total_steps"`
		StepName    string         `json:"step_name"`
		WaitingFor  string         `json:"waiting_for,omitempty"`
		UpdatedAt   time.Time      `json:"updated_at"`
		Source      string         `json:"source,omitempty"`
		WorkspaceID string         `json:"workspace_id,omitempty"`
	}

	seen := make(map[string]struct{}, len(localSessions))
	out := make([]entry, 0, len(localSessions))
	for _, st := range localSessions {
		seen[st.SessionID] = struct{}{}
		out = append(out, entry{
			SessionID:   st.SessionID,
			AgentID:     st.AgentID,
			Status:      st.Status,
			Mode:        st.Mode,
			CurrentStep: st.CurrentStep,
			TotalSteps:  st.TotalSteps,
			StepName:    st.StepName,
			WaitingFor:  st.WaitingFor,
			UpdatedAt:   st.UpdatedAt,
			Source:      "local",
			WorkspaceID: st.WorkspaceID,
		})
	}

	if includeRemote {
		wsID := argStr(req, "workspace_id")
		remote, err := s.client.ListRemoteSessions(wsID)
		if err != nil {
			log.Printf("[list_sessions] remote fetch failed (degrading to local only): %v", err)
		} else {
			for _, r := range remote {
				if _, ok := seen[r.SessionID]; ok {
					continue // local version takes priority
				}
				seen[r.SessionID] = struct{}{}
				out = append(out, entry{
					SessionID:   r.SessionID,
					AgentID:     r.AgentID,
					Status:      mapRemoteStatus(r.Status),
					Mode:        r.Mode,
					Source:      "remote",
					WorkspaceID: r.WorkspaceID,
				})
			}
		}
	}

	return jsonResult(out)
}

func mapRemoteStatus(s string) session.Status {
	switch strings.ToUpper(s) {
	case "RUNNING":
		return session.StatusRunning
	case "WAIT_INPUT", "WAITING_INPUT":
		return session.StatusWaitingInput
	case "IDLE", "FINISHED", "COMPLETED", "STOPPED":
		return session.StatusCompleted
	case "FAILED", "ERROR":
		return session.StatusError
	case "CANCELED", "CANCELLED":
		return session.StatusCanceled
	default:
		return session.Status(strings.ToLower(s))
	}
}

func (s *Server) handleStopSession(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sid := argStr(req, "session_id")
	if sid == "" {
		return mcp.NewToolResultError("session_id is required"), nil
	}

	if err := s.mgr.StopSession(sid); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to stop session: %v", err)), nil
	}

	return jsonResult(map[string]any{"ok": true})
}

func (s *Server) handleListFiles(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	sid := argStr(req, "session_id")
	if sid == "" {
		return mcp.NewToolResultError("session_id is required"), nil
	}

	var agentID, workspaceID string
	if state, err := s.mgr.GetStatus(sid); err == nil {
		agentID = state.AgentID
		workspaceID = state.WorkspaceID
	}

	files, err := s.client.ListFiles(sid, agentID, workspaceID, argStr(req, "category"))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list files: %v", err)), nil
	}
	return jsonResult(files)
}

func (s *Server) handleListTables(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbID := argStr(req, "database_id")
	if dbID == "" {
		return mcp.NewToolResultError("database_id is required"), nil
	}
	tables, err := s.client.ListTables(dbID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list tables: %v", err)), nil
	}
	return jsonResult(tables)
}

func (s *Server) handleListImportedTables(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbID := argStr(req, "database_id")
	if dbID == "" {
		return mcp.NewToolResultError("database_id is required"), nil
	}
	tables, err := s.client.ListImportedTables(dbID)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list imported tables: %v", err)), nil
	}
	return jsonResult(tables)
}

func (s *Server) handleImportDatabase(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dbID := argStr(req, "database_id")
	tablesStr := argStr(req, "tables")

	if dbID == "" || tablesStr == "" {
		return mcp.NewToolResultError("database_id and tables are required"), nil
	}

	var tables []string
	for _, t := range strings.Split(tablesStr, ",") {
		t = strings.TrimSpace(t)
		if t == "*" {
			return mcp.NewToolResultError("wildcard tables are not supported; pass specific table names"), nil
		}
		if t != "" {
			tables = append(tables, t)
		}
	}
	if len(tables) == 0 {
		return mcp.NewToolResultError("tables must include at least one table name"), nil
	}

	err := s.client.ImportDatabase(dataagent.ImportDatabaseOpts{
		DmsDbID:     dbID,
		Tables:      tables,
		WorkspaceID: argStr(req, "workspace_id"),
	})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to import: %v", err)), nil
	}
	return jsonResult(map[string]any{"ok": true, "database_id": dbID, "tables": tables})
}

func (s *Server) handleSearchInstances(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	instances, err := s.client.ListInstances(argStr(req, "search_key"), argStr(req, "db_type"), 1, 50)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to search instances: %v", err)), nil
	}
	return jsonResult(instances)
}

func (s *Server) handleSearchDatabases(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	key := argStr(req, "search_key")
	if key == "" {
		return mcp.NewToolResultError("search_key is required"), nil
	}
	dbs, err := s.client.SearchDatabases(key, 1, 50)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to search databases: %v", err)), nil
	}
	return jsonResult(dbs)
}

func (s *Server) handleListWorkspaces(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	wsType := argStr(req, "type")
	if wsType == "" {
		wsType = "MY"
	}
	workspaces, err := s.client.ListWorkspaces(wsType)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list workspaces: %v", err)), nil
	}
	return jsonResult(workspaces)
}

func (s *Server) handleListAgents(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	status := argStr(req, "status")
	if status == "" {
		status = "RELEASED"
	}
	agents, err := s.client.ListCustomAgents(status, argStr(req, "workspace_id"))
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to list agents: %v", err)), nil
	}
	return jsonResult(agents)
}

func (s *Server) handleUploadFile(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	filePath := argStr(req, "file_path")
	if filePath == "" {
		return mcp.NewToolResultError("file_path is required"), nil
	}

	realPath, info, err := s.resolveUploadPath(filePath)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	sig, err := s.client.GetFileUploadSignature(info.Name(), info.Size())
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to get upload signature: %v", err)), nil
	}

	f, err := os.Open(realPath)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to open file: %v", err)), nil
	}
	defer f.Close()

	// OSS multipart/form-data POST upload.
	ossKey := sig.UploadDir + "/" + info.Name()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.WriteField("key", ossKey)
	writer.WriteField("policy", sig.Policy)
	writer.WriteField("x-oss-signature", sig.OssSignature)
	writer.WriteField("x-oss-signature-version", sig.OssSignatureVersion)
	writer.WriteField("x-oss-date", sig.OssDate)
	writer.WriteField("x-oss-security-token", sig.OssSecurityToken)
	writer.WriteField("x-oss-credential", sig.OssCredential)
	writer.WriteField("success_action_status", "200")

	// OSS policy requires correct content-type for the file part.
	contentType := detectContentType(info.Name())
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, info.Name()))
	partHeader.Set("Content-Type", contentType)
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create form file: %v", err)), nil
	}
	if _, err := io.Copy(part, f); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to write file data: %v", err)), nil
	}
	writer.Close()

	httpReq, err := http.NewRequest("POST", sig.UploadHost, &body)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to create upload request: %v", err)), nil
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("upload failed: %v", err)), nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		log.Printf("[DEBUG] OSS upload error: status=%d body=%s", resp.StatusCode, string(respBody))
		return mcp.NewToolResultError(fmt.Sprintf("upload returned HTTP %d: %s", resp.StatusCode, string(respBody))), nil
	}

	// Callback with OSS path to get the Data Center file ID.
	fileID, err := s.client.FileUploadCallback(info.Name(), ossKey, info.Size())
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("upload callback failed: %v", err)), nil
	}
	if fileID == "" {
		fileID = sig.UploadDir
	}

	return jsonResult(map[string]any{
		"file_id":  fileID,
		"filename": info.Name(),
		"size":     info.Size(),
	})
}

func detectContentType(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".csv":
		return "text/csv"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".pdf":
		return "application/pdf"
	case ".json":
		return "application/json"
	case ".txt":
		return "text/plain"
	default:
		return "application/octet-stream"
	}
}

// inferImageMIMEType returns the MIME type based on file extension.
func inferImageMIMEType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/png"
	}
}

// getImageFilenames returns the list of image filenames in the session's images directory.
func getImageFilenames(sessDir string) []string {
	imgDir := filepath.Join(sessDir, "images")
	entries, err := os.ReadDir(imgDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}
