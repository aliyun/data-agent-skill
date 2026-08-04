package tenant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	credential "github.com/aliyun/credentials-go/credentials"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
)

// jwtRegistry builds a registry whose credential provider records the
// RoleSessionName it was asked for, so tests can assert on it without calling
// STS.
func jwtRegistry(t *testing.T, sessionClaim string, groups map[string]config.IdentityGroup) (*Registry, *[]string) {
	t.Helper()
	return identityRegistry(t, config.Identity{
		Enabled:          true,
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

	cfg := config.Config{Region: "cn-hangzhou", Identity: id}
	cfg.ApplyDefaults()
	if err := cfgValidate(t, &cfg); err != nil {
		t.Fatalf("config invalid: %v", err)
	}

	r := NewRegistry(context.Background(), cfg, &dataagent.Credential{
		AccessKeyID: "ak", AccessKeySecret: "sk",
	})

	var asked []string
	r.newProviderFn = func(sessionValue, roleArn string) (credential.Credential, error) {
		asked = append(asked, sessionName(cfg.Identity.SessionNamePrefix, sessionValue))
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
	token := signToken(t, testSecret, claims, time.Now().Add(time.Hour))
	return WithJWT(context.Background(), token)
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

			if _, err := r.Resolve(jwtCtx(t, ailyClaims())); err != nil {
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

	tn, err := r.Resolve(jwtCtx(t, ailyClaims()))
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
	if tn.Key != bigUserID {
		t.Errorf("tenant key = %q, want %q", tn.Key, bigUserID)
	}
	// ...while the audited session name uses the configured claim.
	if (*asked)[0] != "aily-E12345" {
		t.Errorf("RoleSessionName = %q, want aily-E12345", (*asked)[0])
	}
}

// Enabling JWT closes the header path: a request without a valid token is
// rejected rather than falling back to forgeable headers or to the server's own
// identity.
func TestResolveJWTFailClosed(t *testing.T) {
	r, asked := jwtRegistry(t, "user_id", nil)

	for name, ctx := range map[string]context.Context{
		"no token":    context.Background(),
		"empty token": WithJWT(context.Background(), ""),
		"garbage":     WithJWT(context.Background(), "not-a-jwt"),
		"wrong secret": WithJWT(context.Background(),
			signToken(t, "other-secret", ailyClaims(), time.Now().Add(time.Hour))),
		"expired": WithJWT(context.Background(),
			signToken(t, testSecret, ailyClaims(), time.Now().Add(-time.Minute))),
	} {
		tn, err := r.Resolve(ctx)
		if err == nil {
			t.Errorf("%s: expected rejection, got tenant %+v", name, tn)
		}
		if tn != nil {
			t.Errorf("%s: tenant returned alongside error", name)
		}
	}

	// Identity headers are not a way around the token requirement, with or
	// without a matching auth_token.
	for name, ctx := range map[string]context.Context{
		"headers only":            WithIdentity(context.Background(), bigUserID, "user@example.com", ""),
		"headers with auth token": WithIdentity(context.Background(), bigUserID, "user@example.com", "anything"),
	} {
		if _, err := r.Resolve(ctx); err == nil {
			t.Errorf("%s: plain identity headers were accepted while JWT is enabled", name)
		}
	}

	if len(*asked) != 0 {
		t.Errorf("rejected requests still triggered AssumeRole: %v", *asked)
	}
}

// A valid token decides the identity even when headers claim someone else.
func TestResolveIgnoresHeadersWhenJWTEnabled(t *testing.T) {
	r, asked := jwtRegistry(t, "user_id", nil)

	ctx := WithIdentity(jwtCtx(t, ailyClaims()), "ou_attacker", "attacker@example.com", "")
	tn, err := r.Resolve(ctx)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if tn.Key != bigUserID {
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
				SessionNameClaim: tc.claim,
				Default:          &config.IdentityGroup{RoleArn: "acs:ram::123:role/da-default"},
			})
			ctx := WithIdentity(context.Background(), "ou_alice", "alice@example.com", "")
			if _, err := r.Resolve(ctx); err != nil {
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

	_, err := r.Resolve(jwtCtx(t, claims))
	if err == nil {
		t.Fatal("expected rejection when the selected claim is empty")
	}
	if !strings.Contains(err.Error(), "enterprise_email") {
		t.Errorf("error should name the offending claim: %v", err)
	}
}

// The session name must stay within the STS charset and length limit even when
// the claim carries characters STS does not allow.
func TestResolveSanitizesSessionName(t *testing.T) {
	r, asked := jwtRegistry(t, "employee_no", nil)

	claims := ailyClaims()
	claims["employee_no"] = "emp/00 1#" + strings.Repeat("x", 80)

	if _, err := r.Resolve(jwtCtx(t, claims)); err != nil {
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
	if _, err := r.Resolve(jwtCtx(t, claims)); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := "aily-" + bigUserID; (*asked)[0] != want {
		t.Errorf("RoleSessionName = %q, want %q", (*asked)[0], want)
	}
}
