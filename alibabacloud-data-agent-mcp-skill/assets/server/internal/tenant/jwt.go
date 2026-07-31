package tenant

// JWT verification for upstream-signed end-user identity.
//
// Feishu Aily signs the caller's identity into a JWT and sends it on every MCP
// request. Because the signature proves the request came from the platform, a
// verified JWT authenticates the caller on its own — unlike the plain identity
// headers, which anyone reaching the endpoint could forge.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// DefaultJWTHeader is the header Aily puts the signed identity in. The value is
// the bare token, with no "Bearer " prefix.
const DefaultJWTHeader = "x-aily-jwt"

// Claims is the verified end-user identity carried by the JWT.
//
// Numeric ids are kept as strings: Aily sends 19-digit values such as
// 7620774801438674448, which exceed the 53-bit mantissa of the float64 that a
// JSON number would otherwise decode into. Rounding one of these would corrupt
// the tenant key and the RoleSessionName recorded in ActionTrail.
type Claims struct {
	UserID          string
	TenantID        string
	Email           string
	EnterpriseEmail string
	EmployeeNo      string
	DepartmentIDs   []string
	AgentID         string
}

// Field returns the claim selected by name, for choosing which value becomes
// the STS RoleSessionName. An unknown name yields an error so a typo in the
// configuration surfaces at startup instead of silently falling back.
func (c *Claims) Field(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "user_id", "user":
		return c.UserID, nil
	case "email":
		return c.Email, nil
	case "enterprise_email":
		return c.EnterpriseEmail, nil
	case "employee_no", "employee_number":
		return c.EmployeeNo, nil
	case "tenant_id":
		return c.TenantID, nil
	case "agent_id":
		return c.AgentID, nil
	default:
		return "", fmt.Errorf("unknown identity claim %q (valid: user_id, email, enterprise_email, employee_no, tenant_id, agent_id)", name)
	}
}

// ValidSessionNameClaims lists the claim names accepted by Claims.Field, for
// validating configuration before the server starts serving.
var ValidSessionNameClaims = []string{
	"user_id", "email", "enterprise_email", "employee_no", "tenant_id", "agent_id",
}

// VerifyJWT checks an Aily identity token and returns its claims.
//
// The signing method is pinned to HS256: accepting whatever the token's own
// "alg" header asks for is the classic algorithm-confusion flaw, where an
// attacker downgrades to "none" or has the HMAC key verified as an RSA public
// key. Expiry is required, so a leaked token cannot be replayed indefinitely.
func VerifyJWT(token, secret string) (*Claims, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("missing identity token")
	}
	if secret == "" {
		return nil, fmt.Errorf("identity.jwt.secret is not configured")
	}

	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		// Decode numbers as json.Number so the 19-digit ids keep every digit.
		jwt.WithJSONNumber(),
	)

	parsed, err := parser.Parse(token, func(*jwt.Token) (interface{}, error) {
		// The platform hands out the secret as a plain string; it is the HMAC
		// key as-is, with no base64 decoding.
		return []byte(secret), nil
	})
	if err != nil {
		// The error text describes the failure (bad signature, expired, wrong
		// alg) without echoing the token.
		return nil, fmt.Errorf("identity token rejected: %w", err)
	}

	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("identity token rejected: unexpected claims type %T", parsed.Claims)
	}

	claims := &Claims{
		UserID:          claimString(mc, "user_id"),
		TenantID:        claimString(mc, "tenant_id"),
		Email:           claimString(mc, "email"),
		EnterpriseEmail: claimString(mc, "enterprise_email"),
		EmployeeNo:      claimString(mc, "employee_no"),
		AgentID:         claimString(mc, "agent_id"),
		DepartmentIDs:   claimStrings(mc, "department_ids"),
	}
	if claims.UserID == "" && claims.Email == "" {
		return nil, fmt.Errorf("identity token rejected: neither user_id nor email is present")
	}
	return claims, nil
}

// claimString reads a claim as text, preserving full precision for the numeric
// ids that arrive as json.Number.
func claimString(mc jwt.MapClaims, key string) string {
	switch v := mc[key].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case bool:
		return fmt.Sprintf("%t", v)
	case float64:
		// Only reachable if the parser was built without WithJSONNumber; format
		// without an exponent so the value stays recognisable.
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func claimStrings(mc jwt.MapClaims, key string) []string {
	raw, ok := mc[key].([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		switch v := item.(type) {
		case string:
			out = append(out, v)
		case json.Number:
			out = append(out, v.String())
		default:
			out = append(out, fmt.Sprintf("%v", v))
		}
	}
	return out
}

type claimsCtxKey struct{}

// WithClaims stores verified claims for the rest of the request.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey{}, c)
}

// ClaimsFromContext returns the claims stored by WithClaims, or nil when the
// request carried no verified identity.
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(claimsCtxKey{}).(*Claims)
	return c
}

type jwtCtxKey struct{}

// WithJWT carries the raw, still unverified token from the transport layer to
// Resolve, which owns the secret needed to verify it.
func WithJWT(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, jwtCtxKey{}, token)
}

// JWTFromContext returns the raw token stored by WithJWT.
func JWTFromContext(ctx context.Context) string {
	t, _ := ctx.Value(jwtCtxKey{}).(string)
	return t
}
