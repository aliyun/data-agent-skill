package dataagent

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	credential "github.com/aliyun/credentials-go/credentials"
)

func isolateCredentialChain(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, key := range []string{
		"DEBUG", "ALIBABA_CLOUD_ACCESS_KEY_ID", "ALIBABA_CLOUD_ACCESS_KEY_SECRET", "ALIBABA_CLOUD_SECURITY_TOKEN",
		"ALIBABA_CLOUD_ROLE_ARN", "ALIBABA_CLOUD_OIDC_PROVIDER_ARN", "ALIBABA_CLOUD_OIDC_TOKEN_FILE",
		"ALIBABA_CLOUD_PROFILE", "ALIBABA_CLOUD_ECS_METADATA", "ALIBABA_CLOUD_CREDENTIALS_URI",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ALIBABA_CLOUD_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "missing"))
	t.Setenv("ALIBABA_CLOUD_CLI_PROFILE_DISABLED", "true")
	t.Setenv("ALIBABA_CLOUD_ECS_METADATA_DISABLED", "true")
}

func TestLoadCredentialUsesSDKEnvironmentProvider(t *testing.T) {
	isolateCredentialChain(t)
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "test-ak")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "test-sk")
	t.Setenv("ALIBABA_CLOUD_SECURITY_TOKEN", "test-sts")
	c, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "test-ak" || c.AccessKeySecret != "test-sk" || c.SecurityToken != "test-sts" {
		t.Fatal("SDK credential tuple not preserved")
	}
}

func TestLoadCredentialUsesSDKCLIProfile(t *testing.T) {
	isolateCredentialChain(t)
	t.Setenv("ALIBABA_CLOUD_CLI_PROFILE_DISABLED", "false")
	configDir := filepath.Join(os.Getenv("HOME"), ".aliyun")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"current":"test","profiles":[{"name":"test","mode":"AK","access_key_id":"profile-ak","access_key_secret":"profile-sk"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessKeyID != "profile-ak" || c.AccessKeySecret != "profile-sk" {
		t.Fatal("SDK profile not selected")
	}
}

func TestCredentialChainUnavailableFailsWithoutSecrets(t *testing.T) {
	isolateCredentialChain(t)
	t.Setenv("ALIBABA_CLOUD_CLI_PROFILE_DISABLED", "false")
	configDir := filepath.Join(os.Getenv("HOME"), ".aliyun")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"secret":"profile-secret",malformed`), 0600); err != nil {
		t.Fatal(err)
	}
	provider, err := NewCredentialProvider()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		c, err := CredentialFromProvider(provider)
		if c != nil || err == nil || strings.Contains(err.Error(), "profile-secret") {
			t.Fatalf("got %v", err)
		}
	}
}

type changingCredentialProvider struct {
	credential.Credential // deprecated per-field access must never be used
	mu                    sync.Mutex
	model                 *credential.CredentialModel
	err                   error
	calls                 int
}

func (p *changingCredentialProvider) GetCredential() (*credential.CredentialModel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.model, p.err
}

func credentialTestModel(ak, sk, token string) *credential.CredentialModel {
	return &credential.CredentialModel{AccessKeyId: &ak, AccessKeySecret: &sk, SecurityToken: &token}
}

func TestRetainedCredentialProviderRotatesAndFailsClosed(t *testing.T) {
	isolateCredentialChain(t)
	fake := &changingCredentialProvider{model: credentialTestModel("first-ak", "first-sk", "first-token")}
	provider := &lockedCredentialProvider{Credential: fake}
	first, err := CredentialFromProvider(provider)
	if err != nil || first.AccessKeyID != "first-ak" {
		t.Fatalf("first fetch: %v", err)
	}
	fake.model = credentialTestModel("second-ak", "second-sk", "second-token")
	second, err := CredentialFromProvider(provider)
	if err != nil || second.AccessKeyID != "second-ak" || second.SecurityToken != "second-token" {
		t.Fatalf("rotation: %v", err)
	}
	fake.err = errors.New("https://sts/?AccessKeyId=secret-ak&Signature=secret-signature raw-profile-secret")
	failed, err := CredentialFromProvider(provider)
	if failed != nil || err != errCredentialUnavailable {
		t.Fatalf("failure did not fail closed: %v", err)
	}
	if fake.calls != 3 {
		t.Fatalf("wanted one coherent SDK read per access, got %d", fake.calls)
	}
}

func TestCredentialProviderRejectsInvalidResults(t *testing.T) {
	isolateCredentialChain(t)
	for _, tc := range []struct {
		name  string
		model *credential.CredentialModel
	}{
		{"nil", nil},
		{"empty key", credentialTestModel("", "sk", "token")},
		{"empty secret", credentialTestModel("ak", "", "token")},
		{"whitespace key", credentialTestModel(" ", "sk", "token")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &changingCredentialProvider{model: tc.model}
			c, err := CredentialFromProvider(provider)
			if c != nil || err != errCredentialUnavailable {
				t.Fatalf("got %v", err)
			}
		})
	}
	if c, err := CredentialFromProvider(nil); c != nil || err == nil {
		t.Fatal("nil provider accepted")
	}
}

func TestCredentialSDKDebugModeRejected(t *testing.T) {
	isolateCredentialChain(t)
	t.Setenv("DEBUG", "other,credential")
	if _, err := NewCredentialProvider(); err == nil {
		t.Fatal("unsafe SDK debug accepted")
	}
	fake := &changingCredentialProvider{model: credentialTestModel("ak", "sk", "token")}
	if _, err := CredentialFromProvider(fake); err == nil || fake.calls != 0 {
		t.Fatal("SDK called with unsafe debug enabled")
	}
}

func TestSDKDefaultChainURIRefreshFailsClosed(t *testing.T) {
	isolateCredentialChain(t)
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n > 2 {
			http.Error(w, "raw-provider-secret", http.StatusInternalServerError)
			return
		}
		ak := "first-ak"
		if n == 2 {
			ak = "rotated-ak"
		}
		json.NewEncoder(w).Encode(map[string]string{
			"AccessKeyId": ak, "AccessKeySecret": "test-sk", "SecurityToken": "test-sts",
			"Expiration": time.Now().Add(time.Minute).UTC().Format("2006-01-02T15:04:05Z"),
		})
	}))
	defer endpoint.Close()
	t.Setenv("ALIBABA_CLOUD_CREDENTIALS_URI", endpoint.URL)
	provider, err := NewCredentialProvider()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first-ak", "rotated-ak"} {
		c, err := CredentialFromProvider(provider)
		if err != nil || c.AccessKeyID != want {
			t.Fatalf("SDK refresh: credential=%v error=%v", c != nil, err)
		}
	}
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "fallback-ak")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "fallback-sk")
	for i := 0; i < 2; i++ {
		if c, err := CredentialFromProvider(provider); c != nil || err != errCredentialUnavailable {
			t.Fatalf("selected SDK provider silently fell back: credential=%v error=%v", c != nil, err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("SDK cached failed credentials: calls=%d", calls.Load())
	}
}

func TestSDKDefaultChainHTTPTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("exercises the SDK's 15-second default HTTP budget")
	}
	isolateCredentialChain(t)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(25 * time.Second):
			http.Error(w, "test deadline", http.StatusGatewayTimeout)
		}
	}))
	defer endpoint.Close()
	t.Setenv("ALIBABA_CLOUD_CREDENTIALS_URI", endpoint.URL)
	provider, err := NewCredentialProvider()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c, err := CredentialFromProvider(provider)
	elapsed := time.Since(start)
	if c != nil || err != errCredentialUnavailable || elapsed < 14*time.Second || elapsed > 23*time.Second {
		t.Fatalf("SDK HTTP budget: elapsed=%s credential=%v error=%v", elapsed, c != nil, err)
	}
}

func TestCredentialProviderConcurrentReads(t *testing.T) {
	isolateCredentialChain(t)
	fake := &changingCredentialProvider{model: credentialTestModel("ak", "sk", "token")}
	provider := &lockedCredentialProvider{Credential: fake}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := CredentialFromProvider(provider); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if fake.calls != 20 {
		t.Fatalf("got %d reads", fake.calls)
	}
}
