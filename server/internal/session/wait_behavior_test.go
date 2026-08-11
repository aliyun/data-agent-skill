package session

import (
	"context"
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
