package session

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/event"
)

func TestStreamOnceUsesLLMDeltaAsASKDataConclusionFallback(t *testing.T) {
	state := &State{
		SessionID: "session-1",
		AgentID:   "agent-1",
		Status:    StatusRunning,
		Mode:      "ASK_DATA",
	}
	watcher := &Watcher{
		state:   state,
		client:  fakeWatcherClient{events: askDataLLMEvents("这是 ASK_DATA 的答案。")},
		sessDir: t.TempDir(),
	}

	finished, isError := watcher.streamOnce(context.Background())
	if !finished || isError {
		t.Fatalf("streamOnce finished=%v isError=%v, want finished without error", finished, isError)
	}

	snap := state.Snapshot()
	if snap.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", snap.Status)
	}
	if snap.Checkpoint != 4 {
		t.Fatalf("checkpoint = %d, want 4", snap.Checkpoint)
	}
	if len(snap.Conclusions) != 1 || snap.Conclusions[0] != "这是 ASK_DATA 的答案。" {
		t.Fatalf("conclusions = %#v, want ASK_DATA llm answer", snap.Conclusions)
	}
}

func TestStreamOncePrefersOutputConclusionOverLLMFallback(t *testing.T) {
	state := &State{
		SessionID: "session-1",
		AgentID:   "agent-1",
		Status:    StatusRunning,
		Mode:      "ANALYSIS",
	}
	watcher := &Watcher{
		state: state,
		client: fakeWatcherClient{events: []dataagent.SSEEvent{
			eventWithCheckpoint("content_start", "llm", "", 1),
			eventWithCheckpoint("delta", "llm", "中间 LLM 文本。", 2),
			eventWithCheckpoint("content_finish", "llm", "", 3),
			eventWithCheckpoint("content_start", "output_conclusion", "", 4),
			eventWithCheckpoint("delta", "output_conclusion", "正式分析结论。", 5),
			eventWithCheckpoint("content_finish", "output_conclusion", "", 6),
			eventWithCheckpoint("chat_finish", "chat", "", 7),
		}},
		sessDir: t.TempDir(),
	}

	finished, isError := watcher.streamOnce(context.Background())
	if !finished || isError {
		t.Fatalf("streamOnce finished=%v isError=%v, want finished without error", finished, isError)
	}

	snap := state.Snapshot()
	if len(snap.Conclusions) != 1 || snap.Conclusions[0] != "正式分析结论。" {
		t.Fatalf("conclusions = %#v, want only output_conclusion", snap.Conclusions)
	}
}

func TestStreamOnceUsesNotebookOutputInsteadOfApproval(t *testing.T) {
	approval := `<result>{"action":"approved","reason":"plan confirmed","answers":{}}</result>`
	output := "metric value\nSeptember revenue 360\nTotal profit 405\n"
	for _, wire := range []string{"delta", "data", "standalone"} {
		t.Run(wire, func(t *testing.T) {
			events := []dataagent.SSEEvent{eventWithCheckpoint("chat_start", "chat", "", 1)}
			for i, content := range []string{approval, notebookOutputContent(t, output)} {
				category := "llm"
				if i == 1 {
					category = "tool_call_response"
				}
				cp := 2 + i*3
				if wire == "standalone" {
					events = append(events, eventWithCheckpoint("data", category, content, cp))
				} else {
					events = append(events,
						eventWithCheckpoint("content_start", category, "", cp),
						eventWithCheckpoint(wire, category, content, cp),
						eventWithCheckpoint("content_finish", category, "", cp))
				}
			}
			events = append(events,
				eventWithCheckpoint("chat_finish", "ask_report_render", "render report?", 8),
				eventWithCheckpoint("chat_finish", "chat", "", 9))
			state := &State{SessionID: "test-session", AgentID: "test-agent", Status: StatusRunning, Mode: "pro", AutoConfirm: true}
			watcher := &Watcher{state: state, client: fakeWatcherClient{events: events}, sessDir: t.TempDir()}
			watcher.streamOnce(context.Background())
			snap := state.Snapshot()
			if len(snap.Conclusions) != 1 || snap.Conclusions[0] != output {
				t.Fatalf("conclusions = %#v, want only executed output", snap.Conclusions)
			}
			if snap.PendingLLM != "" || snap.Status != StatusWaitingInput || !snap.Requests[snap.PendingAsk].Ready {
				t.Fatalf("approval leaked or manual report state changed: %+v", snap)
			}
		})
	}
}

func TestStreamOncePrefersFormalConclusionOverNotebookOutput(t *testing.T) {
	state := &State{SessionID: "test-session", AgentID: "test-agent", Status: StatusRunning}
	watcher := &Watcher{state: state, sessDir: t.TempDir(), client: fakeWatcherClient{events: []dataagent.SSEEvent{
		eventWithCheckpoint("data", "tool_call_response", notebookOutputContent(t, "intermediate rows"), 1),
		eventWithCheckpoint("data", "output_conclusion", "The final answer is 405.", 2),
		eventWithCheckpoint("chat_finish", "chat", "", 3),
	}}}
	watcher.streamOnce(context.Background())
	if got := state.GetConclusions(); len(got) != 1 || got[0] != "The final answer is 405." {
		t.Fatalf("intermediate output replaced formal conclusion: %#v", got)
	}
}

func TestStreamOnceFallbackSnapshotAndReplayDoNotDuplicate(t *testing.T) {
	for _, category := range []string{"llm", "tool_call_response"} {
		t.Run(category, func(t *testing.T) {
			content := "Revenue 360, profit 405."
			if category == "tool_call_response" {
				content = notebookOutputContent(t, content)
			}
			block := []dataagent.SSEEvent{
				eventWithCheckpoint("content_start", category, "", 1),
				eventWithCheckpoint("delta", category, content[:len(content)/2], 1),
				eventWithCheckpoint("data", category, content, 1),
				eventWithCheckpoint("content_finish", category, "", 1),
			}
			events := append(append([]dataagent.SSEEvent{}, block...), block...)
			events = append(events, eventWithCheckpoint("chat_finish", "chat", "", 2))
			state := &State{SessionID: "test-session", AgentID: "test-agent", Status: StatusRunning}
			watcher := &Watcher{state: state, sessDir: t.TempDir(), client: fakeWatcherClient{events: events}}
			watcher.streamOnce(context.Background())
			if got := state.GetConclusions(); len(got) != 1 || got[0] != "Revenue 360, profit 405." {
				t.Fatalf("snapshot or replay duplicated fallback: %#v", got)
			}
		})
	}
}

func TestNotebookFallbackSurvivesRestoreAndResetsOnNewTurn(t *testing.T) {
	state := &State{SessionID: "test-session", AgentID: "test-agent", Status: StatusRunning}
	watcher := &Watcher{state: state, sessDir: t.TempDir(), client: fakeWatcherClient{events: []dataagent.SSEEvent{
		eventWithCheckpoint("data", "tool_call_response", notebookOutputContent(t, "Profit 405"), 1),
	}}}
	watcher.streamOnce(context.Background())
	watcher.state = stateFromSnapshot(LoadState(watcher.sessDir, "test-session"))
	watcher.client = fakeWatcherClient{events: []dataagent.SSEEvent{eventWithCheckpoint("chat_finish", "chat", "", 2)}}
	watcher.streamOnce(context.Background())
	if got := watcher.state.GetConclusions(); len(got) != 1 || got[0] != "Profit 405" {
		t.Fatalf("notebook fallback lost after restore: %#v", got)
	}
	watcher.client = fakeWatcherClient{events: []dataagent.SSEEvent{
		eventWithCheckpoint("chat_start", "chat", "", 3),
		eventWithCheckpoint("data", "llm", "A new answer.", 4),
		eventWithCheckpoint("chat_finish", "chat", "", 5),
	}}
	watcher.streamOnce(context.Background())
	snap := watcher.state.Snapshot()
	if snap.PendingToolOutput != "" || len(snap.Conclusions) != 2 || snap.Conclusions[1] != "A new answer." {
		t.Fatalf("prior output leaked into new turn: %+v", snap)
	}
}

func TestStreamOnceRejectsRestoredApprovalFallback(t *testing.T) {
	state := &State{
		SessionID: "test-session", AgentID: "test-agent", Status: StatusRunning,
		PendingLLM: `<result>{"action":"approved","reason":"plan confirmed","answers":{}}</result>`,
	}
	watcher := &Watcher{state: state, sessDir: t.TempDir(), client: fakeWatcherClient{events: []dataagent.SSEEvent{
		eventWithCheckpoint("chat_finish", "chat", "", 1),
	}}}
	watcher.streamOnce(context.Background())
	if got := state.GetConclusions(); len(got) != 0 {
		t.Fatalf("restored control output became a conclusion: %#v", got)
	}
}

func TestStreamOnceDoesNotKeepFallbackBeforeNewTurn(t *testing.T) {
	state := &State{SessionID: "test-session", AgentID: "test-agent", Status: StatusRunning, AwaitingTurn: true}
	watcher := &Watcher{state: state, sessDir: t.TempDir(), client: fakeWatcherClient{events: []dataagent.SSEEvent{
		eventWithCheckpoint("data", "tool_call_response", notebookOutputContent(t, "stale tool result"), 1),
		eventWithCheckpoint("content_start", "llm", "", 2),
		eventWithCheckpoint("delta", "llm", "stale LLM", 3),
		eventWithCheckpoint("content_finish", "llm", "", 4),
	}}}
	watcher.streamOnce(context.Background())
	snap := state.Snapshot()
	if snap.PendingLLM != "" || snap.PendingToolOutput != "" || len(snap.Conclusions) != 0 {
		t.Fatalf("stale content retained while awaiting new turn: %+v", snap)
	}
}

func TestStreamOnceFlushesOnlySafeUnfinishedFallback(t *testing.T) {
	for _, content := range []string{"Revenue 360.", `<result>{"action":"approved","reason":"yes","answers":{}}</result>`} {
		state := &State{SessionID: "test-session", AgentID: "test-agent", Status: StatusRunning}
		watcher := &Watcher{state: state, sessDir: t.TempDir(), client: fakeWatcherClient{events: []dataagent.SSEEvent{
			eventWithCheckpoint("content_start", "llm", "", 1),
			eventWithCheckpoint("delta", "llm", content, 2),
			eventWithCheckpoint("chat_finish", "chat", "", 3),
		}}}
		watcher.streamOnce(context.Background())
		got := state.GetConclusions()
		if strings.Contains(content, "approved") {
			if len(got) != 0 {
				t.Fatalf("unfinished control output became conclusion: %#v", got)
			}
		} else if len(got) != 1 || got[0] != content {
			t.Fatalf("unfinished answer lost: %#v", got)
		}
	}
}

func notebookOutputContent(t *testing.T, text string) string {
	t.Helper()
	inner, err := json.Marshal(map[string]any{
		"content_type": "code", "content": "print(result)", "cell_id": "test-cell",
		"nb_file_outputs": []any{map[string]any{"output_type": "stream", "name": "stdout", "text": text}},
	})
	if err != nil {
		t.Fatal(err)
	}
	outer, err := json.Marshal(map[string]any{"result_type": "jupyter_cell", "result": string(inner)})
	if err != nil {
		t.Fatal(err)
	}
	return string(outer)
}

func askDataLLMEvents(answer string) []dataagent.SSEEvent {
	return []dataagent.SSEEvent{
		eventWithCheckpoint("content_start", "llm", "", 1),
		eventWithCheckpoint("delta", "llm", answer, 2),
		eventWithCheckpoint("content_finish", "llm", "", 3),
		eventWithCheckpoint("chat_finish", "chat", "", 4),
	}
}

func eventWithCheckpoint(eventType, category, content string, checkpoint int) dataagent.SSEEvent {
	return dataagent.SSEEvent{
		EventType:  eventType,
		Category:   category,
		Content:    content,
		Checkpoint: &checkpoint,
	}
}

type fakeWatcherClient struct {
	events []dataagent.SSEEvent
}

func (c fakeWatcherClient) StreamSSE(context.Context, string, string, int) (<-chan dataagent.SSEEvent, error) {
	ch := make(chan dataagent.SSEEvent, len(c.events))
	for _, ev := range c.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func (c fakeWatcherClient) SendMessage(context.Context, dataagent.SendMessageOpts) error {
	return nil
}

// GetChatContent replays the requested checkpoint inclusively.
func TestAutoConfirmNotRepeatedOnInclusiveCheckpointReplay(t *testing.T) {
	state := &State{
		SessionID:   "session-1",
		AgentID:     "agent-1",
		Status:      StatusRunning,
		Mode:        "pro",
		AutoConfirm: true,
	}
	client := &replayWatcherClient{
		events: []dataagent.SSEEvent{
			eventWithCheckpoint("chat_start", "chat", "", 1),
			eventWithCheckpoint("chat_finish", "ask_plan", `{"plan_id":"p1","plans":[]}`, 2),
			eventWithCheckpoint("chat_start", "chat", "", 3),
			eventWithCheckpoint("chat_finish", "chat", "", 4),
		},
		askCheckpoint: 2,
	}
	watcher := &Watcher{state: state, client: client, sessDir: t.TempDir()}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	watcher.Run(ctx)

	if got := client.sendCount(); got != 1 {
		t.Fatalf("SendMessage called %d times, want 1", got)
	}
	if snap := state.Snapshot(); snap.Status != StatusCompleted {
		t.Fatalf("status = %s, want completed", snap.Status)
	}
}

func TestManualReplyDoesNotReenterWaitingOnReplay(t *testing.T) {
	state := &State{SessionID: "session-1", AgentID: "agent-1", Status: StatusRunning, Mode: "pro"}
	ask := eventWithCheckpoint("chat_finish", "ask_plan", `{"plan_id":"p1","plans":[]}`, 2)
	watcher := &Watcher{state: state, client: fakeWatcherClient{}, sessDir: t.TempDir()}

	state.SetCheckpoint(2)
	key, stable := watcher.eventKey(ask)
	watcher.handleConfirmation(context.Background(), event.Parse(ask.EventType, ask.Category, ask.Content, ask.ContentType), key, stable)
	if state.GetStatus() != StatusWaitingInput {
		t.Fatalf("status = %s, want waiting_input", state.GetStatus())
	}
	if err := watcher.SendMessage(context.Background(), "confirm"); err != nil {
		t.Fatal(err)
	}
	watcher.handleConfirmation(context.Background(), event.Parse(ask.EventType, ask.Category, ask.Content, ask.ContentType), key, stable)
	if state.GetStatus() != StatusRunning {
		t.Fatalf("status after replayed ask = %s, want running", state.GetStatus())
	}
}

// replayWatcherClient mimics GetChatContent: a stream resumes at the requested
// checkpoint inclusively and stays open at the ask until it is answered.
type replayWatcherClient struct {
	events        []dataagent.SSEEvent
	askCheckpoint int

	mu    sync.Mutex
	sends int
}

func (c *replayWatcherClient) sendCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sends
}

func (c *replayWatcherClient) StreamSSE(ctx context.Context, _, _ string, checkpoint int) (<-chan dataagent.SSEEvent, error) {
	answered := c.sendCount() > 0
	ch := make(chan dataagent.SSEEvent)
	go func() {
		defer close(ch)
		for _, ev := range c.events {
			if *ev.Checkpoint < checkpoint {
				continue
			}
			if !answered && *ev.Checkpoint > c.askCheckpoint {
				<-ctx.Done()
				return
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (c *replayWatcherClient) SendMessage(context.Context, dataagent.SendMessageOpts) error {
	c.mu.Lock()
	c.sends++
	c.mu.Unlock()
	return nil
}
