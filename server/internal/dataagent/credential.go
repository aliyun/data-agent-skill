package dataagent

import (
	"errors"
	"os"
	"strings"
	"sync"

	credential "github.com/aliyun/credentials-go/credentials"
)

var errCredentialUnavailable = errors.New("Alibaba Cloud credential provider unavailable")

// The SDK captures DEBUG at startup and can print secret-bearing headers to stdout.
var credentialDebugAtStartup = credentialDebugEnabled()

func credentialDebugEnabled() bool {
	for _, flag := range strings.Split(os.Getenv("DEBUG"), ",") {
		if flag == "credential" {
			return true
		}
	}
	return false
}

// Retain the SDK chain for renewal; its HTTP budgets are separate from the gateway budget.
func NewCredentialProvider() (credential.Credential, error) {
	if credentialDebugAtStartup || credentialDebugEnabled() {
		return nil, errors.New("credential SDK debug logging must be disabled")
	}
	provider, err := credential.NewCredential(nil)
	if err != nil || provider == nil {
		return nil, errCredentialUnavailable
	}
	return &lockedCredentialProvider{Credential: provider}, nil
}

// Runtime callers must retain NewCredentialProvider instead of this snapshot.
func LoadCredential() (*Credential, error) {
	provider, err := NewCredentialProvider()
	if err != nil {
		return nil, err
	}
	return CredentialFromProvider(provider)
}

// SDK errors can contain profile secrets or signed URLs, so expose only generic failures.
func CredentialFromProvider(provider credential.Credential) (*Credential, error) {
	model, err := credentialModel(provider)
	if err != nil {
		return nil, err
	}
	return &Credential{
		AccessKeyID:     derefStr(model.AccessKeyId),
		AccessKeySecret: derefStr(model.AccessKeySecret),
		SecurityToken:   derefStr(model.SecurityToken),
	}, nil
}

func credentialModel(provider credential.Credential) (*credential.CredentialModel, error) {
	if provider == nil || credentialDebugAtStartup || credentialDebugEnabled() {
		return nil, errCredentialUnavailable
	}
	model, err := provider.GetCredential()
	if err != nil || model == nil || strings.TrimSpace(derefStr(model.AccessKeyId)) == "" ||
		strings.TrimSpace(derefStr(model.AccessKeySecret)) == "" {
		return nil, errCredentialUnavailable
	}
	return model, nil
}

// The SDK mutates its provider cache, so serialize shared reads across tenants.
type lockedCredentialProvider struct {
	credential.Credential
	mu sync.Mutex
}

func (p *lockedCredentialProvider) GetCredential() (*credential.CredentialModel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return credentialModel(p.Credential)
}

func (p *lockedCredentialProvider) GetAccessKeyId() (*string, error) {
	m, err := p.GetCredential()
	if err != nil {
		return nil, err
	}
	return m.AccessKeyId, nil
}
func (p *lockedCredentialProvider) GetAccessKeySecret() (*string, error) {
	m, err := p.GetCredential()
	if err != nil {
		return nil, err
	}
	return m.AccessKeySecret, nil
}
func (p *lockedCredentialProvider) GetSecurityToken() (*string, error) {
	m, err := p.GetCredential()
	if err != nil {
		return nil, err
	}
	return m.SecurityToken, nil
}

// derefStr safely dereferences a *string, returning "" for nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
