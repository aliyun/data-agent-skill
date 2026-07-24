package dataagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	credential "github.com/aliyun/credentials-go/credentials"
)

// aliyunConfigFile represents the structure of ~/.aliyun/config.json.
type aliyunConfigFile struct {
	Current  string              `json:"current"`
	Profiles []aliyunProfileItem `json:"profiles"`
}

// aliyunProfileItem represents a single profile entry in the config file.
type aliyunProfileItem struct {
	Name               string `json:"name"`
	Mode               string `json:"mode"`
	AccessKeyID        string `json:"access_key_id"`
	AccessKeySecret    string `json:"access_key_secret"`
	STSToken           string `json:"sts_token"`
	SecurityToken      string `json:"security_token"`
	STSTokenCamel      string `json:"stsToken"`
	SecurityTokenCamel string `json:"securityToken"`
	RegionID           string `json:"region_id"`
}

// LoadCredential loads Alibaba Cloud credentials using a three-step fallback:
//  1. Environment variables: ALIBABA_CLOUD_ACCESS_KEY_ID, ALIBABA_CLOUD_ACCESS_KEY_SECRET,
//     ALIBABA_CLOUD_SECURITY_TOKEN
//  2. Aliyun config file: ~/.aliyun/config.json (the "current" profile)
//  3. credentials-go default credential chain
func LoadCredential() (*Credential, error) {
	// Step 1: environment variables
	if cred, ok := loadFromEnv(); ok {
		return cred, nil
	}

	// Step 2: ~/.aliyun/config.json
	if cred, ok := loadFromAliyunConfig(); ok {
		return cred, nil
	}

	// Step 3: credentials-go default chain
	return loadFromDefaultChain()
}

// loadFromEnv tries to read AK/SK/STS token from environment variables.
func loadFromEnv() (*Credential, bool) {
	akID := os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_ID")
	akSecret := os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	if akID == "" || akSecret == "" {
		return nil, false
	}
	return &Credential{
		AccessKeyID:     akID,
		AccessKeySecret: akSecret,
		SecurityToken:   os.Getenv("ALIBABA_CLOUD_SECURITY_TOKEN"),
	}, true
}

// loadFromAliyunConfig reads the "current" profile from ~/.aliyun/config.json.
func loadFromAliyunConfig() (*Credential, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false
	}

	configPath := filepath.Join(home, ".aliyun", "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, false
	}

	var cfg aliyunConfigFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, false
	}

	profileName := cfg.Current
	if profileName == "" {
		profileName = "default"
	}

	for _, p := range cfg.Profiles {
		if p.Name == profileName {
			if p.AccessKeyID == "" || p.AccessKeySecret == "" {
				return nil, false
			}
			return &Credential{
				AccessKeyID:     p.AccessKeyID,
				AccessKeySecret: p.AccessKeySecret,
				SecurityToken:   p.securityToken(),
			}, true
		}
	}

	return nil, false
}

func (p aliyunProfileItem) securityToken() string {
	for _, token := range []string{
		p.STSToken,
		p.SecurityToken,
		p.STSTokenCamel,
		p.SecurityTokenCamel,
	} {
		if token != "" {
			return token
		}
	}
	return ""
}

// loadFromDefaultChain uses github.com/aliyun/credentials-go default provider chain.
func loadFromDefaultChain() (*Credential, error) {
	provider, err := credential.NewCredential(nil)
	if err != nil {
		return nil, fmt.Errorf("credentials-go default chain: %w", err)
	}

	akID, err := provider.GetAccessKeyId()
	if err != nil {
		return nil, fmt.Errorf("credentials-go GetAccessKeyId: %w", err)
	}
	akSecret, err := provider.GetAccessKeySecret()
	if err != nil {
		return nil, fmt.Errorf("credentials-go GetAccessKeySecret: %w", err)
	}

	cred := &Credential{
		AccessKeyID:     derefStr(akID),
		AccessKeySecret: derefStr(akSecret),
	}

	stsToken, err := provider.GetSecurityToken()
	if err == nil && stsToken != nil {
		cred.SecurityToken = *stsToken
	}

	if cred.AccessKeyID == "" || cred.AccessKeySecret == "" {
		return nil, fmt.Errorf("credentials-go returned empty access key")
	}

	return cred, nil
}

// derefStr safely dereferences a *string, returning "" for nil.
func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
