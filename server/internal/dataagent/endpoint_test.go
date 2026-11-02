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
