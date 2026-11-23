package session

import (
	"context"
	"log"
	"time"
)

const (
	housekeepingInterval = 5 * time.Minute
	staleTimeout         = 30 * time.Minute
)

// RunHousekeeping starts a periodic cleanup loop that runs every 5 minutes.
// It removes completed/errored sessions that have been idle for more than 30
// minutes, and reconciles local state with the server-side session status.
//
// It blocks until ctx is canceled.
func (m *Manager) RunHousekeeping(ctx context.Context) {
	ctx, done, err := m.begin(ctx)
	if err != nil {
		return
	}
	defer done()
	ticker := time.NewTicker(housekeepingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.doHousekeeping()
		}
	}
}

// doHousekeeping performs a single housekeeping pass.
func (m *Manager) doHousekeeping() {
	m.mu.RLock()
	entries := make(map[string]*watcherEntry, len(m.watchers))
	for id, e := range m.watchers {
		entries[id] = e
	}
	m.mu.RUnlock()

	now := time.Now()

	for sessionID, entry := range entries {
		status := entry.state.GetStatus()
		updatedAt := entry.state.GetUpdatedAt()
		idle := now.Sub(updatedAt)

		// Remove completed or errored sessions that have been idle.
		if (status == StatusCompleted || status == StatusError) && idle > staleTimeout {
			m.removeEntry(sessionID)
			continue
		}

		// For sessions that appear to still be running, check the server-side
		// status to detect sessions that finished without the SSE stream
		// delivering a terminal event (e.g. network partition).
		if status == StatusRunning || status == StatusWaitingInput {
			m.reconcileWithServer(sessionID, entry)
		}
	}
}

// reconcileWithServer checks the server-side session status and updates the
// local state if the server reports the session as finished.
func (m *Manager) reconcileWithServer(sessionID string, entry *watcherEntry) {
	if ValidateSessionID(sessionID) != nil || entry.state.GetSessionID() != sessionID {
		return
	}
	ctx, done, err := m.begin(m.watchContext())
	if err != nil {
		return
	}
	defer done()
	before := entry.state.Snapshot()
	info, err := m.client.DescribeSession(ctx, sessionID, before.WorkspaceID)
	if err != nil {
		log.Printf("[housekeeping] DescribeSession(%q) failed", logToken(sessionID))
		return
	}
	if info == nil {
		return
	}
	serverStatus := info.SessionStatus
	if serverStatus == "" {
		serverStatus = info.AgentStatus
	}
	// IDLE also means waiting for confirmation; only explicit completion is terminal.
	if serverStatus != "STOPPED" && serverStatus != "FINISHED" && serverStatus != "COMPLETED" {
		return
	}
	unlock := m.lockSessionOperation(sessionID)
	defer unlock()
	m.mu.RLock()
	current := m.watchers[sessionID]
	m.mu.RUnlock()
	if current != entry {
		return
	}
	entry.watcher.opMu.Lock()
	snap := entry.state.Snapshot()
	if !snap.UpdatedAt.Equal(before.UpdatedAt) || (snap.Status != StatusRunning && snap.Status != StatusWaitingInput) {
		entry.watcher.opMu.Unlock()
		return
	}
	if snap.AwaitingTurn || snap.Requests[snap.PendingAsk].Kind == "ask_report_render" {
		entry.watcher.opMu.Unlock()
		return
	}
	entry.state.SetCompleted()
	entry.state.persist(m.store)
	entry.watcher.exited = true
	entry.cancel()
	entry.watcher.opMu.Unlock()
	if entry.done != nil {
		<-entry.done
	}
}
