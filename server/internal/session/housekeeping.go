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
	info, err := m.client.DescribeSession(sessionID, entry.state.GetWorkspaceID())
	if err != nil {
		log.Printf("[housekeeping] DescribeSession(%s) error: %v", sessionID, err)
		return
	}

	serverStatus := info.SessionStatus
	if serverStatus == "" {
		serverStatus = info.AgentStatus
	}

	switch serverStatus {
	case "IDLE", "STOPPED", "FINISHED", "COMPLETED":
		localStatus := entry.state.GetStatus()
		if localStatus == StatusRunning || localStatus == StatusWaitingInput {
			log.Printf("[housekeeping] session %s: server=%s, local=%s -> marking completed",
				sessionID, serverStatus, localStatus)
			entry.state.SetCompleted()
			entry.state.Persist(m.sessDir)
		}
	}
}
