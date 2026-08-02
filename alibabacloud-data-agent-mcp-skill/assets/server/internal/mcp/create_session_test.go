package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
)

// captureManager records the CreateOpts handleCreateSession builds, so the
// tests can assert on validation without reaching the Data Agent API.
type captureManager struct {
	sessionManager // embedded: the unused methods are never called here
	got            session.CreateOpts
	called         bool
}

func (m *captureManager) CreateSession(_ context.Context, opts session.CreateOpts) (*session.State, error) {
	m.got = opts
	m.called = true
	return &session.State{SessionID: "sess-1", Status: session.StatusRunning, Mode: opts.Mode}, nil
}

func createReq(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = "data_agent_create_session"
	req.Params.Arguments = args
	return req
}

// callCreate runs the handler and reports the error text a caller would see.
func callCreate(t *testing.T, s *Server, args map[string]any) (string, session.CreateOpts, bool) {
	t.Helper()
	res, err := s.handleCreateSession(context.Background(), createReq(args))
	if err != nil {
		t.Fatalf("handler returned a transport error: %v", err)
	}
	mgr := s.mgr.(*captureManager)
	if res != nil && res.IsError {
		return resultText(res), mgr.got, mgr.called
	}
	return "", mgr.got, mgr.called
}

func newCreateServer(groupAgent, serverAgent string) *Server {
	return &Server{
		mgr:           &captureManager{},
		defaults:      SessionDefaults{CustomAgentID: groupAgent},
		customAgentID: serverAgent,
	}
}

// The data source is only mandatory when no custom agent is in play: a custom
// agent already carries one, and the Data Agent API accepts a session without
// any data source.
func TestCreateSessionDataSourceRequiredOnlyWithoutCustomAgent(t *testing.T) {
	t.Run("no agent, no data source is rejected", func(t *testing.T) {
		s := newCreateServer("", "")
		errText, _, called := callCreate(t, s, map[string]any{"query": "q"})
		if errText == "" {
			t.Fatal("expected a rejection")
		}
		if !strings.Contains(errText, "database_id or file_id") {
			t.Errorf("unexpected error: %q", errText)
		}
		if called {
			t.Error("session was created despite the rejection")
		}
	})

	t.Run("agent argument alone is enough", func(t *testing.T) {
		s := newCreateServer("", "")
		errText, opts, called := callCreate(t, s, map[string]any{
			"query": "q", "custom_agent_id": "ca-arg",
		})
		if errText != "" {
			t.Fatalf("unexpected rejection: %s", errText)
		}
		if !called {
			t.Fatal("session was not created")
		}
		if opts.CustomAgentID != "ca-arg" {
			t.Errorf("CustomAgentID = %q, want ca-arg", opts.CustomAgentID)
		}
		if opts.DatabaseID != "" || opts.FileID != "" {
			t.Errorf("no data source expected, got db=%q file=%q", opts.DatabaseID, opts.FileID)
		}
	})

	// A default configured on the identity group or the server must relax the
	// requirement too, otherwise those deployments could never omit the data
	// source even though their agent supplies it.
	t.Run("identity group default is enough", func(t *testing.T) {
		s := newCreateServer("ca-group", "")
		errText, opts, _ := callCreate(t, s, map[string]any{"query": "q"})
		if errText != "" {
			t.Fatalf("unexpected rejection: %s", errText)
		}
		if opts.CustomAgentID != "ca-group" {
			t.Errorf("CustomAgentID = %q, want ca-group", opts.CustomAgentID)
		}
	})

	t.Run("server default is enough", func(t *testing.T) {
		s := newCreateServer("", "ca-server")
		errText, opts, _ := callCreate(t, s, map[string]any{"query": "q"})
		if errText != "" {
			t.Fatalf("unexpected rejection: %s", errText)
		}
		if opts.CustomAgentID != "ca-server" {
			t.Errorf("CustomAgentID = %q, want ca-server", opts.CustomAgentID)
		}
	})
}

// With a custom agent the per-field database checks are skipped as well, so a
// partially specified database no longer blocks the call.
func TestCreateSessionSkipsDatabaseFieldChecksWithCustomAgent(t *testing.T) {
	s := newCreateServer("", "")
	errText, _, _ := callCreate(t, s, map[string]any{
		"query": "q", "database_id": "123", // db_name and tables missing
	})
	if !strings.Contains(errText, "db_name is required") {
		t.Errorf("without an agent db_name must be required, got %q", errText)
	}

	s = newCreateServer("", "")
	errText, opts, _ := callCreate(t, s, map[string]any{
		"query": "q", "database_id": "123", "custom_agent_id": "ca-arg",
	})
	if errText != "" {
		t.Fatalf("with an agent the partial database should be accepted, got %q", errText)
	}
	if opts.DatabaseID != "123" {
		t.Errorf("DatabaseID = %q, want 123", opts.DatabaseID)
	}
}

// Malformed calls stay rejected regardless of the agent: these checks catch a
// contradictory request, not a missing data source.
func TestCreateSessionKeepsMalformedCallChecks(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{
			"query is always required",
			map[string]any{"custom_agent_id": "ca-arg"},
			"query is required",
		},
		{
			"database_id and file_id are exclusive even with an agent",
			map[string]any{"query": "q", "database_id": "1", "file_id": "f-1", "custom_agent_id": "ca-arg"},
			"mutually exclusive",
		},
		{
			"file_id still needs file_name even with an agent",
			map[string]any{"query": "q", "file_id": "f-1", "custom_agent_id": "ca-arg"},
			"file_name is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newCreateServer("", "")
			errText, _, called := callCreate(t, s, tc.args)
			if !strings.Contains(errText, tc.want) {
				t.Errorf("error = %q, want it to mention %q", errText, tc.want)
			}
			if called {
				t.Error("session was created despite the rejection")
			}
		})
	}
}
