package dataagent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// SSEEvent represents a single Server-Sent Events event from Data Agent.
type SSEEvent struct {
	EventType   string                 `json:"eventType"`
	Data        map[string]interface{} `json:"data"`
	Checkpoint  *int                   `json:"checkpoint,omitempty"`
	Category    string                 `json:"category,omitempty"`
	Content     string                 `json:"content,omitempty"`
	ContentType string                 `json:"contentType,omitempty"`
}

// SSEClient handles SSE streaming connections to the Data Agent service.
type SSEClient struct {
	httpClient *http.Client
	cred       *Credential
	credFn     func() *Credential // optional dynamic credential provider
	dmsUnitFn  func() string      // optional DMSUnit resolver (set by Client)
	endpoint   string // e.g. "dataagent-cn-hangzhou.aliyuncs.com"
	region     string
}

// NewSSEClient creates a new SSE streaming client.
func NewSSEClient(cred *Credential, region string) *SSEClient {
	return &SSEClient{
		httpClient: &http.Client{Timeout: 0}, // no timeout; SSE streams are long-lived
		cred:       cred,
		endpoint:   fmt.Sprintf("dms.%s.aliyuncs.com", region),
		region:     region,
	}
}

// credential returns the current credential, preferring the dynamic provider.
func (c *SSEClient) credential() *Credential {
	if c.credFn != nil {
		if cred := c.credFn(); cred != nil {
			return cred
		}
	}
	return c.cred
}

// StreamEvents connects to the Data Agent SSE endpoint and yields events via a channel.
// It supports checkpoint-based resumption and auto-reconnect with exponential backoff
// (2s, 4s, 8s ... capped at 30s, max 3 retries) for network errors.
func (c *SSEClient) StreamEvents(ctx context.Context, agentID, sessionID string, checkpoint int) (<-chan SSEEvent, error) {
	ch := make(chan SSEEvent, 64)

	go func() {
		defer close(ch)

		currentCheckpoint := checkpoint
		retryCount := 0
		const maxRetries = 3

		for {
			finished, err := c.doStream(ctx, agentID, sessionID, currentCheckpoint, ch, &currentCheckpoint)
			if finished || err == nil {
				return
			}

			// Check if the context was cancelled.
			if ctx.Err() != nil {
				return
			}

			// Retry on network errors.
			retryCount++
			if retryCount > maxRetries {
				// Send an error event so the caller knows.
				select {
				case ch <- SSEEvent{
					EventType: "ERROR",
					Data:      map[string]interface{}{"error": err.Error()},
					Content:   fmt.Sprintf("SSE connection failed after %d retries: %v", maxRetries, err),
				}:
				case <-ctx.Done():
				}
				return
			}

			wait := time.Duration(1<<uint(retryCount)) * time.Second // 2s, 4s, 8s
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}

			select {
			case <-time.After(wait):
				// Continue with retry using currentCheckpoint.
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// doStream performs a single SSE connection attempt. It writes events to ch and
// updates lastCheckpoint as checkpoints arrive. It returns (finished, error)
// where finished=true means the stream completed normally (SSE_FINISH received
// or clean EOF).
func (c *SSEClient) doStream(
	ctx context.Context,
	agentID, sessionID string,
	checkpoint int,
	ch chan<- SSEEvent,
	lastCheckpoint *int,
) (bool, error) {
	params := map[string]string{
		"AgentId":   agentID,
		"SessionId": sessionID,
	}
	if c.dmsUnitFn != nil {
		if u := c.dmsUnitFn(); u != "" {
			params["DmsUnit"] = u
		}
	}
	if checkpoint > 0 {
		params["Checkpoint"] = strconv.Itoa(checkpoint)
	}

	var req *http.Request
	sseCred := c.credential()
	if sseCred.IsAPIKey() {
		// API Key mode: POST JSON body to stream endpoint with x-api-key header.
		host := fmt.Sprintf("dataagent-stream-%s.aliyuncs.com", c.region)
		bodyMap := map[string]string{
			"Action":   "GetChatContent",
			"Version":  "2025-04-14",
			"RegionId": c.region,
		}
		for k, v := range params {
			bodyMap[k] = v
		}
		bodyBytes, _ := json.Marshal(bodyMap)
		reqURL := fmt.Sprintf("https://%s/apikey", host)
		var err2 error
		req, err2 = http.NewRequestWithContext(ctx, "POST", reqURL, strings.NewReader(string(bodyBytes)))
		if err2 != nil {
			return false, fmt.Errorf("build SSE request: %w", err2)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", sseCred.APIKey)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("User-Agent", userAgent)
	} else {
		// AK/SK mode: signed POST with query string.
		host := c.endpoint
		headers := SignRequest(sseCred, "POST", host, "GetChatContent", params, "")
		headers["Accept"] = "text/event-stream"
		headers["User-Agent"] = userAgent

		qs := BuildSignedQueryString("GetChatContent", "2025-04-14", params)
		reqURL := fmt.Sprintf("https://%s/?%s", host, qs)

		var err2 error
		req, err2 = http.NewRequestWithContext(ctx, "POST", reqURL, nil)
		if err2 != nil {
			return false, fmt.Errorf("build SSE request: %w", err2)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("SSE connect: %w", err)
	}
	defer resp.Body.Close()

	debugSSE := os.Getenv("DATA_AGENT_DEBUG_SSE") != ""
	if debugSSE {
		log.Printf("[sse:%s] HTTP %d content-type=%q request-id=%s",
			sessionID, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("x-acs-request-id"))
	}

	if resp.StatusCode != http.StatusOK {
		requestID := resp.Header.Get("x-acs-request-id")
		return false, fmt.Errorf("SSE HTTP %d (request-id: %s)", resp.StatusCode, requestID)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Allow large SSE lines (up to 1 MB).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var eventType string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if debugSSE && line != "" {
			if len(line) > 300 {
				log.Printf("[sse:%s] raw: %s...", sessionID, line[:300])
			} else {
				log.Printf("[sse:%s] raw: %s", sessionID, line)
			}
		}
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "event:") {
			raw := strings.TrimSpace(line[6:])
			// The server may append " at <timestamp>"; strip it.
			if idx := strings.Index(raw, " at "); idx >= 0 {
				eventType = raw[:idx]
			} else if spaceIdx := strings.IndexByte(raw, ' '); spaceIdx >= 0 {
				eventType = raw[:spaceIdx]
			} else {
				eventType = raw
			}
		} else if strings.HasPrefix(line, "data:") {
			dataStr := strings.TrimSpace(line[5:])
			if dataStr == "" {
				continue
			}
			etype := eventType
			if etype == "" {
				// Some server responses omit the "event:" prefix line; fall
				// back to the event_type field inside the JSON payload (same
				// fix as the CLI skill v1.8.6).
				var probe struct {
					EventType string `json:"event_type"`
				}
				if json.Unmarshal([]byte(dataStr), &probe) == nil {
					etype = probe.EventType
				}
				if etype == "" {
					continue
				}
			}

			event := parseSSEEvent(etype, dataStr)

			if event.Checkpoint != nil {
				*lastCheckpoint = *event.Checkpoint
			}

			select {
			case ch <- event:
			case <-ctx.Done():
				return false, ctx.Err()
			}

			if event.EventType == "SSE_FINISH" {
				return true, nil
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("SSE read: %w", err)
	}

	// Clean EOF without SSE_FINISH.
	return true, nil
}

// parseSSEEvent parses a single SSE event from its type and JSON data string.
func parseSSEEvent(eventType, dataStr string) SSEEvent {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(dataStr), &data); err != nil {
		data = map[string]interface{}{"content": dataStr}
	}

	event := SSEEvent{
		EventType: eventType,
		Data:      data,
	}

	// Extract checkpoint.
	if v, ok := data["checkpoint"]; ok {
		switch n := v.(type) {
		case float64:
			cp := int(n)
			event.Checkpoint = &cp
		case json.Number:
			if i, err := n.Int64(); err == nil {
				cp := int(i)
				event.Checkpoint = &cp
			}
		}
	}

	// Extract category.
	if v, ok := data["category"].(string); ok {
		event.Category = v
	}

	// Extract content.
	if v, ok := data["content"].(string); ok {
		event.Content = v
	}

	// Extract content_type.
	if v, ok := data["content_type"].(string); ok {
		event.ContentType = v
	}

	return event
}
