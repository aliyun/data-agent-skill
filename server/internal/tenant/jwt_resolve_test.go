package tenant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	credential "github.com/aliyun/credentials-go/credentials"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
)

const testAuthToken = "upstream-secret"

// jwtRegistry builds a registry whose credential provider records the
// RoleSessionName it was asked for, so tests can assert on it without calling
// STS.
func jwtRegistry(t *testing.T, sessionClaim string, groups map[string]config.IdentityGroup) (*Registry, *[]string) {
	t.Helper()
	return identityRegistry(t, config.Identity{
		Enabled:          true,
		AuthToken:        testAuthToken,
		SessionNameClaim: sessionClaim,
		JWT: config.JWT{
			Enabled: true,
			Secret:  testSecret,
			Header:  DefaultJWTHeader,
		},
		Default: &config.IdentityGroup{RoleArn: "acs:ram::123:role/da-default"},
		Groups:  groups,
	})
}

// identityRegistry builds a registry from a full identity config, so tests can
// vary auth_token and require_identity alongside the JWT settings.
func identityRegistry(t *testing.T, id config.Identity) (*Registry, *[]string) {
	t.Helper()

	id.Namespace = "test-domain"
	id.RequireIdentity = true
	id.MaxTenants = 128
	if id.JWT.Enabled {
		if id.Default != nil {
			id.Default.TenantID = bigTenantID
			id.Default.AgentID = "agent_abc"
		}
		for name, g := range id.Groups {
			g.TenantID = bigTenantID
			g.AgentID = "agent_abc"
			id.Groups[name] = g
		}
	}
	cfg := config.Config{Region: "cn-hangzhou", SessionsDir: t.TempDir(), Identity: id, HTTP: config.DefaultHTTP()}
	cfg.ApplyDefaults()
	if err := cfgValidate(t, &cfg); err != nil {
		t.Fatalf("config invalid: %v", err)
	}

	baseCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := NewRegistry(baseCtx, cfg, &fakeProvider{})
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})

	var asked []string
	var askedMu sync.Mutex
	r.newProviderFn = func(sessionValue, roleArn string) (credential.Credential, error) {
		askedMu.Lock()
		asked = append(asked, sessionName(cfg.Identity.Prefix(), sessionValue))
		askedMu.Unlock()
		// A static credential keeps currentCredential from calling STS.
		return credential.NewCredential(new(credential.Config).
			SetType("access_key").SetAccessKeyId("ak").SetAccessKeySecret("sk"))
	}
	return r, &asked
}

// cfgValidate exercises the exported Load path indirectly: validate() is
// unexported, so round-trip through ApplyDefaults and check the claim name the
// same way Load would.
func cfgValidate(t *testing.T, c *config.Config) error {
	t.Helper()
	if _, err := (&Claims{UserID: "x"}).Field(c.Identity.SessionNameClaim); err != nil {
		return err
	}
	return nil
}

func jwtCtx(t *testing.T, claims map[string]any) context.Context {
	t.Helper()
	scoped := map[string]any{"tenant_id": bigTenantID, "agent_id": "agent_abc"}
	for k, v := range claims {
		scoped[k] = v
	}
	token := signToken(t, testSecret, scoped, time.Now().Add(time.Hour))
	// auth_token now guards every request, so carry it alongside the JWT.
	return WithIdentity(WithJWT(context.Background(), token), "", "", testAuthToken)
}

func TestLegacyAuthenticationMatrix(t *testing.T) {
	fields := map[string]any{"user_id": "signed-user", "email": "signed@example.com"}
	valid := signToken(t, testSecret, fields, time.Now().Add(time.Hour))
	for _, tc := range []struct {
		name        string
		user        string
		email       string
		token       string
		hasJWT      bool
		jwt         string
		wantMode    string
		wantSession string
	}{
		{name: "Header user fallback", user: "header-user", token: testAuthToken, wantMode: "headers", wantSession: "header-user"},
		{name: "Header email", email: "header@example.com", token: testAuthToken, wantMode: "headers", wantSession: "header@example.com"},
		{name: "Header both", user: "header-user", email: "header@example.com", token: testAuthToken, wantMode: "headers", wantSession: "header@example.com"},
		{name: "JWT without scopes", token: testAuthToken, hasJWT: true, jwt: valid, wantMode: "jwt", wantSession: "signed@example.com"},
		{name: "signed identity wins", user: "other-user", email: "other@example.com", token: testAuthToken, hasJWT: true, jwt: valid, wantMode: "jwt", wantSession: "signed@example.com"},
		{name: "missing identity", token: testAuthToken},
		{name: "missing platform token", user: "header-user"},
		{name: "wrong platform token", user: "header-user", token: "wrong"},
		{name: "JWT missing platform token", hasJWT: true, jwt: valid},
		{name: "JWT wrong platform token", token: "wrong", hasJWT: true, jwt: valid},
		{name: "empty JWT", user: "header-user", token: testAuthToken, hasJWT: true},
		{name: "blank JWT", user: "header-user", token: testAuthToken, hasJWT: true, jwt: "   "},
		{name: "invalid JWT", user: "header-user", token: testAuthToken, hasJWT: true, jwt: "invalid"},
		{name: "wrong signature", user: "header-user", token: testAuthToken, hasJWT: true, jwt: signToken(t, "wrong-key", fields, time.Now().Add(time.Hour))},
		{name: "expired JWT", user: "header-user", token: testAuthToken, hasJWT: true, jwt: signToken(t, testSecret, fields, time.Now().Add(-time.Hour))},
		{name: "missing expiry", user: "header-user", token: testAuthToken, hasJWT: true, jwt: signToken(t, testSecret, fields, time.Time{})},
		{name: "JWT missing selected email", user: "header-user", email: "header@example.com", token: testAuthToken, hasJWT: true, jwt: signToken(t, testSecret, map[string]any{"user_id": "signed-user"}, time.Now().Add(time.Hour))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{SessionsDir: t.TempDir(), HTTP: config.DefaultHTTP(), Identity: config.Identity{
				Enabled: true, RequireIdentity: true, AuthToken: testAuthToken, MaxTenants: 128,
				SessionNameClaim: "email", SessionNamePrefix: new(string),
				JWT:     config.JWT{Enabled: true, Secret: testSecret},
				Default: &config.IdentityGroup{RoleArn: "shared-role"},
			}}
			cfg.ApplyDefaults()
			if err := cfg.ValidateTransport("streamable-http"); err != nil {
				t.Fatal(err)
			}
			r := NewRegistry(context.Background(), cfg, fakeProvider{})
			t.Cleanup(func() { _ = r.Close() })
			var asked string
			r.newProviderFn = func(value, role string) (credential.Credential, error) {
				asked = value
				if role != "shared-role" {
					t.Error("unexpected role mapping")
				}
				return fakeProvider{}, nil
			}
			ctx := WithIdentity(context.Background(), tc.user, tc.email, tc.token)
			if tc.hasJWT {
				ctx = WithJWT(ctx, tc.jwt)
			}
			verified, err := r.Authenticate(ctx)
			if asked != "" {
				t.Fatal("authentication initialized credentials")
			}
			if tc.wantMode == "" {
				if verified != nil || !IsAuthenticationError(err) {
					t.Fatalf("expected authentication rejection: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			principal := PrincipalFromContext(verified)
			if principal.AuthMode != tc.wantMode || principal.TenantID != "" || principal.AgentID != "" || principal.ExpiresAt.IsZero() != (tc.wantMode == "headers") {
				t.Fatal("unexpected legacy principal")
			}
			ten, err := r.Resolve(verified)
			if err != nil || ten == nil || asked != tc.wantSession || ten.roleSessionName != tc.wantSession {
				t.Fatalf("legacy role session name mismatch: %v", err)
			}
		})
	}
}

// The claim named by identity.session_name_claim is what ends up in the
// RoleSessionName that ActionTrail records.
func TestResolveSessionNameFollowsConfiguredClaim(t *testing.T) {
	for _, tc := range []struct {
		claim string
		want  string
	}{
		{"user_id", "aily-" + bigUserID},
		{"email", "aily-user@example.com"},
		{"employee_no", "aily-E12345"},
		{"tenant_id", "aily-" + bigTenantID},
	} {
		t.Run(tc.claim, func(t *testing.T) {
			r, asked := jwtRegistry(t, tc.claim, nil)

			if _, err := resolveAuthenticated(r, jwtCtx(t, ailyClaims())); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if len(*asked) != 1 {
				t.Fatalf("expected one AssumeRole, got %v", *asked)
			}
			if (*asked)[0] != tc.want {
				t.Errorf("RoleSessionName = %q, want %q", (*asked)[0], tc.want)
			}
		})
	}
}

// Group membership is matched on user_id/email even when the session name is
// taken from another claim, so tenants keep landing in the right group.
func TestResolveGroupMatchingIndependentOfSessionNameClaim(t *testing.T) {
	groups := map[string]config.IdentityGroup{
		"analysts": {
			RoleArn: "acs:ram::123:role/da-analysts",
			Mode:    "pro",
			Users:   []string{bigUserID},
		},
	}
	r, asked := jwtRegistry(t, "employee_no", groups)

	tn, err := resolveAuthenticated(r, jwtCtx(t, ailyClaims()))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tn.Group != "analysts" {
		t.Errorf("group = %q, want analysts", tn.Group)
	}
	if tn.RoleArn != "acs:ram::123:role/da-analysts" {
		t.Errorf("role = %q", tn.RoleArn)
	}
	if tn.Defaults.Mode != "pro" {
		t.Errorf("mode = %q, want pro", tn.Defaults.Mode)
	}
	// Isolation still keys on the user id...
	if tn.Key != expectedPrincipalID(r, bigUserID, "") {
		t.Errorf("tenant key = %q, want %q", tn.Key, bigUserID)
	}
	// ...while the audited session name uses the configured claim.
	if (*asked)[0] != "aily-E12345" {
		t.Errorf("RoleSessionName = %q, want aily-E12345", (*asked)[0])
	}
}

// auth_token guards every request first: a wrong or missing token header is
// rejected before any identity is read, on both the JWT and header paths.
func TestResolveAuthTokenGuardsBothPaths(t *testing.T) {
	r, asked := jwtRegistry(t, "user_id", nil)

	// JWT path: valid token but wrong auth_token.
	badAuth := WithIdentity(jwtCtx(t, ailyClaims()), "", "", "wrong")
	if _, err := resolveAuthenticated(r, badAuth); err == nil {
		t.Error("valid JWT was accepted with a wrong auth_token")
	}
	// Header path: headers present but wrong auth_token.
	badAuthHdr := WithIdentity(context.Background(), "ou_alice", "", "wrong")
	if _, err := resolveAuthenticated(r, badAuthHdr); err == nil {
		t.Error("headers were accepted with a wrong auth_token")
	}
	if len(*asked) != 0 {
		t.Errorf("auth failures still triggered AssumeRole: %v", *asked)
	}
}

// A token that is present but unverifiable is rejected outright and never
// retried against the headers, so a broken token cannot downgrade the path.
func TestResolveRejectsUnverifiableTokenNoDowngrade(t *testing.T) {
	r, asked := jwtRegistry(t, "user_id", nil)

	for name, raw := range map[string]string{
		"garbage":      "not-a-jwt",
		"wrong secret": signToken(t, "other-secret", ailyClaims(), time.Now().Add(time.Hour)),
		"expired":      signToken(t, testSecret, ailyClaims(), time.Now().Add(-time.Minute)),
	} {
		// Correct auth_token plus forged headers: a downgrade would succeed.
		ctx := WithIdentity(WithJWT(context.Background(), raw), bigUserID, "u@example.com", testAuthToken)
		if _, err := resolveAuthenticated(r, ctx); err == nil {
			t.Errorf("%s: broken token was accepted", name)
		}
	}
	if len(*asked) != 0 {
		t.Errorf("rejected tokens still triggered AssumeRole: %v", *asked)
	}
}

func TestResolveScopedMappingRejectsHeadersWhenJWTAbsent(t *testing.T) {
	r, asked := jwtRegistry(t, "user_id", nil)

	for name, ctx := range map[string]context.Context{
		"no token":    WithIdentity(context.Background(), "ou_alice", "a@example.com", testAuthToken),
		"empty token": WithIdentity(WithJWT(context.Background(), ""), "ou_alice", "a@example.com", testAuthToken),
	} {
		tn, err := resolveAuthenticated(r, ctx)
		if err == nil || tn != nil {
			t.Fatalf("%s: missing JWT accepted", name)
		}
	}
	if len(*asked) != 0 {
		t.Fatal("rejected request initialized provider")
	}
}

// A valid token decides the identity even when headers claim someone else.
func TestResolveIgnoresHeadersWhenJWTEnabled(t *testing.T) {
	r, asked := jwtRegistry(t, "user_id", nil)

	ctx := WithIdentity(jwtCtx(t, ailyClaims()), "ou_attacker", "attacker@example.com", testAuthToken)
	tn, err := resolveAuthenticated(r, ctx)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tn.Key != expectedPrincipalID(r, bigUserID, "") {
		t.Errorf("tenant key = %q, want the token's user %q", tn.Key, bigUserID)
	}
	if (*asked)[0] != "aily-"+bigUserID {
		t.Errorf("RoleSessionName = %q, want the token's user", (*asked)[0])
	}
}

// session_name_claim also drives the header path, so a deployment that has not
// enabled JWT can still audit by email instead of user id.
func TestResolveSessionNameClaimOnHeaderPath(t *testing.T) {
	for _, tc := range []struct {
		claim string
		want  string
	}{
		{"user_id", "aily-ou_alice"},
		{"email", "aily-alice@example.com"},
		// Only a user id and an email arrive in headers, so a JWT-only claim
		// falls back to the tenant key instead of producing an empty name.
		{"employee_no", "aily-ou_alice"},
	} {
		t.Run(tc.claim, func(t *testing.T) {
			r, asked := identityRegistry(t, config.Identity{
				Enabled:          true,
				AuthToken:        testAuthToken,
				SessionNameClaim: tc.claim,
				Default:          &config.IdentityGroup{RoleArn: "acs:ram::123:role/da-default"},
			})
			ctx := WithIdentity(context.Background(), "ou_alice", "alice@example.com", testAuthToken)
			_, err := resolveAuthenticated(r, ctx)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if (*asked)[0] != tc.want {
				t.Errorf("RoleSessionName = %q, want %q", (*asked)[0], tc.want)
			}
		})
	}
}

// A claim that is empty in the token cannot silently become an empty session
// name, which STS would reject or which would merge distinct users.
func TestResolveRejectsEmptySessionNameClaim(t *testing.T) {
	r, _ := jwtRegistry(t, "enterprise_email", nil)

	claims := ailyClaims()
	claims["enterprise_email"] = "" // the doc notes this is often unset

	_, err := resolveAuthenticated(r, jwtCtx(t, claims))
	if err == nil {
		t.Fatal("expected rejection when the selected claim is empty")
	}
	if !IsAuthenticationError(err) {
		t.Errorf("expected safe authentication category: %v", err)
	}
}

// The session name must stay within the STS charset and length limit even when
// the claim carries characters STS does not allow.
func TestResolveSanitizesSessionName(t *testing.T) {
	r, asked := jwtRegistry(t, "employee_no", nil)

	claims := ailyClaims()
	claims["employee_no"] = "emp/00 1#" + strings.Repeat("x", 80)

	if _, err := resolveAuthenticated(r, jwtCtx(t, claims)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := (*asked)[0]
	if len(got) > 64 {
		t.Errorf("RoleSessionName longer than the STS limit: %d chars (%q)", len(got), got)
	}
	for _, bad := range []string{"/", " ", "#"} {
		if strings.Contains(got, bad) {
			t.Errorf("RoleSessionName kept illegal character %q: %q", bad, got)
		}
	}
}

// Numeric claims keep full precision all the way into the session name.
func TestResolveSessionNameKeepsIDPrecision(t *testing.T) {
	r, asked := jwtRegistry(t, "user_id", nil)

	claims := map[string]any{
		"user_id": json.Number(bigUserID),
		"email":   "user@example.com",
	}
	if _, err := resolveAuthenticated(r, jwtCtx(t, claims)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "aily-" + bigUserID; (*asked)[0] != want {
		t.Errorf("RoleSessionName = %q, want %q", (*asked)[0], want)
	}
}

// End-to-end through Resolve: the RoleSessionName actually handed to
// AssumeRole follows the configured prefix, including dropping it entirely.
func TestResolveRoleSessionNamePrefixVariants(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	for _, tc := range []struct {
		name   string
		prefix *string
		want   string
	}{
		{"absent keeps the historical prefix", nil, "aily-ou_alice"},
		{"explicitly empty drops the prefix", strPtr(""), "ou_alice"},
		{"custom prefix", strPtr("prod"), "prod-ou_alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, asked := identityRegistry(t, config.Identity{
				Enabled:           true,
				AuthToken:         testAuthToken,
				SessionNamePrefix: tc.prefix,
				Default:           &config.IdentityGroup{RoleArn: "acs:ram::123:role/da-default"},
			})
			ctx := WithIdentity(context.Background(), "ou_alice", "", testAuthToken)
			if _, err := resolveAuthenticated(r, ctx); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if len(*asked) == 0 || (*asked)[0] != tc.want {
				t.Errorf("RoleSessionName = %v, want %q", *asked, tc.want)
			}
		})
	}
}
