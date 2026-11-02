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
