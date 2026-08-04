package tenant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "platform-generated-hmac-secret"

// realistic user_id / tenant_id values from the Aily documentation: 19 digits,
// which is what makes float64 decoding unsafe.
const (
	bigUserID   = "7620774801438674448"
	bigTenantID = "7283059256756502547"
)

// signToken builds an HS256 token the way the platform does.
func signToken(t *testing.T, secret string, claims map[string]any, exp time.Time) string {
	t.Helper()
	mc := jwt.MapClaims{}
	for k, v := range claims {
		mc[k] = v
	}
	if !exp.IsZero() {
		mc["exp"] = exp.Unix()
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, mc).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return s
}

// ailyClaims mirrors the documented payload, with the ids as raw JSON numbers.
func ailyClaims() map[string]any {
	return map[string]any{
		"user_id":          json.Number(bigUserID),
		"tenant_id":        json.Number(bigTenantID),
		"email":            "user@example.com",
		"enterprise_email": "user@corp.example.com",
		"employee_no":      "E12345",
		"department_ids":   []any{"7296039729730945044"},
		"agent_id":         "agent_abc",
	}
}

func TestVerifyJWTExtractsDocumentedClaims(t *testing.T) {
	token := signToken(t, testSecret, ailyClaims(), time.Now().Add(time.Hour))

	claims, err := VerifyJWT(token, testSecret)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"UserID", claims.UserID, bigUserID},
		{"TenantID", claims.TenantID, bigTenantID},
		{"Email", claims.Email, "user@example.com"},
		{"EnterpriseEmail", claims.EnterpriseEmail, "user@corp.example.com"},
		{"EmployeeNo", claims.EmployeeNo, "E12345"},
		{"AgentID", claims.AgentID, "agent_abc"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if len(claims.DepartmentIDs) != 1 || claims.DepartmentIDs[0] != "7296039729730945044" {
		t.Errorf("DepartmentIDs = %v", claims.DepartmentIDs)
	}
}

// A 19-digit id exceeds the 53-bit mantissa of float64, so decoding it as a
// JSON number would round it and corrupt both the tenant key and the audited
// RoleSessionName.
func TestVerifyJWTKeepsLargeIDPrecision(t *testing.T) {
	token := signToken(t, testSecret, ailyClaims(), time.Now().Add(time.Hour))

	claims, err := VerifyJWT(token, testSecret)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}
	if claims.UserID != bigUserID {
		t.Fatalf("user_id lost precision: got %q, want %q", claims.UserID, bigUserID)
	}
	if strings.ContainsAny(claims.UserID, "eE+.") {
		t.Errorf("user_id rendered in exponent form: %q", claims.UserID)
	}
}

// Pinning the algorithm is what stops an attacker from downgrading the token.
func TestVerifyJWTRejectsNonHS256(t *testing.T) {
	mc := jwt.MapClaims{"user_id": bigUserID, "exp": time.Now().Add(time.Hour).Unix()}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, mc).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("build alg=none token: %v", err)
	}
	if _, err := VerifyJWT(unsigned, testSecret); err == nil {
		t.Fatal("alg=none token was accepted")
	}
}

func TestVerifyJWTRejectsBadSignatureAndExpiry(t *testing.T) {
	valid := ailyClaims()

	if _, err := VerifyJWT(signToken(t, "other-secret", valid, time.Now().Add(time.Hour)), testSecret); err == nil {
		t.Error("token signed with the wrong secret was accepted")
	}
	if _, err := VerifyJWT(signToken(t, testSecret, valid, time.Now().Add(-time.Minute)), testSecret); err == nil {
		t.Error("expired token was accepted")
	}
	// exp is mandatory: without it a leaked token would be valid forever.
	if _, err := VerifyJWT(signToken(t, testSecret, valid, time.Time{}), testSecret); err == nil {
		t.Error("token without exp was accepted")
	}
	if _, err := VerifyJWT("", testSecret); err == nil {
		t.Error("empty token was accepted")
	}
	if _, err := VerifyJWT(signToken(t, testSecret, valid, time.Now().Add(time.Hour)), ""); err == nil {
		t.Error("verification succeeded without a configured secret")
	}
}

func TestVerifyJWTRequiresAnIdentityClaim(t *testing.T) {
	token := signToken(t, testSecret, map[string]any{"agent_id": "agent_only"}, time.Now().Add(time.Hour))
	if _, err := VerifyJWT(token, testSecret); err == nil {
		t.Fatal("token without user_id or email was accepted")
	}
}

// Errors must describe the failure without echoing the token, which would put
// a replayable credential in the log.
func TestVerifyJWTErrorOmitsToken(t *testing.T) {
	token := signToken(t, "other-secret", ailyClaims(), time.Now().Add(time.Hour))
	_, err := VerifyJWT(token, testSecret)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("error echoed the token: %v", err)
	}
	// The signature segment alone is enough to replay, so check it too.
	if parts := strings.Split(token, "."); len(parts) == 3 && strings.Contains(err.Error(), parts[2]) {
		t.Errorf("error echoed the signature: %v", err)
	}
}

func TestClaimsField(t *testing.T) {
	c := &Claims{
		UserID:          bigUserID,
		TenantID:        bigTenantID,
		Email:           "user@example.com",
		EnterpriseEmail: "user@corp.example.com",
		EmployeeNo:      "E12345",
		AgentID:         "agent_abc",
	}
	for name, want := range map[string]string{
		"":                 bigUserID, // unset selects user_id
		"user_id":          bigUserID,
		"USER_ID":          bigUserID,
		"email":            "user@example.com",
		"enterprise_email": "user@corp.example.com",
		"employee_no":      "E12345",
		"tenant_id":        bigTenantID,
		"agent_id":         "agent_abc",
	} {
		got, err := c.Field(name)
		if err != nil {
			t.Errorf("Field(%q): %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("Field(%q) = %q, want %q", name, got, want)
		}
	}

	if _, err := c.Field("nickname"); err == nil {
		t.Error("unknown claim name was accepted")
	}
}

func TestJWTContextRoundTrip(t *testing.T) {
	ctx := WithJWT(context.Background(), "tok")
	if got := JWTFromContext(ctx); got != "tok" {
		t.Errorf("JWTFromContext = %q", got)
	}
	if got := JWTFromContext(context.Background()); got != "" {
		t.Errorf("empty context should yield no token, got %q", got)
	}

	c := &Claims{UserID: bigUserID}
	ctx = WithClaims(context.Background(), c)
	if got := ClaimsFromContext(ctx); got == nil || got.UserID != bigUserID {
		t.Errorf("ClaimsFromContext = %+v", got)
	}
	if got := ClaimsFromContext(context.Background()); got != nil {
		t.Errorf("empty context should yield no claims, got %+v", got)
	}
}

// Multiple secrets let several upstream agents share one server; a token is
// accepted if any configured key verifies it.
func TestVerifyJWTMultipleSecrets(t *testing.T) {
	claims := ailyClaims()
	tokA := signToken(t, "secret-a", claims, time.Now().Add(time.Hour))
	tokB := signToken(t, "secret-b", claims, time.Now().Add(time.Hour))

	// Either agent's token verifies against the shared key set, in any order.
	for _, tok := range []string{tokA, tokB} {
		if _, err := VerifyJWT(tok, "secret-a", "secret-b"); err != nil {
			t.Errorf("token rejected by the multi-secret set: %v", err)
		}
	}

	// A token signed with an unlisted key is still rejected.
	tokC := signToken(t, "secret-c", claims, time.Now().Add(time.Hour))
	if _, err := VerifyJWT(tokC, "secret-a", "secret-b"); err == nil {
		t.Error("token signed with an unconfigured secret was accepted")
	}

	// Empty entries are skipped, not treated as a valid key.
	if _, err := VerifyJWT(tokA, "", "secret-a"); err != nil {
		t.Errorf("empty secret entry broke verification: %v", err)
	}
	if _, err := VerifyJWT(tokA); err == nil {
		t.Error("verification succeeded with no secrets")
	}
}

// A non-signature failure (expiry) must be reported as such even with several
// secrets, rather than masked as a signature mismatch after trying them all.
func TestVerifyJWTMultipleSecretsReportsExpiry(t *testing.T) {
	expired := signToken(t, "secret-a", ailyClaims(), time.Now().Add(-time.Minute))
	_, err := VerifyJWT(expired, "secret-a", "secret-b")
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected an expiry error, got %v", err)
	}
}
