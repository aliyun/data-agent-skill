package session

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The anti-loop counter must reset when the session makes progress: only
// repeated polls with no checkpoint advance should escalate to a warning.
func TestIncrPollSeqResetsOnCheckpointAdvance(t *testing.T) {
	s := &State{SessionID: "s1", Status: StatusRunning}

	if got := s.IncrPollSeq(); got != 1 {
		t.Fatalf("first poll = %d, want 1", got)
	}
	if got := s.IncrPollSeq(); got != 2 {
		t.Fatalf("second poll = %d, want 2", got)
	}

	s.SetCheckpoint(10)
	if got := s.IncrPollSeq(); got != 1 {
		t.Errorf("poll after progress = %d, want reset to 1", got)
	}

	if got := s.IncrPollSeq(); got != 2 {
		t.Errorf("stalled poll = %d, want 2", got)
	}
}

// newWaitTestManager wires a running in-memory session into a Manager without
// touching the Data Agent API.
func newWaitTestManager(t *testing.T) (*Manager, *State) {
	t.Helper()
	state := &State{
		SessionID: "s1",
		Status:    StatusRunning,
		changed:   make(chan struct{}),
	}
	m := NewManager(nil, t.TempDir())
	m.watchers["s1"] = &watcherEntry{state: state, cancel: func() {}}
	return m, state
}

// A canceled transport context must degrade to the last snapshot instead of
// bubbling up "context canceled" as a tool error.
func TestWaitForResultClientCanceledReturnsSnapshot(t *testing.T) {
	m, _ := newWaitTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	snap, reason, err := m.WaitForResult(ctx, "s1", 5*time.Second)
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}
	if reason != "client_canceled" {
		t.Errorf("reason = %q, want client_canceled", reason)
	}
	if snap == nil || snap.SessionID != "s1" {
		t.Errorf("snapshot missing or wrong session: %+v", snap)
	}
}

func TestWaitForChangeClientCanceledReturnsSnapshot(t *testing.T) {
	m, _ := newWaitTestManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	snap, changed, err := m.WaitForChange(ctx, "s1", 0, 5*time.Second)
	if err != nil {
		t.Fatalf("expected graceful degradation, got error: %v", err)
	}
	if changed {
		t.Error("changed = true on cancellation, want false")
	}
	if snap == nil || snap.SessionID != "s1" {
		t.Errorf("snapshot missing or wrong session: %+v", snap)
	}
}

// A follow-up sent to a finished session whose stale entry is still in the
// watchers map must NOT go through the dead watcher (its Run loop exited on
// completion, so nobody would listen for the follow-up's SSE events). The
// manager must drop the stale entry and take the revive path instead.
func TestSendMessageOnFinishedSessionTakesRevivePath(t *testing.T) {
	state := &State{
		SessionID: "s1",
		AgentID:   "a1",
		Status:    StatusCompleted, // terminal → watcher goroutine has exited
		changed:   make(chan struct{}),
	}
	m := NewManager(nil, t.TempDir())
	m.watchers["s1"] = &watcherEntry{
		watcher: NewWatcher(state, nil, m.sessDir),
		state:   state,
		cancel:  func() {},
	}

	// No persisted state on disk → the revive path fails with "not found",
	// which proves SendMessage did not use the dead watcher (that would
	// have attempted a real API call instead).
	err := m.SendMessage("s1", "follow-up")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected revive-path error, got: %v", err)
	}

	m.mu.RLock()
	_, still := m.watchers["s1"]
	m.mu.RUnlock()
	if still {
		t.Fatal("stale terminal entry must be removed from the watchers map")
	}
}

// Re-emitted mission objectives replace the earlier conclusion copy.
func TestUpsertConclusionReplacesByKey(t *testing.T) {
	s := &State{SessionID: "s1", changed: make(chan struct{})}
	s.UpsertConclusion("k:0:1", "v1")
	s.UpsertConclusion("", "standalone")
	s.UpsertConclusion("k:0:1", "v2")
	if len(s.Conclusions) != 2 || s.Conclusions[0] != "v2" || s.Conclusions[1] != "standalone" {
		t.Fatalf("conclusions = %v", s.Conclusions)
	}
}
