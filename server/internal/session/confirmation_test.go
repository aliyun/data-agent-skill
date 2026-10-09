package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/event"
)

type confirmationClient struct {
	fakeWatcherClient
	send     func() error
	calls    atomic.Int32
	messages []dataagent.SendMessageOpts
}

func (c *confirmationClient) SendMessage(opts dataagent.SendMessageOpts) error {
	c.messages = append(c.messages, opts)
	c.calls.Add(1)
	if c.send != nil {
		return c.send()
	}
	return nil
}

func confirmationWatcher(t *testing.T, auto bool) (*Watcher, *confirmationClient) {
	t.Helper()
	client := &confirmationClient{}
	return &Watcher{state: &State{SessionID: "test-session", AgentID: "test-agent", Status: StatusRunning, AutoConfirm: auto}, client: client, sessDir: t.TempDir()}, client
}

func applyAsk(w *Watcher, ev dataagent.SSEEvent) string {
	w.opMu.Lock()
	defer w.opMu.Unlock()
	key, stable := w.eventKey(ev)
	w.handleConfirmation(event.Parse(ev.EventType, ev.Category, ev.Content, ev.ContentType), key, stable)
	return key
}

func TestReplyBindsOriginalAskWhenCheckpointAdvancesDuringSend(t *testing.T) {
	w, client := confirmationWatcher(t, false)
	first := applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "plan", 210))
	client.send = func() error {
		w.state.SetCheckpoint(211)
		return nil
	}
	if err := w.SendMessage("confirm"); err != nil {
		t.Fatal(err)
	}
	second := applyAsk(w, eventWithCheckpoint("chat_finish", "ask_human", "choose a region", 211))
	snap := w.state.Snapshot()
	if snap.Status != StatusWaitingInput || snap.PendingAsk != second || snap.Requests[first].Status != SendAcknowledged || snap.Requests[second].Status != SendPending {
		t.Fatalf("wrong reply binding: %+v", snap)
	}
}

func TestFailedReplyPreservesWaitingAndDoesNotAutoRetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status SendStatus
	}{
		{"rejected", &dataagent.APIError{StatusCode: 409, Code: "busy"}, SendFailed},
		{"unknown", errors.New("response lost"), SendUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, client := confirmationWatcher(t, true)
			client.send = func() error { return tc.err }
			ask := eventWithCheckpoint("chat_finish", "ask_plan", "original plan", 2)
			key := applyAsk(w, ask)
			for i := 0; i < 5; i++ {
				applyAsk(w, ask)
			}
			snap := w.state.Snapshot()
			if client.calls.Load() != 1 || snap.Requests[key].Status != tc.status || snap.WaitingFor != "ask_plan" || !strings.Contains(snap.WaitingDetail, "original plan") || snap.Status != StatusWaitingInput {
				t.Fatalf("failed reply state=%+v, calls=%d", snap, client.calls.Load())
			}
			if tc.status == SendUnknown {
				if err := w.SendMessage("confirm"); err == nil || client.calls.Load() != 1 {
					t.Fatal("unknown send must not retry")
				}
			} else {
				client.send = nil
				if err := w.SendMessage("confirm"); err != nil {
					t.Fatal(err)
				}
				if client.calls.Load() != 2 || w.state.GetStatus() != StatusRunning {
					t.Fatal("explicit retry of rejected send failed")
				}
			}
		})
	}
}

func TestUnknownAutoConfirmRemainsSuppressedAfterRestore(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	client.send = func() error { return errors.New("timeout") }
	ask := eventWithCheckpoint("chat_finish", "ask_plan", "plan", 2)
	key := applyAsk(w, ask)
	w.state = stateFromSnapshot(LoadState(w.sessDir, "test-session"))
	applyAsk(w, ask)
	if client.calls.Load() != 1 || w.state.Snapshot().Requests[key].Status != SendUnknown {
		t.Fatal("restored unknown confirmation was resent")
	}
}

func TestSendingIntentRestoresAsUnknown(t *testing.T) {
	w, client := confirmationWatcher(t, false)
	key := applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "plan", 2))
	w.state.SetSendStatus(key, SendSending, false)
	if err := w.state.Persist(w.sessDir); err != nil {
		t.Fatal(err)
	}
	w.state = stateFromSnapshot(LoadState(w.sessDir, "test-session"))
	if err := w.SendMessage("confirm"); err == nil || client.calls.Load() != 0 || w.state.Snapshot().Requests[key].Status != SendUnknown {
		t.Fatal("uncertain crash recovery resent the message")
	}
}

func TestSendIntentMustPersistBeforeNetworkCall(t *testing.T) {
	w, client := confirmationWatcher(t, false)
	applyAsk(w, eventWithCheckpoint("chat_finish", "ask_human", "question", 2))
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	w.sessDir = blocker
	if err := w.SendMessage("answer"); err == nil || client.calls.Load() != 0 {
		t.Fatal("sent without durable intent")
	}
	if w.state.GetWaitingFor() != "ask_human" {
		t.Fatal("lost pending question")
	}
}

func TestConcurrentManualAndAutomaticReplySendOnce(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	entered, release := make(chan struct{}), make(chan struct{})
	client.send = func() error { close(entered); <-release; return nil }
	autoDone := make(chan struct{})
	go func() { defer close(autoDone); applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "plan", 2)) }()
	<-entered
	manualDone := make(chan error, 1)
	go func() { manualDone <- w.SendMessage("confirm") }()
	close(release)
	<-autoDone
	<-manualDone
	if client.calls.Load() != 1 {
		t.Fatalf("sent %d times", client.calls.Load())
	}
}

func TestStreamEndDoesNotCompletePendingHumanInput(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	client.events = []dataagent.SSEEvent{
		eventWithCheckpoint("chat_finish", "ask_human", "question", 2),
		{EventType: "SSE_FINISH"},
	}
	finished, isError := w.streamOnce(context.Background())
	if finished || isError || w.state.GetStatus() != StatusWaitingInput || client.calls.Load() != 0 {
		t.Fatalf("idle stream lost waiting state: finished=%v error=%v state=%+v", finished, isError, w.state.Snapshot())
	}
}

func TestFollowUpIgnoresPreviousTurnCompletion(t *testing.T) {
	w, client := confirmationWatcher(t, false)
	client.events = []dataagent.SSEEvent{eventWithCheckpoint("chat_finish", "chat", "", 4)}
	w.streamOnce(context.Background())
	w = &Watcher{state: stateFromSnapshot(LoadState(w.sessDir, "test-session")), client: client, sessDir: w.sessDir}
	if err := w.SendMessage("follow-up"); err != nil {
		t.Fatal(err)
	}
	client.events = []dataagent.SSEEvent{
		eventWithCheckpoint("chat_finish", "chat", "", 4),
		eventWithCheckpoint("chat_start", "chat", "", 5),
		eventWithCheckpoint("data", "output_conclusion", "new answer", 6),
		eventWithCheckpoint("chat_finish", "chat", "", 7),
	}
	finished, isError := w.streamOnce(context.Background())
	snap := w.state.Snapshot()
	if !finished || isError || snap.Checkpoint != 7 || len(snap.Conclusions) != 1 || snap.Conclusions[0] != "new answer" {
		t.Fatalf("follow-up lost: %+v", snap)
	}
}

func TestSharedCheckpointPreservesDistinctAsksAndBatchData(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "plan", 2))
	applyAsk(w, eventWithCheckpoint("chat_finish", "ask_sql", "sql", 2))
	applyAsk(w, eventWithCheckpoint("chat_finish", "ask_report_render", "report", 2))
	if client.calls.Load() != 2 {
		t.Fatalf("plan and SQL requests suppressed: %d", client.calls.Load())
	}
	client.events = []dataagent.SSEEvent{
		eventWithCheckpoint("content_start", "output_conclusion", "", 3),
		eventWithCheckpoint("data", "output_conclusion", "answer", 3),
		eventWithCheckpoint("content_finish", "output_conclusion", "", 3),
		eventWithCheckpoint("data", "file_upload_finish", "report.csv 上传完成", 3),
		eventWithCheckpoint("chat_finish", "chat", "", 3),
	}
	w.streamOnce(context.Background())
	if client.calls.Load() != 3 {
		t.Fatalf("report request suppressed: %d", client.calls.Load())
	}
	w.state = stateFromSnapshot(LoadState(w.sessDir, "test-session"))
	w.streamOnce(context.Background())
	snap := w.state.Snapshot()
	if len(snap.Conclusions) != 1 || len(snap.Artifacts) != 1 {
		t.Fatalf("batch data duplicated or lost: %+v", snap)
	}
}

func TestAskIdentityIncludesScopeAndContentButMissingIdentityIsManual(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	for _, mission := range []string{"first", "second"} {
		ask := eventWithCheckpoint("chat_finish", "ask_plan", "same plan", 2)
		ask.Data = map[string]interface{}{"event_id": nil, "channel": map[string]interface{}{"mission_id": mission}}
		applyAsk(w, ask)
		applyAsk(w, ask)
	}
	applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "revised plan", 2))
	applyAsk(w, dataagent.SSEEvent{EventType: "chat_finish", Category: "ask_plan", Content: "unidentified plan"})
	if client.calls.Load() != 3 || w.state.GetStatus() != StatusWaitingInput {
		t.Fatalf("identity handling calls=%d state=%+v", client.calls.Load(), w.state.Snapshot())
	}
}

func TestPreviewContentDoesNotSendConfirmation(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	client.events = []dataagent.SSEEvent{
		eventWithCheckpoint("data", "ask_plan", `{"plan_id":"p1"}`, 2),
		eventWithCheckpoint("data", "ask_report_render", "preview", 3),
		eventWithCheckpoint("content_start", "tool_call_response", "", 4),
		eventWithCheckpoint("delta", "tool_call_response", `{"result_type":"plan","result":{"plan_id":"p1"}}`, 4),
		eventWithCheckpoint("content_finish", "tool_call_response", "", 4),
	}
	w.streamOnce(context.Background())
	if client.calls.Load() != 0 || w.state.GetStatus() != StatusRunning {
		t.Fatal("preview triggered confirmation")
	}
}

func TestInterruptedContentResumesBeforeLifecycle(t *testing.T) {
	w, client := confirmationWatcher(t, false)
	w.state.SetCheckpoint(1)
	client.events = []dataagent.SSEEvent{
		eventWithCheckpoint("content_start", "output_conclusion", "", 2),
		eventWithCheckpoint("delta", "output_conclusion", "first ", 3),
	}
	w.streamOnce(context.Background())
	if w.state.GetCheckpoint() != 1 {
		t.Fatal("cursor skipped incomplete content")
	}
	client.events = append(client.events,
		eventWithCheckpoint("delta", "output_conclusion", "second", 4),
		eventWithCheckpoint("content_finish", "output_conclusion", "", 5),
		eventWithCheckpoint("chat_finish", "chat", "", 6),
	)
	w.streamOnce(context.Background())
	if got := w.state.GetConclusions(); len(got) != 1 || got[0] != "first second" {
		t.Fatalf("replayed content = %v", got)
	}
}

func TestLLMFallbackSurvivesReconnectAndRestore(t *testing.T) {
	w, client := confirmationWatcher(t, false)
	client.events = askDataLLMEvents("answer")[:3]
	w.streamOnce(context.Background())
	w.state = stateFromSnapshot(LoadState(w.sessDir, "test-session"))
	client.events = []dataagent.SSEEvent{eventWithCheckpoint("chat_finish", "chat", "", 4)}
	w.streamOnce(context.Background())
	if got := w.state.GetConclusions(); len(got) != 1 || got[0] != "answer" {
		t.Fatalf("fallback lost: %v", got)
	}
}

func TestStableEventIDSurvivesCheckpointChanges(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	ask := eventWithCheckpoint("chat_finish", "ask_plan", "plan", 2)
	ask.Data = map[string]interface{}{"event_id": "request-1"}
	applyAsk(w, ask)
	next := 3
	ask.Checkpoint = &next
	applyAsk(w, ask)
	if client.calls.Load() != 1 {
		t.Fatal("same request ID was answered twice")
	}
}

func TestLegacySnapshotReplayRequiresManualConfirmation(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	w.state.SetCheckpoint(2)
	w.state.AddConfirmation("ask_plan", true)
	client.events = []dataagent.SSEEvent{eventWithCheckpoint("chat_finish", "ask_plan", "old plan", 2)}
	w.streamOnce(context.Background())
	if client.calls.Load() != 0 || w.state.GetStatus() != StatusWaitingInput {
		t.Fatal("legacy history cannot prove a replayed ask is unanswered")
	}
}

func TestNewRequestCanProceedAfterUnknownDelivery(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	client.send = func() error { return errors.New("response lost") }
	applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "first", 2))
	client.send = nil
	applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "second", 3))
	if client.calls.Load() != 2 || w.state.GetStatus() != StatusRunning {
		t.Fatal("new request was suppressed by old unknown delivery")
	}
}

func TestAcknowledgementPersistenceFailureDoesNotResend(t *testing.T) {
	w, client := confirmationWatcher(t, false)
	key := applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "plan", 2))
	backup := filepath.Join(t.TempDir(), "saved")
	client.send = func() error {
		dir := filepath.Join(w.sessDir, "test-session")
		if err := os.Rename(dir, backup); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir, []byte("blocked"), 0600); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	if err := w.SendMessage("confirm"); err == nil || !strings.Contains(err.Error(), "accepted") {
		t.Fatalf("expected explicit accepted-but-unpersisted error, got %v", err)
	}
	if w.state.Snapshot().Requests[key].Status != SendAcknowledged {
		t.Fatal("forgot accepted delivery")
	}
	if err := w.SendMessage("confirm"); err == nil || client.calls.Load() != 1 {
		t.Fatal("resent accepted confirmation")
	}
	saved := LoadState(filepath.Dir(backup), filepath.Base(backup))
	if saved == nil {
		t.Fatal("missing durable sending intent")
	}
	w.state = stateFromSnapshot(saved)
	if err := w.SendMessage("confirm"); err == nil || client.calls.Load() != 1 {
		t.Fatal("resent uncertain confirmation after crash")
	}
}

type uncertainDeliveryClient struct {
	confirmationClient
	streams atomic.Int32
}

func (c *uncertainDeliveryClient) StreamSSE(context.Context, string, string, int) (<-chan dataagent.SSEEvent, error) {
	ch := make(chan dataagent.SSEEvent, 2)
	if c.streams.Add(1) == 1 {
		ch <- eventWithCheckpoint("chat_finish", "ask_plan", "plan", 2)
		ch <- dataagent.SSEEvent{EventType: "SSE_FINISH"}
	} else {
		ch <- eventWithCheckpoint("data", "output_conclusion", "accepted remotely", 3)
		ch <- eventWithCheckpoint("chat_finish", "chat", "", 4)
	}
	close(ch)
	return ch, nil
}

func TestUnknownDeliveryKeepsObservingWithoutResending(t *testing.T) {
	w, _ := confirmationWatcher(t, true)
	client := &uncertainDeliveryClient{}
	client.send = func() error { return errors.New("response lost") }
	w.client = client
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w.Run(ctx)
	if ctx.Err() != nil || client.calls.Load() != 1 || client.streams.Load() != 2 || w.state.GetStatus() != StatusCompleted {
		t.Fatalf("unknown delivery stalled or resent: calls=%d streams=%d state=%+v", client.calls.Load(), client.streams.Load(), w.state.Snapshot())
	}
}

func TestTerminalErrorAndCancellationDetachUnknownRequest(t *testing.T) {
	for _, eventType := range []string{"SSE_FAILURE", "chat_canceled"} {
		t.Run(eventType, func(t *testing.T) {
			w, client := confirmationWatcher(t, true)
			client.send = func() error { return errors.New("response lost") }
			key := applyAsk(w, eventWithCheckpoint("chat_finish", "ask_plan", "plan", 2))
			client.events = []dataagent.SSEEvent{eventWithCheckpoint(eventType, "chat", "ended", 3)}
			w.streamOnce(context.Background())
			snap := w.state.Snapshot()
			if snap.PendingAsk != "" || snap.WaitingFor != "" || snap.Requests[key].Status != SendUnknown {
				t.Fatal("terminal event must detach pending reply but retain delivery history")
			}
			client.send = nil
			w = &Watcher{state: stateFromSnapshot(&snap), client: client, sessDir: w.sessDir}
			if err := w.SendMessage("new question"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReportRequestDrainsAnalysisAndWaitsForReportTurn(t *testing.T) {
	w, client := confirmationWatcher(t, true)
	client.events = []dataagent.SSEEvent{
		eventWithCheckpoint("chat_start", "chat", "", 1),
		eventWithCheckpoint("chat_finish", "ask_report_render", "report offer", 2),
		eventWithCheckpoint("data", "output_conclusion", "analysis tail", 3),
		eventWithCheckpoint("data", "file_upload_finish", "summary.csv 上传完成", 4),
		eventWithCheckpoint("chat_finish", "chat", "", 5),
	}
	client.send = func() error {
		snap := w.state.Snapshot()
		if snap.Checkpoint != 5 || len(snap.Conclusions) != 1 || len(snap.Artifacts) != 1 || !snap.AwaitingTurn {
			t.Fatalf("report sent before analysis was drained: %+v", snap)
		}
		return nil
	}
	finished, isError := w.streamOnce(context.Background())
	if finished || isError || w.state.GetStatus() != StatusRunning || client.calls.Load() != 1 {
		t.Fatalf("analysis ended watcher: finished=%v error=%v calls=%d", finished, isError, client.calls.Load())
	}
	if got := client.messages[0]; got.MessageType != "report" || got.Message != "绘制网页报告" {
		t.Fatalf("report request = %+v", got)
	}
	w.state = stateFromSnapshot(LoadState(w.sessDir, "test-session"))
	client.events = []dataagent.SSEEvent{
		eventWithCheckpoint("chat_finish", "ask_report_render", "report offer", 2),
		eventWithCheckpoint("chat_finish", "chat", "", 5),
		eventWithCheckpoint("chat_start", "chat", "", 6),
		eventWithCheckpoint("data", "output_conclusion", "rendered report", 7),
		eventWithCheckpoint("data", "file_upload_finish", "report.html 上传完成", 8),
		eventWithCheckpoint("chat_finish", "chat", "", 9),
	}
	finished, isError = w.streamOnce(context.Background())
	snap := w.state.Snapshot()
	if !finished || isError || snap.Status != StatusCompleted || snap.Checkpoint != 9 || len(snap.Conclusions) != 2 || len(snap.Artifacts) != 2 || client.calls.Load() != 1 {
		t.Fatalf("report turn incomplete or duplicated: %+v calls=%d", snap, client.calls.Load())
	}
}

func TestReportOfferRequiresAnalysisCompletion(t *testing.T) {
	for _, auto := range []bool{true, false} {
		t.Run(fmt.Sprintf("auto=%v", auto), func(t *testing.T) {
			w, client := confirmationWatcher(t, auto)
			client.events = []dataagent.SSEEvent{
				eventWithCheckpoint("chat_finish", "ask_report_render", "report", 2),
				{EventType: "SSE_FINISH"},
			}
			finished, isError := w.streamOnce(context.Background())
			if finished || !isError || client.calls.Load() != 0 {
				t.Fatal("stream end must not authorize report rendering or stop draining")
			}
			if err := w.SendMessage("confirm"); err == nil || client.calls.Load() != 0 {
				t.Fatal("manual approval interrupted the analysis tail")
			}
			client.events = []dataagent.SSEEvent{eventWithCheckpoint("chat_finish", "chat", "", 3)}
			w.streamOnce(context.Background())
			if auto && client.calls.Load() != 1 {
				t.Fatal("ready automatic report not sent")
			}
			if !auto && (client.calls.Load() != 0 || w.state.GetStatus() != StatusWaitingInput) {
				t.Fatal("manual report offer lost at analysis completion")
			}
		})
	}
}

func TestReadyReportRestoresWithoutLosingOrRepeatingSend(t *testing.T) {
	for _, status := range []SendStatus{SendPending, SendSending, SendFailed} {
		t.Run(string(status), func(t *testing.T) {
			w, client := confirmationWatcher(t, false)
			key := applyAsk(w, eventWithCheckpoint("chat_finish", "ask_report_render", "report", 2))
			client.events = []dataagent.SSEEvent{eventWithCheckpoint("chat_finish", "chat", "", 3)}
			w.streamOnce(context.Background())
			if status != SendPending {
				w.state.SetSendStatus(key, status, true)
			}
			snap := w.state.Snapshot()
			snap.AutoConfirm = true
			w.state = stateFromSnapshot(&snap)
			w.streamOnce(context.Background())
			wantCalls := int32(0)
			if status == SendPending {
				wantCalls = 1
			}
			if client.calls.Load() != wantCalls || !w.state.Snapshot().Requests[key].Ready {
				t.Fatalf("restored ready report: calls=%d state=%+v", client.calls.Load(), w.state.Snapshot())
			}
			w.streamOnce(context.Background())
			if client.calls.Load() != wantCalls {
				t.Fatal("report resent on replay")
			}
		})
	}
}

func TestReportSendFailureAndManualMessageType(t *testing.T) {
	for _, tc := range []struct {
		name       string
		message    string
		err        error
		wantType   string
		wantStatus SendStatus
	}{
		{"approve", "confirm", nil, "report", SendAcknowledged},
		{"free text", "instead answer another question", nil, "primary", SendAcknowledged},
		{"rejected", "confirm", &dataagent.APIError{StatusCode: 409, Code: "busy"}, "report", SendFailed},
		{"unknown", "confirm", errors.New("response lost"), "report", SendUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, client := confirmationWatcher(t, false)
			key := applyAsk(w, eventWithCheckpoint("chat_finish", "ask_report_render", "report", 2))
			client.events = []dataagent.SSEEvent{eventWithCheckpoint("chat_finish", "chat", "", 3)}
			w.streamOnce(context.Background())
			client.send = func() error { return tc.err }
			err := w.SendMessage(tc.message)
			if (err != nil) != (tc.err != nil) || client.messages[0].MessageType != tc.wantType || w.state.Snapshot().Requests[key].Status != tc.wantStatus {
				t.Fatalf("unexpected report delivery: err=%v state=%+v", err, w.state.Snapshot())
			}
			client.events = []dataagent.SSEEvent{eventWithCheckpoint("chat_finish", "chat", "", 3), {EventType: "SSE_FINISH"}}
			w.streamOnce(context.Background())
			if client.calls.Load() != 1 || w.state.GetStatus() == StatusCompleted {
				t.Fatal("old analysis completion ended or repeated report request")
			}
			if tc.wantStatus == SendUnknown {
				if err := w.SendMessage("confirm"); err == nil || client.calls.Load() != 1 {
					t.Fatal("unknown report delivery was resent")
				}
				client.events = []dataagent.SSEEvent{
					eventWithCheckpoint("chat_start", "chat", "", 4),
					eventWithCheckpoint("data", "output_conclusion", "report", 5),
					eventWithCheckpoint("chat_finish", "chat", "", 6),
				}
				if finished, _ := w.streamOnce(context.Background()); !finished || client.calls.Load() != 1 {
					t.Fatal("unknown report delivery did not observe remote completion")
				}
			}
		})
	}
}

func TestConcurrentPersistenceKeepsCompleteLatestState(t *testing.T) {
	w, _ := confirmationWatcher(t, false)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.state.AddConclusion("row")
			if err := w.state.Persist(w.sessDir); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if snap := LoadState(w.sessDir, "test-session"); snap == nil || len(snap.Conclusions) != 16 {
		t.Fatalf("stale snapshot: %+v", snap)
	}
}
