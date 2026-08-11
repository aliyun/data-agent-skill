package mcp

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// defaultWaitCap bounds every server-side blocking wait (data_agent_wait_result
// and data_agent_status with wait_timeout). MCP clients commonly cancel tool
// calls at ~120s; a wait that outlives the transport turns into a useless
// "context canceled" error, so the server always answers before that.
const defaultWaitCap = 110 * time.Second

// waitCapFromEnv returns the blocking-wait ceiling, overridable via the
// DATA_AGENT_WAIT_CAP environment variable (seconds).
func waitCapFromEnv() time.Duration {
	if v := os.Getenv("DATA_AGENT_WAIT_CAP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultWaitCap
}

// capWait bounds a caller-requested wait duration to the server wait cap.
// Zero and negative values pass through (they mean "no blocking").
func (s *Server) capWait(d time.Duration) time.Duration {
	if limit := s.effectiveWaitCap(); d > limit {
		return limit
	}
	return d
}

// effectiveWaitCap returns the configured cap, defaulting on zero-value
// Servers (tests construct Server{} directly).
func (s *Server) effectiveWaitCap() time.Duration {
	if s.waitCap <= 0 {
		return defaultWaitCap
	}
	return s.waitCap
}

// waitRegistry tracks which sessions have a blocking wait in flight so that
// duplicate parallel calls (a recurring LLM misbehavior) degrade to immediate
// snapshots instead of stacking multi-minute blocked requests.
type waitRegistry struct {
	mu       sync.Mutex
	inflight map[string]struct{}
}

func newWaitRegistry() *waitRegistry {
	return &waitRegistry{inflight: make(map[string]struct{})}
}

// enter marks a blocking wait as in flight for the session. It returns false
// when another wait is already running, in which case the caller must not
// block (and must not call exit). Nil-safe: a zero-value Server never blocks
// duplicates.
func (w *waitRegistry) enter(sessionID string) bool {
	if w == nil {
		return true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.inflight[sessionID]; ok {
		return false
	}
	w.inflight[sessionID] = struct{}{}
	return true
}

// exit clears the in-flight marker set by a successful enter.
func (w *waitRegistry) exit(sessionID string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.inflight, sessionID)
}

// duplicateWaitWarning is returned to callers whose blocking wait was degraded
// because another wait for the same session is already in flight.
const duplicateWaitWarning = "another blocking wait for this session is already in flight; returned an immediate snapshot instead. Never call data_agent_wait_result or data_agent_status(wait_timeout>0) in parallel for the same session — issue ONE wait call and wait for it to return."
