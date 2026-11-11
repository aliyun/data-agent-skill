package tenant

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
	credential "github.com/aliyun/credentials-go/credentials"
)

func authenticateTest(t *testing.T, r *Registry, user string) context.Context {
	t.Helper()
	ctx, err := r.Authenticate(WithIdentity(context.Background(), user, "", testAuthToken))
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestPrincipalCanonicalTupleAndRotation(t *testing.T) {
	base := &Claims{UserID: "a+b", TenantID: "tenant", AgentID: "agent"}
	p, err := makePrincipal("domain", "jwt", base)
	if err != nil {
		t.Fatal(err)
	}
	raw := `["principal-v2","domain","jwt","tenant","agent","user_id","a+b"]`
	want := sha256.Sum256([]byte(raw))
	if p.ID != hex.EncodeToString(want[:]) {
		t.Fatal("principal encoding changed")
	}
	seen := map[string]bool{p.ID: true}
	for _, tc := range []struct{ namespace, mode, tenant, agent, user, email string }{
		{"other", "jwt", "tenant", "agent", "a+b", ""},
		{"domain", "headers", "", "", "a+b", ""},
		{"domain", "jwt", "", "", "a+b", ""},
		{"domain", "jwt", "tenant", "", "a+b", ""},
		{"domain", "jwt", "", "agent", "a+b", ""},
		{"domain", "jwt", "other", "agent", "a+b", ""},
		{"domain", "jwt", "tenant", "other", "a+b", ""},
		{"domain", "jwt", "tenant", "agent", "", "a+b"},
		{"domain", "jwt", "tenant", "agent", "a_b", ""},
		{"domain", "jwt", "tenant", "agent", "A+b", ""},
		{"domain", "jwt", "tenant", "agent", "é", ""},
		{"domain", "jwt", "tenant", "agent", "e\u0301", ""},
	} {
		got, err := makePrincipal(tc.namespace, tc.mode, &Claims{TenantID: tc.tenant, AgentID: tc.agent, UserID: tc.user, Email: tc.email})
		if err != nil || seen[got.ID] {
			t.Fatalf("principal namespace collision: %v", err)
		}
		seen[got.ID] = true
	}
	var identity string
	for i, key := range []string{"rotation-a", "rotation-b"} {
		claims, err := VerifyJWT(signToken(t, key, ailyClaims(), time.Now().Add(time.Duration(i+1)*time.Hour)), "rotation-a", "rotation-b")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := makePrincipal("domain", "jwt", claims)
		if identity != "" && identity != got.ID {
			t.Fatal("key rotation/expiry changed identity")
		}
		identity = got.ID
	}
}

func TestAuthenticationPureAndTrustedContextOnly(t *testing.T) {
	r, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "role"}})
	var providers atomic.Int32
	r.newProviderFn = func(string, string) (credential.Credential, error) { providers.Add(1); return fakeProvider{}, nil }
	raw := WithIdentity(context.Background(), "alice", "", testAuthToken)
	if _, err := r.Resolve(raw); !IsAuthenticationError(err) {
		t.Fatal("Resolve accepted unverified identity")
	}
	spoofed := WithClaims(raw, &Claims{UserID: "alice"})
	if _, err := r.Resolve(spoofed); !IsAuthenticationError(err) {
		t.Fatal("WithClaims forged trusted auth")
	}
	verified, err := r.Authenticate(raw)
	if err != nil {
		t.Fatal(err)
	}
	principal := PrincipalFromContext(verified)
	if principal == nil || principal.ID == "" || !principal.ExpiresAt.IsZero() {
		t.Fatal("missing header principal")
	}
	original := principal.ID
	principal.ID = "forged"
	principal.ExpiresAt = time.Now().Add(-time.Hour)
	if PrincipalFromContext(verified).ID != original {
		t.Fatal("mutable principal capability")
	}
	if providers.Load() != 0 || len(r.tenants) != 0 || len(r.inflight) != 0 {
		t.Fatal("authentication initialized tenant")
	}
	if entries, err := os.ReadDir(r.cfg.SessionsDir); err != nil || len(entries) != 0 {
		t.Fatal("authentication touched storage")
	}
	other, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "role"}})
	if _, err := other.Resolve(verified); !IsAuthenticationError(err) {
		t.Fatal("cross-registry capability accepted")
	}
	if _, err := r.Resolve(verified); err != nil {
		t.Fatal(err)
	}
	if providers.Load() != 1 {
		t.Fatal("verified resolve did not initialize exactly once")
	}
}

func TestJWTUserIDAndEmailOnlyAreDifferentPrincipals(t *testing.T) {
	r, _ := jwtRegistry(t, "user_id", nil)
	var tenants []*Tenant
	for _, fields := range []map[string]any{{"user_id": "same-value"}, {"email": "same-value"}} {
		ctx, err := r.Authenticate(jwtCtx(t, fields))
		if err != nil {
			t.Fatal(err)
		}
		p := PrincipalFromContext(ctx)
		if p.ExpiresAt.IsZero() {
			t.Fatal("JWT expiry missing")
		}
		tenant, err := r.Resolve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tenants = append(tenants, tenant)
	}
	if tenants[0].Key == tenants[1].Key || tenants[0].Manager == tenants[1].Manager {
		t.Fatal("subject types shared tenant")
	}
}

func TestLegacySharedRoleKeepsJWTPrincipalsAndStorageSeparate(t *testing.T) {
	cfg := config.Config{Region: "cn-hangzhou", SessionsDir: t.TempDir(), HTTP: config.DefaultHTTP(), Identity: config.Identity{
		Enabled: true, RequireIdentity: true, AuthToken: testAuthToken, MaxTenants: 128,
		JWT:     config.JWT{Enabled: true, Secret: testSecret},
		Default: &config.IdentityGroup{RoleArn: "shared-role"},
	}}
	cfg.ApplyDefaults()
	if err := cfg.ValidateTransport("streamable-http"); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(context.Background(), cfg, fakeProvider{})
	t.Cleanup(func() { _ = r.Close() })
	r.newProviderFn = func(string, string) (credential.Credential, error) { return fakeProvider{}, nil }
	seen := make(map[string]*Tenant)
	for _, fields := range []map[string]any{
		{"tenant_id": "t1", "agent_id": "a1", "user_id": "same-user"},
		{"tenant_id": "t2", "agent_id": "a1", "user_id": "same-user"},
		{"tenant_id": "t1", "agent_id": "a2", "user_id": "same-user"},
		{"tenant_id": "t1", "agent_id": "a1", "email": "same-user"},
		{"user_id": "same-user"},
		{"tenant_id": "t1", "user_id": "same-user"},
		{"agent_id": "a1", "user_id": "same-user"},
		{"email": "same-user"},
	} {
		token := signToken(t, testSecret, fields, time.Now().Add(time.Hour))
		ctx, err := r.Authenticate(WithIdentity(WithJWT(context.Background(), token), "", "", testAuthToken))
		if err != nil {
			t.Fatal(err)
		}
		principal := PrincipalFromContext(ctx)
		tenantID, _ := fields["tenant_id"].(string)
		agentID, _ := fields["agent_id"].(string)
		if principal == nil || principal.Namespace != "default" || principal.TenantID != tenantID || principal.AgentID != agentID {
			t.Fatal("legacy role mapping lost verified scope")
		}
		ten, err := r.Resolve(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := seen[principal.ID]; exists {
			t.Fatal("different JWT scopes shared principal")
		}
		for _, other := range seen {
			if other.Manager == ten.Manager || other.Client == ten.Client {
				t.Fatal("shared role reused another principal's state")
			}
		}
		seen[principal.ID] = ten
		if ten.Group != "default" || ten.RoleArn != "shared-role" || ten.Key != principal.ID {
			t.Fatal("legacy role mapping changed")
		}
		if info, err := os.Stat(filepath.Join(cfg.SessionsDir, "identity-v2", principal.ID, "owner.json")); err != nil || !info.Mode().IsRegular() {
			t.Fatal("missing isolated owner record")
		}
		again, err := r.Resolve(ctx)
		if err != nil || again != ten {
			t.Fatal("same principal did not reuse its own tenant")
		}
	}
	ctx, err := r.Authenticate(WithIdentity(context.Background(), "same-user", "", testAuthToken))
	if err != nil {
		t.Fatal("legacy Header identity rejected", err)
	}
	principal := PrincipalFromContext(ctx)
	if principal.AuthMode != "headers" || !principal.ExpiresAt.IsZero() || seen[principal.ID] != nil {
		t.Fatal("Header identity shared a JWT principal")
	}
	ten, err := r.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range seen {
		if ten.Manager == other.Manager || ten.Client == other.Client || ten.Key == other.Key {
			t.Fatal("Header identity shared JWT state")
		}
	}
	if _, err := os.Stat(filepath.Join(cfg.SessionsDir, "identity-v2", principal.ID, "owner.json")); err != nil {
		t.Fatal("missing Header owner record", err)
	}
}

func TestStrictJWTIdentityFieldTypes(t *testing.T) {
	invalid := []any{nil, true, false, []any{"id"}, map[string]any{"id": "value"}, json.Number("1.5"), json.Number("1e3"), "a\nb", "\x00id"}
	for _, field := range []string{"user_id", "tenant_id", "agent_id", "email", "enterprise_email", "employee_no"} {
		for _, value := range invalid {
			claims := ailyClaims()
			claims[field] = value
			if _, err := VerifyJWT(signToken(t, testSecret, claims, time.Now().Add(time.Hour)), testSecret); err == nil {
				t.Fatalf("accepted invalid %s type %T", field, value)
			}
		}
	}
	for _, field := range []string{"tenant_id", "agent_id"} {
		for _, empty := range []string{"", "   "} {
			fields := ailyClaims()
			fields[field] = empty
			claims, err := VerifyJWT(signToken(t, testSecret, fields, time.Now().Add(time.Hour)), testSecret)
			if err != nil {
				t.Fatal("legacy optional scope rejected", err)
			}
			value, _ := claims.Field(field)
			if value != "" {
				t.Fatal("empty scope was synthesized")
			}
		}
	}
	for _, field := range []string{"email", "enterprise_email"} {
		c := ailyClaims()
		c[field] = json.Number("123")
		if _, err := VerifyJWT(signToken(t, testSecret, c, time.Now().Add(time.Hour)), testSecret); err == nil {
			t.Fatal("numeric email accepted")
		}
	}
	claims := ailyClaims()
	claims["user_id"] = "  用户é  "
	c, err := VerifyJWT(signToken(t, testSecret, claims, time.Now().Add(time.Hour)), testSecret)
	if err != nil || c.UserID != "用户é" {
		t.Fatal("identity trimming/unicode changed")
	}
}

func rawSignedJWT(payload string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	body := h + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func TestJWTRejectsDuplicateKeysAndInvalidUnicode(t *testing.T) {
	for _, prefix := range []string{`"user_id":"a","user_id":"b"`, `"user_id":"a","\u0075ser_id":"b"`, `"user_id":"\ud800"`, `"user_id":"\udc00"`, `"user_id":"\ud800\u0041"`} {
		payload := fmt.Sprintf(`{%s,"tenant_id":"t","agent_id":"a","exp":%d}`, prefix, time.Now().Add(time.Hour).Unix())
		if _, err := VerifyJWT(rawSignedJWT(payload), testSecret); err == nil {
			t.Fatal("ambiguous JWT accepted")
		}
	}
	for _, value := range []string{`\ud83d\ude00`, `\ufffd`} {
		payload := fmt.Sprintf(`{"user_id":"%s","tenant_id":"t","agent_id":"a","exp":%d}`, value, time.Now().Add(time.Hour).Unix())
		if _, err := VerifyJWT(rawSignedJWT(payload), testSecret); err != nil {
			t.Fatal("valid Unicode rejected", err)
		}
	}
}

func TestOwnerLayoutAndLegacyIsolation(t *testing.T) {
	base := t.TempDir()
	id := strings.Repeat("a", 64)
	legacy := filepath.Join(base, "identity", "a_b")
	if err := os.MkdirAll(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "legacy-state"), []byte("old history"), 0600); err != nil {
		t.Fatal(err)
	}
	root, dir, err := openTenantRoot(base, id)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(base, "identity-v2", id) {
		t.Fatal("wrong layout")
	}
	f, err := root.Open("owner.json")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	f.Close()
	if info.Mode().Perm() != 0600 {
		t.Fatal("owner permissions not private")
	}
	if _, err := root.Open("legacy-state"); !os.IsNotExist(err) {
		t.Fatal("legacy history read")
	}
	root.Close()
	again, _, err := openTenantRoot(base, id)
	if err != nil {
		t.Fatal("owner reopen failed", err)
	}
	again.Close()
	if _, err := os.Stat(filepath.Join(legacy, "legacy-state")); err != nil {
		t.Fatal("legacy history changed")
	}
}

func TestPinnedTenantRootsSurviveDirectoryReplacementWithoutEscape(t *testing.T) {
	base := t.TempDir()
	a, dirA, err := openTenantRoot(base, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, dirB, err := openTenantRoot(base, strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, root := range []*os.Root{a, b} {
		f, err := root.OpenFile("synthetic", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(root.Name()))
		f.Close()
	}
	if err := os.Rename(dirA, dirA+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dirB, dirA); err != nil {
		t.Skip(err)
	}
	f, err := a.Open("owner.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var owner ownerRecord
	if err := json.NewDecoder(f).Decode(&owner); err != nil || owner.PrincipalID != strings.Repeat("a", 64) {
		t.Fatal("pinned root changed owner")
	}
	if _, err := a.Open("../" + strings.Repeat("b", 64) + "/synthetic"); err == nil {
		t.Fatal("root escaped to other principal")
	}
	if replacement, _, err := openTenantRoot(base, strings.Repeat("a", 64)); err == nil {
		replacement.Close()
		t.Fatal("replacement symlink accepted")
	}
}

func TestRegistryInternalDefaultBudgets(t *testing.T) {
	r := NewRegistry(context.Background(), config.Config{}, fakeProvider{})
	defer r.Close()
	if r.cfg.Identity.MaxTenants != 128 || r.initLimit != 8 || r.initTimeout != 15*time.Second || cap(r.restores) != 4 {
		t.Fatal("security default budgets changed")
	}
}

func TestOwnerRejectsUnmarkedWrongOwnerAndSymlinks(t *testing.T) {
	for _, kind := range []string{"unmarked", "wrong-owner", "owner-symlink", "tenant-symlink", "layout-symlink", "owner-permissions", "owner-directory"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			id := strings.Repeat("b", 64)
			dir := filepath.Join(base, "identity-v2", id)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			owner, _ := json.Marshal(ownerRecord{Version: 2, PrincipalID: id})
			outside := t.TempDir()
			switch kind {
			case "unmarked":
				os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0600)
			case "wrong-owner":
				os.WriteFile(filepath.Join(dir, "owner.json"), []byte(`{"version":2,"principal_id":"wrong"}`), 0600)
			case "owner-symlink":
				target := filepath.Join(outside, "owner.json")
				os.WriteFile(target, owner, 0600)
				if err := os.Symlink(target, filepath.Join(dir, "owner.json")); err != nil {
					t.Skip(err)
				}
			case "tenant-symlink":
				os.Remove(dir)
				if err := os.Symlink(outside, dir); err != nil {
					t.Skip(err)
				}
			case "layout-symlink":
				os.Remove(dir)
				os.Remove(filepath.Dir(dir))
				if err := os.Symlink(outside, filepath.Dir(dir)); err != nil {
					t.Skip(err)
				}
			case "owner-permissions":
				os.WriteFile(filepath.Join(dir, "owner.json"), owner, 0644)
			case "owner-directory":
				os.Mkdir(filepath.Join(dir, "owner.json"), 0700)
			}
			root, _, err := openTenantRoot(base, id)
			if root != nil {
				root.Close()
			}
			if err == nil {
				t.Fatal("unsafe tenant storage accepted")
			}
			entries, _ := os.ReadDir(outside)
			if kind != "owner-symlink" && len(entries) != 0 {
				t.Fatal("outside directory modified")
			}
		})
	}
}

type gatedProvider struct {
	fakeProvider
	started chan struct{}
	release <-chan struct{}
}

func (p *gatedProvider) GetCredential() (*credential.CredentialModel, error) {
	if p.started != nil {
		p.started <- struct{}{}
	}
	<-p.release
	return p.fakeProvider.GetCredential()
}

func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("bounded synthetic task did not finish")
		var zero T
		return zero
	}
}

func TestInitializationCoalescesWithoutBlockingOtherPrincipals(t *testing.T) {
	r, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "role"}})
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	r.newProviderFn = func(user, _ string) (credential.Credential, error) {
		calls.Add(1)
		if user == "slow" {
			return &gatedProvider{started: started, release: gate}, nil
		}
		return fakeProvider{}, nil
	}
	slow := authenticateTest(t, r, "slow")
	fast := authenticateTest(t, r, "fast")
	results := make(chan *Tenant, 2)
	failures := make(chan error, 2)
	go func() { tn, err := r.Resolve(slow); failures <- err; results <- tn }()
	receive(t, started)
	cancelCtx, cancel := context.WithCancel(slow)
	canceled := make(chan error, 1)
	go func() { _, err := r.Resolve(cancelCtx); canceled <- err }()
	cancel()
	if !errors.Is(receive(t, canceled), context.Canceled) {
		t.Fatal("waiter cancellation ignored")
	}
	ready := make(chan error, 1)
	go func() { _, err := r.Resolve(fast); ready <- err }()
	if err := receive(t, ready); err != nil {
		t.Fatal("slow principal blocked unrelated initialization", err)
	}
	go func() { tn, err := r.Resolve(slow); failures <- err; results <- tn }()
	close(gate)
	for i := 0; i < 2; i++ {
		if err := receive(t, failures); err != nil {
			t.Fatal(err)
		}
	}
	a, b := receive(t, results), receive(t, results)
	if a != b || calls.Load() != 2 {
		t.Fatal("same principal initialized more than once")
	}
}

func TestInitTimeoutRetainsActualWorkSlotsAndCloseWaits(t *testing.T) {
	r, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "role"}})
	r.initTimeout = 40 * time.Millisecond
	r.initLimit = 2
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var calls atomic.Int32
	r.newProviderFn = func(string, string) (credential.Credential, error) {
		calls.Add(1)
		return &gatedProvider{started: started, release: release}, nil
	}
	results := make(chan error, 2)
	for _, user := range []string{"a", "b"} {
		ctx := authenticateTest(t, r, user)
		go func() { _, err := r.Resolve(ctx); results <- err }()
	}
	receive(t, started)
	receive(t, started)
	for i := 0; i < 2; i++ {
		if !errors.Is(receive(t, results), errInitTimeout) {
			t.Fatal("initialization deadline ignored")
		}
	}
	if _, err := r.Resolve(authenticateTest(t, r, "c")); !errors.Is(err, errBusy) {
		t.Fatal("timeout released actual SDK slot")
	}
	if _, err := r.Resolve(authenticateTest(t, r, "a")); !errors.Is(err, errInitTimeout) {
		t.Fatal("same timed-out task replaced")
	}
	if calls.Load() != 2 {
		t.Fatal("unbounded SDK work")
	}
	r.mu.Lock()
	if len(r.inflight) != 2 || len(r.tenants) != 0 {
		t.Error("in-flight accounting wrong")
	}
	r.mu.Unlock()
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned before SDK work exited")
	default:
	}
	close(release)
	if err := receive(t, closed); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.inflight) != 0 || len(r.tenants) != 0 {
		t.Fatal("late tenant published or capacity leaked")
	}
	entries, _ := os.ReadDir(r.cfg.SessionsDir)
	if len(entries) != 0 {
		t.Fatal("late SDK result opened storage")
	}
}

func TestTotalTenantCapacityAndFailureRelease(t *testing.T) {
	r, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "role"}})
	r.cfg.Identity.MaxTenants = 2
	r.newProviderFn = func(user, _ string) (credential.Credential, error) {
		if user == "fail" {
			return nil, errors.New("synthetic-secret")
		}
		return fakeProvider{}, nil
	}
	if _, err := r.Resolve(authenticateTest(t, r, "fail")); err == nil || strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("initialization failure handling")
	}
	a, err := r.Resolve(authenticateTest(t, r, "a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Resolve(authenticateTest(t, r, "b")); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Resolve(authenticateTest(t, r, "c")); !errors.Is(err, errBusy) {
		t.Fatal("max tenants exceeded")
	}
	if cached, err := r.Resolve(authenticateTest(t, r, "a")); err != nil || cached != a {
		t.Fatal("capacity blocked cached tenant")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.root.Open("owner.json"); err == nil {
		t.Fatal("root retained after Close")
	}
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("closed registry accepted request")
	}
}

func TestRestorationBoundedAndDoesNotDelayPublish(t *testing.T) {
	r, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "role"}})
	r.newProviderFn = func(string, string) (credential.Credential, error) { return fakeProvider{}, nil }
	var active, peak atomic.Int32
	started := make(chan struct{}, 8)
	r.restoreFn = func(ctx context.Context, _ *session.Manager) {
		n := active.Add(1)
		for {
			prev := peak.Load()
			if n <= prev || peak.CompareAndSwap(prev, n) {
				break
			}
		}
		started <- struct{}{}
		<-ctx.Done()
		active.Add(-1)
	}
	for i := 0; i < 8; i++ {
		if _, err := r.Resolve(authenticateTest(t, r, fmt.Sprintf("u%d", i))); err != nil {
			t.Fatal("restore delayed tenant publication", err)
		}
	}
	for i := 0; i < 4; i++ {
		receive(t, started)
	}
	if peak.Load() != 4 || len(r.restores) != 4 {
		t.Fatal("restore limit not enforced")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if active.Load() != 0 {
		t.Fatal("restore work survived shutdown")
	}
}

func TestCachedAuthorizationFingerprint(t *testing.T) {
	for _, field := range []string{"mode", "workspace", "agent", "session-name"} {
		t.Run(field, func(t *testing.T) {
			r, _ := jwtRegistry(t, "employee_no", nil)
			first := ailyClaims()
			if _, err := resolveAuthenticated(r, jwtCtx(t, first)); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "mode":
				r.cfg.Identity.Default.Mode = "pro"
			case "workspace":
				r.cfg.Identity.Default.WorkspaceID = "changed"
			case "agent":
				r.cfg.Identity.Default.CustomAgentID = "changed"
			case "session-name":
				first["employee_no"] = "new-employee"
			}
			ctx, err := r.Authenticate(jwtCtx(t, first))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Resolve(ctx); !errors.Is(err, errMappingChanged) {
				t.Fatal("cached fingerprint mismatch accepted", err)
			}
		})
	}
}

func TestSharedBaseProviderSerializationIsBounded(t *testing.T) {
	// The SDK base source may serialize refreshes across roles; registry slots
	// must still cap work waiting behind that provider. No network is involved.
	var mu sync.Mutex
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	r, _ := identityRegistry(t, config.Identity{Enabled: true, AuthToken: testAuthToken, Default: &config.IdentityGroup{RoleArn: "role"}})
	r.initLimit = 2
	r.initTimeout = 40 * time.Millisecond
	r.newProviderFn = func(string, string) (credential.Credential, error) {
		mu.Lock()
		defer mu.Unlock()
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return fakeProvider{}, nil
	}
	results := make(chan error, 2)
	for _, u := range []string{"a", "b"} {
		ctx := authenticateTest(t, r, u)
		go func() { _, e := r.Resolve(ctx); results <- e }()
	}
	receive(t, started)
	for i := 0; i < 2; i++ {
		if !errors.Is(receive(t, results), errInitTimeout) {
			t.Fatal("serialized provider wait unbounded")
		}
	}
	if _, err := r.Resolve(authenticateTest(t, r, "c")); !errors.Is(err, errBusy) {
		t.Fatal("base serialization bypassed slot limit")
	}
	close(release)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func FuzzPrincipalNamespace(f *testing.F) {
	f.Add("a+b", "a_b")
	f.Add("é", "e\u0301")
	f.Add("Alice", "alice")
	f.Fuzz(func(t *testing.T, a, b string) {
		a, err := config.NormalizeIdentity(a)
		if err != nil || a == "" {
			return
		}
		b, err = config.NormalizeIdentity(b)
		if err != nil || b == "" || a == b {
			return
		}
		pa, _ := makePrincipal("fuzz", "headers", &Claims{UserID: a})
		pb, _ := makePrincipal("fuzz", "headers", &Claims{UserID: b})
		if pa.ID == pb.ID {
			t.Fatal("different identity values shared namespace")
		}
	})
}
