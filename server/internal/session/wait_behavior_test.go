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

func TestResultReasonAutoConfirm(t *testing.T) {
	for _, tc := range []struct {
		name        string
		kind        string
		sendStatus  SendStatus
		autoConfirm bool
		stable      bool
		want        string
	}{
		{"plan pending", "ask_plan", SendPending, true, true, ""},
		{"plan sending", "ask_plan", SendSending, true, true, ""},
		{"sql pending", "ask_sql", SendPending, true, true, ""},
		{"sql sending", "ask_sql", SendSending, true, true, ""},
		{"report pending", "ask_report_render", SendPending, true, true, "waiting_input"},
		{"report sending", "ask_report_render", SendSending, true, true, "waiting_input"},
		{"human pending", "ask_human", SendPending, true, true, "waiting_input"},
		{"human sending", "ask_human", SendSending, true, true, "waiting_input"},
		{"other kind", "other", SendPending, true, true, "waiting_input"},
		{"empty kind", "", SendPending, true, true, "waiting_input"},
		{"manual pending", "ask_plan", SendPending, false, true, "waiting_input"},
		{"manual sending", "ask_plan", SendSending, false, true, "waiting_input"},
		{"unstable pending", "ask_plan", SendPending, true, false, "waiting_input"},
		{"unstable sending", "ask_plan", SendSending, true, false, "waiting_input"},
		{"failed", "ask_plan", SendFailed, true, true, "waiting_input"},
		{"unknown", "ask_plan", SendUnknown, true, true, "waiting_input"},
		{"acknowledged but still waiting", "ask_plan", SendAcknowledged, true, true, "waiting_input"},
		{"empty send status", "ask_plan", "", true, true, "waiting_input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := StateSnapshot{
				Status: StatusWaitingInput, WaitingFor: "ask_plan",
				AutoConfirm: tc.autoConfirm, PendingAsk: "ask",
				Requests: map[string]ConfirmationRequest{
					"ask": {Key: "ask", Kind: tc.kind, Status: tc.sendStatus, Stable: tc.stable, Ready: true},
				},
			}
			if got := resultReason(&snap); got != tc.want {
				t.Fatalf("resultReason = %q, want %q", got, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		modify func(*StateSnapshot)
		want   string
	}{
		{"no ledger", func(s *StateSnapshot) { s.Requests = nil }, "waiting_input"},
		{"no pending ask", func(s *StateSnapshot) { s.PendingAsk = "" }, "waiting_input"},
		{"missing pending request", func(s *StateSnapshot) { s.PendingAsk = "missing" }, "waiting_input"},
		{"empty key is not pending", func(s *StateSnapshot) {
			s.Requests[""] = s.Requests[s.PendingAsk]
			s.PendingAsk = ""
		}, "waiting_input"},
		{"no waiting kind", func(s *StateSnapshot) { s.WaitingFor = "" }, ""},
		{"running", func(s *StateSnapshot) { s.Status = StatusRunning }, ""},
		{"completed", func(s *StateSnapshot) { s.Status = StatusCompleted }, "completed"},
		{"error", func(s *StateSnapshot) { s.Status = StatusError }, "error"},
		{"canceled", func(s *StateSnapshot) { s.Status = StatusCanceled }, "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := StateSnapshot{
				Status: StatusWaitingInput, WaitingFor: "ask_plan",
				AutoConfirm: true, PendingAsk: "ask",
				Requests: map[string]ConfirmationRequest{
					"ask": {Key: "ask", Kind: "ask_plan", Status: SendPending, Stable: true},
				},
			}
			tc.modify(&snap)
			if got := resultReason(&snap); got != tc.want {
				t.Fatalf("resultReason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWaitForResultAutoConfirmLifecycle(t *testing.T) {
	for _, kind := range []string{"ask_plan", "ask_sql"} {
		for _, outcome := range []SendStatus{SendAcknowledged, SendFailed, SendUnknown} {
			t.Run(kind+"/"+string(outcome), func(t *testing.T) {
				m, state := newWaitTestManager(t)
				state.AutoConfirm = true
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				type result struct {
					snap   *StateSnapshot
					reason string
					err    error
				}
				done := make(chan result, 1)
				go func() {
					snap, reason, err := m.WaitForResult(ctx, "s1", 5*time.Second)
					done <- result{snap, reason, err}
				}()
				managerAssertBlocked(t, done)

				// Exercise the notification that precedes automatic sending.
				state.RegisterAsk(ConfirmationRequest{Key: "ask", Kind: kind, Stable: true})
				managerAssertBlocked(t, done)
				state.SetSendStatus("ask", SendSending, true)
				managerAssertBlocked(t, done)
				state.SetSendStatus("ask", outcome, true)

				wantReason, wantStatus := "waiting_input", StatusWaitingInput
				if outcome == SendAcknowledged {
					managerAssertBlocked(t, done)
					state.SetStatus(StatusCompleted)
					wantReason, wantStatus = "completed", StatusCompleted
				}
				got := managerReceive(t, done)
				if got.err != nil || got.reason != wantReason || got.snap == nil {
					t.Fatalf("WaitForResult = %+v, want reason %q and snapshot", got, wantReason)
				}
				if got.snap.Status != wantStatus || got.snap.Requests["ask"].Status != outcome {
					t.Fatalf("unexpected final snapshot: %+v", got.snap)
				}
				if outcome == SendAcknowledged {
					if got.snap.PendingAsk != "" || got.snap.WaitingFor != "" {
						t.Fatalf("acknowledged request still pending: %+v", got.snap)
					}
				} else if got.snap.PendingAsk != "ask" || got.snap.WaitingFor != kind {
					t.Fatalf("failed or unknown request no longer pending: %+v", got.snap)
				}
			})
		}
	}
}

func TestWaitForResultReportRequiresManualConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		autoConfirm bool
		stable      bool
	}{
		{"auto stable", true, true},
		{"auto unstable", true, false},
		{"manual stable", false, true},
		{"manual unstable", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, state := newWaitTestManager(t)
			state.AutoConfirm = tc.autoConfirm
			state.RegisterAsk(ConfirmationRequest{Key: "report", Kind: "ask_report_render", Stable: tc.stable})
			done := make(chan string, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				_, reason, _ := m.WaitForResult(ctx, "s1", time.Second)
				done <- reason
			}()
			managerAssertBlocked(t, done)
			state.MarkReportReady("report")
			if got := managerReceive(t, done); got != "waiting_input" {
				t.Fatalf("ready report reason=%q", got)
			}
			snap := state.Snapshot()
			if snap.Status != StatusWaitingInput || snap.WaitingFor != "ask_report_render" || snap.Requests["report"].Status != SendPending || snap.SendGeneration != 0 {
				t.Fatalf("report must await manual confirmation: %+v", snap)
			}
		})
	}
}

func TestWaitForResultAutoConfirmTimeout(t *testing.T) {
	for _, status := range []SendStatus{SendPending, SendSending} {
		t.Run(string(status), func(t *testing.T) {
			m, state := newWaitTestManager(t)
			state.AutoConfirm = true
			state.RegisterAsk(ConfirmationRequest{Key: "ask", Kind: "ask_plan", Stable: true})
			if status == SendSending {
				state.SetSendStatus("ask", status, true)
			}
			snap, reason, err := m.WaitForResult(context.Background(), "s1", 20*time.Millisecond)
			if err != nil || reason != "timeout" || snap == nil {
				t.Fatalf("WaitForResult = (%+v, %q, %v), want timeout and snapshot", snap, reason, err)
			}
			if snap.Status != StatusWaitingInput || snap.Requests["ask"].Status != status {
				t.Fatalf("timeout changed pending request: %+v", snap)
			}
		})
	}
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
	err := m.SendMessage(context.Background(), "s1", "follow-up")
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
