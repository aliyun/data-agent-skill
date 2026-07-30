package dataagent

import "testing"

// The client talks to four different hosts; each must fall back to the
// region-derived default and honour an explicit override.

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
	c := NewClient(&Credential{}, "cn-shanghai")
	if got, want := c.APIKeyEndpoint(), "dataagent-cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("default api key endpoint = %q, want %q", got, want)
	}
	if got, want := c.APIKeyStreamEndpoint(), "dataagent-stream-cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("default api key stream endpoint = %q, want %q", got, want)
	}

	const (
		ctrl   = "dataagent-internal.example.com"
		stream = "dataagent-stream-internal.example.com"
	)
	c = NewClient(&Credential{}, "cn-shanghai",
		WithAPIKeyEndpoint(ctrl), WithAPIKeyStreamEndpoint(stream))
	if got := c.APIKeyEndpoint(); got != ctrl {
		t.Errorf("overridden api key endpoint = %q, want %q", got, ctrl)
	}
	if got := c.APIKeyStreamEndpoint(); got != stream {
		t.Errorf("overridden api key stream endpoint = %q, want %q", got, stream)
	}
}

// The SSE client builds its own requests, so it must inherit the endpoint
// overrides instead of deriving hosts from the region on its own.
func TestSSEClientInheritsEndpointOverrides(t *testing.T) {
	const (
		signed = "dms-vpc.cn-shanghai.aliyuncs.com"
		stream = "dataagent-stream-internal.example.com"
	)
	c := NewClient(&Credential{}, "cn-shanghai",
		WithDataAgentEndpoint(signed), WithAPIKeyStreamEndpoint(stream))

	if got := c.sse.endpoint; got != signed {
		t.Errorf("sse signed endpoint = %q, want %q", got, signed)
	}
	if got := c.sse.streamEndpoint; got != stream {
		t.Errorf("sse stream endpoint = %q, want %q", got, stream)
	}

	// Without overrides the SSE client keeps the region defaults.
	c = NewClient(&Credential{}, "cn-shanghai")
	if got, want := c.sse.endpoint, "dms.cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("sse default signed endpoint = %q, want %q", got, want)
	}
	if got, want := c.sse.streamEndpoint, "dataagent-stream-cn-shanghai.aliyuncs.com"; got != want {
		t.Errorf("sse default stream endpoint = %q, want %q", got, want)
	}
}
