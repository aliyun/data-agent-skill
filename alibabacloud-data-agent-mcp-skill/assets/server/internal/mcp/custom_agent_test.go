package mcp

import "testing"

// custom_agent_id resolves the same way workspace_id does: the explicit tool
// argument first, then the caller's identity group, then the server default.
func TestResolveCustomAgentID(t *testing.T) {
	for _, tc := range []struct {
		name     string
		arg      string
		group    string
		server   string
		expected string
	}{
		{"nothing configured", "", "", "", ""},
		{"server default only", "", "", "srv-agent", "srv-agent"},
		{"group beats server", "", "group-agent", "srv-agent", "group-agent"},
		{"argument beats group", "arg-agent", "group-agent", "srv-agent", "arg-agent"},
		{"argument beats server", "arg-agent", "", "srv-agent", "arg-agent"},
		{"group only", "", "group-agent", "", "group-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				defaults:      SessionDefaults{CustomAgentID: tc.group},
				customAgentID: tc.server,
			}
			if got := s.resolveCustomAgentID(tc.arg); got != tc.expected {
				t.Errorf("resolveCustomAgentID(%q) with group=%q server=%q = %q, want %q",
					tc.arg, tc.group, tc.server, got, tc.expected)
			}
		})
	}
}

// The server default must not leak across tenants: withTenant copies the
// Server per request and swaps in the group defaults, so a group that names no
// agent still falls back to the server default rather than to another group's.
func TestResolveCustomAgentIDPerTenant(t *testing.T) {
	base := &Server{customAgentID: "srv-agent"}

	scopedA := *base
	scopedA.defaults = SessionDefaults{CustomAgentID: "team-a-agent"}
	if got := scopedA.resolveCustomAgentID(""); got != "team-a-agent" {
		t.Errorf("tenant A = %q, want team-a-agent", got)
	}

	scopedB := *base
	scopedB.defaults = SessionDefaults{} // group names no agent
	if got := scopedB.resolveCustomAgentID(""); got != "srv-agent" {
		t.Errorf("tenant B = %q, want the server default srv-agent", got)
	}

	// The base server is untouched by either scoped copy.
	if got := base.resolveCustomAgentID(""); got != "srv-agent" {
		t.Errorf("base server = %q, want srv-agent", got)
	}
}
