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
