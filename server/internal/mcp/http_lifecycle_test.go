package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestStdioRunHonorsCancellation(t *testing.T) {
	t.Setenv("MCP_TRANSPORT", "stdio")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	previous := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = previous }()
	s := New(nil, nil, "test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled stdio returned %v", err)
		}
	case <-time.After(2 * time.Second):
		_ = writer.Close()
		<-done
		t.Fatal("stdio ignored service cancellation")
	}
}

func TestRunRejectsUnknownTransport(t *testing.T) {
	t.Setenv("MCP_TRANSPORT", "http")
	if err := New(nil, nil, "test").Run(context.Background()); err == nil {
		t.Fatal("unknown transport silently selected stdio")
	}
}

func TestHTTPInitializationReservationBeforeBody(t *testing.T) {
	limits := httpLimits()
	limits.MaxSessions, limits.MaxSessionsPerPrincipal = 2, 1
	f := newHTTPFixture(t, "streamable-http", limits, false)
	c, err := net.DialTimeout("tcp", f.ts.Listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(c, "POST /mcp HTTP/1.1\r\nHost: local\r\nx-aily-token: synthetic-platform-token\r\nx-aily-user: A\r\nContent-Type: application/json\r\nContent-Length: 1\r\n\r\n")
	awaitHTTP(t, func() bool { n, in := f.counts(); return n == 1 && in == 1 })
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", "", "A", initMessage)), 429)
	f.initialize(t, "B")
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", "", "B", initMessage)), 429)
	fmt.Fprint(c, "{")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	statusHTTP(t, resp, 400)
	awaitHTTP(t, func() bool { n, in := f.counts(); return n == 1 && in == 0 })
	f.initialize(t, "A")
}

func TestSSEUnknownNotificationActualCompletion(t *testing.T) {
	limits := httpLimits()
	limits.MaxInflightRequests, limits.MaxInflightPerSession = 1, 1
	f := newHTTPFixture(t, "sse", limits, false)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.s.mcp.AddNotificationHandler("extension/unknown", func(ctx context.Context, _ mcp.JSONRPCNotification) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	})
	c := f.connectSSE(t, "A")
	f.initializeSSE(t, c)
	f.ssePost(t, c, "A", `{"jsonrpc":"2.0","method":"extension/unknown"}`, 202)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("notification not dispatched")
	}
	f.ssePost(t, c, "A", pingMessage, 429)
	if _, in := f.counts(); in != 1 {
		t.Fatal("unknown notification lost completion lease")
	}
	unblock()
	awaitHTTP(t, func() bool { _, in := f.counts(); return in == 0 })
	f.ssePost(t, c, "A", pingMessage, 202)
	c.next(t)
}

func TestSSERevokedBetweenDispatchHookAndBusinessEntry(t *testing.T) {
	for _, expiration := range []bool{false, true} {
		t.Run(fmt.Sprint(expiration), func(t *testing.T) {
			limits := httpLimits()
			if expiration {
				limits.MaxStreamLifetimeSeconds = 1
			}
			f := newHTTPFixture(t, "sse", limits, false)
			entered, release := make(chan context.Context, 1), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			hooks := remoteHooks()
			hooks.AddOnRequestInitialization(func(ctx context.Context, _ any, message any) error {
				if strings.Contains(string(message.(json.RawMessage)), `"tools/call"`) {
					entered <- ctx
					select {
					case <-release:
					case <-time.After(3 * time.Second):
					}
				}
				return nil
			})
			// Install public options before the first request, never mutate live hooks.
			server.WithHooks(hooks)(f.s.mcp)
			var calls atomic.Int64
			f.s.mcp.AddTool(mcp.NewTool("business"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				return mcp.NewToolResultText("unreachable"), nil
			})
			c := f.connectSSE(t, "A")
			f.initializeSSE(t, c)
			f.ssePost(t, c, "A", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"business"}}`, 202)
			var ctx context.Context
			select {
			case ctx = <-entered:
			case <-time.After(time.Second):
				t.Fatal("dispatch did not reach hook")
			}
			if !expiration {
				_ = c.response.Body.Close()
			}
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("revocation did not reach dispatch")
			}
			if _, in := f.counts(); in != 1 {
				t.Fatal("revocation released unfinished dispatch")
			}
			// Also exercise the value-only context that v0.32.0 MessageHandler uses.
			handler := remoteLeaseMiddleware(func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) { calls.Add(1); return nil, nil })
			if _, err := handler(context.WithoutCancel(ctx), mcp.CallToolRequest{}); err == nil {
				t.Fatal("WithoutCancel bypassed session revocation")
			}
			unblock()
			awaitHTTP(t, func() bool { n, in := f.counts(); return n == 0 && in == 0 })
			if calls.Load() != 0 {
				t.Fatal("revoked dispatch entered business handler")
			}
		})
	}
}

func TestHTTPNotificationsErrorsAndConcurrentOutput(t *testing.T) {
	f := newHTTPFixture(t, "streamable-http", httpLimits(), false)
	f.s.mcp.AddTool(mcp.NewTool("notify"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		for i := 0; i < 16; i++ {
			_ = f.s.mcp.SendNotificationToClient(ctx, "notifications/progress", map[string]any{"progress": i})
		}
		return mcp.NewToolResultText("finished"), nil
	})
	id := f.initialize(t, "A")
	for _, body := range []string{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","method":"unknown/notification"}`, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":42}}`, `{"jsonrpc":"2.0","id":1,"method":"unknown/method"}`, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"missing"}}`} {
		want := 200
		if !strings.Contains(body, `"id":`) {
			want = 202
		}
		statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", id, "A", body)), want)
		awaitHTTP(t, func() bool { _, in := f.counts(); return in == 0 })
	}
	for i := 0; i < 8; i++ {
		resp := f.do(t, f.request(t, "POST", "/mcp", id, "A", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"notify"}}`))
		body := statusHTTP(t, resp, 202)
		if !strings.Contains(body, "finished") {
			t.Fatal("stream lost final response")
		}
	}
}

type failingHTTPWriter struct {
	*httptest.ResponseRecorder
	mu    sync.Mutex
	reads []time.Time
}

func (w *failingHTTPWriter) SetReadDeadline(d time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reads = append(w.reads, d)
	return nil
}
func (*failingHTTPWriter) SetWriteDeadline(time.Time) error { return nil }
func (*failingHTTPWriter) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }

func TestHTTPInitializeWriteFailureAndSSEEndpointFailure(t *testing.T) {
	for _, kind := range []string{"streamable-http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			f := newHTTPFixture(t, kind, httpLimits(), false)
			method, path, body := "POST", "/mcp", initMessage
			if kind == "sse" {
				method, path, body = "GET", "/sse", ""
			}
			w := &failingHTTPWriter{ResponseRecorder: httptest.NewRecorder()}
			f.rt.ServeHTTP(w, f.request(t, method, path, "", "A", body))
			if n, in := f.counts(); n != 0 || in != 0 {
				t.Fatalf("failed write leaked session %d/%d", n, in)
			}
			if kind != "sse" {
				if len(w.reads) != 2 || w.reads[0].IsZero() || !w.reads[1].IsZero() {
					t.Fatal("body deadline was not cleared")
				}
				id := w.Header().Get("Mcp-Session-Id")
				if id != "" {
					statusHTTP(t, f.do(t, f.request(t, "GET", "/mcp", id, "A", "")), 404)
				}
			}
		})
	}
}

func TestHTTPJWTExpiryAndSSEStreamLifetime(t *testing.T) {
	for _, kind := range []string{"streamable-http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			limits := httpLimits()
			limits.MaxStreamLifetimeSeconds = 5
			f := newHTTPFixture(t, kind, limits, true)
			path, id := "/sse", ""
			if kind == "streamable-http" {
				id = f.initialize(t, "A")
				path = "/mcp"
			}
			r := f.request(t, "GET", path, id, "A", "")
			exp := time.Now().Add(2 * time.Second)
			r.Header.Set("x-aily-jwt", signHTTPToken(t, "A", testHTTPSecret, exp))
			resp := f.do(t, r)
			if kind == "sse" {
				scan := bufio.NewScanner(resp.Body)
				for scan.Scan() {
					if strings.HasPrefix(scan.Text(), "data: ") {
						break
					}
				}
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if time.Now().Before(time.Unix(exp.Unix(), 0)) {
				t.Fatal("stream ended before lease")
			}
			// A GET cannot extend a previous stream, even when the HTTP session
			// itself has a longer renewed authentication lease.
			if kind == "sse" {
				awaitHTTP(t, func() bool { n, _ := f.counts(); return n == 0 })
			} else {
				statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", id, "A", pingMessage)), 200)
			}
		})
	}
	limits := httpLimits()
	limits.MaxStreamLifetimeSeconds = 1
	f := newHTTPFixture(t, "sse", limits, false)
	c := f.connectSSE(t, "A")
	select {
	case _, ok := <-c.events:
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("header-mode SSE lease did not close")
	}
	awaitHTTP(t, func() bool { n, _ := f.counts(); return n == 0 })
}

func TestHTTPExpiredSessionIsNotResurrectedByRenewal(t *testing.T) {
	f := newHTTPFixture(t, "streamable-http", httpLimits(), true)
	r := f.request(t, "POST", "/mcp", "", "A", initMessage)
	r.Header.Set("x-aily-jwt", signHTTPToken(t, "A", testHTTPSecret, time.Now().Add(2*time.Second)))
	resp := f.do(t, r)
	id := resp.Header.Get("Mcp-Session-Id")
	statusHTTP(t, resp, 200)
	awaitHTTP(t, func() bool { n, _ := f.counts(); return n == 0 })
	statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", id, "A", pingMessage)), 404)
}

func TestHTTPDisconnectAndShutdownRetainRunningLease(t *testing.T) {
	limits := httpLimits()
	limits.MaxInflightRequests, limits.MaxInflightPerSession = 1, 1
	f := newHTTPFixture(t, "streamable-http", limits, false)
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	f.s.mcp.AddTool(mcp.NewTool("block"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			close(cancelled)
		case <-time.After(3 * time.Second):
			return nil, errors.New("cancellation timeout")
		}
		select {
		case <-release:
		case <-time.After(3 * time.Second):
		}
		return nil, ctx.Err()
	})
	id := f.initialize(t, "A")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := f.request(t, "POST", "/mcp", id, "A", `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"block"}}`).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(r)
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	// Idle sweep must never free active work, even with an old touched time.
	f.rt.sessions.sweep(time.Now().Add(time.Hour))
	if n, in := f.counts(); n != 1 || in != 1 {
		t.Fatal("idle sweep reclaimed active dispatch")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("disconnect did not cancel tool")
	}
	if _, in := f.counts(); in != 1 {
		t.Fatal("client disconnect prematurely released running lease")
	}
	// DELETE is available even with the only dispatch slot occupied.
	statusHTTP(t, f.do(t, f.request(t, "DELETE", "/mcp", id, "A", "")), 200)
	shutdown := make(chan struct{})
	go func() { f.rt.close(); close(shutdown) }()
	select {
	case <-shutdown:
		t.Fatal("shutdown returned before real completion")
	default:
	}
	unblock()
	select {
	case <-shutdown:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not drain")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP request did not return")
	}
}

// This intentionally takes 56 seconds: shortening the original 55-second wait
// to make a body deadline test pass would conceal a transport regression.
func TestHTTPBodyDeadlineClearedForLongWait(t *testing.T) {
	if testing.Short() {
		t.Skip("56-second real transport wait")
	}
	limits := httpLimits()
	limits.ReadBodyTimeoutSeconds = 1
	limits.MaxStreamLifetimeSeconds = 60
	f := newHTTPFixture(t, "streamable-http", limits, false)
	f.s.mcp.AddTool(mcp.NewTool("longwait"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		timer := time.NewTimer(56 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			return mcp.NewToolResultText("long wait completed"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	id := f.initialize(t, "A")
	body := statusHTTP(t, f.do(t, f.request(t, "POST", "/mcp", id, "A", `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"longwait"}}`)), 200)
	if !strings.Contains(body, "long wait completed") {
		t.Fatalf("body deadline interrupted long dispatch: %s", body)
	}
}
