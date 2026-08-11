// Package event provides SSE event parsing for the Data Agent streaming protocol.
//
// The Data Agent server emits Server-Sent Events during analysis sessions.
// Each event carries an event_type and category that together determine what
// action the client should take: show results, request confirmation, track
// progress, etc.
//
// This package ports the dispatch logic from the Python streaming.py reference
// implementation into a stateless Parse function suitable for the Go MCP server.
package event

// SSE event types emitted by the Data Agent server.
const (
	EventChatStart    = "chat_start"
	EventContentStart = "content_start"
	EventDelta        = "delta"
	EventData         = "data"
	EventContentFinish = "content_finish"
	EventStatusChange = "status_change"
	EventChatFinish   = "chat_finish"
	EventChatCanceled = "chat_canceled"
	EventSSEFinish    = "SSE_FINISH"
	EventSSEFailure   = "SSE_FAILURE"
	EventSSEVersion   = "SSE_VERSION"
	EventHeartbeat    = "HEARTBEAT"
	EventStream       = "STREAM"
)

// Category constants for the SSE event category field.
// Categories qualify the event_type and carry domain-specific semantics.
const (
	CatAskPlan            = "ask_plan"
	CatAskSQL             = "ask_sql"
	CatAskReportRender    = "ask_report_render"
	CatAskHuman           = "ask_human"
	CatChat               = "chat"
	CatPlan               = "plan"
	CatOutputConclusion   = "output_conclusion"
	CatToolCallResponse   = "tool_call_response"
	CatToolCallChoices    = "tool_call_choices"
	CatRequestDatasource  = "request_datasource"
	CatRecommendedQuestion = "recommended_question"
	CatLLM                = "llm"
	CatThink              = "think"
)

// Action tells the session watcher what to do with a parsed event.
type Action int

const (
	ActionNone          Action = iota // Ignore; no action needed
	ActionConfirmPlan                 // Server wants plan confirmation
	ActionConfirmSQL                  // Server wants SQL execution confirmation
	ActionConfirmReport               // Server wants report render confirmation
	ActionHumanInput                  // Server needs free-form human input
	ActionStepProgress                // Plan step progress update
	ActionConclusion                  // Analysis conclusion / result
	ActionCompleted                   // Stream completed normally
	ActionError                       // Stream error
	ActionCanceled                    // Stream canceled
	ActionRecommendedQuestion         // Server sent recommended follow-up questions
	ActionReportGenerated             // jsx_report or mission_report generated
)

// String returns a human-readable name for the action.
func (a Action) String() string {
	switch a {
	case ActionNone:
		return "none"
	case ActionConfirmPlan:
		return "confirm_plan"
	case ActionConfirmSQL:
		return "confirm_sql"
	case ActionConfirmReport:
		return "confirm_report"
	case ActionHumanInput:
		return "human_input"
	case ActionStepProgress:
		return "step_progress"
	case ActionConclusion:
		return "conclusion"
	case ActionCompleted:
		return "completed"
	case ActionError:
		return "error"
	case ActionCanceled:
		return "canceled"
	case ActionRecommendedQuestion:
		return "recommended_question"
	case ActionReportGenerated:
		return "report_generated"
	default:
		return "unknown"
	}
}

// NeedsConfirmation returns true if the action requires user or auto confirmation
// before the analysis can proceed.
func (a Action) NeedsConfirmation() bool {
	return a == ActionConfirmPlan || a == ActionConfirmSQL || a == ActionConfirmReport || a == ActionHumanInput
}

// IsTerminal returns true if this action means the SSE stream has ended and
// no further events should be expected.
func (a Action) IsTerminal() bool {
	return a == ActionCompleted || a == ActionError || a == ActionCanceled
}

// Base64Image represents a base64-encoded image extracted from Markdown content.
type Base64Image struct {
	Alt      string // image alt text
	Format   string // image format (png/jpg/webp)
	B64Data  string // raw base64-encoded data
	MIMEType string // e.g. "image/png"
}

// ParsedEvent is the result of parsing a single SSE event.
// Fields are populated selectively depending on the Action.
type ParsedEvent struct {
	Action      Action                 // What the caller should do
	Category    string                 // Original event category
	Content     string                 // Key content (conclusion text, plan JSON, SQL, error message)
	StepCurrent int                    // Current step number (for plan progress)
	StepTotal   int                    // Total steps (for plan progress)
	StepName    string                 // Current step name
	RawData     map[string]interface{} // Full parsed JSON data (nil when not applicable)
	Artifacts   []string               // File references extracted from events
	Images      []Base64Image          // base64 images extracted from conclusion markdown
}
