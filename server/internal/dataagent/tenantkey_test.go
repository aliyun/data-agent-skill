package dataagent

import "testing"

func TestTenantKeyAPIKey(t *testing.T) {
	a := (&Credential{APIKey: "key-a"}).TenantKey()
	b := (&Credential{APIKey: "key-b"}).TenantKey()
	if a == "" || b == "" {
		t.Fatal("API keys must produce a tenant key")
	}
	if a == b {
		t.Error("different API keys must produce different tenant keys")
	}
	if a != (&Credential{APIKey: "key-a"}).TenantKey() {
		t.Error("tenant key must be stable for the same API key")
	}
	if len(a) != 16 {
		t.Errorf("tenant key length = %d, want 16", len(a))
	}
}

func TestTenantKeyLongTermAK(t *testing.T) {
	k := (&Credential{AccessKeyID: "LTAI-test", AccessKeySecret: "s"}).TenantKey()
	if k == "" {
		t.Error("long-term AK must produce a tenant key")
	}
}

func TestTenantKeySTSAndNilProduceNoKey(t *testing.T) {
	sts := &Credential{AccessKeyID: "STS.x", AccessKeySecret: "s", SecurityToken: "tok"}
	if k := sts.TenantKey(); k != "" {
		t.Errorf("STS credential tenant key = %q, want empty", k)
	}
	var nilCred *Credential
	if k := nilCred.TenantKey(); k != "" {
		t.Errorf("nil credential tenant key = %q, want empty", k)
	}
}

func TestTenantKeyAPIKeyWinsOverAK(t *testing.T) {
	both := &Credential{APIKey: "key-a", AccessKeyID: "LTAI-test"}
	if both.TenantKey() != (&Credential{APIKey: "key-a"}).TenantKey() {
		t.Error("API key identity must win when both are present")
	}
}
