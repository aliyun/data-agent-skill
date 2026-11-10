package mcp

import (
	"context"
	"encoding/json"
	"reflect"
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

func TestCreateSessionWorkMode(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    any
		supplied bool
		want     string
		invalid  bool
	}{
		{"omitted", nil, false, "", false},
		{"empty", "", true, "", false},
		{"blank", "  ", true, "", false},
		{"chat", "CHAT", true, "CHAT", false},
		{"analysis", "ANALYSIS", true, "ANALYSIS", false},
		{"normalized", " chat ", true, "CHAT", false},
		{"invalid enum", "synthetic-private-value", true, "", true},
		{"tier is not work mode", "pro", true, "", true},
		{"number", 1, true, "", true},
		{"boolean", false, true, "", true},
		{"null", nil, true, "", true},
		{"array", []any{"CHAT"}, true, "", true},
		{"object", map[string]any{"value": "CHAT"}, true, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"query": "synthetic query", "custom_agent_id": "custom-agent", "mode": "lite", "plan_mode": "force"}
			if tc.supplied {
				args["work_mode"] = tc.value
			}
			errText, opts, called := callCreate(t, newCreateServer("", ""), args)
			if tc.invalid {
				if called || !strings.Contains(errText, "work_mode") || strings.Contains(errText, "synthetic-private-value") {
					t.Fatalf("invalid work_mode must be rejected without dispatch or value disclosure: %q", errText)
				}
				return
			}
			if errText != "" || !called || opts.WorkMode != tc.want {
				t.Fatalf("work_mode=%q want=%q called=%t error=%q", opts.WorkMode, tc.want, called, errText)
			}
			if opts.Mode != "lite" || opts.PlanMode != "force" || !opts.AutoConfirm {
				t.Fatal("work_mode changed the tier, plan mode or auto confirmation")
			}
		})
	}
	for _, source := range []map[string]any{
		{"database_id": "db", "db_name": "synthetic_db", "tables": "synthetic_table"},
		{"file_id": "file", "file_name": "synthetic.csv"},
	} {
		source["query"], source["work_mode"], source["auto_confirm"] = "synthetic query", "CHAT", false
		errText, opts, called := callCreate(t, newCreateServer("", ""), source)
		if errText != "" || !called || opts.WorkMode != "CHAT" || opts.AutoConfirm {
			t.Fatalf("work_mode not preserved for data source: %q", errText)
		}
	}
}

func TestCreateSessionWorkModeSchema(t *testing.T) {
	data, err := json.Marshal(createSessionTool)
	if err != nil {
		t.Fatal(err)
	}
	var tool struct {
		InputSchema struct {
			Properties map[string]struct {
				Type        string   `json:"type"`
				Enum        []string `json:"enum"`
				Description string   `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(data, &tool); err != nil {
		t.Fatal(err)
	}
	property, ok := tool.InputSchema.Properties["work_mode"]
	if !ok || property.Type != "string" || !reflect.DeepEqual(property.Enum, []string{"ANALYSIS", "CHAT"}) {
		t.Fatal("work_mode enum is not exposed in the creation schema")
	}
	for _, required := range tool.InputSchema.Required {
		if required == "work_mode" {
			t.Fatal("work_mode must remain optional")
		}
	}
	for _, instruction := range []string{"CHAT", "more interaction", "first", "SessionConfig.WorkMode", "auto_confirm"} {
		if !strings.Contains(property.Description, instruction) {
			t.Fatalf("work_mode description missing %q", instruction)
		}
	}
	if _, ok := sendTool.InputSchema.Properties["work_mode"]; ok {
		t.Fatal("follow-up tool must not advertise a WorkMode switch ignored by the upstream")
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
