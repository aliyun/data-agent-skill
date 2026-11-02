package tenant

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	credential "github.com/aliyun/credentials-go/credentials"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
)

func testRegistry(cfg config.Config) *Registry {
	cfg.ApplyDefaults()
	cfg.Identity.Namespace = "test-domain"
	cfg.Identity.RequireIdentity = true
	if cfg.Identity.AuthToken == "" {
		cfg.Identity.AuthToken = testAuthToken
	}
	return NewRegistry(context.Background(), cfg, &fakeProvider{})
}

func resolveAuthenticated(r *Registry, ctx context.Context) (*Tenant, error) {
	verified, err := r.Authenticate(ctx)
	if err != nil {
		caller, _ := ctx.Value(gatewayIdentityKey{}).(*GatewayCaller)
		return nil, gatewayFailure(caller, err)
	}
	return r.Resolve(verified)
}

func expectedPrincipalID(r *Registry, user, email string) string {
	c := &Claims{UserID: user, Email: email}
	mode := "headers"
	if r.cfg.Identity.JWT.Enabled {
		mode = "jwt"
		c.TenantID = bigTenantID
		c.AgentID = "agent_abc"
	}
	p, _ := makePrincipal(r.cfg.Identity.Namespace, mode, c)
	return p.ID
}

func TestIdentityContextRoundTrip(t *testing.T) {
	ctx := WithIdentity(context.Background(), "ou_alice", "alice@example.com", "tok")
	user, email, token := IdentityFromContext(ctx)
	if user != "ou_alice" || email != "alice@example.com" || token != "tok" {
		t.Fatalf("round trip = %q/%q/%q", user, email, token)
	}

	// Empty context yields empty identity, not panics.
	user, email, token = IdentityFromContext(context.Background())
	if user != "" || email != "" || token != "" {
		t.Fatalf("empty context = %q/%q/%q", user, email, token)
	}
}

func TestResolveRejectsBadAuthToken(t *testing.T) {
	r := testRegistry(config.Config{Identity: config.Identity{
		Enabled:   true,
		AuthToken: "expected",
	}})

	ctx := WithIdentity(context.Background(), "ou_alice", "", "wrong")
	if _, err := resolveAuthenticated(r, ctx); !IsAuthenticationError(err) {
		t.Fatalf("expected shared-secret rejection, got %v", err)
	}
}

func TestResolveRequireIdentityFailClosed(t *testing.T) {
	r := testRegistry(config.Config{Identity: config.Identity{
		Enabled:         true,
		RequireIdentity: true,
	}})

	if _, err := resolveAuthenticated(r, context.Background()); !IsAuthenticationError(err) {
		t.Fatalf("expected identity-required rejection, got %v", err)
	}
}

func TestResolveNeverFallsBackToDefaultIdentity(t *testing.T) {
	r := testRegistry(config.Config{Identity: config.Identity{
		Enabled:         true,
		RequireIdentity: false,
	}})

	tn, err := resolveAuthenticated(r, context.Background())
	if err == nil || tn != nil {
		t.Fatal("anonymous fallback accepted")
	}
}

func TestResolveRejectsUnmappedUser(t *testing.T) {
	r := testRegistry(config.Config{Identity: config.Identity{
		Enabled: true,
		Groups: map[string]config.IdentityGroup{
			"g1": {RoleArn: "acs:ram::123:role/aily-alice", Users: []string{"ou_alice"}},
		},
	}})

	ctx := WithIdentity(context.Background(), "ou_mallory", "mallory@example.com", testAuthToken)
	if _, err := resolveAuthenticated(r, ctx); !errors.Is(err, config.ErrIdentityUnmapped) {
		t.Fatalf("expected unmatched-user rejection, got %v", err)
	}
}

func TestSessionNameSanitization(t *testing.T) {
	got := sessionName("aily", "ou_张三/([)] weird")
	if strings.ContainsAny(got, "/([)] ") {
		t.Fatalf("sessionName not sanitized: %q", got)
	}
	if !strings.HasPrefix(got, "aily-") {
		t.Fatalf("sessionName missing prefix: %q", got)
	}
	if len(got) < 2 || len(got) > 64 {
		t.Fatalf("sessionName length out of STS bounds: %q", got)
	}

	long := sessionName("aily", strings.Repeat("a", 100))
	if len(long) != 64 {
		t.Fatalf("long sessionName not truncated: %d", len(long))
	}
}

func TestSessionNamePrefix(t *testing.T) {
	// An empty prefix is deliberate (config resolves an unset one to "aily"),
	// so the RoleSessionName is the bare identity value.
	if got := sessionName("", "ou_alice"); got != "ou_alice" {
		t.Fatalf("empty prefix should drop the prefix, got %q", got)
	}
	// Configured prefix yields "<prefix>-<user>".
	if got := sessionName("prod", "ou_alice"); got != "prod-ou_alice" {
		t.Fatalf("prefixed form = %q, want prod-ou_alice", got)
	}
	// Prefix is sanitized too.
	if got := sessionName("my env/1", "ou_alice"); got != "my_env_1-ou_alice" {
		t.Fatalf("prefix not sanitized: %q", got)
	}
	// Combined length still bounded to 64.
	if got := sessionName(strings.Repeat("p", 40), strings.Repeat("u", 40)); len(got) != 64 {
		t.Fatalf("prefixed sessionName not truncated: %d", len(got))
	}
}

// STS rejects a RoleSessionName shorter than 2 characters, which only becomes
// reachable once the prefix can be dropped.
func TestSessionNameMeetsStsMinimumWithoutPrefix(t *testing.T) {
	for _, key := range []string{"a", ""} {
		got := sessionName("", key)
		if len(got) < 2 {
			t.Errorf("sessionName(%q) = %q, shorter than the STS minimum of 2", key, got)
		}
	}
	// A value already long enough is left untouched.
	if got := sessionName("", "ab"); got != "ab" {
		t.Errorf("sessionName padded a long-enough value: %q", got)
	}
}

func TestIdentityPathUsesDigestNotSanitization(t *testing.T) {
	a, _ := makePrincipal("test", "headers", &Claims{UserID: "a+b"})
	b, _ := makePrincipal("test", "headers", &Claims{UserID: "a_b"})
	if a.ID == b.ID || !principalIDPattern.MatchString(a.ID) {
		t.Fatal("lossy identity path")
	}
}

// fakeProvider satisfies credential.Credential without calling STS.
type fakeProvider struct{}

func (fakeProvider) GetAccessKeyId() (*string, error)     { s := "sts-ak"; return &s, nil }
func (fakeProvider) GetAccessKeySecret() (*string, error) { s := "sts-sk"; return &s, nil }
func (fakeProvider) GetSecurityToken() (*string, error)   { s := "sts-token"; return &s, nil }
func (fakeProvider) GetBearerToken() *string              { s := ""; return &s }
func (fakeProvider) GetType() *string                     { s := "ram_role_arn"; return &s }
func (fakeProvider) GetCredential() (*credential.CredentialModel, error) {
	ak, sk, tok := "sts-ak", "sts-sk", "sts-token"
	return &credential.CredentialModel{AccessKeyId: &ak, AccessKeySecret: &sk, SecurityToken: &tok}, nil
}

// Two users sharing one RAM role (default group) must get separate tenants:
// each carries its own RoleSessionName (from x-aily-user) for ActionTrail
// attribution and its own isolated session directory. Group defaults
// (mode/custom agent) must be carried on the tenant.
func TestUsersSharingRoleGetSeparateTenants(t *testing.T) {
	sharedRole := "acs:ram::123:role/da-role"
	r := testRegistry(config.Config{
		SessionsDir: t.TempDir(),
		Identity: config.Identity{
			Enabled: true,
			Default: &config.IdentityGroup{
				RoleArn:       sharedRole,
				Mode:          "ASK_DATA",
				CustomAgentID: "ca-default",
			},
		},
	})
	t.Cleanup(func() { _ = r.Close() })
	var sessionNames []string
	r.newProviderFn = func(key, roleArn string) (credential.Credential, error) {
		if roleArn != sharedRole {
			t.Fatalf("unexpected role arn %q", roleArn)
		}
		sessionNames = append(sessionNames, sessionName(r.cfg.Identity.Prefix(), key))
		return fakeProvider{}, nil
	}

	alice, err := resolveAuthenticated(r, WithIdentity(context.Background(), "ou_alice", "", testAuthToken))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := resolveAuthenticated(r, WithIdentity(context.Background(), "ou_bob", "", testAuthToken))
	if err != nil {
		t.Fatal(err)
	}

	if alice == bob {
		t.Fatal("users sharing a role must not share a tenant")
	}
	if alice.Manager == bob.Manager || alice.Client == bob.Client {
		t.Fatal("users sharing a role must not share manager/client")
	}
	if alice.Group != "default" || alice.Defaults.Mode != "ASK_DATA" || alice.Defaults.CustomAgentID != "ca-default" {
		t.Fatalf("group defaults not carried: group=%q defaults=%+v", alice.Group, alice.Defaults)
	}
	if len(sessionNames) != 2 || sessionNames[0] != "aily-ou_alice" || sessionNames[1] != "aily-ou_bob" {
		t.Fatalf("RoleSessionName must derive from x-aily-user, got %v", sessionNames)
	}

	// Repeated resolve returns the cached tenant, no extra AssumeRole provider.
	again, err := resolveAuthenticated(r, WithIdentity(context.Background(), "ou_alice", "", testAuthToken))
	if err != nil || again != alice {
		t.Fatalf("cached tenant not reused: %v %v", again, err)
	}
	if len(sessionNames) != 2 {
		t.Fatalf("provider rebuilt on cache hit: %v", sessionNames)
	}
}

// The STS AssumeRole API signs through the query string, so provider errors
// embed the caller's AccessKeyId and signature. Those must not reach the log.
func TestRedactCredentials(t *testing.T) {
	in := `Post "https://sts.cn-hangzhou.aliyuncs.com?AccessKeyId=LTAI5tSecret123&Action=AssumeRole&Signature=abc%3D&SignatureNonce=xyz&Version=2015-04-01": dial tcp: timeout`
	got := redactCredentials(in)

	for _, leak := range []string{"LTAI5tSecret123", "abc%3D"} {
		if strings.Contains(got, leak) {
			t.Errorf("value %q survived redaction: %s", leak, got)
		}
	}
	for _, keep := range []string{"AssumeRole", "SignatureNonce=xyz", "2015-04-01", "dial tcp: timeout"} {
		if !strings.Contains(got, keep) {
			t.Errorf("diagnostic detail %q was lost: %s", keep, got)
		}
	}
	if !strings.Contains(got, "AccessKeyId=<redacted>") {
		t.Errorf("expected redaction marker: %s", got)
	}
}

func TestGatewayJWTRequiresValidTokenNoHeaderDowngrade(t *testing.T) {
	for name, raw := range map[string]string{
		"missing":         "",
		"invalid":         "not-a-jwt",
		"wrong signature": signToken(t, "other-secret", ailyClaims(), time.Now().Add(time.Hour)),
		"expired":         signToken(t, testSecret, ailyClaims(), time.Now().Add(-time.Minute)),
		"no identity":     signToken(t, testSecret, map[string]any{"tenant_id": "tenant-only"}, time.Now().Add(time.Hour)),
	} {
		t.Run(name, func(t *testing.T) {
			r, asked := jwtRegistry(t, "user_id", nil)
			ctx := WithIdentity(WithJWT(context.Background(), raw), bigUserID, "forged@example.com", testAuthToken)
			ctx, caller := WithGatewayIdentity(ctx)
			tn, err := resolveAuthenticated(r, ctx)
			var gatewayErr *dataagent.GatewayError
			if tn != nil || !errors.As(err, &gatewayErr) || gatewayErr.Code != "GATEWAY_IDENTITY_REJECTED" {
				t.Fatalf("got tenant=%v error=%v", tn, err)
			}
			if *caller != (GatewayCaller{}) || len(*asked) != 0 {
				t.Fatal("unverified identity reached caller or provider")
			}
		})
	}
}

func TestGatewayHeaderIdentityRequiredRegardlessLegacySetting(t *testing.T) {
	for _, user := range []string{"", "   "} {
		r, asked := identityRegistry(t, config.Identity{
			Enabled: true, AuthToken: testAuthToken, RequireIdentity: false,
			Default: &config.IdentityGroup{RoleArn: "acs:ram::123:role/shared"},
		})
		ctx, caller := WithGatewayIdentity(WithIdentity(context.Background(), user, "", testAuthToken))
		if tn, err := resolveAuthenticated(r, ctx); tn != nil || err == nil {
			t.Fatal("anonymous gateway fallback accepted")
		}
		if *caller != (GatewayCaller{}) || len(*asked) != 0 {
			t.Fatal("anonymous request reached provider")
		}
	}
}

func TestGatewayPlatformTokenAlwaysRequired(t *testing.T) {
	for _, jwtEnabled := range []bool{true, false} {
		for _, token := range []string{"", "wrong"} {
			r, asked := identityRegistry(t, config.Identity{
				Enabled: true, AuthToken: testAuthToken, JWT: config.JWT{Enabled: jwtEnabled, Secret: testSecret},
				Default: &config.IdentityGroup{RoleArn: "acs:ram::123:role/shared"},
			})
			ctx := WithIdentity(jwtCtx(t, ailyClaims()), bigUserID, "test@example.com", token)
			ctx, caller := WithGatewayIdentity(ctx)
			if tn, err := resolveAuthenticated(r, ctx); tn != nil || err == nil {
				t.Fatal("bad platform token accepted")
			}
			if *caller != (GatewayCaller{}) || len(*asked) != 0 {
				t.Fatal("bad token reached provider")
			}
		}
	}
	// Hand-built invalid registry configurations must not enable anonymous auth.
	for _, id := range []config.Identity{{Enabled: false, AuthToken: testAuthToken}, {Enabled: true}} {
		r := testRegistry(config.Config{Identity: id})
		ctx, _ := WithGatewayIdentity(WithIdentity(context.Background(), "user", "", testAuthToken))
		if _, err := resolveAuthenticated(r, ctx); err == nil {
			t.Fatal("invalid identity config accepted")
		}
	}
}

func TestGatewayVerifiedCallerAndRoleMapping(t *testing.T) {
	for _, jwtEnabled := range []bool{true, false} {
		for _, withDefault := range []bool{true, false} {
			id := config.Identity{
				Enabled: true, AuthToken: testAuthToken, JWT: config.JWT{Enabled: jwtEnabled, Secret: testSecret},
				Groups: map[string]config.IdentityGroup{"analysts": {RoleArn: "acs:ram::123:role/analysts", Users: []string{bigUserID, "mapped@example.com"}}},
			}
			if withDefault {
				id.Default = &config.IdentityGroup{RoleArn: "acs:ram::123:role/shared"}
			}
			r, _ := identityRegistry(t, id)
			for _, tc := range []struct{ user, email, group string }{
				{bigUserID, "verified@example.com", "analysts"},
				{"email-user", "mapped@example.com", "analysts"},
				{"unknown", "unknown@example.com", "default"},
			} {
				ctx := WithIdentity(context.Background(), tc.user, tc.email, testAuthToken)
				if jwtEnabled {
					ctx = WithIdentity(jwtCtx(t, map[string]any{"user_id": tc.user, "email": tc.email}), "forged-user", "forged-email", testAuthToken)
				}
				ctx, caller := WithGatewayIdentity(ctx)
				tn, err := resolveAuthenticated(r, ctx)
				if tc.group == "default" && !withDefault {
					var gatewayErr *dataagent.GatewayError
					if tn != nil || !errors.As(err, &gatewayErr) || gatewayErr.Code != "GATEWAY_IDENTITY_UNMAPPED" || *caller != (GatewayCaller{}) {
						t.Fatalf("unmapped result: %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if caller.UserID != tc.user || caller.Email != tc.email || caller.Group != tc.group || caller.RoleArn != tn.RoleArn {
					t.Fatalf("caller not verified/mapped: %+v", caller)
				}
				// A new request gets a distinct caller record, not cached identity data.
				_, next := WithGatewayIdentity(ctx)
				if next == caller || *next != (GatewayCaller{}) {
					t.Fatal("caller record shared across requests")
				}
			}
		}
	}
}

func TestGatewayRejectsCachedRoleMappingMismatch(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, sameRole := range []bool{false, true} {
			defaultRole := "acs:ram::123:role/restricted"
			if sameRole {
				defaultRole = "acs:ram::123:role/privileged"
			}
			r, asked := identityRegistry(t, config.Identity{
				Enabled: true, AuthToken: testAuthToken,
				JWT:     config.JWT{Enabled: true, Secret: testSecret},
				Default: &config.IdentityGroup{RoleArn: defaultRole},
				Groups:  map[string]config.IdentityGroup{"privileged": {RoleArn: "acs:ram::123:role/privileged", Users: []string{"admin@example.com"}}},
			})
			initial := jwtCtx(t, map[string]any{"user_id": "same-user", "email": "admin@example.com"})
			if !legacy {
				initial, _ = WithGatewayIdentity(initial)
			}
			if tn, err := resolveAuthenticated(r, initial); err != nil || tn.Group != "privileged" {
				t.Fatalf("initial tenant: %v", err)
			}
			changed, caller := WithGatewayIdentity(jwtCtx(t, map[string]any{"user_id": "same-user"}))
			tn, err := resolveAuthenticated(r, changed)
			var failure *dataagent.GatewayError
			if tn != nil || !errors.As(err, &failure) || failure.Code != "GATEWAY_IDENTITY_REJECTED" || *caller != (GatewayCaller{}) {
				t.Fatalf("cached role was reused under changed mapping: tenant=%v caller=%+v error=%v", tn, caller, err)
			}
			if len(*asked) != 1 {
				t.Fatal("mapping rejection created another credential provider")
			}
		}
	}
}

type mutableProvider struct {
	fakeProvider
	mu    sync.Mutex
	value *dataagent.Credential
	err   error
	calls int
}

func (p *mutableProvider) GetCredential() (*credential.CredentialModel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	if p.value == nil {
		return nil, nil
	}
	v := *p.value
	return &credential.CredentialModel{AccessKeyId: &v.AccessKeyID, AccessKeySecret: &v.AccessKeySecret, SecurityToken: &v.SecurityToken}, nil
}

func TestGatewayCredentialRefreshFailureDoesNotReturnSnapshotOrLogSecrets(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	provider := &mutableProvider{value: &dataagent.Credential{AccessKeyID: "old", AccessKeySecret: "sk", SecurityToken: "token"}}
	tn := &Tenant{provider: provider}
	initial, err := tn.strictCredential()
	if err != nil {
		t.Fatal(err)
	}
	provider.err = errors.New("AccessKeyId=secret-ak Signature=secret-signature raw-body-secret")
	if c, err := tn.strictCredential(); c != nil || err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatalf("strict refresh returned snapshot or SDK detail: %v", err)
	}
	if c := tn.currentCredential(); c != initial {
		t.Fatal("legacy analysis fallback changed")
	}
	if strings.Contains(logs.String(), "secret") {
		t.Fatal("credential error leaked to logs")
	}
	provider.err = nil
	provider.value = &dataagent.Credential{AccessKeyID: "new", AccessKeySecret: "new-sk", SecurityToken: "new-token"}
	if c, err := tn.strictCredential(); err != nil || c.AccessKeyID != "new" {
		t.Fatalf("refresh did not recover: %v", err)
	}
	provider.value = nil
	if c, err := tn.strictCredential(); c != nil || err == nil {
		t.Fatal("nil model accepted")
	}
}

func TestGatewayResolverSanitizesProviderErrors(t *testing.T) {
	for _, buildFailure := range []bool{true, false} {
		r, _ := jwtRegistry(t, "user_id", nil)
		r.newProviderFn = func(string, string) (credential.Credential, error) {
			if buildFailure {
				return nil, errors.New("provider-constructor-secret")
			}
			return &mutableProvider{err: errors.New("provider-fetch-secret")}, nil
		}
		ctx, caller := WithGatewayIdentity(jwtCtx(t, ailyClaims()))
		tn, err := resolveAuthenticated(r, ctx)
		var gatewayErr *dataagent.GatewayError
		if tn != nil || !errors.As(err, &gatewayErr) || gatewayErr.Code != "GATEWAY_CREDENTIAL_ERROR" || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsanitized resolver failure: %v", err)
		}
		if *caller != (GatewayCaller{}) {
			t.Fatal("failed resolution populated caller")
		}
	}
}

func TestRoleProviderTracksDynamicBaseAndNeverDowngrades(t *testing.T) {
	base := &mutableProvider{value: &dataagent.Credential{AccessKeyID: "base-ak", AccessKeySecret: "base-sk", SecurityToken: "base-sts"}}
	var seen []dataagent.Credential
	role := &mutableProvider{value: &dataagent.Credential{AccessKeyID: "role-ak", AccessKeySecret: "role-sk", SecurityToken: "role-sts"}}
	p := &rotatingRoleProvider{base: base, build: func(c *dataagent.Credential) (credential.Credential, error) {
		seen = append(seen, *c)
		return role, nil
	}}
	for i := 0; i < 2; i++ {
		if _, err := p.GetCredential(); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 1 {
		t.Fatal("stable base unnecessarily rebuilt provider")
	}
	base.value = &dataagent.Credential{AccessKeyID: "base-ak", AccessKeySecret: "rotated-sk", SecurityToken: "base-sts"}
	if _, err := p.GetCredential(); err != nil {
		t.Fatal(err)
	}
	base.value = &dataagent.Credential{AccessKeyID: "rotated-ak", AccessKeySecret: "rotated-sk", SecurityToken: "rotated-sts"}
	if _, err := p.GetCredential(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen[1].AccessKeySecret != "rotated-sk" || seen[2].SecurityToken != "rotated-sts" {
		t.Fatal("base tuple rotation was not propagated")
	}
	before := role.calls
	base.err = errors.New("base-secret-refresh-failure")
	if c, err := p.GetCredential(); c != nil || err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("base refresh failure: %v", err)
	}
	if role.calls != before {
		t.Fatal("base failure silently used previous role provider")
	}
	base.err = nil
	role.err = errors.New("role-secret-refresh-failure")
	if c, err := p.GetCredential(); c != nil || err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("role refresh failure: %v", err)
	}
	role.err = nil
	base.value = &dataagent.Credential{AccessKeyID: "third-ak", AccessKeySecret: "third-sk", SecurityToken: "third-sts"}
	p.build = func(*dataagent.Credential) (credential.Credential, error) { return nil, errors.New("build-secret") }
	before = role.calls
	if c, err := p.GetCredential(); c != nil || err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("rebuild failure: %v", err)
	}
	if role.calls != before {
		t.Fatal("failed rotation used old role provider")
	}
}

func TestGatewayConcurrentCallersDoNotShareIdentity(t *testing.T) {
	r, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "acs:ram::123:role/shared"}})
	var wg sync.WaitGroup
	for _, user := range []string{"alice", "bob", "carol", "dave"} {
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			ctx, caller := WithGatewayIdentity(WithIdentity(context.Background(), user, "", testAuthToken))
			tn, err := resolveAuthenticated(r, ctx)
			if err != nil {
				t.Error(err)
				return
			}
			if tn.Key != expectedPrincipalID(r, user, "") || caller.UserID != user {
				t.Error("cross-user identity")
			}
		}(user)
	}
	wg.Wait()
	if len(r.tenants) != 4 {
		t.Fatal("per-user tenant cache lost identities")
	}
	// This asserts separate clients/audit identity, not cloud data ACL isolation.
}

type blockedCredentialTestTransport struct{}

func (blockedCredentialTestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network access forbidden in credential test")
}

func TestTenantGatewayOptionsAndStrictProviderAreWired(t *testing.T) {
	transport := http.DefaultTransport
	http.DefaultTransport = blockedCredentialTestTransport{}
	t.Cleanup(func() { http.DefaultTransport = transport })
	r, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "acs:ram::123:role/shared"}})
	r.cfg.AgentGateway = config.AgentGateway{Enabled: true, Endpoint: "dms.cn-hangzhou.aliyuncs.com", RequestTimeoutSeconds: 7, MaxResponseBytes: 12345}
	provider := &mutableProvider{value: &dataagent.Credential{AccessKeyID: "ak", AccessKeySecret: "sk", SecurityToken: "token"}}
	r.newProviderFn = func(string, string) (credential.Credential, error) { return provider, nil }
	ctx, _ := WithGatewayIdentity(WithIdentity(context.Background(), "alice", "", testAuthToken))
	tn, err := resolveAuthenticated(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect option values without introducing production getters or a network
	// endpoint override solely for tests. This is tied to the pinned Client API.
	options := reflect.ValueOf(tn.Client).Elem().FieldByName("gateway")
	if options.FieldByName("Endpoint").String() != r.cfg.AgentGateway.Endpoint ||
		options.FieldByName("RequestTimeout").Int() != int64(7*time.Second) ||
		options.FieldByName("MaxResponseBytes").Int() != 12345 {
		t.Fatal("tenant gateway options were not preserved")
	}
	provider.err = errors.New("refresh-secret")
	_, err = tn.Client.ListAgenticCatalog(ctx, dataagent.GatewayPageOptions{})
	var gatewayErr *dataagent.GatewayError
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != "GATEWAY_CREDENTIAL_ERROR" {
		t.Fatalf("gateway did not use strict tenant provider: %v", err)
	}
}
