package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type inspectedMessage struct{ method string }

var errInvalidMessage = errors.New("invalid protocol message")

// Parse the same bounded bytes passed to MCP, rejecting duplicate decoded
// top-level keys (including case aliases encoding/json accepts for struct
// fields), trailing JSON, batches, invalid envelopes and excessive nesting.
func inspectMessage(body []byte) (inspectedMessage, error) {
	if !utf8.Valid(body) {
		return inspectedMessage{}, errInvalidMessage
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return inspectedMessage{}, errInvalidMessage
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return inspectedMessage{}, errInvalidMessage
		}
		key, ok := token.(string)
		if !ok {
			return inspectedMessage{}, errInvalidMessage
		}
		lower := strings.ToLower(key)
		if _, exists := fields[lower]; exists {
			return inspectedMessage{}, errInvalidMessage
		}
		// MCP's parser accepts case aliases; the boundary deliberately does not.
		if lower != key {
			return inspectedMessage{}, errInvalidMessage
		}
		for _, field := range []string{"jsonrpc", "method", "id", "params", "result", "error"} {
			if strings.EqualFold(key, field) && key != field {
				return inspectedMessage{}, errInvalidMessage
			}
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return inspectedMessage{}, errInvalidMessage
		}
		fields[key] = raw
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return inspectedMessage{}, errInvalidMessage
	}
	if _, err := d.Token(); err != io.EOF {
		return inspectedMessage{}, errInvalidMessage
	}
	depth, scan := 0, json.NewDecoder(bytes.NewReader(body))
	for {
		token, err := scan.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return inspectedMessage{}, errInvalidMessage
		}
		if delim, ok := token.(json.Delim); ok {
			if delim == '{' || delim == '[' {
				depth++
				if depth > 64 {
					return inspectedMessage{}, errInvalidMessage
				}
			} else {
				depth--
			}
		}
	}
	var version, method string
	if json.Unmarshal(fields["jsonrpc"], &version) != nil || version != "2.0" || json.Unmarshal(fields["method"], &method) != nil || method == "" {
		return inspectedMessage{}, errInvalidMessage
	}
	// This server emits notifications, not client requests. Inbound responses
	// are unsupported; do not allow a result field to bypass initialization.
	if fields["result"] != nil || fields["error"] != nil {
		return inspectedMessage{}, errInvalidMessage
	}
	id, hasID := fields["id"]
	if hasID {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(id))
		decoder.UseNumber()
		if decoder.Decode(&value) != nil {
			return inspectedMessage{}, errInvalidMessage
		}
		switch v := value.(type) {
		case string:
		case json.Number:
			// mcp-go decodes envelope IDs through float64. Reject unsupported
			// magnitudes before it allocates an initialization session.
			if _, err := strconv.ParseFloat(string(v), 64); err != nil {
				return inspectedMessage{}, errInvalidMessage
			}
		default:
			return inspectedMessage{}, errInvalidMessage
		}
	}
	if method == "initialize" && !hasID {
		return inspectedMessage{}, errInvalidMessage
	}
	return inspectedMessage{method: method}, nil
}

func parseSessionQuery(r *http.Request) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errInvalidMessage
	}
	// No credentials or aliases in the URL. SSE supports exactly one sessionId.
	for name, entries := range values {
		if name != "sessionId" || len(entries) != 1 || entries[0] == "" || len(entries[0]) > 128 {
			return nil, errInvalidMessage
		}
	}
	return values, nil
}
