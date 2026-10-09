package session

import (
	"context"
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

func (c fakeWatcherClient) SendMessage(dataagent.SendMessageOpts) error {
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
	watcher.handleConfirmation(event.Parse(ask.EventType, ask.Category, ask.Content, ask.ContentType), key, stable)
	if state.GetStatus() != StatusWaitingInput {
		t.Fatalf("status = %s, want waiting_input", state.GetStatus())
	}
	if err := watcher.SendMessage("confirm"); err != nil {
		t.Fatal(err)
	}
	watcher.handleConfirmation(event.Parse(ask.EventType, ask.Category, ask.Content, ask.ContentType), key, stable)
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

func (c *replayWatcherClient) SendMessage(dataagent.SendMessageOpts) error {
	c.mu.Lock()
	c.sends++
	c.mu.Unlock()
	return nil
}
