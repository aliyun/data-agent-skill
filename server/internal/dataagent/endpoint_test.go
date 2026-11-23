package dataagent

import "testing"

// The signed API hosts must fall back to region-derived defaults and honour
// explicit overrides.

func TestDataAgentEndpoint(t *testing.T) {
	c := NewClient(&Credential{}, "cn-shanghai")
	if got, want := c.DataAgentEndpoint(), "dms.cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("default endpoint = %q, want %q", got, want)
	}

	const vpc = "dms-vpc.cn-shanghai.aliyuncs.com"
	c = NewClient(&Credential{}, "cn-shanghai", WithDataAgentEndpoint(vpc))
	if got := c.DataAgentEndpoint(); got != vpc {
		t.Errorf("overridden endpoint = %q, want %q", got, vpc)
	}

	// An empty override must not wipe the region-derived default.
	c = NewClient(&Credential{}, "cn-shanghai", WithDataAgentEndpoint(""))
	if got, want := c.DataAgentEndpoint(), "dms.cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("empty override endpoint = %q, want %q", got, want)
	}
}

func TestAPIKeyEndpoints(t *testing.T) {
	for _, region := range []string{"cn-shanghai", "ap-southeast-1"} {
		for _, opts := range [][]ClientOption{nil, {WithAPIKeyEndpoint(""), WithAPIKeyStreamEndpoint("")}} {
			c := NewClient(&Credential{APIKey: "synthetic-api-key"}, region, opts...)
			if c.APIKeyEndpoint() != "dataagent-"+region+".aliyuncs.com" ||
				c.APIKeyStreamEndpoint() != "dataagent-stream-"+region+".aliyuncs.com" || c.sse.streamEndpoint != c.APIKeyStreamEndpoint() {
				t.Fatal("API Key default endpoints or SSE propagation changed")
			}
		}
	}
	c := NewClient(&Credential{}, "cn-shanghai", WithDataAgentEndpoint("signed.example.invalid"),
		WithAPIKeyEndpoint("control.example.invalid"), WithAPIKeyStreamEndpoint("stream.example.invalid"))
	if c.APIKeyEndpoint() != "control.example.invalid" || c.APIKeyStreamEndpoint() != "stream.example.invalid" ||
		c.sse.streamEndpoint != "stream.example.invalid" || c.sse.endpoint != "signed.example.invalid" {
		t.Fatal("independent endpoint overrides were not propagated")
	}
	sse := NewSSEClient(&Credential{}, "cn-shanghai")
	if sse.streamEndpoint != "dataagent-stream-cn-shanghai.aliyuncs.com" || sse.region != "cn-shanghai" {
		t.Fatal("standalone SSE defaults changed")
	}
}

func TestCredentialIsAPIKey(t *testing.T) {
	for _, tc := range []struct {
		cred *Credential
		want bool
	}{
		{nil, false}, {&Credential{}, false}, {&Credential{AccessKeyID: "ak", AccessKeySecret: "sk"}, false},
		{&Credential{APIKey: "key"}, true}, {&Credential{APIKey: "key", AccessKeyID: "ak", AccessKeySecret: "sk"}, true},
	} {
		if tc.cred.IsAPIKey() != tc.want {
			t.Fatal("incorrect authentication selection")
		}
	}
}

// The SSE client builds its own requests, so it must inherit the endpoint
// override instead of deriving its host from the region on its own.
func TestSSEClientInheritsEndpointOverrides(t *testing.T) {
	const signed = "dms-vpc.cn-shanghai.aliyuncs.com"
	c := NewClient(&Credential{}, "cn-shanghai", WithDataAgentEndpoint(signed))
	if got := c.sse.endpoint; got != signed {
		t.Errorf("sse signed endpoint = %q, want %q", got, signed)
	}

	// Without overrides the SSE client keeps the region default.
	c = NewClient(&Credential{}, "cn-shanghai")
	if got, want := c.sse.endpoint, "dms.cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("sse default signed endpoint = %q, want %q", got, want)
	}
}
