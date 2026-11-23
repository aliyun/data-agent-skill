package dataagent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAPIKeySSEFinishAndCheckpoint(t *testing.T) {
	for _, checkpoint := range []int{-1, 0, 2} {
		t.Run(fmt.Sprint(checkpoint), func(t *testing.T) {
			c := apiKeyTestClient(t, WithAPIKeyEndpoint("control.example.invalid"), WithAPIKeyStreamEndpoint("stream.example.invalid"))
			var calls atomic.Int32
			ctx, ids := WithRequestIDs(context.Background())
			c.sse.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				payload := apiKeyRequestPayload(t, req)
				if req.URL.Host != "stream.example.invalid" || req.Header.Get("Accept") != "text/event-stream" ||
					payload["Action"] != "GetChatContent" || payload["Version"] != "2025-04-14" || payload["RegionId"] != "cn-shanghai" ||
					payload["AgentId"] != "agent" || payload["SessionId"] != "session" || payload["DmsUnit"] != nil || req.Context().Value(requestIDsKey{}) != ids {
					t.Error("API Key stream lost endpoint, scope, or caller context")
				}
				if checkpoint > 0 && payload["Checkpoint"] != "2" || checkpoint <= 0 && payload["Checkpoint"] != nil {
					t.Error("API Key stream changed checkpoint semantics")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(
					"event: content_start at timestamp\ndata: {\"checkpoint\":2,\"category\":\"task_finish\"}\n\n" +
						"event: data\ndata: {\"checkpoint\":2,\"content\":\"result\",\"channel\":{\"mission_id\":\"m\"}}\n\n" +
						"event: content_finish\ndata: {\"checkpoint\":2}\n\n" +
						"event: SSE_FINISH\ndata: {}\n\n" +
						"event: data\ndata: {\"content\":\"must not consume after finish\"}\n\n"))}, nil
			})
			stream, err := c.StreamSSE(ctx, "agent", "session", checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			var events []SSEEvent
			for event := range stream {
				events = append(events, event)
			}
			if calls.Load() != 1 || len(events) != 4 || events[1].Content != "result" || events[1].Data["channel"] == nil || events[3].EventType != "SSE_FINISH" {
				t.Fatalf("stream retried or lost events: calls=%d events=%+v", calls.Load(), events)
			}
			for _, event := range events[:3] {
				if event.Checkpoint == nil || *event.Checkpoint != 2 {
					t.Fatal("shared checkpoint batch changed")
				}
			}
		})
	}
}

func TestAPIKeySSELimitsAndSanitizedLogs(t *testing.T) {
	t.Setenv("DATA_AGENT_DEBUG_SSE", "1")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	const sensitive = "synthetic-api-key private-query https://example.invalid/?Signature=signed-secret"
	for _, mode := range []string{"finish", "eof", "read-error", "oversize", "http-error", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			c := apiKeyTestClient(t)
			var calls atomic.Int32
			c.sse.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				apiKeyRequestPayload(t, req)
				text := "data: {\"event_type\":\"delta\",\"checkpoint\":2,\"content\":\"" + sensitive + "\"}\n\n"
				status := http.StatusOK
				var body io.ReadCloser
				switch mode {
				case "finish":
					text += "data: {\"event_type\":\"SSE_FINISH\"}\n\n"
				case "read-error":
					body = &failingResponseBody{Reader: strings.NewReader(text), err: errors.New(sensitive)}
				case "oversize":
					text = "data: " + strings.Repeat("x", 1024*1024) + "\n"
				case "http-error":
					status = http.StatusForbidden
				case "redirect":
					status = http.StatusTemporaryRedirect
				}
				if body == nil {
					body = io.NopCloser(strings.NewReader(text))
				}
				return &http.Response{StatusCode: status, Header: http.Header{
					"X-Acs-Request-Id": []string{"synthetic-api-key"}, "Content-Type": []string{sensitive},
				}, Body: body}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			events := make(chan SSEEvent, 4)
			finished, err := c.sse.doStream(ctx, "a", "synthetic-api-key", 1, events)
			wantSuccess := mode == "finish" || mode == "eof"
			if (err == nil) != wantSuccess || finished != wantSuccess {
				t.Fatalf("mode=%s finished=%t error=%v", mode, finished, err)
			}
			if !wantSuccess {
				for _, secret := range []string{"synthetic-api-key", "private-query", "https://", "signed-secret"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatal("stream error exposed sensitive upstream text")
					}
				}
			}
			stream, err := c.StreamSSE(ctx, "a", "synthetic-api-key\nFORGED", 1)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for range stream {
				count++
			}
			wantCount := 0
			if mode == "finish" {
				wantCount = 2
			} else if mode == "eof" || mode == "read-error" {
				wantCount = 1
			}
			if count != wantCount || calls.Load() != 2 || ctx.Err() != nil {
				t.Fatalf("stream retried or consumed invalid body: count=%d calls=%d", count, calls.Load())
			}
		})
	}
	for _, secret := range []string{"synthetic-api-key", "private-query", "https://", "signed-secret", "\nFORGED"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("stream log exposed sensitive upstream text")
		}
	}
	if !strings.Contains(logs.String(), "received line bytes=") || !strings.Contains(logs.String(), "connection ended") {
		t.Fatal("safe stream diagnostics missing")
	}
}

func TestSSERedirectsRejectedOnEveryConnection(t *testing.T) {
	t.Setenv("DATA_AGENT_DEBUG_SSE", "1")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	for _, status := range []int{301, 302, 303, 307, 308} {
		for _, destination := range []string{"samehost", "crosshost", "downgrade", "malformed"} {
			t.Run(fmt.Sprintf("%d/%s", status, destination), func(t *testing.T) {
				c, initial, redirected := signedRedirectFixture(t, status, destination)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				// Exercise caller-controlled reconnection without automatic endpoint changes.
				for checkpoint := 1; checkpoint <= 2; checkpoint++ {
					events := make(chan SSEEvent, 1)
					finished, err := c.sse.doStream(ctx, "agent", "session", checkpoint, events)
					assertSignedRedirectError(t, err)
					if finished || len(events) != 0 {
						t.Fatal("redirect response treated as SSE data or completion")
					}
				}
				if initial.Load() != 2 || redirected.Load() != 0 {
					t.Fatalf("unexpected reconnect counts: initial=%d target=%d", initial.Load(), redirected.Load())
				}
			})
		}
	}
	// Production StreamEvents logs the controlled error; debug headers must not leak.
	c, initial, redirected := signedRedirectFixture(t, 302, "samehost")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := c.StreamSSE(ctx, "agent", "session", 0)
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
		t.Error("redirect produced stream data")
	}
	if initial.Load() != 1 || redirected.Load() != 0 || !strings.Contains(logs.String(), "connection ended") {
		t.Fatal("redirect was retried, forwarded, or not logged")
	}
	assertSignedRedirectError(t, errors.New(logs.String()))
}

func TestSSEDebugOmitsRawPayloadAndEscapesIdentity(t *testing.T) {
	t.Setenv("DATA_AGENT_DEBUG_SSE", "1")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previous) })
	client := NewSSEClient(&Credential{AccessKeyID: "fake-ak", AccessKeySecret: "fake-sk", SecurityToken: "fake-sts"}, "test")
	client.httpClient.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK,
			Header: http.Header{"X-Acs-Request-Id": []string{"fake-sts"}},
			Body:   io.NopCloser(strings.NewReader("event: delta\ndata: {\"content\":\"https://example.invalid/?Signature=fake-signature\"}\n\nevent: SSE_FINISH\ndata: {}\n\n")),
		}, nil
	})
	if _, err := client.doStream(context.Background(), "agent", "session\nFORGED\r", 0, make(chan SSEEvent, 4)); err != nil {
		t.Fatal(err)
	}
	out := logs.String()
	for _, secret := range []string{"fake-sts", "fake-signature", "https://", "\nFORGED", "\r"} {
		if strings.Contains(out, secret) {
			t.Fatalf("debug log exposed untrusted payload or identity: %q", out)
		}
	}
	if !strings.Contains(out, "received line bytes=") {
		t.Fatal("safe stream diagnostics missing")
	}
}

func TestSSEUserAgent(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		var calls atomic.Int32
		useSignedTLSFixture(t, c, func(w http.ResponseWriter, req *http.Request) {
			calls.Add(1)
			if got := req.Header.Get("User-Agent"); got != userAgent || !strings.HasSuffix(got, " DataAgent_MCP") {
				t.Errorf("User-Agent = %q, want %q with DataAgent_MCP token", got, userAgent)
			}
			if req.Header.Get("Accept") != "text/event-stream" || req.URL.Query().Get("Action") != "GetChatContent" {
				t.Error("missing signed SSE action or Accept header")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: SSE_FINISH\ndata: {}\n\n")
		})
		_, err := c.sse.doStream(context.Background(), "ua-test-agent", "ua-test-session", 0, make(chan SSEEvent, 1))
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 {
			t.Fatalf("doStream() made %d requests, want 1", calls.Load())
		}
	})
}

func TestSSEResolverUsesStreamContext(t *testing.T) {
	ctx, ids := WithRequestIDs(context.Background())
	c := NewClient(&Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk"}, "test")
	c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Context().Value(requestIDsKey{}) != ids || req.URL.Query().Get("Action") != "GetActiveRouteUnit" {
			t.Fatal("DMS unit resolver lost the stream context or action")
		}
		return jsonHTTPResponse(t, map[string]any{
			"RequestId": "route-id", "Route": map[string]any{"RegionId": "stream-unit"},
		}), nil
	})
	c.sse.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Context().Value(requestIDsKey{}) != ids || req.URL.Query().Get("DmsUnit") != "stream-unit" {
			t.Fatal("stream lost its context or resolved DMS unit")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("event: SSE_FINISH\ndata: {}\n\n"))}, nil
	})
	if _, err := c.sse.doStream(ctx, "agent", "session", 0, make(chan SSEEvent, 1)); err != nil {
		t.Fatal(err)
	}
	got := ids.Snapshot()
	if len(got) != 1 || got[0] != (APIRequest{Action: "GetActiveRouteUnit", RequestID: "route-id"}) {
		t.Fatalf("resolver requests = %+v", got)
	}
}

func TestStreamEventsLeavesReconnectToAppliedCursorOwner(t *testing.T) {
	var calls atomic.Int32
	client := NewSSEClient(&Credential{AccessKeyID: "test-ak", AccessKeySecret: "test-sk", SecurityToken: "test-sts"}, "test")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Body: &failingResponseBody{
			Reader: strings.NewReader("event: content_start\ndata: {\"category\":\"output_conclusion\",\"checkpoint\":2}\n\nevent: delta\ndata: {\"category\":\"output_conclusion\",\"checkpoint\":3,\"content\":\"partial\"}\n\n"),
			err:    errors.New("connection reset"),
		}}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := client.StreamEvents(ctx, "agent", "session", 1)
	if err != nil {
		t.Fatal(err)
	}
	var events []SSEEvent
	for ev := range stream {
		events = append(events, ev)
	}
	if ctx.Err() != nil || calls.Load() != 1 || len(events) != 2 {
		t.Fatalf("transport retried from an unapplied checkpoint: calls=%d events=%d context=%v", calls.Load(), len(events), ctx.Err())
	}
}

func TestStreamEventsPreservesSharedCheckpointBatch(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		useSignedTLSFixture(t, c, func(w http.ResponseWriter, req *http.Request) {
			query := req.URL.Query()
			if query.Get("Action") != "GetChatContent" || query.Get("Checkpoint") != "2" ||
				query.Get("AgentId") != "agent" || query.Get("SessionId") != "session" || query.Get("DmsUnit") != "test-unit" {
				t.Error("signed SSE request lost checkpoint or session scope")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w,
				"event: content_start\ndata: {\"checkpoint\":2,\"category\":\"task_finish\"}\n\n"+
					"event: data\ndata: {\"checkpoint\":2,\"category\":\"task_finish\",\"content\":\"result\",\"event_id\":null,\"channel\":{\"mission_id\":\"test\"}}\n\n"+
					"event: content_finish\ndata: {\"checkpoint\":2,\"category\":\"task_finish\"}\n\n"+
					"event: SSE_FINISH\ndata: {}\n\n")
		})
		stream, err := c.StreamSSE(context.Background(), "agent", "session", 2)
		if err != nil {
			t.Fatal(err)
		}
		var events []SSEEvent
		for ev := range stream {
			events = append(events, ev)
		}
		if len(events) != 4 || events[1].Content != "result" || events[1].Data["channel"] == nil {
			t.Fatalf("lost batch content or scope: %+v", events)
		}
	})
}
