package session

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
)

// CreateOpts are the options for creating a new Data Agent session via the
// Manager.
type CreateOpts struct {
	DatabaseID    string
	DbName        string
	Tables        []string
	Query         string
	Mode          string // auto, lite, pro, ultra
	PlanMode      string // "force" or "disable"; empty = server default
	AutoConfirm   bool
	InstanceID    string
	InstanceName  string
	Engine        string
	WorkspaceID   string
	CustomAgentID string
	FileID        string // uploaded file ID (alternative to DatabaseID)
	FileName      string // original filename for file-based DataSource
}

// WatchOpts identifies an existing remote Data Agent session to monitor.
type WatchOpts struct {
	SessionID   string
	AgentID     string
	WorkspaceID string
	Mode        string
	AutoConfirm bool
}

// Manager tracks all active session watchers. It provides the high-level
// operations exposed as MCP tools: create, get status, send message, get
// result, list, and stop.
type Manager struct {
	mu       sync.RWMutex
	watchers map[string]*watcherEntry
	client   *dataagent.Client
	sessDir  string
	baseCtx  context.Context // process-lifetime context for watcher goroutines
}

// watcherEntry bundles a Watcher with its cancel function.
type watcherEntry struct {
	watcher *Watcher
	state   *State
	cancel  context.CancelFunc
}

// NewManager creates a Manager that uses the given client and persists state
// under sessDir.
func NewManager(client *dataagent.Client, sessDir string) *Manager {
	return &Manager{
		watchers: make(map[string]*watcherEntry),
		client:   client,
		sessDir:  sessDir,
	}
}

// watchContext returns the process-lifetime context that watcher goroutines
// must be derived from. Request-scoped contexts are unsuitable: the HTTP
// transports cancel them right after the tool response is written.
func (m *Manager) watchContext() context.Context {
	if m.baseCtx != nil {
		return m.baseCtx
	}
	return context.Background()
}

// RestoreSessions scans the sessions directory for persisted state files and
// resumes SSE monitoring for sessions that were still active (running or
// waiting_input) when the previous server process exited.
// The given ctx is also adopted as the base context for all future watcher
// goroutines: watchers must outlive the MCP tool call that spawned them
// (Streamable HTTP cancels the request context as soon as the response is
// written, which would otherwise kill the SSE stream immediately).
func (m *Manager) RestoreSessions(ctx context.Context) {
	m.baseCtx = ctx
	entries, err := os.ReadDir(m.sessDir)
	if err != nil {
		return
	}

	restored := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessionID := e.Name()
		snap := LoadState(m.sessDir, sessionID)
		if snap == nil {
			continue
		}

		if snap.Status != StatusRunning && snap.Status != StatusWaitingInput {
			continue
		}
		if snap.AgentID == "" {
			continue
		}

		// Verify session is still active on server side.
		info, err := m.client.DescribeSession(sessionID, snap.WorkspaceID)
		if err != nil || info == nil {
			continue
		}
		serverStatus := strings.ToUpper(info.SessionStatus)
		if serverStatus == "STOPPED" || serverStatus == "FAILED" {
			continue
		}

		state := stateFromSnapshot(snap)
		watcher := NewWatcher(state, m.client, m.sessDir)
		watchCtx, watchCancel := context.WithCancel(ctx)

		entry := &watcherEntry{
			watcher: watcher,
			state:   state,
			cancel:  watchCancel,
		}

		m.mu.Lock()
		m.watchers[sessionID] = entry
		m.mu.Unlock()

		go watcher.Run(watchCtx)
		restored++
		log.Printf("restored session %s (status=%s, checkpoint=%d)", sessionID, snap.Status, snap.Checkpoint)
	}

	if restored > 0 {
		log.Printf("restored %d active session(s) from %s", restored, m.sessDir)
	}
}

// CreateSession creates a new Data Agent session, waits for it to become
// RUNNING, sends the initial query, and starts a background watcher goroutine.
func (m *Manager) CreateSession(ctx context.Context, opts CreateOpts) (*State, error) {
	// Fill in default workspace if not specified.
	if opts.WorkspaceID == "" {
		opts.WorkspaceID = m.client.ResolveWorkspaceID()
	}

	// 1. Create session on server.
	info, err := m.client.CreateSession(dataagent.CreateSessionOpts{
		Mode:          opts.Mode,
		PlanMode:      opts.PlanMode,
		DatabaseID:    opts.DatabaseID,
		FileID:        opts.FileID,
		EnableSearch:  false,
		WorkspaceID:   opts.WorkspaceID,
		CustomAgentID: opts.CustomAgentID,
	})
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	sessionID := info.SessionID
	agentID := info.AgentID

	// 2. Wait for session to become RUNNING (poll up to 60s).
	if err := m.waitForRunning(ctx, sessionID, opts.WorkspaceID, 60*time.Second); err != nil {
		return nil, fmt.Errorf("session %s did not start: %w", sessionID, err)
	}

	// 3. Build the data source for the initial message.
	var ds *dataagent.DataSource
	if opts.FileID != "" {
		baseName := opts.FileName
		if idx := strings.LastIndex(baseName, "."); idx > 0 {
			baseName = baseName[:idx]
		}
		ds = &dataagent.DataSource{
			DataSourceType: "remote_data_center",
			FileID:         opts.FileID,
			Database:       opts.FileName,
			Tables:         []string{baseName},
			RegionID:       m.client.Region(),
		}
	} else if opts.DatabaseID != "" {
		ds = &dataagent.DataSource{
			DataSourceType: "database",
			DmsDatabaseID:  opts.DatabaseID,
			DmsInstanceID:  opts.InstanceID,
			InstanceName:   opts.InstanceName,
			DbName:         opts.DbName,
			Database:       opts.DbName,
			Tables:         opts.Tables,
			Engine:         opts.Engine,
			RegionID:       m.client.Region(),
		}
	}

	// 4. Send initial query.
	if err := m.client.SendMessage(dataagent.SendMessageOpts{
		AgentID:     agentID,
		SessionID:   sessionID,
		Message:     opts.Query,
		DataSource:  ds,
		WorkspaceID: opts.WorkspaceID,
		Mode:        opts.Mode,
		PlanMode:    opts.PlanMode,
	}); err != nil {
		return nil, fmt.Errorf("send initial query: %w", err)
	}

	// 5. Build state.
	now := time.Now()
	state := &State{
		SessionID:   sessionID,
		AgentID:     agentID,
		Status:      StatusRunning,
		Mode:        opts.Mode,
		AutoConfirm: opts.AutoConfirm,
		WorkspaceID: opts.WorkspaceID,
		CreatedAt:   now,
		UpdatedAt:   now,
		changed:     make(chan struct{}),
	}

	// 6. Create and start watcher.
	watcher := NewWatcher(state, m.client, m.sessDir)

	watchCtx, watchCancel := context.WithCancel(m.watchContext())

	entry := &watcherEntry{
		watcher: watcher,
		state:   state,
		cancel:  watchCancel,
	}

	m.mu.Lock()
	m.watchers[sessionID] = entry
	m.mu.Unlock()

	go watcher.Run(watchCtx)

	// 7. Persist initial state.
	state.Persist(m.sessDir)

	return state, nil
}

// WatchSession attaches the local MCP server to an existing remote session and
// starts the same background SSE watcher used for sessions created locally.
func (m *Manager) WatchSession(ctx context.Context, opts WatchOpts) (*StateSnapshot, error) {
	if opts.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if opts.WorkspaceID == "" {
		opts.WorkspaceID = m.client.ResolveWorkspaceID()
	}

	m.mu.RLock()
	if entry, ok := m.watchers[opts.SessionID]; ok {
		snap := entry.state.Snapshot()
		m.mu.RUnlock()
		return &snap, nil
	}
	m.mu.RUnlock()

	info, err := m.client.DescribeSession(opts.SessionID, opts.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("describe session: %w", err)
	}
	if info == nil {
		return nil, fmt.Errorf("session %s not found", opts.SessionID)
	}

	agentID := opts.AgentID
	if info.AgentID != "" {
		agentID = info.AgentID
	}
	if agentID == "" {
		return nil, fmt.Errorf("session %s has no agent id", opts.SessionID)
	}

	status := statusFromSessionInfo(info)
	now := time.Now()
	// Reuse persisted state when available so checkpoint, conclusions, and
	// artifacts survive re-watching; never overwrite history with a blank
	// state (that would break follow-up questions on finished sessions).
	var state *State
	if snap := LoadState(m.sessDir, opts.SessionID); snap != nil {
		state = stateFromSnapshot(snap)
		state.SetStatus(status)
		if state.GetAgentID() == "" {
			state.AgentID = agentID
		}
		if state.Mode == "" {
			state.Mode = opts.Mode
		}
	} else {
		state = &State{
			SessionID:   opts.SessionID,
			AgentID:     agentID,
			Status:      status,
			Mode:        opts.Mode,
			AutoConfirm: opts.AutoConfirm,
			WorkspaceID: opts.WorkspaceID,
			CreatedAt:   now,
			UpdatedAt:   now,
			changed:     make(chan struct{}),
		}
	}

	if status == StatusCompleted || status == StatusError || status == StatusCanceled {
		state.Persist(m.sessDir)
		snap := state.Snapshot()
		return &snap, nil
	}

	watcher := NewWatcher(state, m.client, m.sessDir)
	watchCtx, watchCancel := context.WithCancel(m.watchContext())
	entry := &watcherEntry{
		watcher: watcher,
		state:   state,
		cancel:  watchCancel,
	}

	m.mu.Lock()
	if existing, ok := m.watchers[opts.SessionID]; ok {
		snap := existing.state.Snapshot()
		m.mu.Unlock()
		watchCancel()
		return &snap, nil
	}
	m.watchers[opts.SessionID] = entry
	m.mu.Unlock()

	go watcher.Run(watchCtx)
	state.Persist(m.sessDir)

	snap := state.Snapshot()
	return &snap, nil
}

// GetStatus returns a snapshot of the current state for the given session.
func (m *Manager) GetStatus(sessionID string) (*StateSnapshot, error) {
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()

	if ok {
		snap := entry.state.Snapshot()
		return &snap, nil
	}

	// Fall back to persisted state on disk.
	st := LoadState(m.sessDir, sessionID)
	if st != nil {
		return st, nil
	}

	return nil, fmt.Errorf("session %s not found", sessionID)
}

// WaitForChange blocks until the session's checkpoint advances or status
// becomes non-running, or until timeout elapses.
// Returns (snapshot, changed, error). changed=false means timeout with no progress.
//
// Implementation uses the State.Changed() channel — the SSE watcher fires
// notify() on every meaningful state mutation, so this call wakes up
// immediately rather than polling.
func (m *Manager) WaitForChange(ctx context.Context, sessionID string, fromCheckpoint int, timeout time.Duration) (*StateSnapshot, bool, error) {
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()

	if !ok {
		// Session not in memory — return disk snapshot immediately (no live watcher to wait on).
		if st := LoadState(m.sessDir, sessionID); st != nil {
			changed := st.Checkpoint > fromCheckpoint || st.Status != StatusRunning
			return st, changed, nil
		}
		return nil, false, fmt.Errorf("session %s not found", sessionID)
	}

	deadline := time.Now().Add(timeout)

	for {
		// Grab channel reference BEFORE snapshot to avoid missing a notification
		// that fires between the snapshot read and the select below.
		ch := entry.state.Changed()

		snap, err := m.GetStatus(sessionID)
		if err != nil {
			return nil, false, err
		}
		if snap.Checkpoint > fromCheckpoint || snap.Status != StatusRunning {
			return snap, true, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return snap, false, nil
		}

		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(remaining):
			snap, _ = m.GetStatus(sessionID)
			if snap == nil {
				return nil, false, fmt.Errorf("session %s not found", sessionID)
			}
			return snap, false, nil
		case <-ch:
			// State changed — loop to re-evaluate condition.
		}
	}
}

// WaitForResult blocks until the session reaches a state that requires LLM
// attention: completed, error, canceled, or waiting for manual input.
// For auto_confirm=true sessions this typically fires only on completion/error,
// eliminating all intermediate status polling by the LLM.
// Returns (snapshot, reason, error) where reason is one of:
// "completed", "error", "canceled", "waiting_input", "timeout".
func (m *Manager) WaitForResult(ctx context.Context, sessionID string, timeout time.Duration) (*StateSnapshot, string, error) {
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()

	if !ok {
		if st := LoadState(m.sessDir, sessionID); st != nil {
			return st, resultReason(st), nil
		}
		return nil, "", fmt.Errorf("session %s not found", sessionID)
	}

	deadline := time.Now().Add(timeout)

	for {
		ch := entry.state.Changed()

		snap, err := m.GetStatus(sessionID)
		if err != nil {
			return nil, "", err
		}
		if reason := resultReason(snap); reason != "" {
			return snap, reason, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return snap, "timeout", nil
		}

		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(remaining):
			snap, _ = m.GetStatus(sessionID)
			if snap == nil {
				return nil, "timeout", nil
			}
			return snap, "timeout", nil
		case <-ch:
			// State changed — loop to re-evaluate condition.
		}
	}
}

// resultReason returns a non-empty string when the session needs LLM attention.
func resultReason(snap *StateSnapshot) string {
	switch snap.Status {
	case StatusCompleted:
		return "completed"
	case StatusError:
		return "error"
	case StatusCanceled:
		return "canceled"
	case StatusWaitingInput:
		if snap.WaitingFor != "" {
			return "waiting_input"
		}
	}
	return ""
}

// SendMessage sends a user message to an active session (for manual
// confirmation or free-form input).
func (m *Manager) SendMessage(sessionID, message string) error {
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()

	if ok {
		return entry.watcher.SendMessage(message)
	}

	// No active watcher: the session may have completed (watcher exited) or
	// the server may have restarted. The remote Data Agent session itself is
	// multi-turn, so revive it from persisted state to support follow-up
	// questions on finished sessions.
	return m.reviveAndSend(sessionID, message)
}

// reviveAndSend reloads a persisted session, restarts its SSE watcher, and
// sends the follow-up message within the restored conversation context.
func (m *Manager) reviveAndSend(sessionID, message string) error {
	snap := LoadState(m.sessDir, sessionID)
	if snap == nil {
		return fmt.Errorf("session %s not found or not active", sessionID)
	}
	if snap.AgentID == "" {
		return fmt.Errorf("session %s has no agent id; cannot revive", sessionID)
	}

	state := stateFromSnapshot(snap)
	state.SetStatus(StatusRunning)

	watcher := NewWatcher(state, m.client, m.sessDir)
	watchCtx, watchCancel := context.WithCancel(m.watchContext())
	entry := &watcherEntry{watcher: watcher, state: state, cancel: watchCancel}

	m.mu.Lock()
	if existing, ok := m.watchers[sessionID]; ok {
		// Lost the race against a concurrent revive/watch; reuse the winner.
		m.mu.Unlock()
		watchCancel()
		return existing.watcher.SendMessage(message)
	}
	m.watchers[sessionID] = entry
	m.mu.Unlock()

	// Send first so a rejected message doesn't leave a zombie watcher.
	if err := watcher.SendMessage(message); err != nil {
		m.mu.Lock()
		delete(m.watchers, sessionID)
		m.mu.Unlock()
		watchCancel()
		return err
	}

	go watcher.Run(watchCtx)
	log.Printf("revived session %s for follow-up (checkpoint=%d)", sessionID, snap.Checkpoint)
	return nil
}

// GetResult returns the analysis result for a session. Primarily useful once
// the session has completed, but works at any point.
func (m *Manager) GetResult(sessionID string) (*StateSnapshot, error) {
	return m.GetStatus(sessionID)
}

// ListSessions returns snapshots of currently active (in-memory) sessions.
func (m *Manager) ListSessions() []*StateSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*StateSnapshot, 0, len(m.watchers))
	for _, entry := range m.watchers {
		snap := entry.state.Snapshot()
		result = append(result, &snap)
	}
	return result
}

// ListAllSessions returns snapshots of all sessions: active watchers merged
// with persisted sessions on disk. Active sessions override disk entries with
// the same session ID. Disk sessions are sorted by updated_at descending.
func (m *Manager) ListAllSessions() []*StateSnapshot {
	m.mu.RLock()
	active := make(map[string]*StateSnapshot, len(m.watchers))
	for id, entry := range m.watchers {
		snap := entry.state.Snapshot()
		active[id] = &snap
	}
	m.mu.RUnlock()

	// Scan persisted sessions from disk.
	entries, err := os.ReadDir(m.sessDir)
	if err != nil {
		// Disk unavailable — return active only.
		result := make([]*StateSnapshot, 0, len(active))
		for _, snap := range active {
			result = append(result, snap)
		}
		return result
	}

	// Merge: active sessions take precedence over disk entries.
	seen := make(map[string]struct{}, len(active))
	for id := range active {
		seen[id] = struct{}{}
	}

	var diskSessions []*StateSnapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sid := e.Name()
		if _, ok := seen[sid]; ok {
			continue
		}
		snap := LoadState(m.sessDir, sid)
		if snap != nil {
			diskSessions = append(diskSessions, snap)
		}
	}

	// Sort disk sessions by updated_at descending (most recent first).
	sort.Slice(diskSessions, func(i, j int) bool {
		return diskSessions[i].UpdatedAt.After(diskSessions[j].UpdatedAt)
	})

	// Active sessions first, then disk sessions.
	result := make([]*StateSnapshot, 0, len(active)+len(diskSessions))
	for _, snap := range active {
		result = append(result, snap)
	}
	result = append(result, diskSessions...)
	return result
}

// StopSession stops watching a session and removes it from the active map.
func (m *Manager) StopSession(sessionID string) error {
	m.mu.Lock()
	entry, ok := m.watchers[sessionID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %s not found", sessionID)
	}
	delete(m.watchers, sessionID)
	m.mu.Unlock()

	entry.watcher.Stop()
	entry.cancel()
	return nil
}

// waitForRunning polls DescribeSession until the session status is RUNNING or
// the timeout is reached.
func (m *Manager) waitForRunning(ctx context.Context, sessionID, workspaceID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		info, err := m.client.DescribeSession(sessionID, workspaceID)
		if err != nil {
			log.Printf("[DEBUG] DescribeSession(%s) error: %v", sessionID, err)
		} else {
			log.Printf("[DEBUG] DescribeSession(%s) => SessionStatus=%q AgentStatus=%q AgentID=%q", sessionID, info.SessionStatus, info.AgentStatus, info.AgentID)
		}
		if err == nil && isSessionReady(info) {
			return nil
		}

		if time.Now().After(deadline) {
			status := "unknown"
			if info != nil {
				status = info.SessionStatus + "/" + info.AgentStatus
			}
			return fmt.Errorf("timeout waiting for RUNNING (last status: %s)", status)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Continue polling.
		}
	}
}

// isSessionReady returns true if the session is usable. Matches Python's
// SessionInfo.is_running(): RUNNING, IDLE, or WAIT_INPUT session status,
// or agentStatus=running (server may report sessionStatus=init while the
// agent is already usable).
func isSessionReady(info *dataagent.SessionInfo) bool {
	ss := strings.ToUpper(info.SessionStatus)
	as := strings.ToLower(info.AgentStatus)
	return ss == "RUNNING" || ss == "IDLE" || ss == "WAIT_INPUT" || as == "running"
}

func statusFromSessionInfo(info *dataagent.SessionInfo) Status {
	ss := strings.ToUpper(info.SessionStatus)
	as := strings.ToLower(info.AgentStatus)

	switch ss {
	case "RUNNING":
		return StatusRunning
	case "WAIT_INPUT", "WAITING_INPUT":
		return StatusWaitingInput
	case "IDLE", "STOPPED", "FINISHED", "COMPLETED":
		return StatusCompleted
	case "FAILED", "ERROR":
		return StatusError
	case "CANCELED", "CANCELLED":
		return StatusCanceled
	}

	switch as {
	case "running":
		return StatusRunning
	case "failed", "error":
		return StatusError
	}
	return StatusRunning
}

// IncrPollSeq atomically increments and returns the poll sequence number for
// a session. Returns 0 if the session is not active.
func (m *Manager) IncrPollSeq(sessionID string) int {
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	return entry.state.IncrPollSeq()
}

// SessionDir returns the directory path for the given session.
func (m *Manager) SessionDir(sessionID string) string {
	return m.sessDir + "/" + sessionID
}

// removeEntry removes a session from the watchers map. Used by housekeeping.
func (m *Manager) removeEntry(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, ok := m.watchers[sessionID]; ok {
		entry.cancel()
		delete(m.watchers, sessionID)
		log.Printf("[housekeeping] removed session %s", sessionID)
	}
}
