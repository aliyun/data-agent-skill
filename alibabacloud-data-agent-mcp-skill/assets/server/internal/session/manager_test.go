package session

import (
	"testing"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
)

func TestStatusFromSessionInfoTreatsIdleAsCompleted(t *testing.T) {
	got := statusFromSessionInfo(&dataagent.SessionInfo{
		SessionStatus: "IDLE",
		AgentStatus:   "running",
	})
	if got != StatusCompleted {
		t.Fatalf("statusFromSessionInfo(IDLE/running) = %q, want %q", got, StatusCompleted)
	}
}

func TestStatusFromSessionInfoFallsBackToAgentRunning(t *testing.T) {
	got := statusFromSessionInfo(&dataagent.SessionInfo{
		SessionStatus: "INIT",
		AgentStatus:   "running",
	})
	if got != StatusRunning {
		t.Fatalf("statusFromSessionInfo(INIT/running) = %q, want %q", got, StatusRunning)
	}
}
