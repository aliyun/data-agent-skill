package session

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/event"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
)

func captureSessionLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &output
}

func TestSessionLogTokensAreBoundedAndExcludeURLsAndFreeText(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"RUNNING", "RUNNING"},
		{"session_123-a", "session_123-a"},
		{strings.Repeat("s", 129), strings.Repeat("s", 128)},
		{"", ""},
		{"https://example.invalid/report?Signature=private", "[redacted]"},
		{"prefix https://example.invalid/?token=private", "[redacted]"},
		{"https%3A%2F%2Fexample.invalid", "[redacted]"},
		{"//example.invalid/?token=private", "[redacted]"},
		{"status\r\nforged=entry", "[redacted]"},
		{"status\x1b[31m", "[redacted]"},
		{strings.Repeat("会", 129), "[redacted]"},
		{"\xff", "[redacted]"},
	} {
		if got := logToken(tc.input); got != tc.want || len([]rune(got)) > 128 {
			t.Errorf("log token = %q, want %q", got, tc.want)
		}
	}
}

func TestManagerDescribeLogsOmitUpstreamErrors(t *testing.T) {
	output := captureSessionLogs(t)
	m := newLifecycleManager(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("private-upstream-error https://example.invalid/?Signature=private-signature\nforged-entry")
	})
	if err := m.waitForRunning(context.Background(), "s1", "workspace", 0); err == nil {
		t.Fatal("failed describe unexpectedly succeeded")
	}
	state := &State{SessionID: "s1", AgentID: "a1", Status: StatusRunning}
	m.reconcileWithServer("s1", &watcherEntry{state: state})
	logged := output.String()
	if strings.Count(logged, `DescribeSession("s1") failed`) != 2 || !strings.Contains(logged, "[housekeeping]") {
		t.Fatalf("missing quoted failure logs: %s", logged)
	}
	for _, secret := range []string{"private-upstream-error", "https://", "Signature", "private-signature", "forged-entry"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("upstream detail leaked: %q", secret)
		}
	}
}

func TestManagerDescribeLogsBoundAndQuoteUpstreamStatuses(t *testing.T) {
	output := captureSessionLogs(t)
	status := strings.Repeat("S", 200)
	m := newLifecycleManager(t, func(*http.Request) (*http.Response, error) {
		body, err := json.Marshal(map[string]any{"Data": map[string]string{
			"SessionStatus": status,
			"AgentStatus":   "https://example.invalid/?Signature=private\nforged-entry",
			"AgentId":       "private-agent-payload",
		}})
		if err != nil {
			return nil, err
		}
		return managerResponse(http.StatusOK, string(body))
	})
	id := strings.Repeat("s", 128)
	if err := m.waitForRunning(context.Background(), id, "workspace", 0); err == nil {
		t.Fatal("unknown status unexpectedly ready")
	}
	logged := output.String()
	for _, want := range []string{fmt.Sprintf("DescribeSession(%q)", id), fmt.Sprintf("SessionStatus=%q", status[:128]), `AgentStatus="[redacted]"`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("missing bounded quoted field: %s", logged)
		}
	}
	for _, forbidden := range []string{status[:129], "https://", "Signature", "private", "forged-entry", "[DEBUG]"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("unexpected upstream payload in logs: %q", forbidden)
		}
	}
}

func TestWatcherLogsQuoteBoundKeysWithoutPayloads(t *testing.T) {
	for _, key := range []string{"k", strings.Repeat("k", 200), "key\nforged-entry", "https://example.invalid/?Signature=private"} {
		t.Run(fmt.Sprintf("key-length-%d", len(key)), func(t *testing.T) {
			output := captureSessionLogs(t)
			w, client := confirmationWatcher(t, true)
			client.send = func() error {
				return errors.New("private-upstream-error https://example.invalid/?Signature=private")
			}
			pe := event.Parse("chat_finish", "ask_plan", "private-plan-payload", "")
			w.handleConfirmation(context.Background(), pe, key, true)
			w.handleParsedEvent(event.ParsedEvent{Action: event.ActionArtifact, Artifacts: []string{"https://example.invalid/?Signature=private"}})
			logged := output.String()
			want := fmt.Sprintf("[session:%q] confirmation request=%q delivery=%q", "test-session", logToken(key), "unknown")
			if !strings.Contains(logged, want) {
				t.Fatalf("missing quoted confirmation log: %s", logged)
			}
			for _, forbidden := range []string{"private", "https://", "Signature", "forged-entry", strings.Repeat("k", 129)} {
				if strings.Contains(logged, forbidden) {
					t.Fatalf("unexpected upstream payload in logs: %q", forbidden)
				}
			}
		})
	}
}

func TestSEC01RejectTraversalAndMismatchedState(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "local")
	for _, dir := range []string{root, filepath.Join(base, "other"), filepath.Join(root, "alias")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{filepath.Join(base, "other"), filepath.Join(root, "alias")} {
		if err := os.WriteFile(filepath.Join(dir, "status.json"), []byte(`{"session_id":"other","conclusions":["synthetic private result"]}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"../other", "alias"} {
		if snap := LoadState(root, id); snap != nil {
			t.Errorf("unsafe state accepted for %q", id)
		}
	}
}

func testManager(t *testing.T, client *dataagent.Client, dir string) *Manager {
	t.Helper()
	m, err := NewManager(client, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Error(err)
		}
	})
	return m
}

func testStore(t *testing.T) *sessionStore {
	t.Helper()
	return testManager(t, nil, t.TempDir()).store
}

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

func invalidSessionIDs() []string {
	return []string{"", ".", "..", "../other", "a/b", "a//b", "/absolute", `C:\other`, `\\host\share`, `a\b`, "a:b", "%2e%2e%2fother", "a%252fb", "a\x00b", "a\nb", "a\rb", " a", "a ", "a.", "é", "会话", strings.Repeat("a", 129), "CON", "con", "PrN", "aUx", "nul", "COM1", "com9", "LPT1", "lpt9", "COM¹", "CONIN$", "CONOUT$", "CLOCK$", "NUL.txt"}
}

func TestSEC01ValidateAndRejectBeforeLocksOrIO(t *testing.T) {
	m := testManager(t, nil, t.TempDir())
	for _, id := range []string{"a", "Z_09-", strings.Repeat("z", 128), "COM10", "xCON", "con_", "CLOCK"} {
		if err := ValidateSessionID(id); err != nil {
			t.Fatalf("valid ID rejected: %q", id)
		}
	}
	// Invalid calls must not acquire the map or lifecycle lock, allocate a
	// session operation lock, access storage, or dereference the nil client.
	m.mu.Lock()
	m.lifeMu.Lock()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for _, id := range invalidSessionIDs() {
			check := func(err error) {
				if !errors.Is(err, ErrInvalidSessionID) || err.Error() != "invalid session_id" {
					t.Errorf("invalid ID error: %v", err)
				}
			}
			check(ValidateSessionID(id))
			_, err := m.GetStatus(id)
			check(err)
			_, err = m.GetResult(id)
			check(err)
			_, err = m.ReadImages(id)
			check(err)
			_, err = m.WatchSession(context.Background(), WatchOpts{SessionID: id})
			check(err)
			_, _, err = m.WaitForResult(context.Background(), id, time.Second)
			check(err)
			_, _, err = m.WaitForChange(context.Background(), id, 0, time.Second)
			check(err)
			check(m.SendMessage(context.Background(), id, "confirm"))
			check(m.StopSession(id))
			if m.IncrPollSeq(id) != 0 || m.restoreSession(id) || m.lockSessionOperation(id) != nil {
				t.Error("invalid ID entered internal maps")
			}
			m.removeEntry(id)
		}
	}()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		m.lifeMu.Unlock()
		m.mu.Unlock()
		t.Fatal("invalid ID reached a manager lock")
	}
	m.lifeMu.Unlock()
	m.mu.Unlock()
	assertNoSessionOperations(t, m)
	entries, err := readEntries(m.store.root)
	if err != nil || len(entries) != 0 || len(m.ListSessions()) != 0 {
		t.Fatal("invalid ID changed storage or watchers")
	}
	missing := filepath.Join(t.TempDir(), "must-not-be-created")
	for _, id := range invalidSessionIDs() {
		if LoadState(missing, id) != nil {
			t.Fatal("invalid state read")
		}
		if err := (&State{SessionID: id}).Persist(missing); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("invalid persist: %v", err)
		}
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation performed filesystem IO")
	}
}

func TestSEC01UpstreamInvalidIDStopsAfterCreate(t *testing.T) {
	for _, id := range []string{"../other", "/absolute", "NUL", "", strings.Repeat("a", 129)} {
		t.Run(fmt.Sprintf("length-%d-%x", len(id), []byte(id)[:min(2, len(id))]), func(t *testing.T) {
			var calls atomic.Int32
			m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				if req.URL.Query().Get("Action") != "CreateDataAgentSession" {
					t.Error("invalid upstream ID used in follow-up call")
				}
				return managerResponse(200, fmt.Sprintf(`{"Data":{"SessionId":%q,"AgentId":"a1"}}`, id))
			})
			_, err := m.CreateSession(context.Background(), CreateOpts{Query: "synthetic", WorkspaceID: "workspace"})
			if !errors.Is(err, ErrInvalidSessionID) || !strings.Contains(err.Error(), "remote creation may have occurred") {
				t.Fatalf("creation error: %v", err)
			}
			if calls.Load() != 1 || len(m.ListSessions()) != 0 {
				t.Fatal("invalid upstream ID caused further work")
			}
			entries, _ := readEntries(m.store.root)
			if len(entries) != 0 {
				t.Fatal("invalid upstream ID persisted")
			}
			assertNoSessionOperations(t, m)
		})
	}
}

func TestSEC01InvalidWatcherCannotSendOrConfirm(t *testing.T) {
	for _, id := range invalidSessionIDs() {
		client := &confirmationClient{}
		w := &Watcher{state: &State{SessionID: id, AutoConfirm: true}, client: client}
		if err := w.SendMessage(context.Background(), "confirm"); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("send error: %v", err)
		}
		w.Run(context.Background())
		w.streamOnce(context.Background())
		w.handleConfirmation(context.Background(), event.Parse("chat_finish", "ask_plan", "plan", ""), "test-key", true)
		if err := w.sendMessage(context.Background(), "", "question", true); !errors.Is(err, ErrInvalidSessionID) {
			t.Fatal(err)
		}
		if got := w.persistImages([]event.Base64Image{{Format: "png", B64Data: "YQ=="}}); len(got) != 0 {
			t.Fatal("invalid image persisted")
		}
		if client.calls.Load() != 0 || w.state.Snapshot().Requests != nil {
			t.Fatal("invalid watcher mutated state or sent")
		}
	}
}

func writeSyntheticState(t *testing.T, path, id string) {
	t.Helper()
	data, err := json.Marshal(StateSnapshot{SessionID: id, AgentID: "a1", Status: StatusRunning, Conclusions: []string{"synthetic private result"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSEC01RejectSymlinksAndUnexpectedTypes(t *testing.T) {
	for _, kind := range []string{"session-link", "state-link", "state-directory", "session-file", "images-link", "image-link", "image-directory", "images-file", "internal-state-link", "id-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			outside := testManager(t, nil, filepath.Join(base, "outside"))
			if err := (&State{SessionID: "s1", AgentID: "private-agent", Status: StatusRunning, Conclusions: []string{"outside-only"}}).persist(outside.store); err != nil {
				t.Fatal(err)
			}
			if err := outside.store.writeImage("s1", "img_0.png", []byte("outside-image")); err != nil {
				t.Fatal(err)
			}
			m := newLifecycleManager(t, func(*http.Request) (*http.Response, error) {
				t.Error("unsafe storage reached cloud")
				return managerResponse(500, `{}`)
			})
			if err := (&State{SessionID: "s1", AgentID: "a1", Status: StatusRunning}).persist(m.store); err != nil {
				t.Fatal(err)
			}
			if err := m.store.writeImage("s1", "img_0.png", []byte("local-image")); err != nil {
				t.Fatal(err)
			}
			root := m.store.root.Name()
			outsideSession := filepath.Join(outside.store.root.Name(), "s1")
			target, link := "", ""
			switch kind {
			case "session-link":
				target, link = filepath.Join(root, "s1"), outsideSession
			case "state-link":
				target, link = filepath.Join(root, "s1", "status.json"), filepath.Join(outsideSession, "status.json")
			case "internal-state-link":
				target, link = filepath.Join(root, "s1", "status.json"), "other.json"
				writeSyntheticState(t, filepath.Join(root, "s1", "other.json"), "s1")
			case "images-link":
				target, link = filepath.Join(root, "s1", "images"), filepath.Join(outsideSession, "images")
			case "image-link":
				target, link = filepath.Join(root, "s1", "images", "img_0.png"), filepath.Join(outsideSession, "images", "img_0.png")
			case "state-directory":
				target = filepath.Join(root, "s1", "status.json")
			case "image-directory":
				target = filepath.Join(root, "s1", "images", "img_0.png")
			case "session-file":
				target = filepath.Join(root, "s1")
			case "images-file":
				target = filepath.Join(root, "s1", "images")
			case "id-mismatch":
				writeSyntheticState(t, filepath.Join(root, "s1", "status.json"), "s2")
			}
			if target != "" {
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
				if link != "" {
					if err := os.Symlink(link, target); err != nil {
						t.Fatal(err)
					}
				} else if strings.HasSuffix(kind, "directory") {
					if err := os.Mkdir(target, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(target, []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if images, err := m.ReadImages("s1"); !errors.Is(err, ErrSessionUnavailable) || images != nil {
				t.Fatalf("unsafe image read: %+v, %v", images, err)
			}
			if err := m.store.writeImage("s1", "img_0.png", []byte("overwrite")); err == nil {
				t.Fatal("unsafe image write accepted")
			}
			if !strings.HasPrefix(kind, "image") {
				if _, err := m.GetResult("s1"); !errors.Is(err, ErrSessionUnavailable) {
					t.Fatalf("unsafe state read: %v", err)
				}
				if err := (&State{SessionID: "s1"}).persist(m.store); err == nil {
					t.Fatal("unsafe state overwritten")
				}
				if _, err := m.WatchSession(context.Background(), WatchOpts{SessionID: "s1"}); !errors.Is(err, ErrSessionUnavailable) {
					t.Fatalf("unsafe watch: %v", err)
				}
				if err := m.SendMessage(context.Background(), "s1", "question"); !errors.Is(err, ErrSessionUnavailable) {
					t.Fatalf("unsafe send: %v", err)
				}
				m.RestoreSessions(context.Background())
				if len(m.ListAllSessions()) != 0 {
					t.Fatal("unsafe state enumerated")
				}
			}
			images, err := outside.ReadImages("s1")
			if err != nil || len(images) != 1 || string(images[0].Data) != "outside-image" {
				t.Fatal("outside image altered")
			}
			snap, err := outside.GetStatus("s1")
			if err != nil || snap.AgentID != "private-agent" || snap.Conclusions[0] != "outside-only" {
				t.Fatal("outside state altered")
			}
		})
	}
}

func TestSEC01RootInitializationAndPinnedReplacement(t *testing.T) {
	base := t.TempDir()
	outside := testManager(t, nil, filepath.Join(base, "outside"))
	if err := (&State{SessionID: "s1", Conclusions: []string{"outside"}}).persist(outside.store); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(outside.store.root.Name(), link); err != nil {
		t.Fatal(err)
	}
	badFile := filepath.Join(base, "file")
	if err := os.WriteFile(badFile, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, badFile, filepath.Join(badFile, "child")} {
		if m, err := NewManager(nil, path); err == nil || m != nil {
			if m != nil {
				m.Close()
			}
			t.Fatal("unsafe root accepted")
		}
	}
	original := filepath.Join(base, "root")
	m := testManager(t, nil, original)
	if err := (&State{SessionID: "s1", Conclusions: []string{"local"}}).persist(m.store); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(original, filepath.Join(base, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside.store.root.Name(), original); err != nil {
		t.Fatal(err)
	}
	if snap, err := m.GetResult("s1"); err != nil || snap.Conclusions[0] != "local" {
		t.Fatal("root replacement redirected read")
	}
	if err := (&State{SessionID: "s1", Conclusions: []string{"local-updated"}}).persist(m.store); err != nil {
		t.Fatal(err)
	}
	if snap, err := outside.GetResult("s1"); err != nil || snap.Conclusions[0] != "outside" {
		t.Fatal("root replacement redirected write")
	}
}

func TestSEC01PrivateAtomicPersistenceAndImages(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new-root")
	m := testManager(t, nil, root)
	state := &State{SessionID: "s1", AgentID: "a1", Status: StatusCompleted, Checkpoint: 7}
	if err := state.persist(m.store); err != nil {
		t.Fatal(err)
	}
	w := newWatcher(state, nil, m.store)
	images := []event.Base64Image{{Format: "png", B64Data: base64.StdEncoding.EncodeToString([]byte("local-png"))}, {Format: "jpeg", B64Data: "Yg=="}}
	if got := w.persistImages(images); !reflect.DeepEqual(got, []string{"img_0.png", "img_1.jpeg"}) {
		t.Fatalf("image names: %v", got)
	}
	for _, format := range []string{"../outside", "png/../../outside", `png\outside`, "https://invalid", "svg", "PNG"} {
		if got := w.persistImages([]event.Base64Image{{Format: format, B64Data: "YQ=="}}); len(got) != 0 {
			t.Fatal("unsafe image name persisted")
		}
	}
	if err := m.store.root.WriteFile("s1/images/img_0.png.tmp", []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	// Random O_EXCL temporaries cannot follow or overwrite the old predictable name.
	if err := m.store.writeImage("s1", "img_0.png", []byte("updated")); err != nil {
		t.Fatal(err)
	}
	if b, _ := m.store.root.ReadFile("s1/images/img_0.png.tmp"); string(b) != "sentinel" {
		t.Fatal("predictable temporary overwritten")
	}
	m.store.root.Remove("s1/images/img_0.png.tmp")
	result, err := m.ReadImages("s1")
	if err != nil || len(result) != 2 || result[0].Name != "img_0.png" || string(result[0].Data) != "updated" {
		t.Fatalf("images: %+v %v", result, err)
	}
	if runtime.GOOS != "windows" {
		for path, mode := range map[string]os.FileMode{".": 0700, "s1": 0700, "s1/images": 0700, "s1/status.json": 0600, "s1/images/img_0.png": 0600} {
			info, err := m.store.root.Stat(path)
			if err != nil || info.Mode().Perm() != mode {
				t.Fatalf("mode %s: %v %v", path, info, err)
			}
		}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			state.AddConclusion("row")
			if err := state.persist(m.store); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if snap, err := m.GetStatus("s1"); err != nil || snap.Checkpoint != 7 || len(snap.Conclusions) != 8 {
		t.Fatal("atomic state lost")
	}
	for _, dir := range []string{"s1", "s1/images"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".tmp-") {
				t.Fatal("temporary file leaked")
			}
		}
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, nil, root)
	if snap, err := reopened.GetStatus("s1"); err != nil || len(snap.Conclusions) != 8 {
		t.Fatal("state failed restart")
	}
	if _, err := reopened.ReadImages("s1"); err != nil {
		t.Fatal(err)
	}
}

func TestSEC01CaseAliasCannotReadOrOverwrite(t *testing.T) {
	m := testManager(t, nil, t.TempDir())
	if err := (&State{SessionID: "Session", Conclusions: []string{"original"}}).persist(m.store); err != nil {
		t.Fatal(err)
	}
	// Emulate a case-insensitive alias even on case-sensitive test hosts by
	// placing a snapshot whose embedded ID differs only in case.
	if _, err := m.store.root.Stat("session"); errors.Is(err, os.ErrNotExist) {
		if err := m.store.root.Mkdir("session", 0700); err != nil {
			t.Fatal(err)
		}
		writeSyntheticState(t, filepath.Join(m.store.root.Name(), "session", "status.json"), "Session")
	}
	if _, err := m.GetStatus("session"); !errors.Is(err, ErrSessionUnavailable) {
		t.Fatal("case alias read")
	}
	if err := (&State{SessionID: "session"}).persist(m.store); err == nil {
		t.Fatal("case alias overwritten")
	}
	if snap, err := m.GetStatus("Session"); err != nil || snap.Conclusions[0] != "original" {
		t.Fatal("original case damaged")
	}
}

func TestSEC01CloseWaitsForFinalWatcherPersistence(t *testing.T) {
	m := testManager(t, nil, t.TempDir())
	state := &State{SessionID: "s1", Status: StatusRunning, MessageStatus: SendSending}
	if err := state.persist(m.store); err != nil {
		t.Fatal(err)
	}
	finishing := &managerFinishingClient{state: state, store: m.store, started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	release := func() { once.Do(func() { close(finishing.release) }) }
	defer release()
	w := newWatcher(state, nil, m.store)
	w.client = finishing
	unlock := m.lockSessionOperation("s1")
	m.startWatcher(w)
	unlock()
	managerReceive(t, finishing.started)
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	managerReceive(t, finishing.canceled)
	managerAssertBlocked(t, closed)
	if _, err := m.store.root.Stat("."); err != nil {
		t.Fatal("root closed before watcher finished")
	}
	release()
	if err := managerReceive(t, closed); err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.root.Stat("."); err == nil {
		t.Fatal("root not closed")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadImages("s1"); !errors.Is(err, ErrManagerClosed) {
		t.Fatal("read after close")
	}
	if _, err := m.WatchSession(context.Background(), WatchOpts{SessionID: "s1"}); !errors.Is(err, ErrManagerClosed) {
		t.Fatal("watch after close")
	}
	reopened := testManager(t, nil, m.store.root.Name())
	snap, err := reopened.GetStatus("s1")
	if err != nil || snap.Checkpoint != 42 || snap.MessageStatus != SendSending || len(snap.Conclusions) != 1 {
		t.Fatal("shutdown lost final persistence or uncertain delivery")
	}
	if stateFromSnapshot(snap).Snapshot().MessageStatus != SendUnknown {
		t.Fatal("shutdown changed unknown recovery")
	}
}

func TestSEC01CloseCancelsAndJoinsRestoration(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	m := newLifecycleManager(t, func(req *http.Request) (*http.Response, error) {
		close(entered)
		<-req.Context().Done()
		close(canceled)
		<-release
		return nil, req.Context().Err()
	})
	defer unblock()
	if err := (&State{SessionID: "s1", AgentID: "a1", Status: StatusRunning}).persist(m.store); err != nil {
		t.Fatal(err)
	}
	restored := make(chan struct{})
	go func() { defer close(restored); m.RestoreSessions(context.Background()) }()
	managerReceive(t, entered)
	closed := make(chan error, 1)
	go func() { closed <- m.Close() }()
	managerReceive(t, canceled)
	managerAssertBlocked(t, closed)
	if _, err := m.store.root.Stat("."); err != nil {
		t.Fatal("root closed during restore")
	}
	unblock()
	managerReceive(t, restored)
	if err := managerReceive(t, closed); err != nil {
		t.Fatal(err)
	}
	if len(m.ListSessions()) != 0 {
		t.Fatal("restore published watcher after close")
	}
}

func TestSEC01ConcurrentDirectoryReplacementStaysRooted(t *testing.T) {
	m := testManager(t, nil, t.TempDir())
	outside := testManager(t, nil, t.TempDir())
	for _, mgr := range []*Manager{m, outside} {
		if err := (&State{SessionID: "s1", Conclusions: []string{"synthetic"}}).persist(mgr.store); err != nil {
			t.Fatal(err)
		}
		payload := "local"
		if mgr == outside {
			payload = "outside-secret"
		}
		if err := mgr.store.writeImage("s1", "img_0.png", []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	ready := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		<-ready
		for range 64 {
			if err := m.store.root.Rename("s1", "held"); err != nil {
				t.Error(err)
				return
			}
			if err := m.store.root.Symlink(filepath.Join(outside.store.root.Name(), "s1"), "s1"); err != nil {
				t.Error(err)
				return
			}
			runtime.Gosched()
			if err := m.store.root.Remove("s1"); err != nil {
				t.Error(err)
				return
			}
			if err := m.store.root.Rename("held", "s1"); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	close(ready)
	for range 128 {
		images, err := m.ReadImages("s1")
		if err == nil {
			for _, image := range images {
				if string(image.Data) != "local" {
					t.Error("directory race escaped root")
				}
			}
		}
		// writeImage never creates the session directory; replacements cannot
		// divert an atomic write into the outside tree.
		_ = m.store.writeImage("s1", "img_0.png", []byte("local"))
	}
	managerReceive(t, finished)
	images, err := outside.ReadImages("s1")
	if err != nil || len(images) != 1 || string(images[0].Data) != "outside-secret" {
		t.Fatal("directory race wrote outside root")
	}
}

func TestSEC01InitDoesNotChangeExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode assertion")
	}
	dir := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(dir, 0750); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := testManager(t, nil, dir)
	after, err := m.store.root.Stat(".")
	if err != nil || after.Mode().Perm() != before.Mode().Perm() {
		t.Fatal("initializer changed existing permissions")
	}
}

func TestSEC01CloseCancelsHousekeepingAndWaits(t *testing.T) {
	m, _ := newWaitTestManager(t)
	finished := make(chan struct{}, 3)
	go func() { m.RunHousekeeping(context.Background()); finished <- struct{}{} }()
	go func() { m.WaitForChange(context.Background(), "s1", 0, time.Minute); finished <- struct{}{} }()
	go func() { m.WaitForResult(context.Background(), "s1", time.Minute); finished <- struct{}{} }()
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		managerReceive(t, finished)
	}
}

func FuzzValidateSessionID(f *testing.F) {
	for _, id := range append(invalidSessionIDs(), "valid_123-", strings.Repeat("a", 128)) {
		f.Add(id)
	}
	f.Fuzz(func(t *testing.T, id string) {
		err := ValidateSessionID(id)
		if err != nil {
			if err != ErrInvalidSessionID {
				t.Fatal("unstable validation error")
			}
			return
		}
		if len(id) < 1 || len(id) > 128 {
			t.Fatal("length accepted")
		}
		for _, c := range []byte(id) {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
				t.Fatal("non-ASCII or path character accepted")
			}
		}
	})
}
