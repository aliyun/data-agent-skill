package mcp

import (
	"testing"

	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
)

func TestMapRemoteStatusNormalizesServerStates(t *testing.T) {
	cases := map[string]session.Status{
		"RUNNING":       session.StatusRunning,
		"WAIT_INPUT":    session.StatusWaitingInput,
		"WAITING_INPUT": session.StatusWaitingInput,
		"IDLE":          session.StatusCompleted,
		"FINISHED":      session.StatusCompleted,
		"COMPLETED":     session.StatusCompleted,
		"STOPPED":       session.StatusCompleted,
		"FAILED":        session.StatusError,
		"ERROR":         session.StatusError,
		"CANCELED":      session.StatusCanceled,
	}

	for raw, want := range cases {
		if got := mapRemoteStatus(raw); got != want {
			t.Fatalf("mapRemoteStatus(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizeMode(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"auto":     "auto",
		"lite":     "lite",
		"pro":      "pro",
		"ultra":    "ultra",
		"PRO":      "pro",   // case-insensitive
		"ASK_DATA": "lite",  // legacy mapping
		"ANALYSIS": "pro",   // legacy mapping
		"INSIGHT":  "ultra", // legacy mapping
		"CLAW":     "CLAW",  // unknown values pass through
	}
	for in, want := range cases {
		if got := normalizeMode(in); got != want {
			t.Fatalf("normalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
}
