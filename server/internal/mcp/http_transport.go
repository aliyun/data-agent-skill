package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

// ConfigureHTTP must be called before Run. Authentication must validate the
// platform token, identity and mapping, and install a trusted tenant Principal;
// it must not resolve a Tenant, fetch STS credentials or restore disk history.
// Configuration is startup-only, not a hot-reload API.
func (s *Server) ConfigureHTTP(limits config.HTTP, authenticate func(context.Context) (context.Context, error)) error {
	if authenticate == nil {
		return errors.New("remote MCP requires configured authentication")
	}
	if err := validateHTTPLimits(limits); err != nil {
		return err
	}
	s.httpLimits, s.authenticate = limits, authenticate
	return nil
}

func validateHTTPLimits(l config.HTTP) error {
	if l.MaxRequestBytes <= 0 || l.MaxRequestBytes == math.MaxInt64 || l.MaxHeaderBytes <= 0 || l.MaxHeaderBytes > math.MaxInt-4096 || l.MaxSessions <= 0 || l.MaxSessionsPerPrincipal <= 0 || l.MaxSessionsPerPrincipal > l.MaxSessions || l.MaxInflightRequests <= 0 || l.MaxInflightPerSession <= 0 || l.MaxInflightPerSession > l.MaxInflightRequests {
		return errors.New("invalid HTTP capacity limits")
	}
	for _, seconds := range []int{l.ReadHeaderTimeoutSeconds, l.ReadBodyTimeoutSeconds, l.IdleTimeoutSeconds, l.SessionIdleTimeoutSeconds, l.MaxStreamLifetimeSeconds} {
		if seconds <= 0 || int64(seconds) > math.MaxInt64/int64(time.Second) {
			return errors.New("invalid HTTP time limits")
		}
	}
	return nil
}

type remoteTransport struct {
	owner    *Server
	kind     string
	sessions *protocolSessions
	sse      *server.SSEServer
	http     *server.StreamableHTTPServer
	cancel   context.CancelFunc
	stopped  chan struct{}
}

func (s *Server) newRemoteTransport(ctx context.Context, kind string) (*remoteTransport, error) {
	if s.authenticate == nil {
		return nil, errors.New("remote MCP disabled: configure required identity authentication first")
	}
	if !isRemoteTransport(kind) {
		return nil, errors.New("unsupported remote MCP transport")
	}
	if err := validateHTTPLimits(s.httpLimits); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, name := range []string{s.hdrUser, s.hdrEmail, s.hdrToken, s.hdrJWT} {
		lower := strings.ToLower(name)
		if !validHeaderName(name) || seen[lower] {
			return nil, errors.New("invalid or conflicting identity header configuration")
		}
		switch lower {
		case "mcp-session-id", "content-type", "content-length", "content-encoding", "host", "connection", "transfer-encoding":
			return nil, errors.New("identity header conflicts with transport")
		}
		seen[lower] = true
	}
	ctx, cancel := context.WithCancel(ctx)
	t := &remoteTransport{owner: s, kind: kind, sessions: newProtocolSessions(ctx, s.httpLimits), cancel: cancel, stopped: make(chan struct{})}
	t.sessions.sse = kind == "sse"
	if kind == "sse" {
		t.sse = server.NewSSEServer(s.mcp, server.WithUseFullURLForMessageEndpoint(false))
	} else {
		t.http = server.NewStreamableHTTPServer(s.mcp, server.WithSessionIdManager(t.sessions), server.WithHTTPContextFunc(func(ctx context.Context, _ *http.Request) context.Context {
			l := leaseFrom(ctx)
			if l != nil {
				b := l.binding
				b.store.mu.Lock()
				fresh := b.id == ""
				b.store.mu.Unlock()
				if fresh && !b.store.bind(b, server.ClientSessionFromContext(ctx)) {
					b.store.revoke(b)
				}
			}
			return ctx
		}))
		// Called only after cancellation AND actual dispatch/GET completion, so
		// late session tools/heartbeat counters cannot be recreated after DELETE.
		t.sessions.cleanup = func(id string) {
			r, _ := http.NewRequest(http.MethodDelete, "/mcp", nil)
			r.Header.Set("Mcp-Session-Id", id)
			t.http.ServeHTTP(&discardResponse{header: make(http.Header)}, r)
		}
	}
	go func() {
		defer close(t.stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				t.sessions.sweep(now)
			case <-ctx.Done():
				t.sessions.close()
				return
			}
		}
	}()
	return t, nil
}

func (t *remoteTransport) close() { t.cancel(); t.sessions.close(); <-t.stopped; <-t.sessions.drained }

func (s *Server) ownedHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler,
		MaxHeaderBytes:    s.httpLimits.MaxHeaderBytes,
		ReadHeaderTimeout: time.Duration(s.httpLimits.ReadHeaderTimeoutSeconds) * time.Second,
		IdleTimeout:       time.Duration(s.httpLimits.IdleTimeoutSeconds) * time.Second,
		// ReadTimeout/WriteTimeout intentionally zero: a body-read deadline is
		// cleared before dispatch; each stream has its own absolute lease.
	}
}
func (s *Server) runHTTP(ctx context.Context, kind string) error {
	t, err := s.newRemoteTransport(ctx, kind)
	if err != nil {
		return err
	}
	defer t.close()
	if s.transport != nil {
		s.transport.Store(t)
		defer s.transport.Store(nil)
	}
	addr, err := mcpAddr()
	if err != nil {
		return err
	}
	h := s.ownedHTTPServer(addr, t)
	finished := make(chan struct{})
	defer close(finished)
	go func() {
		select {
		case <-ctx.Done():
			t.sessions.close()
			_ = h.Close()
		case <-finished:
		}
	}()
	err = h.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) && ctx.Err() != nil {
		return nil
	}
	return err
}

func rejectHTTP(w http.ResponseWriter, code int) {
	// Do not drain an unauthenticated or rejected body to preserve keep-alive.
	w.Header().Set("Connection", "close")
	http.Error(w, http.StatusText(code), code)
}

func (t *remoteTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Fixed categories only; never echo an unverified header, token or body.
	if code := t.checkHeaders(r); code != 0 {
		rejectHTTP(w, code)
		return
	}
	if (t.kind == "sse" && !((r.URL.Path == "/sse" && r.Method == http.MethodGet) || (r.URL.Path == "/message" && r.Method == http.MethodPost))) || (t.kind != "sse" && (r.URL.Path != "/mcp" || (r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete))) {
		rejectHTTP(w, http.StatusMethodNotAllowed)
		return
	}
	ctx, err := t.owner.authenticate(t.owner.identityHTTPContext(r.Context(), r))
	if err != nil {
		code := http.StatusForbidden
		if tenant.IsAuthenticationError(err) {
			code = http.StatusUnauthorized
		}
		rejectHTTP(w, code)
		return
	}
	if ctx == nil {
		rejectHTTP(w, http.StatusUnauthorized)
		return
	}
	principal := tenant.PrincipalFromContext(ctx)
	if principal == nil || principal.ID == "" || (!principal.ExpiresAt.IsZero() && !time.Now().Before(principal.ExpiresAt)) {
		rejectHTTP(w, http.StatusUnauthorized)
		return
	}
	r = r.WithContext(ctx)
	id := r.Header.Get("Mcp-Session-Id")
	if t.kind == "sse" {
		id = r.URL.Query().Get("sessionId")
		// SSE reconnects always issue a new ID, never adopt a caller's ID.
		if r.Method == http.MethodGet && id != "" {
			rejectHTTP(w, http.StatusNotFound)
			return
		}
	}
	if r.Method == http.MethodDelete {
		if code := t.sessions.deleteOwned(id, principal); code != 0 {
			rejectHTTP(w, code)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	stream := r.Method == http.MethodGet
	fresh := (t.kind == "sse" && stream) || (t.kind != "sse" && r.Method == http.MethodPost && id == "")
	var b *protocolBinding
	var code int
	if fresh {
		b, code = t.sessions.reserve(principal)
		if code != 0 {
			rejectHTTP(w, code)
			return
		}
		defer func() {
			t.sessions.mu.Lock()
			unused := !b.ready
			t.sessions.mu.Unlock()
			if unused {
				t.sessions.revoke(b)
			}
		}()
	}
	b, l, code := t.sessions.acquire(id, principal, b, stream)
	if code != 0 {
		if b != nil && fresh {
			t.sessions.revoke(b)
		}
		rejectHTTP(w, code)
		return
	}
	// SSE POST transfers completion ownership to a single bounded goroutine.
	transferred := false
	defer func() {
		if !transferred {
			l.finish()
		}
	}()
	if stream {
		ctx, done := leaseContext(context.WithValue(ctx, bindingKey{}, b), l)
		defer done()
		if t.kind == "sse" {
			defer t.sessions.revoke(b)
		}
		// A write blocked on a client that stopped reading must not outlive its
		// stream. This is not the short body timeout or a common WriteTimeout.
		end, _ := l.ctx.Deadline()
		defer boundResponse(w, ctx, end)()
		out := &observedResponse{ResponseWriter: w, lease: l}
		if t.kind == "sse" {
			t.sse.SSEHandler().ServeHTTP(out, r.WithContext(ctx))
		} else {
			t.http.ServeHTTP(out, r.WithContext(ctx))
		}
		return
	}
	if r.Header.Get("Content-Type") != "application/json" {
		rejectHTTP(w, http.StatusUnsupportedMediaType)
		return
	}
	body, code := t.readBody(w, r)
	if code != 0 {
		rejectHTTP(w, code)
		return
	}
	message, err := inspectMessage(body)
	if err != nil {
		rejectHTTP(w, http.StatusBadRequest)
		return
	}
	initialize := message.method == "initialize"
	if (fresh && !initialize) || (t.kind != "sse" && id != "" && initialize) || !t.sessions.begin(b, initialize) {
		code := http.StatusBadRequest
		if l.check() != nil {
			code = http.StatusNotFound
		}
		rejectHTTP(w, code)
		return
	}
	if err := l.check(); err != nil {
		rejectHTTP(w, http.StatusNotFound)
		return
	}
	if t.kind == "sse" {
		// v0.32.0 MessageHandler uses WithoutCancel, returns 202, then dispatches
		// asynchronously. Its hooks miss notifications, unknown methods, early
		// errors and responses. Use public dispatch + send APIs instead, retaining
		// the same wire protocol but an unconditional actual-completion defer.
		w.WriteHeader(http.StatusAccepted)
		ctx, done := leaseContext(context.WithoutCancel(ctx), l)
		transferred = true
		go func() {
			defer l.finish()
			defer done()
			defer func() {
				if recover() != nil {
					t.sessions.revoke(b)
				}
			}()
			if l.check() != nil {
				return
			}
			ctx = t.owner.mcp.WithContext(ctx, b.client)
			response := t.owner.mcp.HandleMessage(ctx, body)
			if initialize && (!l.initializeSuccess.Load() || l.check() != nil) {
				t.sessions.revoke(b)
				return
			}
			if response != nil && l.check() == nil {
				if err := t.sse.SendEventToSession(b.id, response); err != nil {
					t.sessions.revoke(b)
					return
				}
			}
			if initialize {
				t.sessions.commit(b)
			}
		}()
		return
	}
	ctx, done := leaseContext(ctx, l)
	defer done() // also stops the dependency's notification goroutine on nil response
	r = r.WithContext(ctx)
	r.Body = io.NopCloser(bytes.NewReader(body))
	end, _ := l.ctx.Deadline()
	defer boundResponse(w, ctx, end)()
	out := &observedResponse{ResponseWriter: w, lease: l, initialize: initialize}
	if initialize {
		defer func() {
			if !out.initializedOK() || ctx.Err() != nil || !t.sessions.commit(b) {
				t.sessions.revoke(b)
			}
		}()
	}
	// Synchronous HTTP dispatch retains its quota even after client disconnect
	// until ServeHTTP (including the actual tool handler) really returns.
	t.http.ServeHTTP(out, r)
	if initialize && out.initializedOK() {
		// net/http may have accepted only a buffered Write. Force the local
		// write/flush before committing; a failed flush must not leave a usable ID.
		_ = out.FlushError()
	}
}

// Synchronize the cancellation callback before clearing the deadline; otherwise
// a late callback could poison the next request on a reused keep-alive socket.
func boundResponse(w http.ResponseWriter, ctx context.Context, end time.Time) func() {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(end)
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(done); _ = controller.SetWriteDeadline(time.Now()) })
	return func() {
		if !stop() {
			<-done
		}
		_ = controller.SetWriteDeadline(time.Time{})
	}
}

func (t *remoteTransport) readBody(w http.ResponseWriter, r *http.Request) ([]byte, int) {
	limit := t.owner.httpLimits.MaxRequestBytes
	if r.ContentLength > limit {
		return nil, http.StatusRequestEntityTooLarge
	}
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(time.Duration(t.owner.httpLimits.ReadBodyTimeoutSeconds) * time.Second)
	if err := controller.SetReadDeadline(deadline); err != nil {
		return nil, http.StatusInternalServerError
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if clearErr := controller.SetReadDeadline(time.Time{}); clearErr != nil {
		return nil, http.StatusRequestTimeout
	}
	if err == nil {
		return body, 0
	}
	var max *http.MaxBytesError
	if errors.As(err, &max) {
		return nil, http.StatusRequestEntityTooLarge
	}
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		return nil, http.StatusRequestTimeout
	}
	return nil, http.StatusBadRequest
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}
func (t *remoteTransport) checkHeaders(r *http.Request) int {
	size := len(r.Method) + len(r.RequestURI) + len(r.Proto) + len(r.Host) + 16
	for name, values := range r.Header {
		for _, v := range values {
			size += len(name) + len(v) + 4
		}
	}
	if size > t.owner.httpLimits.MaxHeaderBytes {
		return http.StatusRequestHeaderFieldsTooLarge
	}
	// Count case-insensitively even for synthetic requests whose maps were not
	// canonicalized by net/http. Also reject proxy-coalesced identity duplicates.
	sensitive := []string{t.owner.hdrUser, t.owner.hdrEmail, t.owner.hdrToken, t.owner.hdrJWT, "Authorization", "Mcp-Session-Id", "Content-Type", "Content-Encoding"}
	for _, target := range sensitive {
		count := 0
		for key, values := range r.Header {
			if strings.EqualFold(key, target) {
				count += len(values)
				for _, v := range values {
					if strings.ContainsAny(v, ",\r\n\x00") {
						return http.StatusBadRequest
					}
				}
			}
		}
		if count > 1 {
			return http.StatusBadRequest
		}
	}
	if !t.owner.jwtIdentity && len(r.Header.Values(t.owner.hdrJWT)) != 0 {
		return http.StatusBadRequest
	}
	if r.Header.Get("Authorization") != "" && !strings.EqualFold(t.owner.hdrToken, "Authorization") && !strings.EqualFold(t.owner.hdrJWT, "Authorization") {
		return http.StatusBadRequest // no competing, silently ignored credential channel
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return http.StatusUnsupportedMediaType
	}
	if r.Method != http.MethodPost && (r.ContentLength != 0 || len(r.TransferEncoding) != 0) {
		return http.StatusBadRequest
	}
	query, err := parseSessionQuery(r)
	if err != nil {
		return http.StatusBadRequest
	}
	if t.kind == "sse" {
		if r.Header.Get("Mcp-Session-Id") != "" {
			return http.StatusBadRequest
		}
	} else if len(query) != 0 {
		return http.StatusBadRequest
	}
	return 0
}

// observedResponse observes only scalar state, never buffers streaming output.
// The dependency serializes stream writes; the observer's own fields are also
// locked because HTTP notifications may be written by a separate goroutine.
// Unwrap preserves ResponseController, including deadlines, flush and hijack.
type observedResponse struct {
	http.ResponseWriter
	mu            sync.Mutex
	lease         *requestLease
	initialize    bool
	status, bytes int
	failed        bool
}

func (w *observedResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *observedResponse) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status != 0 {
		return
	}
	if w.initialize && !w.lease.initializeSuccess.Load() {
		w.Header().Del("Mcp-Session-Id")
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *observedResponse) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lease.check() != nil {
		w.failed = true
		return 0, errSessionClosed
	}
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	if err != nil || n != len(data) {
		w.failed = true
		w.lease.cancel()
	}
	return n, err
}
func (w *observedResponse) Flush() { _ = w.FlushError() }
func (w *observedResponse) FlushError() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.lease.check() != nil {
		w.failed = true
		return errSessionClosed
	}
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil {
		w.failed = true
		w.lease.cancel()
	}
	return err
}
func (w *observedResponse) initializedOK() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lease.initializeSuccess.Load() && !w.failed && w.status == http.StatusOK && w.bytes > 0
}

type discardResponse struct{ header http.Header }

func (d *discardResponse) Header() http.Header       { return d.header }
func (*discardResponse) WriteHeader(int)             {}
func (*discardResponse) Write(b []byte) (int, error) { return len(b), nil }

// Keep compile-time checks near the transport boundary.
var _ http.Handler = (*remoteTransport)(nil)
var _ http.Flusher = (*observedResponse)(nil)
var _ server.SessionIdManager = (*protocolSessions)(nil)
