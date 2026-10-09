package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	opMu    sync.Mutex
	exited  bool

	cancelMu sync.Mutex
	cancel   context.CancelFunc

	// sseCancel cancels the current SSE stream so we can reconnect after
	// sending a confirmation message.
	sseCancelMu sync.Mutex
	sseCancel   context.CancelFunc
}

type watcherClient interface {
	StreamSSE(ctx context.Context, agentID, sessionID string, checkpoint int) (<-chan dataagent.SSEEvent, error)
	SendMessage(context.Context, dataagent.SendMessageOpts) error
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
	defer func() {
		w.opMu.Lock()
		w.exited = true
		w.opMu.Unlock()
	}()

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

		changed := w.state.Changed()
		snap := w.state.Snapshot()
		reportDraining := snap.Requests[snap.PendingAsk].Kind == "ask_report_render" && !snap.Requests[snap.PendingAsk].Ready
		if resultReason(&snap) == "waiting_input" && !reportDraining {
			var retry <-chan time.Time
			var timer *time.Timer
			if snap.MessageStatus == SendUnknown || snap.Requests[snap.PendingAsk].Status == SendUnknown {
				// Unknown delivery forbids resending, not observing the accepted remote turn.
				timer = time.NewTimer(2 * time.Second)
				retry = timer.C
			}
			select {
			case <-changed:
			case <-retry:
			case <-ctx.Done():
			}
			if timer != nil {
				timer.Stop()
			}
			continue
		}

		if isError {
			consecutiveErrors++
			if consecutiveErrors > maxConsecutiveErrors {
				log.Printf("[session:%s] giving up after %d consecutive SSE errors",
					w.state.GetSessionID(), maxConsecutiveErrors)
				w.opMu.Lock()
				w.exited = true
				w.state.SetError("SSE connection failed: too many consecutive errors")
				w.state.Persist(w.sessDir)
				w.opMu.Unlock()
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

var errWatcherExited = errors.New("session watcher has exited")

func (w *Watcher) SendMessage(ctx context.Context, message string) error {
	before := w.state.Snapshot()
	if before.MessageStatus == SendSending || before.Requests[before.PendingAsk].Status == SendSending {
		return fmt.Errorf("another message is being sent; inspect session status before retrying")
	}
	w.opMu.Lock()
	defer w.opMu.Unlock()
	if w.exited {
		return errWatcherExited
	}
	snap := w.state.Snapshot()
	if before.PendingAsk != snap.PendingAsk || before.SendGeneration != snap.SendGeneration {
		return fmt.Errorf("session changed while waiting to send; inspect the current pending request")
	}
	return w.sendMessage(ctx, snap.PendingAsk, message, false)
}

// The caller holds opMu across event application and sending to bind replies to one ask.
func (w *Watcher) sendMessage(ctx context.Context, key, message string, auto bool) error {
	snap := w.state.Snapshot()
	if key == "" && strings.EqualFold(strings.TrimSpace(message), "confirm") {
		return fmt.Errorf("no pending confirmation request; inspect session status before sending")
	}
	if key != "" {
		request, ok := snap.Requests[key]
		if !ok || snap.PendingAsk != key {
			return fmt.Errorf("confirmation request is no longer pending")
		}
		if request.Status == SendUnknown || request.Status == SendSending {
			return fmt.Errorf("message delivery is unknown; verify remote state before retrying")
		}
		if request.Status == SendAcknowledged || (auto && request.Status != SendPending) {
			return fmt.Errorf("confirmation request has already been attempted")
		}
		if request.Kind == "ask_report_render" && !request.Ready {
			return fmt.Errorf("analysis output is still being received; wait before requesting a report")
		}
	} else if snap.MessageStatus == SendUnknown || snap.MessageStatus == SendSending {
		return fmt.Errorf("message delivery is unknown; verify remote state before retrying")
	}

	w.state.SetSendStatus(key, SendSending, auto)
	if err := w.state.Persist(w.sessDir); err != nil {
		w.state.SetSendStatus(key, SendFailed, auto)
		return fmt.Errorf("persist send intent: %w", err)
	}
	messageType := "primary"
	if snap.Requests[key].Kind == "ask_report_render" && strings.EqualFold(strings.TrimSpace(message), "confirm") {
		messageType, message = "report", "绘制网页报告"
	}
	err := w.client.SendMessage(ctx, dataagent.SendMessageOpts{
		AgentID: snap.AgentID, SessionID: snap.SessionID, Message: message,
		MessageType: messageType, Mode: snap.Mode, WorkspaceID: snap.WorkspaceID,
	})
	if err != nil {
		status := SendUnknown
		if dataagent.IsDefiniteRejection(err) {
			status = SendFailed
		}
		w.state.SetSendStatus(key, status, auto)
		if persistErr := w.state.Persist(w.sessDir); persistErr != nil {
			return fmt.Errorf("send message: %w (cannot persist delivery state: %v)", err, persistErr)
		}
		return fmt.Errorf("send message: %w", err)
	}

	w.state.SetSendStatus(key, SendAcknowledged, auto)
	persistErr := w.state.Persist(w.sessDir)
	w.sseCancelMu.Lock()
	if w.sseCancel != nil {
		w.sseCancel()
	}
	w.sseCancelMu.Unlock()
	if persistErr != nil {
		return fmt.Errorf("message accepted but acknowledgement could not be persisted; do not resend: %w", persistErr)
	}
	return nil
}

// streamOnce connects to the SSE stream and processes events until the stream
// ends. Returns (finished, isError):
//   - (true, false): session reached terminal state (completed/canceled), watcher should exit
//   - (false, false): caller should reconnect (e.g. after auto-confirmation)
//   - (false, true): transient SSE error, caller should retry with backoff
func (w *Watcher) streamOnce(ctx context.Context) (bool, bool) {
	w.opMu.Lock()
	if ctx.Err() != nil {
		w.opMu.Unlock()
		return true, false
	}
	sseCtx, sseCancel := context.WithCancel(ctx)
	w.sseCancelMu.Lock()
	w.sseCancel = sseCancel
	w.sseCancelMu.Unlock()
	initial := w.state.Snapshot()
	checkpoint := initial.Checkpoint
	legacyReplay := checkpoint > 0 && len(initial.Requests) == 0 && len(initial.Confirmations) > 0
	w.opMu.Unlock()
	defer sseCancel()

	ch, err := w.client.StreamSSE(sseCtx, w.state.GetAgentID(), w.state.GetSessionID(), checkpoint)
	if err != nil {
		return ctx.Err() != nil, ctx.Err() == nil
	}

	var contentCategory string
	var accum []byte
	latestCheckpoint := checkpoint

	process := func(ev dataagent.SSEEvent) (finished, reconnect, streamEnded bool) {
		w.opMu.Lock()
		defer w.opMu.Unlock()
		if sseCtx.Err() != nil {
			return ctx.Err() != nil, ctx.Err() == nil, false
		}
		if ev.Checkpoint != nil && *ev.Checkpoint > latestCheckpoint {
			latestCheckpoint = *ev.Checkpoint
		}
		key, stable := w.eventKey(ev)
		if ev.EventType == "chat_start" {
			if !w.state.EventApplied(key) {
				w.state.MarkEventApplied(key)
				w.state.StartTurn(key)
				w.state.Persist(w.sessDir)
			}
		}
		switch ev.EventType {
		case "content_start":
			contentCategory, accum = ev.Category, nil
			return
		case "delta":
			if contentCategory == "output_conclusion" || contentCategory == "tool_call_response" || contentCategory == "llm" {
				accum = append(accum, ev.Content...)
			}
			return
		case "data":
			if (ev.Category == "llm" || ev.Category == "tool_call_response") && contentCategory == ev.Category {
				accum = []byte(ev.Content)
				return
			}
		case "content_finish":
			if len(accum) > 0 {
				ev.Category, ev.Content = contentCategory, string(accum)
				key, stable = w.eventKey(ev)
			}
			contentCategory, accum = "", nil
		}

		// Keep the resume cursor before an unfinished content lifecycle so reconnect can rebuild it.
		if contentCategory == "" && latestCheckpoint > w.state.GetCheckpoint() {
			w.state.SetCheckpoint(latestCheckpoint)
		}
		parsed := event.Parse(ev.EventType, ev.Category, ev.Content, ev.ContentType)
		if parsed.Action == event.ActionStreamEnded {
			return false, false, true
		}
		if parsed.Action == event.ActionNone {
			return
		}
		if w.state.EventApplied(key) {
			return
		}
		if w.state.Snapshot().AwaitingTurn {
			return
		}
		if parsed.Action.NeedsConfirmation() {
			// Old snapshots have no request ledger, so replay cannot prove an ask is unanswered.
			if legacyReplay && (ev.Checkpoint == nil || *ev.Checkpoint <= checkpoint) {
				stable = false
			}
			return false, w.handleConfirmation(ctx, parsed, key, stable), false
		}
		w.state.MarkEventApplied(key)
		if parsed.Action.IsTerminal() {
			if len(accum) > 0 {
				w.handleParsedEvent(event.Parse("content_finish", contentCategory, string(accum), ""))
			}
			snap := w.state.Snapshot()
			if !snap.HasTurnConclusion {
				fallback := snap.PendingToolOutput
				if fallback == "" {
					fallback = event.Parse("content_finish", "llm", snap.PendingLLM, "").Content
				}
				if fallback != "" {
					w.state.AddConclusion(fallback)
				}
			}
			if parsed.Action == event.ActionCompleted && snap.Requests[snap.PendingAsk].Kind == "ask_report_render" {
				// The report offer precedes the analysis tail; rendering starts a separate turn.
				w.state.MarkReportReady(snap.PendingAsk)
				if err := w.state.Persist(w.sessDir); err != nil {
					w.state.SetSendStatus(snap.PendingAsk, SendFailed, false)
					return false, false, true
				}
				return false, false, true
			}
		}
		w.handleParsedEvent(parsed)
		w.state.Persist(w.sessDir)
		w.exited = parsed.Action.IsTerminal()
		return w.exited, false, false
	}

	for ev := range ch {
		finished, reconnect, streamEnded := process(ev)
		if finished || reconnect {
			return finished, false
		}
		if streamEnded {
			break
		}
	}
	if ctx.Err() != nil {
		return true, false
	}
	if sseCtx.Err() != nil {
		return false, false
	}
	snap := w.state.Snapshot()
	request := snap.Requests[snap.PendingAsk]
	return false, resultReason(&snap) == "" || (request.Kind == "ask_report_render" && !request.Ready)
}

func (w *Watcher) eventKey(ev dataagent.SSEEvent) (string, bool) {
	eventID := ev.Data["event_id"]
	checkpoint := ev.Checkpoint
	if eventID != nil && eventID != "" {
		checkpoint = nil
	}
	stable := checkpoint != nil || (eventID != nil && eventID != "")
	turn := ""
	if !stable && ev.EventType != "chat_start" {
		turn = w.state.Snapshot().TurnKey
	}
	payload, _ := json.Marshal([]any{checkpoint, eventID, ev.Data["channel"], ev.EventType, ev.Category, ev.Content, turn})
	return fmt.Sprintf("%x", sha256.Sum256(payload)), stable
}

func (w *Watcher) handleConfirmation(ctx context.Context, pe event.ParsedEvent, key string, stable bool) bool {
	request, fresh := w.state.RegisterAsk(ConfirmationRequest{Key: key, Kind: pe.Category, Content: pe.Content, Stable: stable})
	if !fresh {
		return false
	}
	if err := w.state.Persist(w.sessDir); err != nil {
		w.state.SetSendStatus(key, SendFailed, false)
		return false
	}
	if pe.Action == event.ActionHumanInput || pe.Action == event.ActionConfirmReport || !w.state.GetAutoConfirm() || !request.Stable {
		return false
	}
	if err := w.sendMessage(ctx, key, "confirm", true); err != nil {
		log.Printf("[session:%s] confirmation request=%s delivery=%s", w.state.GetSessionID(), key[:12], w.state.Snapshot().Requests[key].Status)
	}
	return w.state.Snapshot().Requests[key].Status == SendAcknowledged
}

func (w *Watcher) handleParsedEvent(pe event.ParsedEvent) {
	switch pe.Action {
	case event.ActionStepProgress:
		w.state.SetStepProgress(pe.StepCurrent, pe.StepTotal, pe.StepName)
	case event.ActionFallback:
		if pe.Content != "" {
			if pe.Category == event.CatToolCallResponse {
				w.state.AppendToolOutput(pe.Content)
			} else {
				w.state.AppendLLMFallback(pe.Content)
			}
		}
	case event.ActionConclusion:
		if pe.Content != "" {
			w.state.UpsertConclusion(pe.DedupKey, pe.Content)
			for _, filename := range w.persistImages(pe.Images) {
				w.state.AddArtifact("image:" + filename)
			}
		}
	case event.ActionArtifact:
		for _, artifact := range pe.Artifacts {
			w.state.AddArtifact(artifact)
		}
	case event.ActionReportGenerated:
		if pe.Content != "" {
			w.state.AddArtifact(pe.Content)
		}
	case event.ActionRecommendedQuestion:
		if pe.Content != "" {
			w.state.SetRecommendedQuestions(strings.Split(pe.Content, "\n"))
		}
	case event.ActionCompleted:
		w.state.SetCompleted()
	case event.ActionError:
		w.state.SetError(pe.Content)
	case event.ActionCanceled:
		w.state.SetCanceled()
	}
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
