// Package tenant maps upstream end-user identities (forwarded as HTTP
// headers by callers such as Feishu Aily, gateways, or portals) to per-user
// Alibaba Cloud RAM roles (STS AssumeRole) and manages isolated Data Agent
// clients and session managers for each tenant.
//
// Flow (matches the multi-tenant architecture):
//
//	identity headers (user / email / token)  →  group → RAM role ARN
//	→ AssumeRole via credentials-go ram_role_arn provider (auto-refresh)
//	→ per-tenant dataagent.Client + session.Manager (own sessions dir)
//
// RAM access policies and DMS data permissions attached to the role decide
// what each user can query; the MCP server itself performs no data-level
// authorization.
package tenant

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	credential "github.com/aliyun/credentials-go/credentials"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
)

type ctxKey string

const (
	ctxKeyUser  ctxKey = "identity-user"
	ctxKeyEmail ctxKey = "identity-email"
	ctxKeyToken ctxKey = "identity-token"
)

// WithIdentity stores the end-user identity headers in the request context.
func WithIdentity(ctx context.Context, user, email, token string) context.Context {
	ctx = context.WithValue(ctx, ctxKeyUser, user)
	ctx = context.WithValue(ctx, ctxKeyEmail, email)
	ctx = context.WithValue(ctx, ctxKeyToken, token)
	return ctx
}

// IdentityFromContext extracts the identity previously stored by WithIdentity.
func IdentityFromContext(ctx context.Context) (user, email, token string) {
	user, _ = ctx.Value(ctxKeyUser).(string)
	email, _ = ctx.Value(ctxKeyEmail).(string)
	token, _ = ctx.Value(ctxKeyToken).(string)
	return
}

// Tenant bundles the per-user client and session manager.
type Tenant struct {
	Key     string // upstream user id (or email fallback)
	Group   string // identity group name ("default" for the catch-all)
	RoleArn string
	Client  *dataagent.Client
	Manager *session.Manager
	// Defaults carries the group-level session defaults applied when a tool
	// call omits them (custom_agent_id, mode).
	Defaults config.IdentityGroup

	provider credential.Credential // ram_role_arn provider with built-in refresh
	mu       sync.Mutex
	snapshot *dataagent.Credential
}

// currentCredential pulls the current STS credential from the provider.
// credentials-go caches the AssumeRole result internally and refreshes it
// ahead of expiry, so this is cheap on the hot path. On provider errors the
// last known snapshot is returned so in-flight work can proceed.
func (t *Tenant) currentCredential() *dataagent.Credential {
	t.mu.Lock()
	defer t.mu.Unlock()

	model, err := t.provider.GetCredential()
	if err != nil {
		log.Printf("tenant %s: refresh STS credential: %v (using last snapshot)", t.Key, redactCredentials(err.Error()))
		return t.snapshot
	}
	ak, sk, token := strVal(model.AccessKeyId), strVal(model.AccessKeySecret), strVal(model.SecurityToken)
	if t.snapshot == nil || t.snapshot.AccessKeyID != ak || t.snapshot.SecurityToken != token {
		t.snapshot = &dataagent.Credential{
			AccessKeyID:     ak,
			AccessKeySecret: sk,
			SecurityToken:   token,
		}
	}
	return t.snapshot
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// credentialQueryParams matches query-string parameters that must never reach
// the log. STS AssumeRole (2015-04-01) signs through the query string, so an
// error from the credentials provider embeds the full URL — including the
// caller's AccessKeyId and request signature.
var credentialQueryParams = regexp.MustCompile(`(?i)(AccessKeyId|AccessKeySecret|SecurityToken|Signature)=[^&"\s]*`)

// redactCredentials strips credential-bearing query parameters from text that
// is about to be logged.
func redactCredentials(s string) string {
	return credentialQueryParams.ReplaceAllStringFunc(s, func(m string) string {
		if i := strings.Index(m, "="); i > 0 {
			return m[:i+1] + "<redacted>"
		}
		return m
	})
}

// Registry resolves request identities to tenants, creating them lazily.
type Registry struct {
	baseCtx context.Context // process-lifetime context for tenant daemons
	cfg     config.Config
	base    *dataagent.Credential // long-term AK/SK used to call AssumeRole

	mu      sync.Mutex
	tenants map[string]*Tenant // keyed by upstream user identity (user id / email)

	// newProviderFn builds the STS credential provider; overridable in tests.
	newProviderFn func(key, roleArn string) (credential.Credential, error)
}

// NewRegistry creates a tenant registry. base must be a long-term AK/SK
// credential with sts:AssumeRole permission on the mapped roles. baseCtx
// scopes the per-tenant background daemons (SSE watchers, housekeeping) to
// the server process lifetime rather than a single request.
func NewRegistry(baseCtx context.Context, cfg config.Config, base *dataagent.Credential) *Registry {
	r := &Registry{baseCtx: baseCtx, cfg: cfg, base: base, tenants: make(map[string]*Tenant)}
	r.newProviderFn = r.newRoleProvider
	return r
}

// Resolve returns the tenant for the identity carried in ctx.
//
// identity.auth_token authenticates every request first, on both paths: it is
// what proves the call came from the trusted upstream, so it is checked before
// any identity is read. Identity itself is then resolved:
//   - a signed JWT (identity.jwt) when the caller sends one — the signature
//     names the user; and
//   - the plain identity headers otherwise.
//
// Fail-closed rules when identity mode is enabled:
//   - auth_token configured but the token header is missing/mismatched → error
//   - a JWT is present but unverifiable → error (never retried against the
//     headers, which would let a broken token downgrade to the weaker path)
//   - identity absent and require_identity=true → error
//   - identity present but no role mapping → error
//
// When identity is absent and require_identity=false, (nil, nil) is returned
// and the caller falls back to the default server identity.
func (r *Registry) Resolve(ctx context.Context) (*Tenant, error) {
	// auth_token guards both paths, so it is verified once up front rather than
	// only inside the header branch.
	if r.cfg.Identity.AuthToken != "" {
		if _, _, token := IdentityFromContext(ctx); token != r.cfg.Identity.AuthToken {
			return nil, fmt.Errorf("identity request rejected: missing or invalid %s header", r.cfg.Identity.Headers.Token)
		}
	}

	// A signed token is preferred whenever the caller sends one; only its
	// absence falls through to the headers. A token that is present but
	// unverifiable is rejected inside resolveFromJWT, so a deliberately broken
	// token cannot be used to reach the header path.
	if r.cfg.Identity.JWT.Enabled {
		if raw := JWTFromContext(ctx); raw != "" {
			return r.resolveFromJWT(raw)
		}
	}
	return r.resolveFromHeaders(ctx)
}

// resolveFromJWT verifies the signed identity token and resolves its tenant.
func (r *Registry) resolveFromJWT(raw string) (*Tenant, error) {
	claims, err := VerifyJWT(raw, r.cfg.Identity.JWT.AllSecrets()...)
	if err != nil {
		return nil, fmt.Errorf("identity request rejected: %w", err)
	}

	sessionValue, err := claims.Field(r.cfg.Identity.SessionNameClaim)
	if err != nil {
		return nil, fmt.Errorf("identity request rejected: %w", err)
	}
	// The token is expected to carry whichever claim was configured, so an
	// empty one is a misconfiguration worth surfacing rather than silently
	// auditing every user under the same name.
	if sessionValue == "" {
		return nil, fmt.Errorf("identity request rejected: claim %q selected for the role session name is empty in the token",
			r.cfg.Identity.SessionNameClaim)
	}
	return r.tenantFor(claims, sessionValue)
}

// resolveFromHeaders resolves the tenant from the plain identity headers.
// auth_token has already been checked by Resolve.
func (r *Registry) resolveFromHeaders(ctx context.Context) (*Tenant, error) {
	user, email, _ := IdentityFromContext(ctx)

	if user == "" && email == "" {
		if r.cfg.Identity.RequireIdentity {
			return nil, fmt.Errorf("identity request rejected: %s/%s header required",
				r.cfg.Identity.Headers.User, r.cfg.Identity.Headers.Email)
		}
		return nil, nil // default identity
	}

	claims := &Claims{UserID: user, Email: email}
	sessionValue, err := claims.Field(r.cfg.Identity.SessionNameClaim)
	if err != nil {
		return nil, fmt.Errorf("identity request rejected: %w", err)
	}
	// The headers only carry a user id and an email, so a claim such as
	// employee_no has no source here. Fall back to the tenant key rather than
	// rejecting, which would make this path unusable for a deployment that
	// picked a JWT-only claim.
	if sessionValue == "" {
		sessionValue = identityKey(claims)
	}
	return r.tenantFor(claims, sessionValue)
}

// tenantFor maps a resolved identity to its group and tenant. sessionValue is
// the already-selected role session name.
func (r *Registry) tenantFor(claims *Claims, sessionValue string) (*Tenant, error) {
	// Groups list members by user id or email, so match on those regardless of
	// which claim was chosen for the session name.
	groupName, mapped, err := r.cfg.ResolveGroup(claims.UserID, claims.Email)
	if err != nil {
		return nil, err
	}
	return r.tenant(identityKey(claims), groupName, mapped, sessionValue)
}

// identityKey is the value tenants are cached and isolated by: the user id,
// falling back to the email when the upstream sends only that.
func identityKey(claims *Claims) string {
	if claims.UserID != "" {
		return claims.UserID
	}
	return claims.Email
}

// tenant returns the cached tenant for the user or creates it.
//
// The cache is keyed by user identity, not group/role: several users may
// share one RAM role, but each must get its own STS provider so AssumeRole
// carries that user's RoleSessionName in ActionTrail audit logs, and its own
// session manager/directory for isolation.
//
// sessionValue is the identity field chosen for the RoleSessionName, which is
// not necessarily the cache key: identity.session_name_claim may select e.g.
// the employee number while grouping and isolation still key on the user id.
func (r *Registry) tenant(key, groupName string, mapped config.IdentityGroup, sessionValue string) (*Tenant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if t, ok := r.tenants[key]; ok {
		return t, nil
	}

	provider, err := r.newProviderFn(sessionValue, mapped.RoleArn)
	if err != nil {
		return nil, fmt.Errorf("assume role %s for user %s: %w", mapped.RoleArn, key, err)
	}

	t := &Tenant{Key: key, Group: groupName, RoleArn: mapped.RoleArn, Defaults: mapped, provider: provider}
	// Prime the credential once so misconfigured roles fail fast at first use.
	if cred := t.currentCredential(); cred == nil {
		return nil, fmt.Errorf("assume role %s for user %s: no credential returned", mapped.RoleArn, key)
	}

	opts := []dataagent.ClientOption{dataagent.WithCredentialProvider(t.currentCredential)}
	if r.cfg.DMSUnit != "" {
		opts = append(opts, dataagent.WithDMSUnit(r.cfg.DMSUnit))
	}
	// Endpoint overrides; empty values keep the region-derived defaults.
	opts = append(opts,
		dataagent.WithDataAgentEndpoint(r.cfg.DataAgentEndpoint),
		dataagent.WithDMSEnterpriseEndpoint(r.cfg.DMSEnterpriseEndpoint),
		dataagent.WithAPIKeyEndpoint(r.cfg.APIKeyEndpoint),
		dataagent.WithAPIKeyStreamEndpoint(r.cfg.APIKeyStreamEndpoint),
	)
	workspaceID := mapped.WorkspaceID
	if workspaceID == "" {
		workspaceID = r.cfg.WorkspaceID
	}
	if workspaceID != "" {
		opts = append(opts, dataagent.WithWorkspaceID(workspaceID))
	}
	// The static credential is only a fallback; the provider drives signing.
	t.Client = dataagent.NewClient(t.snapshot, r.cfg.Region, opts...)

	// Per-tenant session dir keeps session listing/attach isolated between users.
	sessDir := filepath.Join(r.cfg.SessionsDir, "identity", sanitize(key))
	t.Manager = session.NewManager(t.Client, sessDir)
	t.Manager.RestoreSessions(r.baseCtx)
	go t.Manager.RunHousekeeping(r.baseCtx)

	r.tenants[key] = t
	log.Printf("tenant created: user=%s group=%s role=%s session_name=%s sessions=%s",
		key, groupName, mapped.RoleArn, sessionName(r.cfg.Identity.Prefix(), sessionValue), sessDir)
	return t, nil
}

// newRoleProvider builds a credentials-go ram_role_arn provider, which calls
// STS AssumeRole with the base AK/SK and transparently refreshes the
// temporary credential before expiry (same mechanism as the Java demo's
// AgentAssumeRoleLoginHelper.assumeRole, minus the manual refresh).
//
// sessionValue is the identity field selected by identity.session_name_claim.
func (r *Registry) newRoleProvider(sessionValue, roleArn string) (credential.Credential, error) {
	conf := new(credential.Config).
		SetType("ram_role_arn").
		SetAccessKeyId(r.base.AccessKeyID).
		SetAccessKeySecret(r.base.AccessKeySecret).
		SetRoleArn(roleArn).
		SetRoleSessionName(sessionName(r.cfg.Identity.Prefix(), sessionValue)).
		SetRoleSessionExpiration(r.cfg.STS.SessionExpiration).
		SetSTSEndpoint(r.cfg.STSEndpoint())
	if r.base.SecurityToken != "" {
		conf.SetSecurityToken(r.base.SecurityToken)
	}
	return credential.NewCredential(conf)
}

var sessionNameSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._@-]`)

// sessionName builds the STS RoleSessionName, which shows up in ActionTrail
// audit logs and must stay recognizable per user.
//
// An empty prefix is honoured as "no prefix" and yields the bare identity
// value; config resolves an unset setting to "aily" before this point, so
// empty here is always deliberate. STS requires 2-64 characters, so a value
// too short to stand alone is padded rather than rejected — losing the audit
// trail over a one-character user id would be worse than a padded name.
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

var pathSanitizer = regexp.MustCompile(`[^a-zA-Z0-9._@-]`)

func sanitize(s string) string {
	s = pathSanitizer.ReplaceAllString(s, "_")
	return strings.Trim(s, ".")
}
