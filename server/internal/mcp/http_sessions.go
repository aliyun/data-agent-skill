package mcp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

var errSessionClosed = errors.New("protocol session unavailable")

type bindingKey struct{}
type leaseKey struct{}

// All mutable binding fields are protected by protocolSessions.mu. Closing records
// keep their capacity until their last real dispatch and connection have exited.
// There is no ID reuse, timeout-based release of running work, or tombstone list.
type protocolBinding struct {
	store                                *protocolSessions
	id, owner                            string
	client                               server.ClientSession
	ctx                                  context.Context
	cancel                               context.CancelFunc
	expires, touched                     time.Time
	ready, initializing, closing, stream bool
	inflight                             int
}

type protocolSessions struct {
	mu        sync.Mutex
	limits    config.HTTP
	ctx       context.Context
	closed    bool
	sse       bool
	all       map[*protocolBinding]struct{}
	ids       map[string]*protocolBinding
	owners    map[string]int
	inflight  int
	cleanup   func(string)
	drained   chan struct{}
	drainOnce sync.Once
}

func newProtocolSessions(ctx context.Context, limits config.HTTP) *protocolSessions {
	return &protocolSessions{ctx: ctx, limits: limits, all: make(map[*protocolBinding]struct{}), ids: make(map[string]*protocolBinding), owners: make(map[string]int), drained: make(chan struct{})}
}

func (p *protocolSessions) reserve(principal *tenant.Principal) (*protocolBinding, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil {
		return nil, http.StatusServiceUnavailable
	}
	if len(p.all) >= p.limits.MaxSessions || p.owners[principal.ID] >= p.limits.MaxSessionsPerPrincipal {
		return nil, http.StatusTooManyRequests
	}
	ctx, cancel := context.WithCancel(p.ctx)
	b := &protocolBinding{store: p, owner: principal.ID, ctx: ctx, cancel: cancel, expires: principal.ExpiresAt, touched: time.Now()}
	p.all[b] = struct{}{}
	p.owners[b.owner]++
	return b, 0
}

func (p *protocolSessions) liveLocked(b *protocolBinding) bool {
	return b != nil && !p.closed && !b.closing && b.ctx.Err() == nil && (b.expires.IsZero() || time.Now().Before(b.expires))
}

func (p *protocolSessions) bind(b *protocolBinding, client server.ClientSession) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.liveLocked(b) || b.id != "" || client == nil || client.SessionID() == "" || p.ids[client.SessionID()] != nil {
		return false
	}
	b.id, b.client = client.SessionID(), client
	p.ids[b.id] = b
	return true
}

// acquire validates ownership and reserves dispatch/stream capacity atomically,
// before reading any message body. Fresh initialization reservations use b directly.
func (p *protocolSessions) acquire(id string, principal *tenant.Principal, b *protocolBinding, stream bool) (*protocolBinding, *requestLease, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b == nil {
		b = p.ids[id]
	}
	if !p.liveLocked(b) || b.owner != principal.ID || (id != "" && !b.ready && (!p.sse || stream || b.initializing)) {
		return b, nil, http.StatusNotFound
	}
	if stream {
		if b.stream {
			return nil, nil, http.StatusConflict
		}
		b.stream = true
	} else {
		if p.inflight >= p.limits.MaxInflightRequests || b.inflight >= p.limits.MaxInflightPerSession {
			return b, nil, http.StatusTooManyRequests
		}
		b.inflight++
		p.inflight++
	}
	// A verified renewal of the same principal can extend a HTTP session, but
	// never extends an already-open stream's independently captured deadline.
	if principal.ExpiresAt.After(b.expires) {
		b.expires = principal.ExpiresAt
	}
	b.touched = time.Now()
	end := time.Now().Add(time.Duration(p.limits.MaxStreamLifetimeSeconds) * time.Second)
	if !principal.ExpiresAt.IsZero() && principal.ExpiresAt.Before(end) {
		end = principal.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(b.ctx, end)
	l := &requestLease{binding: b, ctx: ctx, cancel: cancel, stream: stream}
	return b, l, 0
}

func (p *protocolSessions) begin(b *protocolBinding, initialize bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.liveLocked(b) {
		return false
	}
	if initialize {
		if b.ready || b.initializing || b.inflight != 1 {
			return false
		}
		b.initializing = true
		return true
	}
	return b.ready
}

func (p *protocolSessions) commit(b *protocolBinding) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.liveLocked(b) || !b.initializing || b.id == "" {
		return false
	}
	b.ready, b.initializing = true, false
	return true
}

// DELETE is a control operation, not a new MCP dispatch: it must remain usable
// when every dispatch slot is occupied by work the caller wants to terminate.
func (p *protocolSessions) deleteOwned(id string, principal *tenant.Principal) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.ids[id]
	if !p.liveLocked(b) || !b.ready || b.owner != principal.ID {
		return http.StatusNotFound
	}
	p.revokeLocked(b)
	return 0
}

func (p *protocolSessions) revoke(b *protocolBinding) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revokeLocked(b)
}
func (p *protocolSessions) revokeLocked(b *protocolBinding) {
	if b == nil {
		return
	}
	b.closing = true
	b.cancel()
	p.collectLocked(b)
}
func (p *protocolSessions) collectLocked(b *protocolBinding) {
	if !b.closing || b.stream || b.inflight != 0 {
		return
	}
	if _, exists := p.all[b]; !exists {
		return
	}
	// The cleanup callback is bounded and uses the dependency's public DELETE
	// handler, whose SessionIdManager.Terminate below does not reenter this lock.
	if b.id != "" && p.cleanup != nil {
		p.cleanup(b.id)
	}
	delete(p.ids, b.id)
	delete(p.all, b)
	p.owners[b.owner]--
	if p.owners[b.owner] == 0 {
		delete(p.owners, b.owner)
	}
	if p.closed && len(p.all) == 0 {
		p.drainOnce.Do(func() { close(p.drained) })
	}
}
func (p *protocolSessions) sweep(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for b := range p.all {
		if (!b.expires.IsZero() && !now.Before(b.expires)) || (!b.stream && b.inflight == 0 && now.Sub(b.touched) >= time.Duration(p.limits.SessionIdleTimeoutSeconds)*time.Second) {
			p.revokeLocked(b)
		}
	}
}
func (p *protocolSessions) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for b := range p.all {
		p.revokeLocked(b)
	}
	if len(p.all) == 0 {
		p.drainOnce.Do(func() { close(p.drained) })
	}
}

// Generate has no request context. Only the HTTP context callback can associate
// this unpredictable ID with an already reserved owner, before dispatch/publication.
func (p *protocolSessions) Generate() string { return "mcp-session-" + uuid.NewString() }
func (p *protocolSessions) Validate(id string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	b := p.ids[id]
	if !p.liveLocked(b) || !b.ready {
		// The outer handler already checked issuance/ownership. If revocation
		// races the dependency's second validation, preserve its 404 path.
		return true, nil
	}
	return false, nil
}

// External DELETE never calls this directly; the outer handler revokes first and
// invokes dependency cleanup only after all actual users have returned.
func (p *protocolSessions) Terminate(string) (bool, error) { return false, nil }

type requestLease struct {
	binding           *protocolBinding
	ctx               context.Context
	cancel            context.CancelFunc
	stream            bool
	once              sync.Once
	initializeSuccess atomic.Bool
}

func (l *requestLease) finish() {
	l.once.Do(func() {
		l.cancel()
		p, b := l.binding.store, l.binding
		p.mu.Lock()
		defer p.mu.Unlock()
		if l.stream {
			b.stream = false
		} else {
			b.inflight--
			p.inflight--
		}
		b.touched = time.Now()
		p.collectLocked(b)
	})
}
func (l *requestLease) check() error {
	p := l.binding.store
	p.mu.Lock()
	defer p.mu.Unlock()
	if l.ctx.Err() != nil || !p.liveLocked(l.binding) {
		return errSessionClosed
	}
	return nil
}
func leaseFrom(ctx context.Context) *requestLease {
	l, _ := ctx.Value(leaseKey{}).(*requestLease)
	return l
}

// Reassociate cancellation inside actual business handlers as well as dispatch.
// Values survive mcp-go's WithoutCancel; cancellation alone does not. No release
// occurs here: the adapter/outer synchronous handler owns actual completion.
func leaseContext(ctx context.Context, l *requestLease) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(l.ctx, cancel)
	if l.ctx.Err() != nil {
		cancel()
	}
	return context.WithValue(ctx, leaseKey{}, l), func() { stop(); cancel() }
}
func remoteLeaseMiddleware(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		if l := leaseFrom(ctx); l != nil {
			var done func()
			ctx, done = leaseContext(ctx, l)
			defer done()
			if err := l.check(); err != nil {
				return nil, err
			}
		}
		defer func() {
			if recover() != nil {
				result = nil
				err = errors.New("tool execution failed")
			}
		}()
		return next(ctx, req)
	}
}

func remoteHooks() *server.Hooks {
	h := &server.Hooks{}
	h.AddOnRegisterSession(func(ctx context.Context, client server.ClientSession) {
		if b, _ := ctx.Value(bindingKey{}).(*protocolBinding); b != nil {
			// Only SSE's first GET binds here. HTTP GET already has an issued ID.
			b.store.mu.Lock()
			fresh := b.id == ""
			b.store.mu.Unlock()
			if fresh && !b.store.bind(b, client) {
				b.store.revoke(b)
			}
		}
	})
	h.AddOnUnregisterSession(func(ctx context.Context, _ server.ClientSession) {
		if b, _ := ctx.Value(bindingKey{}).(*protocolBinding); b != nil && b.store.sse {
			b.store.revoke(b)
		}
	})
	h.AddOnRequestInitialization(func(ctx context.Context, _ any, _ any) error {
		if l := leaseFrom(ctx); l != nil {
			return l.check()
		}
		return nil // stdio remains a process-local identity
	})
	h.AddAfterInitialize(func(ctx context.Context, _ any, _ *mcp.InitializeRequest, _ *mcp.InitializeResult) {
		if l := leaseFrom(ctx); l != nil {
			l.initializeSuccess.Store(true)
		}
	})
	// OnError/OnSuccess are NOT completion callbacks: errors for an unrelated
	// notification can use the same context; unknown methods/notifications and
	// initialization-hook failures omit them entirely in mcp-go v0.32.0.
	return h
}
