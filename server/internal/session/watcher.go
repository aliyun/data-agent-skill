package session

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/event"
)

// Watcher monitors a single Data Agent session's SSE stream. It translates
// raw SSE events into State mutations, handles auto-confirmation when enabled,
// and supports external message injection for manual confirmations.
type Watcher struct {
	state   *State
	client  watcherClient
	sessDir string

	cancelMu sync.Mutex
	cancel   context.CancelFunc

	// sseCancel cancels the current SSE stream so we can reconnect after
	// sending a confirmation message.
	sseCancelMu sync.Mutex
	sseCancel   context.CancelFunc
}

type watcherClient interface {
	StreamSSE(ctx context.Context, agentID, sessionID string, checkpoint int) (<-chan dataagent.SSEEvent, error)
	SendMessage(dataagent.SendMessageOpts) error
}

// NewWatcher creates a new session watcher. The watcher does not start
// processing until Run is called.
func NewWatcher(state *State, client *dataagent.Client, sessDir string) *Watcher {
	return &Watcher{
		state:   state,
		client:  client,
		sessDir: sessDir,
	}
}

// Run starts the SSE monitoring loop. It blocks until the context is canceled
// or the stream ends (completed / error / canceled). The caller is expected to
// invoke this in a goroutine.
func (w *Watcher) Run(ctx context.Context) {
	// Store the cancel function so StopSession can call it.
	w.cancelMu.Lock()
	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.cancelMu.Unlock()

	defer cancel()

	consecutiveErrors := 0
	const maxConsecutiveErrors = 10

	for {
		if ctx.Err() != nil {
			return
		}

		finished, isError := w.streamOnce(ctx)
		if finished {
			return
		}

		if isError {
			consecutiveErrors++
			if consecutiveErrors > maxConsecutiveErrors {
				log.Printf("[session:%s] giving up after %d consecutive SSE errors",
					w.state.GetSessionID(), maxConsecutiveErrors)
				w.state.SetError("SSE connection failed: too many consecutive errors")
				w.state.Persist(w.sessDir)
				return
			}
			wait := time.Duration(min(1<<uint(consecutiveErrors), 60)) * time.Second
			log.Printf("[session:%s] SSE error, retry %d/%d in %v",
				w.state.GetSessionID(), consecutiveErrors, maxConsecutiveErrors, wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		} else {
			consecutiveErrors = 0
		}
	}
}

// Stop cancels the watcher goroutine.
func (w *Watcher) Stop() {
	w.cancelMu.Lock()
	defer w.cancelMu.Unlock()
	if w.cancel != nil {
		w.cancel()
	}
}

// SendMessage sends a message to the session on behalf of the user (manual
// confirmation or free-form input). It clears the waiting state, sends the
// message, and triggers an SSE reconnect from the current checkpoint.
func (w *Watcher) SendMessage(message string) error {
	w.state.ClearWaiting()

	err := w.client.SendMessage(dataagent.SendMessageOpts{
		AgentID:     w.state.GetAgentID(),
		SessionID:   w.state.GetSessionID(),
		Message:     message,
		Mode:        w.state.GetMode(),
		WorkspaceID: w.state.GetWorkspaceID(),
	})
	if err != nil {
		return fmt.Errorf("send message: %w", err)
	}

	w.state.Persist(w.sessDir)

	// Cancel the current SSE stream so the Run loop reconnects with the
	// updated checkpoint and picks up new events.
	w.sseCancelMu.Lock()
	if w.sseCancel != nil {
		w.sseCancel()
	}
	w.sseCancelMu.Unlock()

	return nil
}

// streamOnce connects to the SSE stream and processes events until the stream
// ends. Returns (finished, isError):
//   - (true, false): session reached terminal state (completed/canceled), watcher should exit
//   - (false, false): caller should reconnect (e.g. after auto-confirmation)
//   - (false, true): transient SSE error, caller should retry with backoff
func (w *Watcher) streamOnce(ctx context.Context) (bool, bool) {
	agentID := w.state.GetAgentID()
	sessionID := w.state.GetSessionID()
	checkpoint := w.state.GetCheckpoint()

	// Create a child context so we can cancel just this SSE stream when we
	// need to reconnect after sending a confirmation.
	sseCtx, sseCancel := context.WithCancel(ctx)
	w.sseCancelMu.Lock()
	w.sseCancel = sseCancel
	w.sseCancelMu.Unlock()

	defer sseCancel()

	ch, err := w.client.StreamSSE(sseCtx, agentID, sessionID, checkpoint)
	if err != nil {
		if ctx.Err() != nil {
			return true, false
		}
		log.Printf("[session:%s] SSE connect error: %v", sessionID, err)
		return false, true // transient error, retry
	}

	// Cross-event accumulation for content lifecycle (output_conclusion, tool_call_response).
	// ASK_DATA sessions may deliver the final answer as llm deltas instead of
	// output_conclusion; keep those as a terminal fallback so ANALYSIS results
	// with explicit conclusions still win. Compare against the conclusion count
	// at stream start so follow-up turns on revived sessions (which already
	// carry conclusions from earlier turns) still get their new answer.
	var contentCategory string
	var accum []byte
	var llmFallback []byte
	baselineConclusions := len(w.state.GetConclusions())

	flushLLMFallback := func() {
		if contentCategory == "llm" && len(accum) > 0 {
			llmFallback = append(llmFallback, accum...)
		}
	}

	for ev := range ch {
		if ctx.Err() != nil {
			return true, false
		}

		// Update checkpoint.
		if ev.Checkpoint != nil {
			w.state.SetCheckpoint(*ev.Checkpoint)
		}

		// Track content lifecycle for delta accumulation.
		switch ev.EventType {
		case "content_start":
			flushLLMFallback()
			contentCategory = ev.Category
			accum = accum[:0]
		case "delta":
			if contentCategory == "output_conclusion" || contentCategory == "tool_call_response" || contentCategory == "llm" {
				accum = append(accum, ev.Content...)
			}
			continue // deltas are accumulated, not parsed individually
		case "data":
			// data events inside content lifecycle (e.g. task_finish, output_conclusion)
			// are self-contained — parse them directly without accumulation.
			// Route through handleParsedEvent so every action is honored
			// (conclusions, recommended questions, ...), not just conclusions.
			if contentCategory != "" {
				parsed := event.Parse(ev.EventType, ev.Category, ev.Content, ev.ContentType)
				if w.handleParsedEvent(parsed) {
					return false, false
				}
				continue
			}
		case "content_finish":
			if len(accum) > 0 {
				if contentCategory == "llm" {
					llmFallback = append(llmFallback, accum...)
					contentCategory = ""
					accum = accum[:0]
					continue
				}
				// Feed accumulated content to parser at content_finish boundary.
				parsed := event.Parse("content_finish", contentCategory, string(accum), ev.ContentType)
				if w.handleParsedEvent(parsed) {
					return false, false
				}
			}
			contentCategory = ""
			accum = accum[:0]
			continue
		}

		// Parse the event (non-delta, non-content-lifecycle events).
		parsed := event.Parse(ev.EventType, ev.Category, ev.Content, ev.ContentType)

		shouldReconnect := w.handleParsedEvent(parsed)
		if shouldReconnect {
			return false, false
		}

		// Check for terminal actions.
		if parsed.Action.IsTerminal() {
			// Flush any pending accumulation.
			if len(accum) > 0 && contentCategory == "output_conclusion" {
				accText := string(accum)
				w.state.AddConclusion(accText)
				// Extract and persist images from raw accumulated text
				images := event.ExtractBase64Images(accText)
				if len(images) > 0 {
					filenames := w.persistImages(images)
					for _, fn := range filenames {
						w.state.AddArtifact("image:" + fn)
					}
				}
			} else if len(accum) > 0 && contentCategory == "llm" {
				// ASK_DATA: answer accumulated in llm deltas, no content_finish received
				llmFallback = append(llmFallback, accum...)
			} else {
				flushLLMFallback()
			}
			if len(llmFallback) > 0 && len(w.state.GetConclusions()) == baselineConclusions {
				w.state.AddConclusion(string(llmFallback))
			}
			w.state.Persist(w.sessDir)
			return true, false
		}
	}

	// Channel closed without SSE_FINISH.
	if sseCtx.Err() != nil && ctx.Err() == nil {
		// SSE child context canceled (reconnect after auto-confirm).
		return false, false
	}

	// Channel closed unexpectedly (network drop, server closed connection).
	// Treat as transient error — caller will retry with backoff.
	log.Printf("[session:%s] SSE channel closed unexpectedly, will retry", w.state.GetSessionID())
	return false, true
}

// handleParsedEvent applies a parsed event to state. Returns true if the
// caller should reconnect the SSE stream (because we auto-confirmed and
// sent a message).
func (w *Watcher) handleParsedEvent(pe event.ParsedEvent) bool {
	switch pe.Action {

	case event.ActionConfirmPlan:
		if w.state.GetAutoConfirm() {
			return w.autoConfirm("ask_plan", "confirm")
		}
		w.state.SetWaiting("ask_plan", pe.Content)
		w.state.Persist(w.sessDir)

	case event.ActionConfirmSQL:
		if w.state.GetAutoConfirm() {
			return w.autoConfirm("ask_sql", "confirm")
		}
		w.state.SetWaiting("ask_sql", pe.Content)
		w.state.Persist(w.sessDir)

	case event.ActionConfirmReport:
		if w.state.GetAutoConfirm() {
			return w.autoConfirm("ask_report_render", "confirm")
		}
		w.state.SetWaiting("ask_report_render", pe.Content)
		w.state.Persist(w.sessDir)

	case event.ActionHumanInput:
		// Human input always requires manual input; never auto-confirm.
		w.state.SetWaiting("ask_human", pe.Content)
		w.state.Persist(w.sessDir)

	case event.ActionStepProgress:
		w.state.SetStepProgress(pe.StepCurrent, pe.StepTotal, pe.StepName)
		w.state.Persist(w.sessDir)

	case event.ActionConclusion:
		if pe.Content != "" {
			w.state.AddConclusion(pe.Content)
			// Persist extracted images
			if len(pe.Images) > 0 {
				filenames := w.persistImages(pe.Images)
				for _, fn := range filenames {
					w.state.AddArtifact("image:" + fn)
				}
			}
			w.state.Persist(w.sessDir)
		}

	case event.ActionRecommendedQuestion:
		// Follow-up questions suggested by the backend (newline-joined by the
		// parser); surfaced through the result tool's recommended_questions.
		if pe.Content != "" {
			w.state.SetRecommendedQuestions(strings.Split(pe.Content, "\n"))
			w.state.Persist(w.sessDir)
		}

	case event.ActionCompleted:
		w.state.SetCompleted()
		w.state.Persist(w.sessDir)

	case event.ActionError:
		w.state.SetError(pe.Content)
		w.state.Persist(w.sessDir)

	case event.ActionCanceled:
		w.state.SetCanceled()
		w.state.Persist(w.sessDir)

	case event.ActionNone:
		// No action required.
	}

	return false
}

// persistImages decodes base64 images and writes them to the session images directory.
// Returns the list of filenames that were successfully persisted.
func (w *Watcher) persistImages(images []event.Base64Image) []string {
	if len(images) == 0 {
		return nil
	}

	// Allocate contiguous sequence numbers
	startSeq := w.state.AllocImageSeq(len(images))

	// Create images directory under session dir
	imgDir := filepath.Join(w.sessDir, w.state.GetSessionID(), "images")
	if err := os.MkdirAll(imgDir, 0o755); err != nil {
		log.Printf("[session:%s] mkdir images failed: %v", w.state.GetSessionID(), err)
		return nil
	}

	var persisted []string
	for i, img := range images {
		seq := startSeq + i
		filename := fmt.Sprintf("img_%d.%s", seq, img.Format)
		destPath := filepath.Join(imgDir, filename)

		// Decode base64
		decoded, err := base64.StdEncoding.DecodeString(img.B64Data)
		if err != nil {
			log.Printf("[session:%s] decode image %s failed: %v", w.state.GetSessionID(), filename, err)
			continue
		}

		// Atomic write: tmp + rename
		tmpPath := destPath + ".tmp"
		if err := os.WriteFile(tmpPath, decoded, 0o644); err != nil {
			log.Printf("[session:%s] write image %s failed: %v", w.state.GetSessionID(), filename, err)
			continue
		}
		if err := os.Rename(tmpPath, destPath); err != nil {
			log.Printf("[session:%s] rename image %s failed: %v", w.state.GetSessionID(), filename, err)
			os.Remove(tmpPath)
			continue
		}

		persisted = append(persisted, filename)
		log.Printf("[session:%s] persisted image: %s (%d bytes)", w.state.GetSessionID(), filename, len(decoded))
	}

	return persisted
}

// autoConfirm sends a confirmation message and records it. Returns true to
// signal the caller to reconnect the SSE stream.
func (w *Watcher) autoConfirm(confirmType, message string) bool {
	sessionID := w.state.GetSessionID()

	err := w.client.SendMessage(dataagent.SendMessageOpts{
		AgentID:     w.state.GetAgentID(),
		SessionID:   sessionID,
		Message:     message,
		Mode:        w.state.GetMode(),
		WorkspaceID: w.state.GetWorkspaceID(),
	})
	if err != nil {
		log.Printf("[session:%s] auto-confirm %s failed: %v", sessionID, confirmType, err)
		// Fall back to waiting for manual input.
		w.state.SetWaiting(confirmType, fmt.Sprintf("auto-confirm failed: %v", err))
		w.state.Persist(w.sessDir)
		return false
	}

	w.state.AddConfirmation(confirmType, true)
	w.state.Persist(w.sessDir)

	// Cancel the current SSE stream so the Run loop reconnects.
	w.sseCancelMu.Lock()
	if w.sseCancel != nil {
		w.sseCancel()
	}
	w.sseCancelMu.Unlock()

	return true
}
