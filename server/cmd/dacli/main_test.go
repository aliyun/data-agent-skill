package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrettyPreservesJSONNumbers(t *testing.T) {
	input := `{"data":[{"ID":9007199254740993,"Amount":0.123456789012345678901,"Exponent":1e+30,"Description":null}]}`
	output := pretty(input)
	if !json.Valid([]byte(output)) || !strings.Contains(output, "\n") {
		t.Fatalf("expected formatted JSON: %q", output)
	}
	for _, literal := range []string{"9007199254740993", "0.123456789012345678901", "1e+30", "null"} {
		if !strings.Contains(output, literal) {
			t.Fatalf("lost literal %s: %s", literal, output)
		}
	}
}

func TestPrettyPreservesNonJSONText(t *testing.T) {
	for _, input := range []string{"not JSON", "", `{"unfinished":`, `{} {}`} {
		if got := pretty(input); got != input {
			t.Fatalf("pretty(%q) = %q", input, got)
		}
	}
}

func TestAuthenticatedProtocolLifecycle(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-aily-token") != "synthetic-token" || r.Header.Get("x-aily-jwt") != "synthetic-jwt" {
			t.Error("all protocol requests require credentials")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("x-aily-user") != "" || r.Header.Get("x-aily-email") != "" {
			t.Error("JWT mode must not also send conflicting plain identity headers")
		}
		if r.Method == http.MethodDelete {
			methods = append(methods, "DELETE")
			if r.Header.Get("Mcp-Session-Id") != "synthetic-session" {
				t.Error("DELETE missing issued session")
			}
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		method, _ := body["method"].(string)
		methods = append(methods, method)
		if method == "initialize" {
			if r.Header.Get("Mcp-Session-Id") != "" {
				t.Error("initialize must not reuse an issued ID")
			}
			w.Header().Set("Mcp-Session-Id", "synthetic-session")
		} else if r.Header.Get("Mcp-Session-Id") != "synthetic-session" {
			t.Error("missing issued session")
		}
		if method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"result":{}}`, body["id"])
	}))
	defer srv.Close()
	c := &client{url: srv.URL, user: "ignored-plain-user", email: "ignored@example.invalid", token: "synthetic-token", jwt: "synthetic-jwt", http: srv.Client()}
	if err := c.connect(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.rpc("tools/list", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(methods, ","); got != "initialize,notifications/initialized,tools/list,DELETE" {
		t.Fatal(got)
	}
	if c.sid != "" {
		t.Fatal("deleted session retained")
	}
}

func TestDecodeHTTPRejectionDoesNotEchoBody(t *testing.T) {
	for _, status := range []int{301, 401, 403, 404, 413, 429} {
		_, err := decode(&http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("synthetic-secret"))})
		if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("unsafe rejection: %v", err)
		}
	}
}
