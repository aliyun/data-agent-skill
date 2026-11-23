package session

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
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
	WorkMode      string // ANALYSIS or CHAT; passed only with the initial query
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
	store    *sessionStore
	baseCtx  context.Context // process-lifetime context for watcher goroutines

	lifeMu    sync.Mutex
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	work      sync.WaitGroup
	closeOnce sync.Once
	closeErr  error

	operationsMu sync.Mutex
	operations   map[string]*sessionOperation
}

type sessionOperation struct {
	mu   sync.Mutex
	refs int // holders and waiters, guarded by operationsMu
}

// watcherEntry bundles a Watcher with its cancellation and completion signals.
type watcherEntry struct {
	watcher *Watcher
	state   *State
	cancel  context.CancelFunc
	done    chan struct{} // nil when Run has never been launched
}

func (e *watcherEntry) cancelAndWait() {
	if e.cancel != nil {
		e.cancel()
	}
	if e.done != nil {
		<-e.done
	}
}

// lockSessionOperation pins the lock before waiting, including across watcher replacement.
func (m *Manager) lockSessionOperation(sessionID string) func() {
	if ValidateSessionID(sessionID) != nil {
		return nil
	}
	m.operationsMu.Lock()
	if m.operations == nil {
		m.operations = make(map[string]*sessionOperation)
	}
	operation := m.operations[sessionID]
	if operation == nil {
		operation = &sessionOperation{}
		m.operations[sessionID] = operation
	}
	operation.refs++
	m.operationsMu.Unlock()
	operation.mu.Lock()
	return func() {
		m.operationsMu.Lock()
		operation.mu.Unlock()
		operation.refs--
		if operation.refs == 0 {
			delete(m.operations, sessionID)
		}
		m.operationsMu.Unlock()
	}
}

// logToken excludes free-form text and URLs; accepted ASCII tokens are at most 128 runes.
func logToken(value string) string {
	for _, c := range value {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return "[redacted]"
		}
	}
	return value[:min(len(value), 128)]
}

// The session operation lock must be held before publishing a watcher.
func (m *Manager) startWatcher(watcher *Watcher) {
	id := watcher.state.GetSessionID()
	if ValidateSessionID(id) != nil {
		return
	}
	m.lifeMu.Lock()
	defer m.lifeMu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithCancel(m.watchContext())
	if ctx.Err() != nil {
		cancel()
		return
	}
	stop := context.AfterFunc(m.ctx, cancel)
	entry := &watcherEntry{watcher: watcher, state: watcher.state, cancel: cancel, done: make(chan struct{})}
	m.work.Add(1)
	m.mu.Lock()
	m.watchers[id] = entry
	m.mu.Unlock()
	go func() {
		defer m.work.Done()
		defer close(entry.done)
		defer stop()
		defer cancel()
		watcher.Run(ctx)
	}()
}

// NewManager eagerly opens its storage root. The caller owns Close.
func NewManager(client *dataagent.Client, sessDir string) (*Manager, error) {
	store, err := openSessionStore(sessDir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{watchers: make(map[string]*watcherEntry), client: client, store: store, ctx: ctx, cancel: cancel, baseCtx: ctx}, nil
}

// begin registers work before shutdown can close the root and links request
// cancellation to the manager lifetime without losing request context values.
func (m *Manager) begin(ctx context.Context) (context.Context, func(), error) {
	m.lifeMu.Lock()
	defer m.lifeMu.Unlock()
	if m.closed || m.ctx.Err() != nil {
		return nil, nil, ErrManagerClosed
	}
	m.work.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	return ctx, func() { stop(); cancel(); m.work.Done() }, nil
}

// Close cancels and joins all local work before releasing the root. It does
// not claim to cancel remote execution or replay uncertain message delivery.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.lifeMu.Lock()
		m.closed = true
		m.cancel()
		m.lifeMu.Unlock()
		m.work.Wait()
		m.mu.Lock()
		entries := m.watchers
		m.watchers = make(map[string]*watcherEntry)
		m.mu.Unlock()
		for _, entry := range entries {
			entry.cancelAndWait()
		}
		m.closeErr = m.store.root.Close()
	})
	return m.closeErr
}

// watchContext returns the process-lifetime context that watcher goroutines
// must be derived from. Request-scoped contexts are unsuitable: the HTTP
// transports cancel them right after the tool response is written.
func (m *Manager) watchContext() context.Context {
	m.mu.RLock()
	defer m.mu.RUnlock()
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
	// Keep process context values for future watchers, but never adopt a tool context.
	m.mu.Lock()
	m.baseCtx = ctx
	m.mu.Unlock()
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return
	}
	defer done()
	entries, err := readEntries(m.store.root)
	if err != nil {
		return
	}
	// Serial restoration is bounded (one upstream operation at a time).
	restored := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}
		if e.IsDir() && ValidateSessionID(e.Name()) == nil && m.restoreSessionContext(ctx, e.Name()) {
			restored++
		}
	}
	if restored > 0 {
		log.Printf("restored %d active session(s)", restored)
	}
}

func (m *Manager) restoreSession(sessionID string) bool {
	if ValidateSessionID(sessionID) != nil {
		return false
	}
	return m.restoreSessionContext(m.watchContext(), sessionID)
}

func (m *Manager) restoreSessionContext(ctx context.Context, sessionID string) bool {
	if ValidateSessionID(sessionID) != nil {
		return false
	}
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return false
	}
	defer done()
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()
	if ctx.Err() != nil {
		return false
	}

	m.mu.RLock()
	_, exists := m.watchers[sessionID]
	m.mu.RUnlock()
	if exists {
		return false
	}

	snap := loadState(m.store, sessionID)
	if snap == nil || snap.AgentID == "" {
		return false
	}
	state := stateFromSnapshot(snap)
	if state.GetStatus() != StatusRunning && state.GetStatus() != StatusWaitingInput {
		return false
	}

	// IDLE can also mean an unanswered question.
	info, err := m.client.DescribeSession(ctx, sessionID, snap.WorkspaceID)
	if err != nil || info == nil {
		return false
	}
	serverStatus := strings.ToUpper(info.SessionStatus)
	if serverStatus == "STOPPED" || serverStatus == "FAILED" {
		return false
	}

	m.startWatcher(newWatcher(state, m.client, m.store))
	log.Printf("restored session %q (status=%q, checkpoint=%d)", logToken(sessionID), logToken(string(state.GetStatus())), snap.Checkpoint)
	return true
}

// CreateSession creates a new Data Agent session, waits for it to become
// RUNNING, sends the initial query, and starts a background watcher goroutine.
func (m *Manager) CreateSession(ctx context.Context, opts CreateOpts) (*State, error) {
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	// Fill in default workspace if not specified.
	if opts.WorkspaceID == "" {
		opts.WorkspaceID = m.client.ResolveWorkspaceID(ctx)
	}

	// 1. Create session on server.
	info, err := m.client.CreateSession(ctx, dataagent.CreateSessionOpts{
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

	if info == nil || ValidateSessionID(info.SessionID) != nil {
		return nil, fmt.Errorf("%w: upstream creation returned an unusable ID; remote creation may have occurred, no further action was taken", ErrInvalidSessionID)
	}
	sessionID := info.SessionID
	agentID := info.AgentID
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Refuse existing or corrupt local state before any follow-up cloud calls.
	if _, err := m.store.load(sessionID); !errors.Is(err, fs.ErrNotExist) {
		return nil, ErrSessionUnavailable
	}

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

	// 4. Persist intent before sending. A storage failure must never dispatch
	// a message, and cancellation/unknown delivery must remain recoverable.
	now := time.Now()
	state := &State{
		SessionID: sessionID, AgentID: agentID, Status: StatusRunning,
		Mode: opts.Mode, AutoConfirm: opts.AutoConfirm, WorkspaceID: opts.WorkspaceID,
		CreatedAt: now, UpdatedAt: now, changed: make(chan struct{}),
	}
	state.SetSendStatus("", SendSending, false)
	if err := state.persist(m.store); err != nil {
		return nil, err
	}
	sendErr := m.client.SendMessage(ctx, dataagent.SendMessageOpts{
		AgentID: agentID, SessionID: sessionID, Message: opts.Query, DataSource: ds,
		WorkspaceID: opts.WorkspaceID, Mode: opts.Mode, PlanMode: opts.PlanMode,
		WorkMode: opts.WorkMode,
	})
	delivery := SendAcknowledged
	if sendErr != nil {
		delivery = SendUnknown
		if dataagent.IsDefiniteRejection(sendErr) {
			delivery = SendFailed
		}
	}
	state.SetSendStatus("", delivery, false)
	persistErr := state.persist(m.store)
	if delivery != SendFailed {
		m.startWatcher(newWatcher(state, m.client, m.store))
	}
	if sendErr != nil {
		return nil, fmt.Errorf("send initial query: %w", sendErr)
	}
	if persistErr != nil {
		return nil, fmt.Errorf("initial message accepted but acknowledgement could not be persisted; do not resend: %w", persistErr)
	}
	return state, nil
}

// WatchSession attaches the local MCP server to an existing remote session and
// starts the same background SSE watcher used for sessions created locally.
func (m *Manager) WatchSession(ctx context.Context, opts WatchOpts) (*StateSnapshot, error) {
	if err := ValidateSessionID(opts.SessionID); err != nil {
		return nil, err
	}
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	unlock := m.lockSessionOperation(opts.SessionID)
	defer unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	saved, loadErr := m.store.load(opts.SessionID)
	if loadErr != nil && !errors.Is(loadErr, fs.ErrNotExist) {
		return nil, ErrSessionUnavailable
	}

	if opts.WorkspaceID == "" {
		opts.WorkspaceID = m.client.ResolveWorkspaceID(ctx)
	}

	m.mu.RLock()
	if entry, ok := m.watchers[opts.SessionID]; ok {
		snap := entry.state.Snapshot()
		m.mu.RUnlock()
		return &snap, nil
	}
	m.mu.RUnlock()

	info, err := m.client.DescribeSession(ctx, opts.SessionID, opts.WorkspaceID)
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
	if saved != nil {
		state = stateFromSnapshot(saved)
		if state.GetStatus() == StatusWaitingInput && state.GetWaitingFor() != "" && (strings.EqualFold(info.SessionStatus, "IDLE") || status == StatusRunning || status == StatusWaitingInput) {
			// Coarse remote status cannot resolve a pending or uncertain local reply.
			status = StatusWaitingInput
		}
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
		if err := state.persist(m.store); err != nil {
			return nil, err
		}
		snap := state.Snapshot()
		return &snap, nil
	}

	if err := state.persist(m.store); err != nil {
		return nil, err
	}
	m.startWatcher(newWatcher(state, m.client, m.store))

	snap := state.Snapshot()
	return &snap, nil
}

// GetStatus returns a snapshot of the current state for the given session.
func (m *Manager) GetStatus(sessionID string) (*StateSnapshot, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	_, done, err := m.begin(context.Background())
	if err != nil {
		return nil, err
	}
	defer done()
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()

	if ok {
		snap := entry.state.Snapshot()
		return &snap, nil
	}

	// Fall back to persisted state on disk.
	st := loadState(m.store, sessionID)
	if st != nil {
		return st, nil
	}

	return nil, ErrSessionUnavailable
}

// WaitForChange blocks until the session's checkpoint advances or status
// becomes non-running, or until timeout elapses.
// Returns (snapshot, changed, error). changed=false means timeout with no progress.
//
// Implementation uses the State.Changed() channel — the SSE watcher fires
// notify() on every meaningful state mutation, so this call wakes up
// immediately rather than polling.
func (m *Manager) WaitForChange(ctx context.Context, sessionID string, fromCheckpoint int, timeout time.Duration) (*StateSnapshot, bool, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, false, err
	}
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer done()
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()

	if !ok {
		// Session not in memory — return disk snapshot immediately (no live watcher to wait on).
		if st := loadState(m.store, sessionID); st != nil {
			changed := st.Checkpoint > fromCheckpoint || st.Status != StatusRunning
			return st, changed, nil
		}
		return nil, false, ErrSessionUnavailable
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
			// Transport canceled the request (client timeout/disconnect).
			// Degrade to the last known snapshot instead of surfacing a
			// "context canceled" tool error: if the response can still be
			// written it carries usable progress.
			if snap, err := m.GetStatus(sessionID); err == nil {
				return snap, false, nil
			}
			return nil, false, ctx.Err()
		case <-time.After(remaining):
			snap, _ = m.GetStatus(sessionID)
			if snap == nil {
				return nil, false, ErrSessionUnavailable
			}
			return snap, false, nil
		case <-ch:
			// State changed — loop to re-evaluate condition.
		}
	}
}

// WaitForResult blocks until the session reaches a state that requires LLM
// attention: completed, error, canceled, or waiting for manual input.
// Returns (snapshot, reason, error) where reason is one of:
// "completed", "error", "canceled", "waiting_input", "timeout",
// "client_canceled" (transport canceled the request mid-wait).
func (m *Manager) WaitForResult(ctx context.Context, sessionID string, timeout time.Duration) (*StateSnapshot, string, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, "", err
	}
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer done()
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()

	if !ok {
		if st := loadState(m.store, sessionID); st != nil {
			return st, resultReason(st), nil
		}
		return nil, "", ErrSessionUnavailable
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
			// Transport canceled the request (client timeout/disconnect).
			// Return the last known snapshot with a distinct reason instead
			// of a "context canceled" tool error, so callers that receive
			// the response still get usable progress.
			if snap, err := m.GetStatus(sessionID); err == nil {
				return snap, "client_canceled", nil
			}
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
			request, ok := snap.Requests[snap.PendingAsk]
			if ok && request.Kind == "ask_report_render" && !request.Ready && request.Status == SendPending {
				return ""
			}
			if snap.AutoConfirm && snap.PendingAsk != "" && ok && request.Stable {
				switch request.Kind {
				case "ask_plan", "ask_sql":
					// RegisterAsk notifies waiters before auto-confirmation starts.
					if request.Status == SendPending || request.Status == SendSending {
						return ""
					}
				}
			}
			return "waiting_input"
		}
	}
	return ""
}

// SendMessage sends a user message to an active session (for manual
// confirmation or free-form input).
func (m *Manager) SendMessage(ctx context.Context, sessionID, message string) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	before, _ := m.GetStatus(sessionID)
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	current, _ := m.GetStatus(sessionID)
	if before != nil && current != nil && (before.PendingAsk != current.PendingAsk || before.SendGeneration != current.SendGeneration || before.MessageStatus != current.MessageStatus || before.Requests[before.PendingAsk].Status != current.Requests[current.PendingAsk].Status) {
		return fmt.Errorf("session changed while waiting to send; inspect the current pending request")
	}

	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()

	if ok {
		switch entry.state.GetStatus() {
		case StatusCompleted, StatusError, StatusCanceled:
		default:
			if err := entry.watcher.SendMessage(ctx, message); err != errWatcherExited {
				return err
			}
		}
		// Join before loading the final snapshot, including when Run finished during SendMessage.
		entry.cancelAndWait()
		m.mu.Lock()
		delete(m.watchers, sessionID)
		m.mu.Unlock()
	}

	// No live watcher: the session may have completed (watcher exited) or
	// the server may have restarted. The remote Data Agent session itself is
	// multi-turn, so revive it from persisted state to support follow-up
	// questions on finished sessions.
	return m.reviveAndSend(ctx, sessionID, message)
}

// The session operation lock must be held while reviving and sending.
func (m *Manager) reviveAndSend(ctx context.Context, sessionID, message string) error {
	snap := loadState(m.store, sessionID)
	if snap == nil {
		return ErrSessionUnavailable
	}
	if snap.AgentID == "" {
		return fmt.Errorf("session %s has no agent id; cannot revive", sessionID)
	}

	state := stateFromSnapshot(snap)
	watcher := newWatcher(state, m.client, m.store)

	// Rejected sends stay unpublished; uncertain delivery still needs an observer.
	if err := watcher.SendMessage(ctx, message); err != nil {
		after := state.Snapshot()
		if after.MessageStatus == SendUnknown || after.Requests[after.PendingAsk].Status == SendUnknown || after.Status == StatusRunning {
			m.startWatcher(watcher)
		}
		return err
	}

	m.startWatcher(watcher)
	log.Printf("revived session %q for follow-up (checkpoint=%d)", logToken(sessionID), snap.Checkpoint)
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

// WatcherStats counts active watchers by session status for the perf log.
func (m *Manager) WatcherStats() map[string]int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]int{"total": len(m.watchers)}
	for _, entry := range m.watchers {
		out[string(entry.state.GetStatus())]++
	}
	m.operationsMu.Lock()
	out["locked_operations"] = len(m.operations)
	m.operationsMu.Unlock()
	return out
}

// ListAllSessions returns snapshots of all sessions: active watchers merged
// with persisted sessions on disk. Active sessions override disk entries with
// the same session ID. Disk sessions are sorted by updated_at descending.
func (m *Manager) ListAllSessions() []*StateSnapshot {
	_, done, err := m.begin(context.Background())
	if err != nil {
		return nil
	}
	defer done()
	m.mu.RLock()
	active := make(map[string]*StateSnapshot, len(m.watchers))
	for id, entry := range m.watchers {
		snap := entry.state.Snapshot()
		active[id] = &snap
	}
	m.mu.RUnlock()

	// Scan persisted sessions from disk.
	entries, err := readEntries(m.store.root)
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
		snap := loadState(m.store, sid)
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
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	_, done, err := m.begin(context.Background())
	if err != nil {
		return err
	}
	defer done()
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()

	m.mu.Lock()
	entry, ok := m.watchers[sessionID]
	if !ok {
		m.mu.Unlock()
		return ErrSessionUnavailable
	}
	delete(m.watchers, sessionID)
	m.mu.Unlock()

	entry.watcher.Stop()
	entry.cancelAndWait()
	return nil
}

// waitForRunning polls DescribeSession until the session status is RUNNING or
// the timeout is reached.
func (m *Manager) waitForRunning(ctx context.Context, sessionID, workspaceID string, timeout time.Duration) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		info, err := m.client.DescribeSession(ctx, sessionID, workspaceID)
		if err != nil {
			log.Printf("DescribeSession(%q) failed", logToken(sessionID))
		} else if info != nil {
			log.Printf("DescribeSession(%q) SessionStatus=%q AgentStatus=%q", logToken(sessionID), logToken(info.SessionStatus), logToken(info.AgentStatus))
		}
		if err == nil && info != nil && isSessionReady(info) {
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
	if ValidateSessionID(sessionID) != nil {
		return 0
	}
	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	return entry.state.IncrPollSeq()
}

// ReadImages returns a single rooted enumeration/read, with no naked paths.
func (m *Manager) ReadImages(sessionID string) ([]Image, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	_, done, err := m.begin(context.Background())
	if err != nil {
		return nil, err
	}
	defer done()
	return m.store.readImages(sessionID)
}

// removeEntry removes a stale session from the watchers map. Used by housekeeping.
func (m *Manager) removeEntry(sessionID string) {
	if ValidateSessionID(sessionID) != nil {
		return
	}
	_, done, err := m.begin(context.Background())
	if err != nil {
		return
	}
	defer done()
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()

	m.mu.RLock()
	entry, ok := m.watchers[sessionID]
	m.mu.RUnlock()
	if !ok {
		return
	}

	// Housekeeping inspected an earlier map snapshot. A send/watch may have
	// revived this session while it waited for the operation lock.
	snap := entry.state.Snapshot()
	if (snap.Status != StatusCompleted && snap.Status != StatusError) || time.Since(snap.UpdatedAt) <= staleTimeout {
		return
	}

	entry.cancelAndWait()
	m.mu.Lock()
	delete(m.watchers, sessionID)
	m.mu.Unlock()
	log.Printf("[housekeeping] removed session %q", logToken(sessionID))
}
