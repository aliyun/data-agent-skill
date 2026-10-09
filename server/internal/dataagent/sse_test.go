package dataagent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSSEUserAgent(t *testing.T) {
	testClientAuthModes(t, func(t *testing.T, c *Client) {
		client := NewSSEClient(c.cred, c.region)
		calls := 0
		client.httpClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if got := req.Header.Get("User-Agent"); got != userAgent || !strings.HasSuffix(got, " DataAgent_MCP") {
				t.Errorf("User-Agent = %q, want %q with DataAgent_MCP token", got, userAgent)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("event: SSE_FINISH\ndata: {}\n\n")),
			}, nil
		})}
		_, err := client.doStream(context.Background(), "ua-test-agent", "ua-test-session", 0, make(chan SSEEvent, 1))
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("doStream() made %d requests, want 1", calls)
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
	client := NewSSEClient(&Credential{APIKey: "test-key"}, "test")
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
	client := NewSSEClient(&Credential{APIKey: "test-key"}, "test")
	client.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
			"event: content_start\ndata: {\"checkpoint\":2,\"category\":\"task_finish\"}\n\n" +
				"event: data\ndata: {\"checkpoint\":2,\"category\":\"task_finish\",\"content\":\"result\",\"event_id\":null,\"channel\":{\"mission_id\":\"test\"}}\n\n" +
				"event: content_finish\ndata: {\"checkpoint\":2,\"category\":\"task_finish\"}\n\n" +
				"event: SSE_FINISH\ndata: {}\n\n"))}, nil
	})}
	stream, err := client.StreamEvents(context.Background(), "agent", "session", 2)
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
}
