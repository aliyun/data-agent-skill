package tenant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
)

// Principal is a verified identity snapshot. Accessors return copies so callers
// cannot mutate the private authority used by Resolve or other accessors.
type Principal struct {
	ID           string
	ExpiresAt    time.Time
	Namespace    string
	AuthMode     string
	TenantID     string
	AgentID      string
	SubjectKind  string
	SubjectValue string
	UserID       string
	Email        string
}

type verifiedKey struct{}
type verifiedIdentity struct {
	registry        *Registry
	principal       Principal
	group           string
	mapped          config.IdentityGroup
	roleSessionName string
	sessionValue    string
}

func PrincipalFromContext(ctx context.Context) *Principal {
	verified, _ := ctx.Value(verifiedKey{}).(*verifiedIdentity)
	if verified == nil {
		return nil
	}
	copy := verified.principal
	return &copy
}

func makePrincipal(namespace, mode string, c *Claims) (*Principal, error) {
	if c == nil {
		return nil, errAuthentication
	}
	copy := *c
	c = &copy
	for _, field := range []*string{&c.UserID, &c.Email, &c.TenantID, &c.AgentID} {
		value, err := config.NormalizeIdentity(*field)
		if err != nil {
			return nil, errAuthentication
		}
		*field = value
	}
	if !config.ValidNamespace(namespace) || (mode != "jwt" && mode != "headers") {
		return nil, errAuthentication
	}
	if mode == "jwt" && (c.TenantID == "" || c.AgentID == "") || mode == "headers" && (c.TenantID != "" || c.AgentID != "") {
		return nil, errAuthentication
	}
	kind, value := "user_id", c.UserID
	if value == "" {
		kind, value = "email", c.Email
	}
	if value == "" {
		return nil, errAuthentication
	}
	// Fixed array field order is the versioned canonical wire encoding.
	canonical, _ := json.Marshal([7]string{"principal-v2", namespace, mode, c.TenantID, c.AgentID, kind, value})
	digest := sha256.Sum256(canonical)
	return &Principal{ID: hex.EncodeToString(digest[:]), ExpiresAt: c.ExpiresAt, Namespace: namespace, AuthMode: mode, TenantID: c.TenantID, AgentID: c.AgentID, SubjectKind: kind, SubjectValue: value, UserID: c.UserID, Email: c.Email}, nil
}

type authenticationError struct{}

func (*authenticationError) Error() string { return "identity authentication rejected" }

var errAuthentication error = &authenticationError{}

// IsAuthenticationError distinguishes 401 from authenticated-but-forbidden 403.
func IsAuthenticationError(err error) bool {
	var rejected *authenticationError
	return errors.As(err, &rejected)
}

var errMappingChanged = errors.New("identity authorization context changed")
var errBusy = errors.New("tenant initialization busy; retry later")
var errInitTimeout = errors.New("tenant initialization timed out; retry later")
var errClosed = errors.New("tenant registry closed")
