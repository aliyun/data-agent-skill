package event

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Parse takes a raw SSE event and returns a ParsedEvent with the determined action.
//
// This is the main entry point for event processing. It is intentionally stateless:
// cross-event accumulation (e.g. delta chunks for output_conclusion) is handled by
// the caller (session watcher) which feeds accumulated content back into Parse at
// the appropriate content_finish boundary.
//
// Parameters:
//   - eventType:   the SSE event type (e.g. "chat_finish", "data", "delta")
//   - category:    the event category (e.g. "ask_plan", "plan", "output_conclusion")
//   - content:     the event payload, typically JSON
//   - contentType: the content encoding hint (e.g. "json", "text")
func Parse(eventType, category, content, contentType string) ParsedEvent {
	switch eventType {

	// ----- terminal / lifecycle events -----

	case EventSSEFinish:
		return ParsedEvent{Action: ActionCompleted, Category: category}

	case EventSSEFailure:
		msg := content
		if contentType == "json" || looksLikeJSON(content) {
			if parsed, ok := tryParseJSON(content); ok {
				if m, _ := stringField(parsed, "message"); m != "" {
					msg = m
				} else if e, _ := stringField(parsed, "error"); e != "" {
					msg = e
				}
			}
		}
		return ParsedEvent{Action: ActionError, Category: category, Content: msg}

	case EventChatCanceled:
		return ParsedEvent{Action: ActionCanceled, Category: category}

	// ----- no-op events -----

	case EventHeartbeat, EventSSEVersion, EventStream, EventStatusChange:
		return ParsedEvent{Action: ActionNone, Category: category}

	// ----- chat_start -----

	case EventChatStart:
		return ParsedEvent{Action: ActionNone, Category: category}

	// ----- content_start -----

	case EventContentStart:
		// The caller uses this to initialise accumulators; no action needed here.
		return ParsedEvent{Action: ActionNone, Category: category}

	// ----- delta -----

	case EventDelta:
		// Deltas for llm, think, tool_call_response, output_conclusion are
		// accumulated by the caller. Nothing to decide at parse time.
		return ParsedEvent{Action: ActionNone, Category: category}

	// ----- data -----

	case EventData:
		return parseDataEvent(category, content, contentType)

	// ----- content_finish -----

	case EventContentFinish:
		return parseContentFinish(category, content, contentType)

	// ----- chat_finish -----

	case EventChatFinish:
		return parseChatFinish(category, content, contentType)

	default:
		return ParsedEvent{Action: ActionNone, Category: category}
	}
}

// ---------------------------------------------------------------------------
// data event sub-parser
// ---------------------------------------------------------------------------

func parseDataEvent(category, content, contentType string) ParsedEvent {
	switch category {
	case CatPlan:
		return parsePlanProgress(content)

	case CatOutputConclusion:
		// A standalone data/output_conclusion (not inside a content lifecycle).
		// Try to extract base64 images from the conclusion markdown.
		var images []Base64Image
		if parsed, ok := tryParseJSON(content); ok {
			if result, found := stringField(parsed, "result"); found {
				images = ExtractBase64Images(result)
			}
		}
		return ParsedEvent{
			Action:   ActionConclusion,
			Category: category,
			Content:  content,
			Images:   images,
		}

	case "task_finish":
		return parseTaskFinish(content)

	case CatAskPlan:
		return parsePlanContent(content, category)

	case CatAskReportRender:
		return ParsedEvent{
			Action:   ActionConfirmReport,
			Category: category,
			Content:  content,
		}

	case "jsx_report":
		return parseJSXReport(content, category)

	case "mission_report":
		return parseMissionReport(content, category)

	case CatRecommendedQuestion:
		return parseRecommendedQuestion(content, category)

	default:
		return ParsedEvent{Action: ActionNone, Category: category}
	}
}

// ---------------------------------------------------------------------------
// content_finish sub-parser
// ---------------------------------------------------------------------------

func parseContentFinish(category, content, contentType string) ParsedEvent {
	switch category {
	case CatOutputConclusion:
		// The caller should have accumulated delta chunks and passes the
		// concatenated text as content here.
		// Try to extract base64 images: content may be JSON with "result" field or plain markdown.
		var images []Base64Image
		if parsed, ok := tryParseJSON(content); ok {
			if result, found := stringField(parsed, "result"); found {
				images = ExtractBase64Images(result)
			}
		} else {
			images = ExtractBase64Images(content)
		}
		return ParsedEvent{
			Action:   ActionConclusion,
			Category: category,
			Content:  content,
			Images:   images,
		}

	case CatToolCallResponse:
		// Check accumulated tool_call_response content for result_type.
		return parseToolCallResponse(content)

	default:
		return ParsedEvent{Action: ActionNone, Category: category}
	}
}

// ---------------------------------------------------------------------------
// chat_finish sub-parser
// ---------------------------------------------------------------------------

func parseChatFinish(category, content, contentType string) ParsedEvent {
	switch category {
	case CatChat:
		// Current turn complete.
		return ParsedEvent{Action: ActionCompleted, Category: category}

	case CatAskPlan:
		return parsePlanContent(content, category)

	case CatAskSQL:
		return parseSQLContent(content, category)

	case CatAskReportRender:
		return ParsedEvent{
			Action:   ActionConfirmReport,
			Category: category,
			Content:  content,
		}

	case CatAskHuman:
		return ParsedEvent{
			Action:   ActionHumanInput,
			Category: category,
			Content:  content,
		}

	default:
		return ParsedEvent{Action: ActionNone, Category: category}
	}
}

// ---------------------------------------------------------------------------
// Domain-specific parsers
// ---------------------------------------------------------------------------

// parsePlanContent extracts plan steps from the content JSON and returns
// an ActionConfirmPlan event.
//
// Expected JSON shape:
//
//	{
//	  "plan_id": "...",
//	  "plans": [{ "plan": { "steps": [{ "order":1, "name":"...", ... }] } }]
//	}
// parseTaskFinish extracts insights from task_finish events (ASK_DATA mode).
// Content is a JSON array: [{"title":"...", "summary":"...", "chart_type":"...", "data":"..."}]
func parseTaskFinish(content string) ParsedEvent {
	// task_finish content may be a JSON array of insight objects.
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(content), &items); err == nil && len(items) > 0 {
		var parts []string
		for _, item := range items {
			title, _ := item["title"].(string)
			summary, _ := item["summary"].(string)
			if summary != "" {
				if title != "" {
					parts = append(parts, title+": "+summary)
				} else {
					parts = append(parts, summary)
				}
			}
		}
		if len(parts) > 0 {
			combined := ""
			for i, p := range parts {
				if i > 0 {
					combined += "\n"
				}
				combined += p
			}
			return ParsedEvent{
				Action:   ActionConclusion,
				Category: "task_finish",
				Content:  combined,
			}
		}
	}
	// Single object fallback.
	if parsed, ok := tryParseJSON(content); ok {
		if summary, _ := stringField(parsed, "summary"); summary != "" {
			title, _ := stringField(parsed, "title")
			c := summary
			if title != "" {
				c = title + ": " + summary
			}
			return ParsedEvent{Action: ActionConclusion, Category: "task_finish", Content: c}
		}
	}
	// Plain text fallback — treat any non-empty content as conclusion.
	if strings.TrimSpace(content) != "" {
		return ParsedEvent{Action: ActionConclusion, Category: "task_finish", Content: content}
	}
	return ParsedEvent{Action: ActionNone, Category: "task_finish"}
}

func parsePlanContent(content, category string) ParsedEvent {
	parsed, ok := tryParseJSON(content)
	if !ok {
		return ParsedEvent{
			Action:   ActionConfirmPlan,
			Category: category,
			Content:  content,
		}
	}

	pe := ParsedEvent{
		Action:   ActionConfirmPlan,
		Category: category,
		Content:  content,
		RawData:  parsed,
	}

	// Extract step count from plans[0].plan.steps
	steps := planSteps(parsed)
	pe.StepTotal = len(steps)

	return pe
}

// parseSQLContent extracts the SQL statement from the content JSON and returns
// an ActionConfirmSQL event.
//
// Expected JSON shape:
//
//	{ "sql": "SELECT ...", "question": "...", "explain_result": "..." }
func parseSQLContent(content, category string) ParsedEvent {
	parsed, ok := tryParseJSON(content)
	if !ok {
		return ParsedEvent{
			Action:   ActionConfirmSQL,
			Category: category,
			Content:  content,
		}
	}

	sql, _ := stringField(parsed, "sql")
	if sql == "" {
		sql, _ = stringField(parsed, "query")
	}

	pe := ParsedEvent{
		Action:   ActionConfirmSQL,
		Category: category,
		Content:  sql,
		RawData:  parsed,
	}
	return pe
}

// parsePlanProgress extracts step progress from a data/plan event.
//
// Expected JSON shape:
//
//	{
//	  "current_step": 2,
//	  "plan_status": "running",
//	  "plans": [{ "plan": { "steps": [...] } }]
//	}
func parsePlanProgress(content string) ParsedEvent {
	parsed, ok := tryParseJSON(content)
	if !ok {
		return ParsedEvent{Action: ActionNone, Category: CatPlan}
	}

	currentStep, _ := intField(parsed, "current_step")
	steps := planSteps(parsed)
	total := len(steps)

	stepName := ""
	for _, s := range steps {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		order, _ := intField(sm, "order")
		if order == currentStep {
			stepName, _ = stringField(sm, "name")
			break
		}
	}

	return ParsedEvent{
		Action:      ActionStepProgress,
		Category:    CatPlan,
		StepCurrent: currentStep,
		StepTotal:   total,
		StepName:    stepName,
		RawData:     parsed,
	}
}

// parseToolCallResponse inspects accumulated tool_call_response content.
// If result_type is "plan", the inner result is parsed as a plan and
// ActionConfirmPlan is returned. Otherwise ActionNone.
func parseToolCallResponse(content string) ParsedEvent {
	parsed, ok := tryParseJSON(content)
	if !ok {
		return ParsedEvent{Action: ActionNone, Category: CatToolCallResponse}
	}

	resultType, _ := stringField(parsed, "result_type")
	if resultType != "plan" {
		return ParsedEvent{Action: ActionNone, Category: CatToolCallResponse, RawData: parsed}
	}

	// The inner "result" field contains the plan JSON (possibly as a string).
	resultRaw, hasResult := parsed["result"]
	if !hasResult {
		return ParsedEvent{
			Action:   ActionConfirmPlan,
			Category: CatToolCallResponse,
			Content:  content,
			RawData:  parsed,
		}
	}

	var innerPlan map[string]interface{}
	switch v := resultRaw.(type) {
	case string:
		innerPlan, _ = tryParseJSON(v)
	case map[string]interface{}:
		innerPlan = v
	}

	steps := planSteps(innerPlan)

	return ParsedEvent{
		Action:    ActionConfirmPlan,
		Category:  CatToolCallResponse,
		Content:   content,
		StepTotal: len(steps),
		RawData:   parsed,
	}
}

// ---------------------------------------------------------------------------
// Report and recommendation parsers
// ---------------------------------------------------------------------------

// parseJSXReport extracts the report type from a jsx_report event.
func parseJSXReport(content, category string) ParsedEvent {
	parsed, ok := tryParseJSON(content)
	if !ok {
		return ParsedEvent{Action: ActionReportGenerated, Category: category, Content: "report:unknown"}
	}
	reportType, _ := stringField(parsed, "type")
	if reportType == "" {
		reportType = "unknown"
	}
	return ParsedEvent{
		Action:   ActionReportGenerated,
		Category: category,
		Content:  "report:" + reportType,
		RawData:  parsed,
	}
}

// parseMissionReport extracts the title from a mission_report event.
func parseMissionReport(content, category string) ParsedEvent {
	parsed, ok := tryParseJSON(content)
	if !ok {
		return ParsedEvent{Action: ActionReportGenerated, Category: category, Content: "mission_report:"}
	}
	title, _ := stringField(parsed, "title")
	return ParsedEvent{
		Action:   ActionReportGenerated,
		Category: category,
		Content:  "mission_report:" + title,
		RawData:  parsed,
	}
}

// parseRecommendedQuestion extracts follow-up questions and joins them with newlines.
func parseRecommendedQuestion(content, category string) ParsedEvent {
	parsed, ok := tryParseJSON(content)
	if !ok {
		return ParsedEvent{Action: ActionNone, Category: category}
	}
	questionsRaw, hasQ := parsed["questions"]
	if !hasQ {
		return ParsedEvent{Action: ActionNone, Category: category}
	}
	questionsArr, ok := questionsRaw.([]interface{})
	if !ok || len(questionsArr) == 0 {
		return ParsedEvent{Action: ActionNone, Category: category}
	}
	var questions []string
	for _, q := range questionsArr {
		if s, ok := q.(string); ok && s != "" {
			questions = append(questions, s)
		}
	}
	if len(questions) == 0 {
		return ParsedEvent{Action: ActionNone, Category: category}
	}
	return ParsedEvent{
		Action:   ActionRecommendedQuestion,
		Category: category,
		Content:  strings.Join(questions, "\n"),
		RawData:  parsed,
	}
}

// base64ImagePattern matches Markdown embedded base64 images: ![alt](data:image/format;base64,DATA)
var base64ImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(data:image/([a-zA-Z]+);base64,([A-Za-z0-9+/=\s]+)\)`)

// ExtractBase64Images extracts all base64-encoded images from Markdown text.
// It filters out mocked/placeholder images (e.g. "mocked image data").
func ExtractBase64Images(markdown string) []Base64Image {
	matches := base64ImagePattern.FindAllStringSubmatch(markdown, -1)
	var images []Base64Image
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		b64Data := m[3]
		// Filter out mock/placeholder data
		if strings.TrimSpace(b64Data) == "mocked image data" || len(b64Data) < 100 {
			continue
		}
		images = append(images, Base64Image{
			Alt:      m[1],
			Format:   strings.ToLower(m[2]),
			B64Data:  b64Data,
			MIMEType: "image/" + strings.ToLower(m[2]),
		})
	}
	return images
}

// fileRefPattern matches HTML anchor tags with file references like <a href="#file-f-xxx">filename</a>.
var fileRefPattern = regexp.MustCompile(`<a\s+href="#file-f-[^"]*">([^<]+)</a>`)

// extractArtifacts extracts file references from task_finish item data fields.
// It looks for chart_type and HTML anchor tags referencing files.
func extractArtifacts(items []map[string]interface{}) []string {
	var artifacts []string
	for _, item := range items {
		if ct, ok := item["chart_type"].(string); ok && ct != "" {
			artifacts = append(artifacts, "chart:"+ct)
		}
		if data, ok := item["data"].(string); ok && data != "" {
			matches := fileRefPattern.FindAllStringSubmatch(data, -1)
			for _, m := range matches {
				if len(m) >= 2 {
					artifacts = append(artifacts, "file:"+m[1])
				}
			}
		}
	}
	return artifacts
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

// tryParseJSON attempts to unmarshal s as a JSON object.
func tryParseJSON(s string) (map[string]interface{}, bool) {
	if s == "" {
		return nil, false
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, false
	}
	return m, true
}

// looksLikeJSON is a quick heuristic check.
func looksLikeJSON(s string) bool {
	if len(s) < 2 {
		return false
	}
	return s[0] == '{' || s[0] == '['
}

// stringField extracts a string value from a map.
func stringField(m map[string]interface{}, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// intField extracts an integer value from a map.
// JSON numbers are decoded as float64 by encoding/json.
func intField(m map[string]interface{}, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

// planSteps extracts the steps slice from plans[0].plan.steps.
// Returns nil if the path does not exist or is malformed.
func planSteps(m map[string]interface{}) []interface{} {
	if m == nil {
		return nil
	}
	plansRaw, ok := m["plans"]
	if !ok {
		return nil
	}
	plans, ok := plansRaw.([]interface{})
	if !ok || len(plans) == 0 {
		return nil
	}
	first, ok := plans[0].(map[string]interface{})
	if !ok {
		return nil
	}
	planObj, ok := first["plan"]
	if !ok {
		return nil
	}
	planMap, ok := planObj.(map[string]interface{})
	if !ok {
		return nil
	}
	stepsRaw, ok := planMap["steps"]
	if !ok {
		return nil
	}
	steps, ok := stepsRaw.([]interface{})
	if !ok {
		return nil
	}
	return steps
}
