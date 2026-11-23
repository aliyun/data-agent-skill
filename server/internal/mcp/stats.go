package mcp

import (
	"sort"
	"sync"
	"time"
)

// toolStats counts tool calls per tool name. Counters are cumulative; the
// perf collector reports them and the reader diffs successive samples.
type toolStats struct {
	mu       sync.Mutex
	inflight int
	byTool   map[string]*toolCounter
}

type toolCounter struct {
	calls, errors, toolErrors uint64
	inflight                  int
	totalDuration             time.Duration
	maxDuration               time.Duration
	// window tracks the slowest call since the last snapshot so a one-off
	// stall is visible even when the cumulative average hides it.
	windowMax time.Duration
}

func newToolStats() *toolStats {
	return &toolStats{byTool: make(map[string]*toolCounter)}
}

func (t *toolStats) begin(tool string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.counter(tool)
	c.calls++
	c.inflight++
	t.inflight++
}

func (t *toolStats) end(tool string, d time.Duration, failed, toolError bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.counter(tool)
	c.inflight--
	t.inflight--
	c.totalDuration += d
	if d > c.maxDuration {
		c.maxDuration = d
	}
	if d > c.windowMax {
		c.windowMax = d
	}
	if failed {
		c.errors++
	}
	if toolError {
		c.toolErrors++
	}
}

func (t *toolStats) counter(tool string) *toolCounter {
	c := t.byTool[tool]
	if c == nil {
		c = &toolCounter{}
		t.byTool[tool] = c
	}
	return c
}

// snapshot returns the counters and resets per-window maxima.
func (t *toolStats) snapshot() map[string]any {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.byTool))
	for name := range t.byTool {
		names = append(names, name)
	}
	sort.Strings(names)
	var calls, errs, toolErrs uint64
	tools := make(map[string]any, len(names))
	for _, name := range names {
		c := t.byTool[name]
		calls += c.calls
		errs += c.errors
		toolErrs += c.toolErrors
		entry := map[string]any{
			"calls":         c.calls,
			"errors":        c.errors,
			"tool_errors":   c.toolErrors,
			"inflight":      c.inflight,
			"max_ms":        c.maxDuration.Milliseconds(),
			"window_max_ms": c.windowMax.Milliseconds(),
		}
		if c.calls > 0 {
			entry["avg_ms"] = (c.totalDuration / time.Duration(c.calls)).Milliseconds()
		}
		c.windowMax = 0
		tools[name] = entry
	}
	return map[string]any{
		"inflight":    t.inflight,
		"calls":       calls,
		"errors":      errs,
		"tool_errors": toolErrs,
		"tools":       tools,
	}
}
