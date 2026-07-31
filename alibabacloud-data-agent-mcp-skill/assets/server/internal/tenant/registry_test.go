package tenant

import (
	"context"
	"strings"
	"testing"

	credential "github.com/aliyun/credentials-go/credentials"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
)

func testRegistry(cfg config.Config) *Registry {
	cfg.ApplyDefaults() // fill Aily-compatible header names and prefix
	base := &dataagent.Credential{AccessKeyID: "ak", AccessKeySecret: "sk"}
	return NewRegistry(context.Background(), cfg, base)
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
	if _, err := r.Resolve(ctx); err == nil || !strings.Contains(err.Error(), "x-aily-token") {
		t.Fatalf("expected shared-secret rejection, got %v", err)
	}
}

func TestResolveRequireIdentityFailClosed(t *testing.T) {
	r := testRegistry(config.Config{Identity: config.Identity{
		Enabled:         true,
		RequireIdentity: true,
	}})

	if _, err := r.Resolve(context.Background()); err == nil || !strings.Contains(err.Error(), "x-aily-user") {
		t.Fatalf("expected identity-required rejection, got %v", err)
	}
}

func TestResolveFallsBackToDefaultIdentity(t *testing.T) {
	r := testRegistry(config.Config{Identity: config.Identity{
		Enabled:         true,
		RequireIdentity: false,
	}})

	tn, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tn != nil {
		t.Fatal("expected nil tenant (default identity) when headers absent")
	}
}

func TestResolveRejectsUnmappedUser(t *testing.T) {
	r := testRegistry(config.Config{Identity: config.Identity{
		Enabled: true,
		Groups: map[string]config.IdentityGroup{
			"g1": {RoleArn: "acs:ram::123:role/aily-alice", Users: []string{"ou_alice"}},
		},
	}})

	ctx := WithIdentity(context.Background(), "ou_mallory", "mallory@example.com", "")
	if _, err := r.Resolve(ctx); err == nil || !strings.Contains(err.Error(), "no identity group") {
		t.Fatalf("expected unmatched-user rejection, got %v", err)
	}
}

func TestSessionNameSanitization(t *testing.T) {
	got := sessionName("", "ou_张三/([)] weird")
	if strings.ContainsAny(got, "/([)] ") {
		t.Fatalf("sessionName not sanitized: %q", got)
	}
	if !strings.HasPrefix(got, "aily-") {
		t.Fatalf("sessionName missing prefix: %q", got)
	}
	if len(got) < 2 || len(got) > 64 {
		t.Fatalf("sessionName length out of STS bounds: %q", got)
	}

	long := sessionName("", strings.Repeat("a", 100))
	if len(long) != 64 {
		t.Fatalf("long sessionName not truncated: %d", len(long))
	}
}

func TestSessionNamePrefix(t *testing.T) {
	// Empty prefix falls back to the historical "aily-<user>" form.
	if got := sessionName("", "ou_alice"); got != "aily-ou_alice" {
		t.Fatalf("empty prefix changed legacy form: %q", got)
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

func TestSanitizePath(t *testing.T) {
	if got := sanitize("../../etc/passwd"); strings.Contains(got, "/") || strings.HasPrefix(got, ".") {
		t.Fatalf("path traversal not neutralized: %q", got)
	}
	if got := sanitize("bob@example.com"); got != "bob@example.com" {
		t.Fatalf("safe chars mangled: %q", got)
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
	var sessionNames []string
	r.newProviderFn = func(key, roleArn string) (credential.Credential, error) {
		if roleArn != sharedRole {
			t.Fatalf("unexpected role arn %q", roleArn)
		}
		sessionNames = append(sessionNames, sessionName(r.cfg.Identity.SessionNamePrefix, key))
		return fakeProvider{}, nil
	}

	alice, err := r.Resolve(WithIdentity(context.Background(), "ou_alice", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	bob, err := r.Resolve(WithIdentity(context.Background(), "ou_bob", "", ""))
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
	again, err := r.Resolve(WithIdentity(context.Background(), "ou_alice", "", ""))
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
