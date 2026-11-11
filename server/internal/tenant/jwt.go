package tenant

// A supplied JWT must verify; only its absence permits trusted Header identity.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
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
	ExpiresAt       time.Time
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
		return "", errors.New("unknown identity claim (valid: user_id, email, enterprise_email, employee_no, tenant_id, agent_id)")
	}
}

// ValidSessionNameClaims lists the claim names accepted by Claims.Field, for
// validating configuration before the server starts serving.
var ValidSessionNameClaims = []string{
	"user_id", "email", "enterprise_email", "employee_no", "tenant_id", "agent_id",
}

// VerifyJWT checks an Aily identity token against one or more HMAC secrets and
// returns its claims. Multiple secrets let several upstream agents, each
// signing with its own key, share one server: the token is accepted if any
// secret verifies it.
//
// The signing method is pinned to HS256: accepting whatever the token's own
// "alg" header asks for is the classic algorithm-confusion flaw, where an
// attacker downgrades to "none" or has the HMAC key verified as an RSA public
// key. Expiry is required, so a leaked token cannot be replayed indefinitely.
func VerifyJWT(token string, secrets ...string) (*Claims, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("missing identity token")
	}
	if len(secrets) == 0 {
		return nil, fmt.Errorf("identity.jwt secret is not configured")
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 64*1024 {
		return nil, errors.New("identity token rejected")
	}
	for _, part := range parts[:2] {
		data, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil || !strictJWTJSON(data) {
			return nil, errors.New("identity token rejected")
		}
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
		// Decode numbers as json.Number so the 19-digit ids keep every digit.
		jwt.WithJSONNumber(),
	)

	var parsed *jwt.Token
	var lastErr error
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		tok, err := parser.Parse(token, func(*jwt.Token) (interface{}, error) {
			// The platform hands out the secret as a plain string; it is the
			// HMAC key as-is, with no base64 decoding.
			return []byte(secret), nil
		})
		if err == nil {
			parsed = tok
			break
		}
		lastErr = err
		// Only a signature mismatch is worth trying the next key. Anything else
		// (expired, malformed, wrong alg) fails identically for every key, so
		// stop and report it instead of masking it as a signature failure.
		if !errors.Is(err, jwt.ErrSignatureInvalid) {
			break
		}
	}
	if parsed == nil {
		if errors.Is(lastErr, jwt.ErrTokenExpired) {
			return nil, errors.New("identity token expired")
		}
		return nil, errors.New("identity token rejected")
	}

	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("identity token rejected: unexpected claims type %T", parsed.Claims)
	}

	claims := &Claims{}
	fields := map[string]*string{"user_id": &claims.UserID, "tenant_id": &claims.TenantID, "agent_id": &claims.AgentID, "email": &claims.Email, "enterprise_email": &claims.EnterpriseEmail, "employee_no": &claims.EmployeeNo}
	for key, dest := range fields {
		value, present := mc[key]
		if !present {
			continue
		}
		s, err := identityScalar(value, key != "email" && key != "enterprise_email")
		if err != nil {
			return nil, err
		}
		*dest = s
	}
	if value, present := mc["department_ids"]; present {
		items, ok := value.([]any)
		if !ok {
			return nil, errors.New("invalid identity field")
		}
		for _, item := range items {
			s, err := identityScalar(item, true)
			if err != nil {
				return nil, err
			}
			claims.DepartmentIDs = append(claims.DepartmentIDs, s)
		}
	}
	if claims.UserID == "" && claims.Email == "" {
		return nil, errors.New("identity token missing required identity")
	}
	// Do not let numeric date parsing round an overflowing/fractional expiry.
	exp, ok := mc["exp"].(json.Number)
	if !ok || !integerLiteral.MatchString(string(exp)) {
		return nil, errors.New("invalid identity expiry")
	}
	seconds, err := strconv.ParseInt(string(exp), 10, 64)
	if err != nil || seconds <= time.Now().Unix() || seconds > 253402300799 {
		return nil, errors.New("invalid identity expiry")
	}
	claims.ExpiresAt = time.Unix(seconds, 0)
	return claims, nil
}

var integerLiteral = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func identityScalar(value any, numeric bool) (string, error) {
	switch v := value.(type) {
	case string:
		return config.NormalizeIdentity(v)
	case json.Number:
		if numeric && integerLiteral.MatchString(string(v)) {
			return string(v), nil
		}
	}
	return "", errors.New("invalid identity field")
}

// Reject duplicate decoded keys, invalid UTF-8 and unpaired Unicode escapes
// before jwt's map decoder can silently replace or overwrite them.
func strictJWTJSON(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return false
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		tok, err := d.Token()
		if err != nil {
			return false
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return true
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return false
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return false
				}
				seen[s] = true
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim('}')
		case '[':
			for d.More() {
				if !value(depth + 1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		default:
			return false
		}
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

type claimsCtxKey struct{}

// WithClaims carries claim annotations only; it grants no authentication
// authority. Use Authenticate and PrincipalFromContext for trusted identity.
func WithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey{}, c)
}

// ClaimsFromContext returns annotations stored by WithClaims, not proof of
// authentication. Resolve never consumes them.
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
