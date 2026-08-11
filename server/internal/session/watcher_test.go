package session

import (
	"context"
	"testing"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
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
