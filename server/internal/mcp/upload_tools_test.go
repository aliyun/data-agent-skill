package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
)

// uploadClient stubs the two upload APIs; every other client method is never
// reached by these handlers.
type uploadClient struct {
	dataAgentClient // embedded: unused methods are never called here

	sigName string
	sigSize int64
	sig     *dataagent.UploadSignature

	cbName string
	cbKey  string
	cbSize int64
	cbID   string
}

func (c *uploadClient) GetFileUploadSignature(name string, size int64) (*dataagent.UploadSignature, error) {
	c.sigName, c.sigSize = name, size
	return c.sig, nil
}

func (c *uploadClient) FileUploadCallback(name, key string, size int64) (string, error) {
	c.cbName, c.cbKey, c.cbSize = name, key, size
	return c.cbID, nil
}

func uploadReq(name string, args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

func decodeUploadResult(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(res))
	}
	return decodeResult(t, res)
}

func TestGetUploadSignatureReturnsFormFields(t *testing.T) {
	client := &uploadClient{sig: &dataagent.UploadSignature{
		UploadHost:          "https://bucket.oss-cn-hangzhou.aliyuncs.com",
		UploadDir:           "uploads/abc",
		Policy:              "pol",
		OssSignature:        "sig",
		OssSignatureVersion: "OSS4-HMAC-SHA256",
		OssDate:             "20260901T000000Z",
		OssSecurityToken:    "tok",
		OssCredential:       "cred",
	}}
	s := &Server{client: client}

	res, err := s.handleGetUploadSignature(context.Background(), uploadReq(
		"data_agent_get_upload_signature",
		map[string]any{"file_name": "sales.csv", "file_size": float64(1234)},
	))
	if err != nil {
		t.Fatal(err)
	}
	out := decodeUploadResult(t, res)

	if client.sigName != "sales.csv" || client.sigSize != 1234 {
		t.Errorf("signature requested for %q/%d, want sales.csv/1234", client.sigName, client.sigSize)
	}
	if out["upload_host"] != "https://bucket.oss-cn-hangzhou.aliyuncs.com" {
		t.Errorf("upload_host = %v", out["upload_host"])
	}
	if out["oss_key"] != "uploads/abc/sales.csv" {
		t.Errorf("oss_key = %v, want uploads/abc/sales.csv", out["oss_key"])
	}
	if out["file_content_type"] != "text/csv" {
		t.Errorf("file_content_type = %v, want text/csv", out["file_content_type"])
	}
	fields, ok := out["form_fields"].(map[string]any)
	if !ok {
		t.Fatalf("form_fields missing: %v", out)
	}
	for k, want := range map[string]string{
		"key":                     "uploads/abc/sales.csv",
		"policy":                  "pol",
		"x-oss-signature":         "sig",
		"x-oss-signature-version": "OSS4-HMAC-SHA256",
		"x-oss-date":              "20260901T000000Z",
		"x-oss-security-token":    "tok",
		"x-oss-credential":        "cred",
		"success_action_status":   "200",
	} {
		if fields[k] != want {
			t.Errorf("form_fields[%q] = %v, want %q", k, fields[k], want)
		}
	}
}

// A caller-supplied path must not leak into the OSS key: only the base name
// is used.
func TestGetUploadSignatureStripsPathComponents(t *testing.T) {
	client := &uploadClient{sig: &dataagent.UploadSignature{UploadDir: "uploads/x"}}
	s := &Server{client: client}

	res, err := s.handleGetUploadSignature(context.Background(), uploadReq(
		"data_agent_get_upload_signature",
		map[string]any{"file_name": "../../etc/passwd.csv", "file_size": float64(1)},
	))
	if err != nil {
		t.Fatal(err)
	}
	out := decodeUploadResult(t, res)
	if out["oss_key"] != "uploads/x/passwd.csv" {
		t.Errorf("oss_key = %v, want uploads/x/passwd.csv", out["oss_key"])
	}
	if client.sigName != "passwd.csv" {
		t.Errorf("signature requested for %q, want passwd.csv", client.sigName)
	}
}

func TestGetUploadSignatureValidation(t *testing.T) {
	s := &Server{client: &uploadClient{}}
	for name, args := range map[string]map[string]any{
		"missing file_name": {"file_size": float64(1)},
		"missing file_size": {"file_name": "a.csv"},
		"zero file_size":    {"file_name": "a.csv", "file_size": float64(0)},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := s.handleGetUploadSignature(context.Background(),
				uploadReq("data_agent_get_upload_signature", args))
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Errorf("expected a rejection, got %s", resultText(res))
			}
		})
	}
}

func TestUploadCallbackReturnsFileID(t *testing.T) {
	client := &uploadClient{cbID: "f-123"}
	s := &Server{client: client}

	res, err := s.handleUploadCallback(context.Background(), uploadReq(
		"data_agent_upload_callback",
		map[string]any{
			"file_name": "sales.csv",
			"oss_key":   "uploads/abc/sales.csv",
			// file_size as a string must be accepted too.
			"file_size": "1234",
		},
	))
	if err != nil {
		t.Fatal(err)
	}
	out := decodeUploadResult(t, res)
	if out["file_id"] != "f-123" {
		t.Errorf("file_id = %v, want f-123", out["file_id"])
	}
	if client.cbName != "sales.csv" || client.cbKey != "uploads/abc/sales.csv" || client.cbSize != 1234 {
		t.Errorf("callback got %q/%q/%d", client.cbName, client.cbKey, client.cbSize)
	}
}

// When the API returns no ID, the upload dir doubles as the file ID (legacy
// behaviour of the removed data_agent_upload_file tool).
func TestUploadCallbackFallsBackToUploadDir(t *testing.T) {
	s := &Server{client: &uploadClient{cbID: ""}}
	res, err := s.handleUploadCallback(context.Background(), uploadReq(
		"data_agent_upload_callback",
		map[string]any{"file_name": "a.csv", "oss_key": "uploads/abc/a.csv", "file_size": float64(1)},
	))
	if err != nil {
		t.Fatal(err)
	}
	out := decodeUploadResult(t, res)
	if out["file_id"] != "uploads/abc" {
		t.Errorf("file_id = %v, want uploads/abc", out["file_id"])
	}
}

func TestUploadCallbackValidation(t *testing.T) {
	s := &Server{client: &uploadClient{}}
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"missing oss_key":   {map[string]any{"file_name": "a.csv", "file_size": float64(1)}, "oss_key"},
		"missing file_name": {map[string]any{"oss_key": "k", "file_size": float64(1)}, "file_name"},
		"bad file_size":     {map[string]any{"file_name": "a.csv", "oss_key": "k", "file_size": "x"}, "file_size"},
	} {
		t.Run(name, func(t *testing.T) {
			res, err := s.handleUploadCallback(context.Background(),
				uploadReq("data_agent_upload_callback", tc.args))
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError || !strings.Contains(resultText(res), tc.want) {
				t.Errorf("error = %q, want it to mention %q", resultText(res), tc.want)
			}
		})
	}
}

func TestArgInt64(t *testing.T) {
	for name, tc := range map[string]struct {
		val  any
		want int64
	}{
		"float64":        {float64(42), 42},
		"numeric string": {" 42 ", 42},
		"bad string":     {"nope", 0},
		"nil":            {nil, 0},
	} {
		t.Run(name, func(t *testing.T) {
			req := uploadReq("t", map[string]any{"n": tc.val})
			if got := argInt64(req, "n"); got != tc.want {
				t.Errorf("argInt64 = %d, want %d", got, tc.want)
			}
		})
	}
	if got := argInt64(uploadReq("t", map[string]any{}), "n"); got != 0 {
		t.Errorf("missing key: argInt64 = %d, want 0", got)
	}
}

func TestIsRemoteTransport(t *testing.T) {
	for transport, want := range map[string]bool{
		"":                false,
		"stdio":           false,
		"sse":             true,
		"streamable-http": true,
		"bogus":           false,
	} {
		if got := isRemoteTransport(transport); got != want {
			t.Errorf("isRemoteTransport(%q) = %v, want %v", transport, got, want)
		}
	}
}
