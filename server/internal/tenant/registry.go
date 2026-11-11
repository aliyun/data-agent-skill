// Package tenant authenticates remote principals and owns their isolated clients
// and session managers. Local storage identity is not a cloud authorization ACL.
package tenant

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
	credential "github.com/aliyun/credentials-go/credentials"
)

type ctxKey string

const (
	ctxKeyUser  ctxKey = "identity-user"
	ctxKeyEmail ctxKey = "identity-email"
	ctxKeyToken ctxKey = "identity-token"
)

// WithIdentity carries untrusted transport values. It does not authenticate.
func WithIdentity(ctx context.Context, user, email, token string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyUser, user)
	ctx = context.WithValue(ctx, ctxKeyEmail, email)
	return context.WithValue(ctx, ctxKeyToken, token)
}
func IdentityFromContext(ctx context.Context) (user, email, token string) {
	user, _ = ctx.Value(ctxKeyUser).(string)
	email, _ = ctx.Value(ctxKeyEmail).(string)
	token, _ = ctx.Value(ctxKeyToken).(string)
	return
}

type GatewayCaller struct{ UserID, Email, Group, RoleArn string }
type gatewayIdentityKey struct{}

func WithGatewayIdentity(ctx context.Context) (context.Context, *GatewayCaller) {
	caller := &GatewayCaller{}
	return context.WithValue(ctx, gatewayIdentityKey{}, caller), caller
}

type Tenant struct {
	Key             string // canonical principal ID, never a raw user or role name
	Group           string
	RoleArn         string
	Client          *dataagent.Client
	Manager         *session.Manager
	Defaults        config.IdentityGroup
	provider        credential.Credential
	mu              sync.Mutex
	snapshot        *dataagent.Credential
	roleSessionName string
	root            *os.Root
}

// Legacy analysis may retain its same-principal snapshot on refresh failure.
func (t *Tenant) currentCredential() *dataagent.Credential {
	t.mu.Lock()
	defer t.mu.Unlock()
	current, err := dataagent.CredentialFromProvider(t.provider)
	if err != nil {
		log.Printf("tenant credential refresh failed (using last snapshot)")
		return t.snapshot
	}
	t.snapshot = current
	return current
}
func (t *Tenant) strictCredential() (*dataagent.Credential, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	current, err := dataagent.CredentialFromProvider(t.provider)
	if err != nil {
		return nil, &dataagent.GatewayError{Code: "GATEWAY_CREDENTIAL_ERROR", Message: "Gateway credentials unavailable."}
	}
	t.snapshot = current
	return current, nil
}

var credentialQueryParams = regexp.MustCompile(`(?i)(AccessKeyId|AccessKeySecret|SecurityToken|Signature)=[^&"\s]*`)

func redactCredentials(s string) string {
	return credentialQueryParams.ReplaceAllStringFunc(s, func(m string) string {
		if i := strings.Index(m, "="); i > 0 {
			return m[:i+1] + "<redacted>"
		}
		return m
	})
}

const maxInitializations = 8
const initializationTimeout = 15 * time.Second
const maxRestores = 4

type initialization struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	auth   verifiedIdentity
	tenant *Tenant
	err    error
}

type Registry struct {
	baseCtx       context.Context
	cancel        context.CancelFunc
	cfg           config.Config
	base          credential.Credential
	mu            sync.Mutex
	tenants       map[string]*Tenant
	inflight      map[string]*initialization
	closed        bool
	wg            sync.WaitGroup
	closeOnce     sync.Once
	closeErr      error
	initLimit     int
	initTimeout   time.Duration
	restores      chan struct{}
	newProviderFn func(string, string) (credential.Credential, error)
	restoreFn     func(context.Context, *session.Manager)
}

func NewRegistry(baseCtx context.Context, cfg config.Config, base credential.Credential) *Registry {
	// Freeze mutable configuration collections: no hot-reload or external map
	// mutation can silently alter a cached authorization context.
	cfg.ApplyDefaults()
	if cfg.Identity.MaxTenants == 0 {
		cfg.Identity.MaxTenants = 128
	}
	groups := make(map[string]config.IdentityGroup, len(cfg.Identity.Groups))
	for name, g := range cfg.Identity.Groups {
		g.Users = append([]string(nil), g.Users...)
		groups[name] = g
	}
	cfg.Identity.Groups = groups
	if cfg.Identity.Default != nil {
		g := *cfg.Identity.Default
		g.Users = append([]string(nil), g.Users...)
		cfg.Identity.Default = &g
	}
	cfg.Identity.JWT.Secrets = append([]string(nil), cfg.Identity.JWT.Secrets...)
	prefix := cfg.Identity.Prefix()
	cfg.Identity.SessionNamePrefix = &prefix
	ctx, cancel := context.WithCancel(baseCtx)
	r := &Registry{baseCtx: ctx, cancel: cancel, cfg: cfg, base: base, tenants: map[string]*Tenant{}, inflight: map[string]*initialization{}, initLimit: maxInitializations, initTimeout: initializationTimeout, restores: make(chan struct{}, maxRestores)}
	r.newProviderFn = r.newRoleProvider
	r.restoreFn = func(ctx context.Context, m *session.Manager) { m.RestoreSessions(ctx) }
	return r
}

// Authenticate performs only token verification, strict identity parsing and
// per-request mapping. It never creates providers, tenants or disk roots.
func (r *Registry) Authenticate(ctx context.Context) (context.Context, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.baseCtx.Err() != nil {
		return nil, errClosed
	}
	id := r.cfg.Identity
	user, email, token := IdentityFromContext(ctx)
	expectedHash, actualHash := sha256.Sum256([]byte(id.AuthToken)), sha256.Sum256([]byte(token))
	if !id.Enabled || !id.RequireIdentity || strings.TrimSpace(id.AuthToken) == "" || !config.ValidNamespace(id.Namespace) || subtle.ConstantTimeCompare(expectedHash[:], actualHash[:]) != 1 {
		return nil, errAuthentication
	}
	var claims *Claims
	var err error
	mode := "headers"
	rawJWT, hasJWT := ctx.Value(jwtCtxKey{}).(string)
	if hasJWT {
		if !id.JWT.Enabled {
			return nil, errAuthentication
		}
		mode = "jwt"
		claims, err = VerifyJWT(rawJWT, id.JWT.AllSecrets()...)
	} else {
		claims = &Claims{}
		claims.UserID, err = config.NormalizeIdentity(user)
		if err == nil {
			claims.Email, err = config.NormalizeIdentity(email)
		}
	}
	if err != nil {
		return nil, errAuthentication
	}
	principal, err := makePrincipal(id.Namespace, mode, claims)
	if err != nil {
		return nil, errAuthentication
	}
	sessionValue, err := claims.Field(id.SessionNameClaim)
	if err != nil {
		return nil, errAuthentication
	}
	// Legacy Header callers may not supply the selected JWT-only audit claim.
	if sessionValue == "" && (mode == "headers" || id.SessionNameClaim == "" || id.SessionNameClaim == "user_id") {
		sessionValue = principal.SubjectValue
	}
	if sessionValue == "" {
		return nil, errAuthentication
	}
	group, mapped, err := r.cfg.ResolveGroup(config.IdentityClaims{AuthMode: mode, TenantID: claims.TenantID, AgentID: claims.AgentID, UserID: claims.UserID, Email: claims.Email})
	if err != nil {
		return nil, err
	}
	auth := &verifiedIdentity{registry: r, principal: *principal, group: group, mapped: mapped, sessionValue: sessionValue, roleSessionName: sessionName(id.Prefix(), sessionValue)}
	return context.WithValue(ctx, verifiedKey{}, auth), nil
}

// Resolve accepts only Authenticate's private, registry-bound capability. It
// never trusts raw WithClaims/WithIdentity/WithJWT and never falls back to AK.
func (r *Registry) Resolve(ctx context.Context) (*Tenant, error) {
	caller, _ := ctx.Value(gatewayIdentityKey{}).(*GatewayCaller)
	if caller != nil {
		*caller = GatewayCaller{}
	}
	auth, _ := ctx.Value(verifiedKey{}).(*verifiedIdentity)
	if auth == nil || auth.registry != r || !auth.principal.ExpiresAt.IsZero() && !time.Now().Before(auth.principal.ExpiresAt) {
		return nil, gatewayFailure(caller, errAuthentication)
	}
	t, err := r.tenant(ctx, auth)
	if err != nil {
		return nil, gatewayFailure(caller, err)
	}
	if caller != nil {
		*caller = GatewayCaller{UserID: auth.principal.UserID, Email: auth.principal.Email, Group: auth.group, RoleArn: auth.mapped.RoleArn}
	}
	return t, nil
}

func gatewayFailure(caller *GatewayCaller, err error) error {
	if caller == nil {
		return err
	}
	code, message := "GATEWAY_CREDENTIAL_ERROR", "Gateway credentials unavailable."
	if IsAuthenticationError(err) || errors.Is(err, errMappingChanged) {
		code, message = "GATEWAY_IDENTITY_REJECTED", "Gateway identity rejected."
	}
	if errors.Is(err, config.ErrIdentityGroupConflict) || errors.Is(err, config.ErrIdentityUnmapped) {
		code, message = "GATEWAY_IDENTITY_UNMAPPED", "Gateway identity has no role mapping."
	}
	return &dataagent.GatewayError{Code: code, Message: message}
}

func sameAuthorization(t *Tenant, a *verifiedIdentity) bool {
	return t.Group == a.group && t.RoleArn == a.mapped.RoleArn && t.roleSessionName == a.roleSessionName && t.Defaults.WorkspaceID == a.mapped.WorkspaceID && t.Defaults.CustomAgentID == a.mapped.CustomAgentID && t.Defaults.Mode == a.mapped.Mode
}
func samePending(a, b *verifiedIdentity) bool {
	return a.group == b.group && a.roleSessionName == b.roleSessionName && a.mapped.RoleArn == b.mapped.RoleArn && a.mapped.WorkspaceID == b.mapped.WorkspaceID && a.mapped.CustomAgentID == b.mapped.CustomAgentID && a.mapped.Mode == b.mapped.Mode
}

func (r *Registry) tenant(ctx context.Context, a *verifiedIdentity) (*Tenant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.closed || r.baseCtx.Err() != nil {
		r.mu.Unlock()
		return nil, errClosed
	}
	key := a.principal.ID
	if t := r.tenants[key]; t != nil {
		r.mu.Unlock()
		if !sameAuthorization(t, a) {
			return nil, errMappingChanged
		}
		return t, nil
	}
	job := r.inflight[key]
	if job != nil && !samePending(&job.auth, a) {
		r.mu.Unlock()
		return nil, errMappingChanged
	}
	if job == nil {
		if len(r.tenants)+len(r.inflight) >= r.cfg.Identity.MaxTenants || len(r.inflight) >= r.initLimit {
			r.mu.Unlock()
			return nil, errBusy
		}
		jobCtx, cancel := context.WithTimeout(r.baseCtx, r.initTimeout)
		job = &initialization{ctx: jobCtx, cancel: cancel, done: make(chan struct{}), auth: *a}
		r.inflight[key] = job
		r.wg.Add(1)
		go r.initialize(job)
	}
	r.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-job.ctx.Done():
		if r.baseCtx.Err() != nil {
			return nil, errClosed
		}
		// Normal completion cancels the timer as well; prefer its published
		// result when both channels are ready.
		select {
		case <-job.done:
			return completedInitialization(ctx, job, a)
		default:
			return nil, errInitTimeout
		}
	case <-job.done:
		return completedInitialization(ctx, job, a)
	}
}

func completedInitialization(ctx context.Context, job *initialization, a *verifiedIdentity) (*Tenant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if job.err != nil {
		return nil, job.err
	}
	if !a.principal.ExpiresAt.IsZero() && !time.Now().Before(a.principal.ExpiresAt) {
		return nil, errAuthentication
	}
	return job.tenant, nil
}

func (r *Registry) initialize(job *initialization) {
	defer r.wg.Done()
	defer job.cancel()
	t, err := r.buildTenant(job.ctx, &job.auth)
	r.mu.Lock()
	if err == nil && (job.ctx.Err() != nil || r.closed) {
		err = errInitTimeout
	}
	if err == nil {
		r.tenants[t.Key] = t
		r.wg.Add(1)
		go r.runTenant(t)
	}
	r.mu.Unlock()
	// Timed out non-cancellable SDK work keeps its slot until ALL actual work
	// and handle cleanup finish. Never publish late or release at waiter timeout.
	if err != nil && t != nil {
		_ = closeTenant(t)
	}
	r.mu.Lock()
	delete(r.inflight, job.auth.principal.ID)
	job.tenant, job.err = t, err
	close(job.done)
	r.mu.Unlock()
}

func (r *Registry) buildTenant(ctx context.Context, a *verifiedIdentity) (*Tenant, error) {
	if ctx.Err() != nil {
		return nil, errInitTimeout
	}
	provider, err := r.newProviderFn(a.sessionValue, a.mapped.RoleArn)
	if err != nil {
		return nil, errors.New("assume role credential provider unavailable")
	}
	t := &Tenant{Key: a.principal.ID, Group: a.group, RoleArn: a.mapped.RoleArn, Defaults: a.mapped, roleSessionName: a.roleSessionName, provider: provider}
	if ctx.Err() != nil {
		return t, errInitTimeout
	}
	if _, err = t.strictCredential(); err != nil {
		return t, err
	}
	if ctx.Err() != nil {
		return t, errInitTimeout
	}
	opts := []dataagent.ClientOption{
		dataagent.WithCredentialProvider(t.currentCredential), dataagent.WithStrictCredentialProvider(t.strictCredential),
		dataagent.WithGatewayOptions(dataagent.GatewayOptions{Endpoint: r.cfg.AgentGateway.Endpoint, RequestTimeout: time.Duration(r.cfg.AgentGateway.RequestTimeoutSeconds) * time.Second, MaxResponseBytes: r.cfg.AgentGateway.MaxResponseBytes}),
		dataagent.WithDataAgentEndpoint(r.cfg.DataAgentEndpoint), dataagent.WithDMSEnterpriseEndpoint(r.cfg.DMSEnterpriseEndpoint),
	}
	if r.cfg.DMSUnit != "" {
		opts = append(opts, dataagent.WithDMSUnit(r.cfg.DMSUnit))
	}
	workspace := a.mapped.WorkspaceID
	if workspace == "" {
		workspace = r.cfg.WorkspaceID
	}
	if workspace != "" {
		opts = append(opts, dataagent.WithWorkspaceID(workspace))
	}
	t.Client = dataagent.NewClient(t.snapshot, r.cfg.Region, opts...)
	root, dir, err := openTenantRoot(r.cfg.SessionsDir, t.Key)
	if err != nil {
		return t, err
	}
	t.root = root
	if ctx.Err() != nil {
		return t, errInitTimeout
	}
	t.Manager, err = session.NewManager(t.Client, dir)
	if err != nil {
		return t, errors.New("tenant session storage unavailable")
	}
	return t, nil
}

func (r *Registry) runTenant(t *Tenant) {
	defer r.wg.Done()
	select {
	case r.restores <- struct{}{}:
	case <-r.baseCtx.Done():
		return
	}
	if r.baseCtx.Err() == nil {
		r.restoreFn(r.baseCtx, t.Manager)
	}
	<-r.restores
	if r.baseCtx.Err() == nil {
		t.Manager.RunHousekeeping(r.baseCtx)
	}
}

func closeTenant(t *Tenant) error {
	var errs []error
	if t.Manager != nil {
		if err := t.Manager.Close(); err != nil {
			errs = append(errs, errors.New("tenant manager close failed"))
		}
	}
	if t.root != nil {
		if err := t.root.Close(); err != nil {
			errs = append(errs, errors.New("tenant root close failed"))
		}
	}
	return errors.Join(errs...)
}

// PerfStats reports tenant cache occupancy and the aggregate watcher counts
// across all tenant session managers for the perf log.
func (r *Registry) PerfStats() map[string]any {
	r.mu.Lock()
	tenants := make([]*Tenant, 0, len(r.tenants))
	for _, t := range r.tenants {
		tenants = append(tenants, t)
	}
	initializing := len(r.inflight)
	closed := r.closed
	r.mu.Unlock()

	watchers := map[string]int{}
	for _, t := range tenants {
		if t.Manager == nil {
			continue
		}
		for k, v := range t.Manager.WatcherStats() {
			watchers[k] += v
		}
	}
	return map[string]any{
		"tenants":       len(tenants),
		"max_tenants":   r.cfg.Identity.MaxTenants,
		"initializing":  initializing,
		"restore_queue": len(r.restores),
		"closed":        closed,
		"watchers":      watchers,
	}
}

// Close waits for actual SDK work, restoration and housekeeping before closing
// managers/roots. credentials-go cannot be interrupted; Close may wait for its
// configured network timeouts or for an external provider to return.
func (r *Registry) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.cancel()
		tenants := r.tenants
		r.mu.Unlock()
		// Cancel manager work immediately, including operations created before
		// that tenant reached the restoration queue. Manager.Close joins its
		// operations/restoration/watchers before releasing its own root.
		var closing sync.WaitGroup
		errorsByTenant := make(chan error, len(tenants))
		for _, t := range tenants {
			closing.Add(1)
			go func() { defer closing.Done(); errorsByTenant <- closeTenant(t) }()
		}
		r.wg.Wait()
		closing.Wait()
		close(errorsByTenant)
		for err := range errorsByTenant {
			r.closeErr = errors.Join(r.closeErr, err)
		}
		r.mu.Lock()
		r.tenants = map[string]*Tenant{}
		r.mu.Unlock()
	})
	return r.closeErr
}

// Retain the renewable default SDK provider. STS timeouts are milliseconds.
func (r *Registry) newRoleProvider(sessionValue, roleArn string) (credential.Credential, error) {
	return &rotatingRoleProvider{base: r.base, build: func(base *dataagent.Credential) (credential.Credential, error) {
		conf := new(credential.Config).SetType("ram_role_arn").SetAccessKeyId(base.AccessKeyID).SetAccessKeySecret(base.AccessKeySecret).SetSecurityToken(base.SecurityToken).SetRoleArn(roleArn).SetRoleSessionName(sessionName(r.cfg.Identity.Prefix(), sessionValue)).SetRoleSessionExpiration(r.cfg.STS.SessionExpiration).SetSTSEndpoint(r.cfg.STSEndpoint()).SetConnectTimeout(5000).SetTimeout(10000)
		return credential.NewCredential(conf)
	}}, nil
}

type rotatingRoleProvider struct {
	mu       sync.Mutex
	base     credential.Credential
	previous *dataagent.Credential
	role     credential.Credential
	build    func(*dataagent.Credential) (credential.Credential, error)
}

func (p *rotatingRoleProvider) GetCredential() (*credential.CredentialModel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	base, err := dataagent.CredentialFromProvider(p.base)
	if err != nil {
		return nil, errors.New("base credential provider unavailable")
	}
	if p.role == nil || p.previous == nil || *base != *p.previous {
		role, err := p.build(base)
		if err != nil || role == nil {
			return nil, errors.New("role credential provider unavailable")
		}
		p.role, p.previous = role, base
	}
	current, err := dataagent.CredentialFromProvider(p.role)
	if err != nil {
		return nil, errors.New("role credential provider unavailable")
	}
	return &credential.CredentialModel{AccessKeyId: &current.AccessKeyID, AccessKeySecret: &current.AccessKeySecret, SecurityToken: &current.SecurityToken}, nil
}
func (p *rotatingRoleProvider) GetAccessKeyId() (*string, error) {
	m, err := p.GetCredential()
	if err != nil {
		return nil, err
	}
	return m.AccessKeyId, nil
}
func (p *rotatingRoleProvider) GetAccessKeySecret() (*string, error) {
	m, err := p.GetCredential()
	if err != nil {
		return nil, err
	}
	return m.AccessKeySecret, nil
}
func (p *rotatingRoleProvider) GetSecurityToken() (*string, error) {
	m, err := p.GetCredential()
	if err != nil {
		return nil, err
	}
	return m.SecurityToken, nil
}
func (p *rotatingRoleProvider) GetBearerToken() *string { s := ""; return &s }
func (p *rotatingRoleProvider) GetType() *string        { s := "ram_role_arn"; return &s }

var sessionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._@-]`)

func sessionName(prefix, key string) string {
	name := sessionNameSanitizer.ReplaceAllString(key, "_")
	if prefix != "" {
		name = sessionNameSanitizer.ReplaceAllString(prefix, "_") + "-" + name
	}
	if len(name) > 64 {
		name = name[:64]
	}
	for len(name) < 2 {
		name += "_"
	}
	return name
}
