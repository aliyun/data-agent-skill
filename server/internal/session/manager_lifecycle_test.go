package session

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
)

type managerRoundTripper func(*http.Request) (*http.Response, error)

func (f managerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func managerResponse(status int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// The concrete client uses DefaultTransport. Replace it only in these serial
// tests, with an entirely in-memory transport: no sockets or live credentials.
func newLifecycleManager(t *testing.T, handle managerRoundTripper, streamHandle ...managerRoundTripper) *Manager {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = managerRoundTripper(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "manager.test.invalid" {
			return nil, fmt.Errorf("unexpected host %q", req.URL.Host)
		}
		if req.URL.Query().Get("Action") == "GetChatContent" {
			if len(streamHandle) > 0 {
				return streamHandle[0](req)
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		return handle(req)
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	client := dataagent.NewClient(&dataagent.Credential{
		AccessKeyID: "test-id", AccessKeySecret: "test-secret",
	}, "test", dataagent.WithDataAgentEndpoint("manager.test.invalid"),
		dataagent.WithDMSUnit("test"), dataagent.WithWorkspaceID("workspace"))
	m := NewManager(client, t.TempDir())
	t.Cleanup(func() {
		for _, snap := range m.ListSessions() {
			if err := m.StopSession(snap.SessionID); err != nil {
				t.Errorf("stop test watcher: %v", err)
			}
		}
	})
	return m
}

func managerReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for manager operation")
		var zero T
		return zero
	}
}

func managerAssertBlocked[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case value := <-ch:
		t.Fatalf("operation completed before release: %v", value)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestManagerRequestIDsStayWithToolCallsAndWatcherOutlivesCreate(t *testing.T) {
	type contextKey struct{}
	streams := make(chan context.Context, 3)
	release := make(chan struct{})
	var streamCount atomic.Int32
	m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
		action := req.URL.Query().Get("Action")
		requestID, wantContext, data := "", "create", `{}`
		switch action {
		case "CreateDataAgentSession":
			requestID, data = "create-id", `{"SessionId":"s1","AgentId":"a1"}`
		case "DescribeDataAgentSession":
			requestID, data = "poll-id", `{"SessionStatus":"RUNNING"}`
		case "SendChatMessage":
			switch req.URL.Query().Get("Message") {
			case "initial":
				requestID = "initial-id"
			case "confirm":
				requestID, wantContext = "auto-id", "process"
			case "follow-up":
				requestID, wantContext = "manual-id", "manual"
			}
		}
		if requestID == "" {
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
		if got := req.Context().Value(contextKey{}); got != wantContext {
			t.Errorf("%s context = %v, want %s", requestID, got, wantContext)
		}
		return managerResponse(http.StatusOK, fmt.Sprintf(`{"RequestId":%q,"Data":%s}`, requestID, data))
	}, func(req *http.Request) (*http.Response, error) {
		streams <- req.Context()
		if streamCount.Add(1) == 1 {
			select {
			case <-release:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
			return managerResponse(http.StatusOK, "event: chat_finish\ndata: {\"category\":\"ask_plan\",\"content\":\"plan\",\"checkpoint\":2}\n\n")
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	processCtx, stopProcess := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "process"))
	defer stopProcess()
	m.RestoreSessions(processCtx)
	createCtx, cancelCreate := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "create"))
	defer cancelCreate()
	createCtx, createIDs := dataagent.WithRequestIDs(createCtx)
	if _, err := m.CreateSession(createCtx, CreateOpts{Query: "initial", AutoConfirm: true}); err != nil {
		t.Fatal(err)
	}
	streamCtx := managerReceive(t, streams)
	cancelCreate()
	if streamCtx.Err() != nil || streamCtx.Value(contextKey{}) != "process" {
		t.Fatalf("watcher inherited create request context: %v", streamCtx.Err())
	}
	close(release)
	// Reconnection proves the auto-confirmation completed after cancellation.
	managerReceive(t, streams)
	manualCtx, manualIDs := dataagent.WithRequestIDs(context.WithValue(context.Background(), contextKey{}, "manual"))
	if err := m.SendMessage(manualCtx, "s1", "follow-up"); err != nil {
		t.Fatal(err)
	}
	managerReceive(t, streams)
	wantCreate := []dataagent.APIRequest{
		{Action: "CreateDataAgentSession", RequestID: "create-id"},
		{Action: "DescribeDataAgentSession", RequestID: "poll-id"},
		{Action: "SendChatMessage", RequestID: "initial-id"},
	}
	if got := createIDs.Snapshot(); !reflect.DeepEqual(got, wantCreate) {
		t.Fatalf("create request IDs = %+v, want %+v", got, wantCreate)
	}
	wantManual := []dataagent.APIRequest{{Action: "SendChatMessage", RequestID: "manual-id"}}
	if got := manualIDs.Snapshot(); !reflect.DeepEqual(got, wantManual) {
		t.Fatalf("manual request IDs = %+v, want %+v", got, wantManual)
	}
}

func TestManagerConcurrentReviveRequestIDsAreIsolated(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-release
		return managerResponse(http.StatusOK, fmt.Sprintf(`{"RequestId":%q}`, req.URL.Query().Get("SessionId")))
	})
	defer unblock()
	collectors := make([]*dataagent.RequestIDs, 2)
	results := make(chan error, 2)
	for i, sessionID := range []string{"s1", "s2"} {
		state := &State{SessionID: sessionID, AgentID: "a1", Status: StatusCompleted}
		if err := state.Persist(m.sessDir); err != nil {
			t.Fatal(err)
		}
		ctx, ids := dataagent.WithRequestIDs(context.Background())
		collectors[i] = ids
		go func() { results <- m.SendMessage(ctx, sessionID, "follow-up") }()
	}
	managerReceive(t, entered)
	managerReceive(t, entered)
	unblock()
	for range collectors {
		if err := managerReceive(t, results); err != nil {
			t.Fatal(err)
		}
	}
	for i, ids := range collectors {
		want := []dataagent.APIRequest{{Action: "SendChatMessage", RequestID: fmt.Sprintf("s%d", i+1)}}
		if got := ids.Snapshot(); !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d request IDs = %+v, want %+v", i, got, want)
		}
	}
}

func TestManagerConcurrentTerminalRevivesDoNotShareFailedEntry(t *testing.T) {
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var sends atomic.Int32
	var m *Manager
	m = newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
		sends.Add(1)
		switch req.URL.Query().Get("Message") {
		case "first":
			close(firstEntered)
			<-release
			return managerResponse(http.StatusBadRequest, `{"Code":"Rejected","Message":"try again"}`)
		case "second":
			// This attempt also needs its own unpublished entry; it must
			// not send through a watcher left behind by the failed revive.
			if got := m.ListSessions(); len(got) != 0 {
				t.Errorf("second send discovered an unaccepted watcher: %+v", got)
			}
			return managerResponse(http.StatusOK, `{}`)
		default:
			return nil, fmt.Errorf("unexpected request: %s", req.URL)
		}
	})
	defer unblock()
	state := &State{SessionID: "s1", AgentID: "old-agent", Status: StatusCompleted}
	state.Persist(m.sessDir)
	// Legacy/test entries with no Run have nil done and must not block.
	m.watchers["s1"] = &watcherEntry{state: state, cancel: func() {}}

	first := make(chan error, 1)
	go func() { first <- m.SendMessage(context.Background(), "s1", "first") }()
	managerReceive(t, firstEntered)
	if got := m.ListSessions(); len(got) != 0 {
		t.Fatalf("revive published an unaccepted entry: %+v", got)
	}
	if snap, err := m.GetStatus("s1"); err != nil || snap.Status != StatusCompleted {
		t.Fatalf("unaccepted send changed lifecycle status: %+v, %v", snap, err)
	}
	second := make(chan error, 1)
	go func() { second <- m.SendMessage(context.Background(), "s1", "second") }()
	managerAssertBlocked(t, second)
	if got := sends.Load(); got != 1 {
		t.Fatalf("concurrent send escaped session lock: %d calls", got)
	}
	unblock()
	if err := managerReceive(t, first); err == nil {
		t.Fatal("first send should fail")
	}
	if err := managerReceive(t, second); err == nil {
		t.Fatal("concurrent send must not replace the in-flight message")
	}
	if err := m.SendMessage(context.Background(), "s1", "second"); err != nil {
		t.Fatalf("explicit retry after rejection: %v", err)
	}
	if got := sends.Load(); got != 2 {
		t.Fatalf("send count = %d, want 2", got)
	}
	m.mu.RLock()
	entry := m.watchers["s1"]
	m.mu.RUnlock()
	if entry == nil || entry.done == nil || entry.state.GetStatus() != StatusRunning {
		t.Fatalf("successful revive has no running, tracked watcher: %+v", entry)
	}
}

// Block inside Run's StreamSSE call after cancellation, representing the old
// watcher's final state writes before Run returns.
type managerFinishingClient struct {
	state    *State
	dir      string
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (c *managerFinishingClient) StreamSSE(ctx context.Context, _, _ string, _ int) (<-chan dataagent.SSEEvent, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	<-c.release
	c.state.SetCheckpoint(42)
	c.state.AddConclusion("final persisted conclusion")
	c.state.Persist(c.dir)
	return nil, ctx.Err()
}

func (*managerFinishingClient) SendMessage(context.Context, dataagent.SendMessageOpts) error {
	return fmt.Errorf("must not send through the exiting watcher")
}

func TestManagerReviveWaitsForCancellationAndCompletionBeforeReload(t *testing.T) {
	sent := make(chan string, 2)
	m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
		sent <- req.URL.Query().Get("SessionId")
		return managerResponse(http.StatusOK, `{}`)
	})
	state := &State{SessionID: "s1", AgentID: "a1", Status: StatusCompleted, Checkpoint: 1}
	state.Persist(m.sessDir)
	finishing := &managerFinishingClient{
		state: state, dir: m.sessDir, started: make(chan struct{}),
		canceled: make(chan struct{}), release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(finishing.release) }) }
	defer unblock()
	watcher := NewWatcher(state, m.client, m.sessDir)
	watcher.client = finishing
	operation := m.sessionOperation("s1")
	operation.Lock()
	m.startWatcher(watcher)
	operation.Unlock()
	managerReceive(t, finishing.started)

	result := make(chan error, 1)
	go func() { result <- m.SendMessage(context.Background(), "s1", "follow-up") }()
	managerReceive(t, finishing.canceled)
	managerAssertBlocked(t, result)
	select {
	case id := <-sent:
		t.Fatalf("sent to %s before old Run completed", id)
	default:
	}

	// Waiting for s1 must hold neither m.mu nor an operation lock for s2.
	other := &State{SessionID: "s2", AgentID: "a2", Status: StatusCompleted}
	other.Persist(m.sessDir)
	otherResult := make(chan error, 1)
	go func() { otherResult <- m.SendMessage(context.Background(), "s2", "independent") }()
	if err := managerReceive(t, otherResult); err != nil {
		t.Fatalf("independent session: %v", err)
	}
	if id := managerReceive(t, sent); id != "s2" {
		t.Fatalf("sent to %s before old Run completed", id)
	}

	unblock()
	if err := managerReceive(t, result); err != nil {
		t.Fatal(err)
	}
	snap, err := m.GetStatus("s1")
	if err != nil || snap.Checkpoint != 42 || len(snap.Conclusions) != 1 || snap.Conclusions[0] != "final persisted conclusion" {
		t.Fatalf("revive did not reload final state: %+v, %v", snap, err)
	}
}

func TestManagerRevivesWatcherThatExitedBeforeSendLock(t *testing.T) {
	var sends atomic.Int32
	m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
		sends.Add(1)
		return managerResponse(http.StatusOK, `{}`)
	})
	persisted := &State{SessionID: "s1", AgentID: "a1", Status: StatusCompleted}
	if err := persisted.Persist(m.sessDir); err != nil {
		t.Fatal(err)
	}
	state := &State{SessionID: "s1", AgentID: "a1", Status: StatusRunning}
	m.watchers["s1"] = &watcherEntry{state: state, watcher: &Watcher{state: state, exited: true}, cancel: func() {}}
	if err := m.SendMessage(context.Background(), "s1", "follow-up"); err != nil {
		t.Fatal(err)
	}
	if sends.Load() != 1 || m.watchers["s1"].done == nil {
		t.Fatal("send used an exited watcher")
	}
}

func TestManagerFailedReviveRetainsPersistedWaitingState(t *testing.T) {
	m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
		return managerResponse(http.StatusBadRequest, `{"Code":"Rejected","Message":"not accepted"}`)
	})
	state := &State{
		SessionID: "s1", AgentID: "a1", Status: StatusWaitingInput,
		WaitingFor: "ask_human", WaitingDetail: "Choose a date range", Checkpoint: 9,
	}
	state.Persist(m.sessDir)
	if err := m.SendMessage(context.Background(), "s1", "last month"); err == nil {
		t.Fatal("send should fail")
	}
	snap, err := m.GetStatus("s1")
	if err != nil || snap.Status != StatusWaitingInput || snap.WaitingFor != "ask_human" || snap.WaitingDetail != state.WaitingDetail || snap.Checkpoint != 9 {
		t.Fatalf("failed revive lost waiting state: %+v, %v", snap, err)
	}
	if entries := m.ListSessions(); len(entries) != 0 {
		t.Fatalf("failed revive left published watchers: %+v", entries)
	}
}

func TestManagerWatchSessionSerializesWithRevive(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted=%t", accepted), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			var describes atomic.Int32
			m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("Action") == "DescribeDataAgentSession" {
					describes.Add(1)
					return managerResponse(http.StatusOK, `{"Data":{"AgentId":"a1","SessionStatus":"IDLE"}}`)
				}
				close(entered)
				<-release
				if !accepted {
					return managerResponse(http.StatusBadRequest, `{"Code":"Rejected","Message":"not accepted"}`)
				}
				return managerResponse(http.StatusOK, `{}`)
			})
			defer unblock()
			state := &State{SessionID: "s1", AgentID: "a1", Status: StatusCompleted}
			state.Persist(m.sessDir)
			sendResult := make(chan error, 1)
			go func() { sendResult <- m.SendMessage(context.Background(), "s1", "follow-up") }()
			managerReceive(t, entered)
			watched := make(chan *StateSnapshot, 1)
			go func() {
				snap, err := m.WatchSession(context.Background(), WatchOpts{SessionID: "s1", WorkspaceID: "workspace"})
				if err != nil {
					t.Errorf("watch session: %v", err)
				}
				watched <- snap
			}()
			managerAssertBlocked(t, watched)
			unblock()
			if err := managerReceive(t, sendResult); (err == nil) != accepted {
				t.Fatalf("send error = %v, accepted = %t", err, accepted)
			}
			snap := managerReceive(t, watched)
			wantStatus, wantDescribes := StatusCompleted, int32(1)
			if accepted {
				wantStatus, wantDescribes = StatusRunning, 0
			}
			if snap == nil || snap.Status != wantStatus || describes.Load() != wantDescribes {
				t.Fatalf("watch used an unpublished entry: snapshot=%+v describes=%d", snap, describes.Load())
			}
		})
	}
}

func TestManagerWatchAndRestorePreservePendingWaitingOnIdle(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%t", restore), func(t *testing.T) {
			var describes atomic.Int32
			m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
				describes.Add(1)
				return managerResponse(http.StatusOK, `{"Data":{"AgentId":"a1","SessionStatus":"IDLE"}}`)
			})
			state := &State{
				SessionID: "s1", AgentID: "a1", Status: StatusWaitingInput,
				WaitingFor: "ask_human", WaitingDetail: "Choose a date range", Checkpoint: 9,
			}
			state.Persist(m.sessDir)
			if restore {
				m.RestoreSessions(context.Background())
			} else if _, err := m.WatchSession(context.Background(), WatchOpts{SessionID: "s1"}); err != nil {
				t.Fatal(err)
			}
			snap, err := m.GetStatus("s1")
			if err != nil || snap.Status != StatusWaitingInput || snap.WaitingFor != state.WaitingFor || snap.WaitingDetail != state.WaitingDetail {
				t.Fatalf("IDLE erased waiting state: %+v, %v", snap, err)
			}
			if entries := m.ListSessions(); len(entries) != 1 {
				t.Fatalf("pending session has no active watcher: %+v", entries)
			}
			// Restore must not replace a watcher already installed by watch or restore.
			m.RestoreSessions(context.Background())
			if got := describes.Load(); got != 1 {
				t.Fatalf("restore revisited an existing watcher: %d describes", got)
			}
		})
	}
}

func TestManagerRestoresInFlightFollowUpFromTerminalSnapshot(t *testing.T) {
	for _, restore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%t", restore), func(t *testing.T) {
			m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
				return managerResponse(http.StatusOK, `{"Data":{"AgentId":"a1","SessionStatus":"RUNNING"}}`)
			})
			state := &State{SessionID: "s1", AgentID: "a1", Status: StatusCompleted, MessageStatus: SendSending, AwaitingTurn: true}
			if err := state.Persist(m.sessDir); err != nil {
				t.Fatal(err)
			}
			if restore {
				m.RestoreSessions(context.Background())
			} else if _, err := m.WatchSession(context.Background(), WatchOpts{SessionID: "s1"}); err != nil {
				t.Fatal(err)
			}
			snap, err := m.GetStatus("s1")
			if err != nil || snap.MessageStatus != SendUnknown || snap.Status != StatusWaitingInput || snap.WaitingFor != "message_delivery" || len(m.ListSessions()) != 1 {
				t.Fatalf("uncertain follow-up not restored: %+v error=%v", snap, err)
			}
		})
	}
}

func TestManagerSendOnPersistedSendingRestartsObservationWithoutSending(t *testing.T) {
	var sends atomic.Int32
	m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
		sends.Add(1)
		return managerResponse(http.StatusOK, `{}`)
	})
	state := &State{SessionID: "s1", AgentID: "a1", Status: StatusCompleted, MessageStatus: SendSending, AwaitingTurn: true}
	if err := state.Persist(m.sessDir); err != nil {
		t.Fatal(err)
	}
	if err := m.SendMessage(context.Background(), "s1", "follow-up"); err == nil {
		t.Fatal("uncertain send must not be retried")
	}
	if sends.Load() != 0 || len(m.ListSessions()) != 1 {
		t.Fatal("uncertain recovery must observe without resending")
	}
}

func TestManagerReconciliationPreservesIdleAndConcurrentProgress(t *testing.T) {
	for _, serverStatus := range []string{"IDLE", "COMPLETED"} {
		t.Run(serverStatus, func(t *testing.T) {
			state := &State{SessionID: "s1", AgentID: "a1", Status: StatusWaitingInput, WaitingFor: "ask_human", WaitingDetail: "question", Checkpoint: 2}
			m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
				if serverStatus == "COMPLETED" {
					state.SetCheckpoint(3)
				}
				return managerResponse(http.StatusOK, fmt.Sprintf(`{"Data":{"SessionStatus":%q}}`, serverStatus))
			})
			entry := &watcherEntry{state: state, watcher: NewWatcher(state, m.client, m.sessDir), cancel: func() {}}
			m.watchers["s1"] = entry
			m.reconcileWithServer("s1", entry)
			if state.GetStatus() != StatusWaitingInput || state.GetWaitingFor() != "ask_human" {
				t.Fatal("coarse or stale remote state erased pending question")
			}
		})
	}
}

func TestManagerReconciliationPreservesReportTransition(t *testing.T) {
	for _, serverStatus := range []string{"FINISHED", "COMPLETED", "STOPPED"} {
		for _, delivery := range []SendStatus{SendPending, SendFailed, SendUnknown, SendAcknowledged} {
			t.Run(serverStatus+"/"+string(delivery), func(t *testing.T) {
				state := &State{SessionID: "s1", AgentID: "a1", Status: StatusRunning}
				state.RegisterAsk(ConfirmationRequest{Key: "report", Kind: "ask_report_render", Stable: true})
				state.MarkReportReady("report")
				if delivery != SendPending {
					state.SetSendStatus("report", SendSending, false)
					state.SetSendStatus("report", delivery, false)
				}
				before := state.Snapshot()
				m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
					return managerResponse(http.StatusOK, fmt.Sprintf(`{"Data":{"SessionStatus":%q}}`, serverStatus))
				})
				canceled := false
				entry := &watcherEntry{state: state, watcher: NewWatcher(state, m.client, m.sessDir), cancel: func() { canceled = true }}
				m.watchers["s1"] = entry
				m.reconcileWithServer("s1", entry)
				after := state.Snapshot()
				if canceled || after.Status != before.Status || after.PendingAsk != before.PendingAsk || entry.watcher.exited {
					t.Fatal("analysis status discarded report transition")
				}
			})
		}
	}
}

func TestManagerHousekeepingDoesNotRemoveRevivedSession(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	state := &State{SessionID: "s1", Status: StatusRunning, UpdatedAt: time.Now()}
	m.watchers["s1"] = &watcherEntry{state: state, cancel: func() { t.Error("canceled revived watcher") }}
	m.removeEntry("s1")
	if len(m.ListSessions()) != 1 {
		t.Fatal("stale housekeeping decision removed a revived session")
	}
}

func TestManagerRemovalWaitsWithoutHoldingMapLock(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%t", stop), func(t *testing.T) {
			m := NewManager(nil, t.TempDir())
			state := &State{SessionID: "s1", Status: StatusCompleted, UpdatedAt: time.Now().Add(-2 * staleTimeout)}
			canceled, done := make(chan struct{}), make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(done) }) }
			defer finish()
			m.watchers["s1"] = &watcherEntry{
				state: state, watcher: NewWatcher(state, nil, m.sessDir),
				cancel: func() { close(canceled) }, done: done,
			}
			removed := make(chan error, 1)
			go func() {
				if stop {
					removed <- m.StopSession("s1")
				} else {
					m.removeEntry("s1")
					removed <- nil
				}
			}()
			managerReceive(t, canceled)
			managerAssertBlocked(t, removed)
			listed := make(chan bool, 1)
			go func() { m.ListSessions(); listed <- true }()
			managerReceive(t, listed)
			finish()
			if err := managerReceive(t, removed); err != nil {
				t.Fatal(err)
			}
			if len(m.ListSessions()) != 0 {
				t.Fatal("entry was not removed")
			}
		})
	}
}
