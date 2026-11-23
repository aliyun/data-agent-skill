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
	credFn     func() *Credential           // optional dynamic credential provider
	dmsUnitFn  func(context.Context) string // optional DMSUnit resolver (set by Client)
	endpoint   string                       // AK/SK signed host, e.g. "dms.cn-hangzhou.aliyuncs.com"
	// The API Key data-plane endpoint is independent of the signed endpoint.
	streamEndpoint string
	region         string
}

// NewSSEClient creates a new SSE streaming client.
func NewSSEClient(cred *Credential, region string) *SSEClient {
	return &SSEClient{
		httpClient:     &http.Client{Timeout: 0, CheckRedirect: rejectSignedRedirect}, // no timeout; SSE streams are long-lived
		cred:           cred,
		endpoint:       fmt.Sprintf("dms.%s.aliyuncs.com", region),
		streamEndpoint: fmt.Sprintf("dataagent-stream-%s.aliyuncs.com", region),
		region:         region,
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

// Reconnection belongs to the watcher, whose cursor tracks applied rather than buffered events.
func (c *SSEClient) StreamEvents(ctx context.Context, agentID, sessionID string, checkpoint int) (<-chan SSEEvent, error) {
	ch := make(chan SSEEvent, 64)
	go func() {
		defer close(ch)
		if _, err := c.doStream(ctx, agentID, sessionID, checkpoint, ch); err != nil && ctx.Err() == nil {
			log.Printf("[sse:%.128q] connection ended with an upstream error", sseLogSessionID(sessionID, c.credential()))
		}
	}()
	return ch, nil
}

// API Key responses may reflect a secret in the session identifier as well.
func sseLogSessionID(sessionID string, cred *Credential) string {
	if cred.IsAPIKey() {
		return gatewaySafeRequestID(sessionID, cred)
	}
	return sessionID
}

func (c *SSEClient) doStream(
	ctx context.Context,
	agentID, sessionID string,
	checkpoint int,
	ch chan<- SSEEvent,
) (bool, error) {
	params := map[string]string{
		"AgentId":   agentID,
		"SessionId": sessionID,
	}
	if c.dmsUnitFn != nil {
		if u := c.dmsUnitFn(ctx); u != "" {
			params["DmsUnit"] = u
		}
	}
	if checkpoint > 0 {
		params["Checkpoint"] = strconv.Itoa(checkpoint)
	}

	cred := c.credential()
	var req *http.Request
	var err error
	if cred.IsAPIKey() {
		body := map[string]string{"Action": "GetChatContent", "Version": "2025-04-14", "RegionId": c.region}
		for key, value := range params {
			body[key] = value
		}
		encoded, _ := json.Marshal(body)
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, "https://"+c.streamEndpoint+"/apikey", strings.NewReader(string(encoded)))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-api-key", cred.APIKey)
		}
	} else {
		host := c.endpoint
		headers := SignRequest(cred, "POST", host, "GetChatContent", params, "")
		qs := BuildSignedQueryString("GetChatContent", "2025-04-14", params)
		req, err = http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("https://%s/?%s", host, qs), nil)
		if err == nil {
			for k, v := range headers {
				req.Header.Set(k, v)
			}
		}
	}
	if err != nil {
		return false, fmt.Errorf("build SSE request: %w", &signedHTTPError{cause: err})
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("SSE connect: %w", &signedHTTPError{cause: err})
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return false, signedRedirectError(resp, cred)
	}

	debugSSE := os.Getenv("DATA_AGENT_DEBUG_SSE") != ""
	logSessionID := sseLogSessionID(sessionID, cred)
	if debugSSE {
		log.Printf("[sse:%.128q] HTTP %d request-id=%q",
			logSessionID, resp.StatusCode, gatewaySafeRequestID(resp.Header.Get("x-acs-request-id"), cred))
	}

	if resp.StatusCode != http.StatusOK {
		requestID := gatewaySafeRequestID(resp.Header.Get("x-acs-request-id"), cred)
		return false, fmt.Errorf("SSE HTTP %d (request-id: %s)", resp.StatusCode, requestID)
	}

	scanner := bufio.NewScanner(resp.Body)
	// Allow large SSE lines (up to 1 MB).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var eventType string

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if debugSSE && line != "" {
			log.Printf("[sse:%.128q] received line bytes=%d", logSessionID, len(line))
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
		if cred.IsAPIKey() {
			return false, fmt.Errorf("SSE read: %w", &signedHTTPError{cause: err})
		}
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
