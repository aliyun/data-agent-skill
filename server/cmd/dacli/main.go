// Command dacli is a manual-verification client that talks to the Data
// Agent MCP Server over Streamable HTTP the same way Feishu Aily does:
// every request carries the end-user identity headers
//
//	x-aily-user:  Feishu user_id
//	x-aily-email: Feishu email
//	x-aily-token: shared secret (must match aily.shared_secret)
//
// Usage:
//
//	go run ./cmd/dacli --url http://localhost:61026/mcp \
//	    --user ou_xxxxxxxx --token <auth-token> <command> [args]
//
// Commands:
//
//	tools                          list MCP tools
//	workspaces                     data_agent_list_workspaces
//	dbs [workspace_id]             data_agent_list_workspace_databases
//	tables [db_id] [workspace_id]  data_agent_list_imported_tables ("-" db_id = all)
//	sessions                       data_agent_list_sessions (incl. history)
//	status <session_id>            data_agent_status snapshot
//	wait <session_id> [timeout]    data_agent_wait_result loop (default 240s total)
//	result <session_id>            data_agent_result (saves images to ./dacli-out)
//	files <session_id>             data_agent_list_files
//	send <session_id> <message>    data_agent_send
//	ask <db_name> <tables> <query> end-to-end: resolve db -> create lite
//	                               session -> wait -> print conclusions
//	call <tool> [json-args]        raw tool call
//	repl                           interactive loop of the commands above
package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type client struct {
	url   string
	user  string
	email string
	token string
	sid   string // Mcp-Session-Id
	http  *http.Client
	rpcID int
}

func (c *client) post(body map[string]interface{}) (*http.Response, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", c.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.sid != "" {
		req.Header.Set("Mcp-Session-Id", c.sid)
	}
	// Aily identity headers.
	if c.user != "" {
		req.Header.Set("x-aily-user", c.user)
	}
	if c.email != "" {
		req.Header.Set("x-aily-email", c.email)
	}
	if c.token != "" {
		req.Header.Set("x-aily-token", c.token)
	}
	return c.http.Do(req)
}

// decode extracts the JSON-RPC payload, handling both plain JSON and
// text/event-stream framed responses.
func decode(resp *http.Response) (map[string]interface{}, error) {
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	raw := buf.String()
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		var last string
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				last = strings.TrimSpace(line[5:])
			}
		}
		raw = last
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil // notification acks have no body
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("bad response (HTTP %d): %.200s", resp.StatusCode, raw)
	}
	return out, nil
}

func (c *client) rpc(method string, params interface{}) (map[string]interface{}, error) {
	c.rpcID++
	body := map[string]interface{}{"jsonrpc": "2.0", "id": c.rpcID, "method": method}
	if params != nil {
		body["params"] = params
	}
	resp, err := c.post(body)
	if err != nil {
		return nil, err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.sid = sid
	}
	out, err := decode(resp)
	if err != nil {
		return nil, err
	}
	if out != nil {
		if e, ok := out["error"].(map[string]interface{}); ok {
			return nil, fmt.Errorf("rpc error: %v", e["message"])
		}
	}
	return out, nil
}

func (c *client) connect() error {
	_, err := c.rpc("initialize", map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]interface{}{"name": "dacli", "version": "1.0"},
	})
	if err != nil {
		return err
	}
	resp, err := c.post(map[string]interface{}{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// callTool invokes an MCP tool and returns (textPayload, contents, isError).
func (c *client) callTool(name string, args map[string]interface{}) (string, []interface{}, bool, error) {
	out, err := c.rpc("tools/call", map[string]interface{}{"name": name, "arguments": args})
	if err != nil {
		return "", nil, false, err
	}
	result, _ := out["result"].(map[string]interface{})
	if result == nil {
		return "", nil, false, fmt.Errorf("empty result: %v", out)
	}
	isErr, _ := result["isError"].(bool)
	contents, _ := result["content"].([]interface{})
	var text string
	for _, item := range contents {
		m, _ := item.(map[string]interface{})
		if m != nil && m["type"] == "text" {
			text, _ = m["text"].(string)
			break
		}
	}
	return text, contents, isErr, nil
}

func pretty(text string) string {
	var v interface{}
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return text
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func (c *client) simple(tool string, args map[string]interface{}) error {
	text, _, isErr, err := c.callTool(tool, args)
	if err != nil {
		return err
	}
	if isErr {
		return fmt.Errorf("tool error: %s", text)
	}
	fmt.Println(pretty(text))
	return nil
}

// numStr renders JSON numbers (decoded as float64) without scientific
// notation, so IDs like 73525904 survive the round trip.
func numStr(v interface{}) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

// ask runs the full golden path: resolve the database from the workspace Data
// Center, create a lite (quick Q&A) session, wait for the result, print conclusions.
func (c *client) ask(dbName, tables, query string) error {
	text, _, isErr, err := c.callTool("data_agent_list_workspace_databases", nil)
	if err != nil || isErr {
		return fmt.Errorf("list databases: %v %s", err, text)
	}
	var dbs []map[string]interface{}
	if err := json.Unmarshal([]byte(text), &dbs); err != nil {
		return fmt.Errorf("parse databases: %w", err)
	}
	var db map[string]interface{}
	for _, d := range dbs {
		if fmt.Sprintf("%v", d["db_name"]) == dbName {
			db = d
			break
		}
	}
	if db == nil {
		return fmt.Errorf("database %q not found in workspace Data Center", dbName)
	}
	fmt.Printf("resolved db: db_id=%s instance_id=%s instance=%v engine=%v\n",
		numStr(db["db_id"]), numStr(db["instance_id"]), db["instance_resource_id"], db["db_type"])

	text, _, isErr, err = c.callTool("data_agent_create_session", map[string]interface{}{
		"query":         query,
		"database_id":   numStr(db["db_id"]),
		"db_name":       dbName,
		"tables":        tables,
		"instance_id":   numStr(db["instance_id"]),
		"instance_name": fmt.Sprintf("%v", db["instance_resource_id"]),
		"engine":        fmt.Sprintf("%v", db["db_type"]),
		"mode":          "lite",
		"auto_confirm":  true,
	})
	if err != nil || isErr {
		return fmt.Errorf("create session: %v %s", err, text)
	}
	var created struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil || created.SessionID == "" {
		return fmt.Errorf("parse create response: %s", text)
	}
	fmt.Printf("session created: %s (waiting up to 240s...)\n", created.SessionID)

	text, err = c.waitLoop(created.SessionID, 240)
	if err != nil {
		return err
	}
	var snap struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(text), &snap)
	fmt.Printf("status=%s reason=%s\n", snap.Status, snap.Reason)
	// Fetch the full result (conclusions text + chart images).
	return c.result(created.SessionID)
}

// waitLoop calls data_agent_wait_result until a terminal reason or the total
// deadline. The server caps each block (~55s by default) and returns
// reason=timeout while the session is still running, so a longer overall wait
// is expressed as repeated calls.
func (c *client) waitLoop(sessionID string, totalSeconds int) (string, error) {
	deadline := time.Now().Add(time.Duration(totalSeconds) * time.Second)
	for {
		text, _, isErr, err := c.callTool("data_agent_wait_result",
			map[string]interface{}{"session_id": sessionID, "timeout": totalSeconds})
		if err != nil || isErr {
			return text, fmt.Errorf("wait result: %v %s", err, text)
		}
		var snap struct {
			Reason   string `json:"reason"`
			StepName string `json:"step_name"`
		}
		_ = json.Unmarshal([]byte(text), &snap)
		if snap.Reason != "timeout" && snap.Reason != "client_canceled" {
			return text, nil
		}
		if time.Now().After(deadline) {
			return text, nil
		}
		fmt.Printf("still running (step=%q), waiting again...\n", snap.StepName)
	}
}

// result prints the final result and saves any inline chart images.
func (c *client) result(sessionID string) error {
	text, contents, isErr, err := c.callTool("data_agent_result",
		map[string]interface{}{"session_id": sessionID})
	if err != nil {
		return err
	}
	if isErr {
		return fmt.Errorf("tool error: %s", text)
	}
	fmt.Println(pretty(text))
	outDir := "dacli-out"
	n := 0
	for _, item := range contents {
		m, _ := item.(map[string]interface{})
		if m == nil || m["type"] != "image" {
			continue
		}
		data, _ := m["data"].(string)
		raw, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return err
		}
		path := filepath.Join(outDir, fmt.Sprintf("%s_img_%d.png", sessionID, n))
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			return err
		}
		fmt.Printf("saved image: %s\n", path)
		n++
	}
	return nil
}

func (c *client) listTools() error {
	out, err := c.rpc("tools/list", nil)
	if err != nil {
		return err
	}
	result, _ := out["result"].(map[string]interface{})
	tools, _ := result["tools"].([]interface{})
	fmt.Printf("%d tools:\n", len(tools))
	for _, t := range tools {
		m, _ := t.(map[string]interface{})
		fmt.Printf("  - %v\n", m["name"])
	}
	return nil
}

func (c *client) dispatch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("no command; see -h")
	}
	cmd, rest := args[0], args[1:]
	need := func(n int, usage string) error {
		if len(rest) < n {
			return fmt.Errorf("usage: %s", usage)
		}
		return nil
	}
	switch cmd {
	case "tools":
		return c.listTools()
	case "workspaces":
		return c.simple("data_agent_list_workspaces", nil)
	case "dbs":
		args := map[string]interface{}{}
		if len(rest) > 0 {
			args["workspace_id"] = rest[0]
		}
		return c.simple("data_agent_list_workspace_databases", args)
	case "tables":
		// tables [db_id] [workspace_id] — omit db_id (or pass "-") to list
		// all imported tables across the workspace.
		args := map[string]interface{}{}
		if len(rest) > 0 && rest[0] != "-" {
			args["database_id"] = rest[0]
		}
		if len(rest) > 1 {
			args["workspace_id"] = rest[1]
		}
		return c.simple("data_agent_list_imported_tables", args)
	case "sessions":
		return c.simple("data_agent_list_sessions", map[string]interface{}{"include_history": true})
	case "status":
		if err := need(1, "status <session_id>"); err != nil {
			return err
		}
		return c.simple("data_agent_status", map[string]interface{}{"session_id": rest[0]})
	case "wait":
		if err := need(1, "wait <session_id> [timeout]"); err != nil {
			return err
		}
		timeout := 240
		if len(rest) > 1 {
			timeout, _ = strconv.Atoi(rest[1])
		}
		text, err := c.waitLoop(rest[0], timeout)
		if err != nil {
			return err
		}
		fmt.Println(pretty(text))
		return nil
	case "result":
		if err := need(1, "result <session_id>"); err != nil {
			return err
		}
		return c.result(rest[0])
	case "files":
		if err := need(1, "files <session_id>"); err != nil {
			return err
		}
		return c.simple("data_agent_list_files", map[string]interface{}{"session_id": rest[0]})
	case "send":
		if err := need(2, "send <session_id> <message>"); err != nil {
			return err
		}
		return c.simple("data_agent_send", map[string]interface{}{
			"session_id": rest[0], "message": strings.Join(rest[1:], " ")})
	case "ask":
		if err := need(3, "ask <db_name> <tables> <query...>"); err != nil {
			return err
		}
		return c.ask(rest[0], rest[1], strings.Join(rest[2:], " "))
	case "call":
		if err := need(1, "call <tool> [json-args]"); err != nil {
			return err
		}
		toolArgs := map[string]interface{}{}
		if len(rest) > 1 {
			if err := json.Unmarshal([]byte(strings.Join(rest[1:], " ")), &toolArgs); err != nil {
				return fmt.Errorf("parse json args: %w", err)
			}
		}
		return c.simple(rest[0], toolArgs)
	default:
		return fmt.Errorf("unknown command %q; see -h", cmd)
	}
}

func (c *client) repl() {
	fmt.Printf("dacli connected to %s as user=%q (type 'exit' to quit)\n", c.url, c.user)
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("dacli> ")
		if !scanner.Scan() {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			return
		}
		if err := c.dispatch(strings.Fields(line)); err != nil {
			fmt.Println("ERROR:", err)
		}
	}
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	url := flag.String("url", envDefault("AILY_MCP_URL", "http://localhost:61026/mcp"), "MCP server endpoint")
	user := flag.String("user", os.Getenv("AILY_USER"), "x-aily-user (Feishu user_id)")
	email := flag.String("email", os.Getenv("AILY_EMAIL"), "x-aily-email (Feishu email)")
	token := flag.String("token", os.Getenv("AILY_TOKEN"), "x-aily-token (shared secret)")
	flag.Parse()

	c := &client{
		url: *url, user: *user, email: *email, token: *token,
		http: &http.Client{Timeout: 310 * time.Second},
	}
	if err := c.connect(); err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}

	args := flag.Args()
	if len(args) == 1 && args[0] == "repl" {
		c.repl()
		return
	}
	if err := c.dispatch(args); err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
