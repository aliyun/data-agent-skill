package event

import (
	"fmt"
	"strings"
	"testing"
)

func TestSSEFinish(t *testing.T) {
	r := Parse(EventSSEFinish, "", "", "")
	assertAction(t, r, ActionCompleted, "SSE_FINISH -> ActionCompleted")
	if !r.Action.IsTerminal() {
		t.Fatal("ActionCompleted should be terminal")
	}
	if r.Action.NeedsConfirmation() {
		t.Fatal("ActionCompleted should not need confirmation")
	}
}

func TestSSEFailureJSON(t *testing.T) {
	r := Parse(EventSSEFailure, "", `{"message":"timeout"}`, "json")
	assertAction(t, r, ActionError, "SSE_FAILURE json -> ActionError")
	if r.Content != "timeout" {
		t.Fatalf("expected content 'timeout', got %q", r.Content)
	}
	if !r.Action.IsTerminal() {
		t.Fatal("ActionError should be terminal")
	}
}

func TestSSEFailurePlainText(t *testing.T) {
	r := Parse(EventSSEFailure, "", "plain error", "text")
	assertAction(t, r, ActionError, "SSE_FAILURE plain -> ActionError")
	if r.Content != "plain error" {
		t.Fatalf("expected content 'plain error', got %q", r.Content)
	}
}

func TestSSEFailureErrorField(t *testing.T) {
	r := Parse(EventSSEFailure, "", `{"error":"bad request"}`, "json")
	assertAction(t, r, ActionError, "SSE_FAILURE error field")
	if r.Content != "bad request" {
		t.Fatalf("expected content 'bad request', got %q", r.Content)
	}
}

func TestChatCanceled(t *testing.T) {
	r := Parse(EventChatCanceled, "", "", "")
	assertAction(t, r, ActionCanceled, "chat_canceled -> ActionCanceled")
	if !r.Action.IsTerminal() {
		t.Fatal("ActionCanceled should be terminal")
	}
}

func TestNoOpEvents(t *testing.T) {
	noops := []string{EventHeartbeat, EventSSEVersion, EventStream, EventStatusChange}
	for _, et := range noops {
		r := Parse(et, "", "", "")
		assertAction(t, r, ActionNone, et+" -> ActionNone")
	}
}

func TestDeltaLLMAndThink(t *testing.T) {
	r := Parse(EventDelta, CatLLM, "token", "")
	assertAction(t, r, ActionNone, "delta/llm -> ActionNone")

	r = Parse(EventDelta, CatThink, "thought", "")
	assertAction(t, r, ActionNone, "delta/think -> ActionNone")
}

func TestChatFinishAskPlan(t *testing.T) {
	planJSON := `{
		"plan_id":"abc123",
		"plans":[{"plan":{"steps":[
			{"order":1,"name":"Gather data","description":"d","type":"query","status":"pending"},
			{"order":2,"name":"Analyze","description":"d2","type":"analysis","status":"pending"}
		]}}]
	}`
	r := Parse(EventChatFinish, CatAskPlan, planJSON, "json")
	assertAction(t, r, ActionConfirmPlan, "chat_finish/ask_plan -> ActionConfirmPlan")
	if !r.Action.NeedsConfirmation() {
		t.Fatal("ActionConfirmPlan should need confirmation")
	}
	if r.Action.IsTerminal() {
		t.Fatal("ActionConfirmPlan should not be terminal")
	}
	if r.StepTotal != 2 {
		t.Fatalf("expected 2 steps, got %d", r.StepTotal)
	}
	if r.RawData == nil {
		t.Fatal("RawData should be populated")
	}
}

func TestChatFinishAskSQL(t *testing.T) {
	sqlJSON := `{"sql":"SELECT * FROM users","question":"large table","explain_result":"full scan"}`
	r := Parse(EventChatFinish, CatAskSQL, sqlJSON, "json")
	assertAction(t, r, ActionConfirmSQL, "chat_finish/ask_sql -> ActionConfirmSQL")
	if r.Content != "SELECT * FROM users" {
		t.Fatalf("expected SQL content, got %q", r.Content)
	}
	if !r.Action.NeedsConfirmation() {
		t.Fatal("ActionConfirmSQL should need confirmation")
	}
}

func TestChatFinishAskSQLQueryField(t *testing.T) {
	sqlJSON := `{"query":"SELECT 1"}`
	r := Parse(EventChatFinish, CatAskSQL, sqlJSON, "json")
	assertAction(t, r, ActionConfirmSQL, "ask_sql with query field")
	if r.Content != "SELECT 1" {
		t.Fatalf("expected 'SELECT 1', got %q", r.Content)
	}
}

func TestChatFinishAskReportRender(t *testing.T) {
	r := Parse(EventChatFinish, CatAskReportRender, `{}`, "json")
	assertAction(t, r, ActionConfirmReport, "chat_finish/ask_report_render -> ActionConfirmReport")
	if !r.Action.NeedsConfirmation() {
		t.Fatal("ActionConfirmReport should need confirmation")
	}
}

func TestChatFinishAskHuman(t *testing.T) {
	r := Parse(EventChatFinish, CatAskHuman, "Which database?", "text")
	assertAction(t, r, ActionHumanInput, "chat_finish/ask_human -> ActionHumanInput")
	if r.Content != "Which database?" {
		t.Fatalf("expected question text, got %q", r.Content)
	}
	if !r.Action.NeedsConfirmation() {
		t.Fatal("ActionHumanInput should need confirmation")
	}
}

func TestChatFinishChat(t *testing.T) {
	r := Parse(EventChatFinish, CatChat, "", "")
	assertAction(t, r, ActionCompleted, "chat_finish/chat -> ActionCompleted")
}

func TestDataPlanProgress(t *testing.T) {
	progressJSON := `{
		"current_step":2,
		"plan_status":"running",
		"plans":[{"plan":{"steps":[
			{"order":1,"name":"Step A"},
			{"order":2,"name":"Step B"},
			{"order":3,"name":"Step C"}
		]}}]
	}`
	r := Parse(EventData, CatPlan, progressJSON, "json")
	assertAction(t, r, ActionStepProgress, "data/plan -> ActionStepProgress")
	if r.StepCurrent != 2 {
		t.Fatalf("expected current step 2, got %d", r.StepCurrent)
	}
	if r.StepTotal != 3 {
		t.Fatalf("expected 3 total steps, got %d", r.StepTotal)
	}
	if r.StepName != "Step B" {
		t.Fatalf("expected step name 'Step B', got %q", r.StepName)
	}
}

func TestDataPlanInvalidJSON(t *testing.T) {
	r := Parse(EventData, CatPlan, "not json", "text")
	assertAction(t, r, ActionNone, "data/plan invalid json -> ActionNone")
}

func TestDataOutputConclusion(t *testing.T) {
	r := Parse(EventData, CatOutputConclusion, "The analysis shows...", "text")
	assertAction(t, r, ActionConclusion, "data/output_conclusion -> ActionConclusion")
	if r.Content != "The analysis shows..." {
		t.Fatalf("unexpected content: %q", r.Content)
	}
}

func TestContentFinishOutputConclusion(t *testing.T) {
	r := Parse(EventContentFinish, CatOutputConclusion, "accumulated text", "")
	assertAction(t, r, ActionConclusion, "content_finish/output_conclusion -> ActionConclusion")
	if r.Content != "accumulated text" {
		t.Fatalf("unexpected content: %q", r.Content)
	}
}

func TestContentFinishToolCallResponsePlan(t *testing.T) {
	tcrPlan := `{"result_type":"plan","result":"{\"plans\":[{\"plan\":{\"steps\":[{\"order\":1,\"name\":\"X\"}]}}]}"}`
	r := Parse(EventContentFinish, CatToolCallResponse, tcrPlan, "json")
	assertAction(t, r, ActionConfirmPlan, "content_finish/tool_call_response(plan) -> ActionConfirmPlan")
	if r.StepTotal != 1 {
		t.Fatalf("expected 1 inner plan step, got %d", r.StepTotal)
	}
}

func TestContentFinishToolCallResponseOther(t *testing.T) {
	tcrOther := `{"result_type":"jupyter_cell","result":"{}"}`
	r := Parse(EventContentFinish, CatToolCallResponse, tcrOther, "json")
	assertAction(t, r, ActionNone, "content_finish/tool_call_response(jupyter) -> ActionNone")
}

func TestContentFinishToolCallResponseInvalidJSON(t *testing.T) {
	r := Parse(EventContentFinish, CatToolCallResponse, "not json", "")
	assertAction(t, r, ActionNone, "content_finish/tool_call_response invalid json -> ActionNone")
}

func TestDataAskReportRender(t *testing.T) {
	r := Parse(EventData, CatAskReportRender, `{"report":"data"}`, "json")
	assertAction(t, r, ActionConfirmReport, "data/ask_report_render -> ActionConfirmReport")
}

func TestDataAskPlan(t *testing.T) {
	planJSON := `{"plan_id":"xyz","plans":[{"plan":{"steps":[{"order":1,"name":"Only step"}]}}]}`
	r := Parse(EventData, CatAskPlan, planJSON, "json")
	assertAction(t, r, ActionConfirmPlan, "data/ask_plan -> ActionConfirmPlan")
	if r.StepTotal != 1 {
		t.Fatalf("expected 1 step, got %d", r.StepTotal)
	}
}

func TestActionString(t *testing.T) {
	cases := map[Action]string{
		ActionNone:          "none",
		ActionConfirmPlan:   "confirm_plan",
		ActionConfirmSQL:    "confirm_sql",
		ActionConfirmReport: "confirm_report",
		ActionHumanInput:    "human_input",
		ActionStepProgress:  "step_progress",
		ActionConclusion:    "conclusion",
		ActionCompleted:     "completed",
		ActionError:         "error",
		ActionCanceled:      "canceled",
	}
	for action, expected := range cases {
		if action.String() != expected {
			t.Fatalf("Action(%d).String() = %q, want %q", action, action.String(), expected)
		}
	}
}

func TestUnknownActionString(t *testing.T) {
	a := Action(999)
	if a.String() != "unknown" {
		t.Fatalf("unknown action string = %q, want 'unknown'", a.String())
	}
}

func TestChatStartContentStart(t *testing.T) {
	r := Parse(EventChatStart, "", `{"message":"hello"}`, "json")
	assertAction(t, r, ActionNone, "chat_start -> ActionNone")

	r = Parse(EventContentStart, CatOutputConclusion, "", "")
	assertAction(t, r, ActionNone, "content_start -> ActionNone")
}

func TestUnknownEventType(t *testing.T) {
	r := Parse("totally_unknown", "whatever", "stuff", "")
	assertAction(t, r, ActionNone, "unknown event type -> ActionNone")
}

func TestToolCallResponsePlanWithMapResult(t *testing.T) {
	// result as an object (not string)
	tcrPlan := `{"result_type":"plan","result":{"plans":[{"plan":{"steps":[{"order":1,"name":"A"},{"order":2,"name":"B"}]}}]}}`
	r := Parse(EventContentFinish, CatToolCallResponse, tcrPlan, "json")
	assertAction(t, r, ActionConfirmPlan, "tool_call_response plan with map result")
	if r.StepTotal != 2 {
		t.Fatalf("expected 2 steps from map result, got %d", r.StepTotal)
	}
}

func TestToolCallResponsePlanNoResult(t *testing.T) {
	tcrPlan := `{"result_type":"plan"}`
	r := Parse(EventContentFinish, CatToolCallResponse, tcrPlan, "json")
	assertAction(t, r, ActionConfirmPlan, "tool_call_response plan with no result field")
}

func TestNeedsConfirmationExhaustive(t *testing.T) {
	confirm := []Action{ActionConfirmPlan, ActionConfirmSQL, ActionConfirmReport, ActionHumanInput}
	noConfirm := []Action{ActionNone, ActionStepProgress, ActionConclusion, ActionCompleted, ActionError, ActionCanceled}

	for _, a := range confirm {
		if !a.NeedsConfirmation() {
			t.Fatalf("%s should need confirmation", a)
		}
	}
	for _, a := range noConfirm {
		if a.NeedsConfirmation() {
			t.Fatalf("%s should not need confirmation", a)
		}
	}
}

func TestIsTerminalExhaustive(t *testing.T) {
	terminal := []Action{ActionCompleted, ActionError, ActionCanceled}
	nonTerminal := []Action{ActionNone, ActionConfirmPlan, ActionConfirmSQL, ActionConfirmReport, ActionHumanInput, ActionStepProgress, ActionConclusion}

	for _, a := range terminal {
		if !a.IsTerminal() {
			t.Fatalf("%s should be terminal", a)
		}
	}
	for _, a := range nonTerminal {
		if a.IsTerminal() {
			t.Fatalf("%s should not be terminal", a)
		}
	}
}

// ---------------------------------------------------------------------------
// ExtractBase64Images tests
// ---------------------------------------------------------------------------

func TestExtractBase64Images_SingleImage(t *testing.T) {
	b64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="
	// Generate a base64 string longer than 100 characters to pass the filter
	longB64 := strings.Repeat("AAAA", 30) + b64 // > 100 chars

	markdown := fmt.Sprintf("# 分析结果\n\n![销售趋势图](data:image/png;base64,%s)\n\n结论文本", longB64)

	images := ExtractBase64Images(markdown)

	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].Alt != "销售趋势图" {
		t.Errorf("expected alt '销售趋势图', got %q", images[0].Alt)
	}
	if images[0].Format != "png" {
		t.Errorf("expected format 'png', got %q", images[0].Format)
	}
	if images[0].MIMEType != "image/png" {
		t.Errorf("expected mimeType 'image/png', got %q", images[0].MIMEType)
	}
	if images[0].B64Data != longB64 {
		t.Errorf("b64Data mismatch")
	}
}

func TestExtractBase64Images_MultipleImages(t *testing.T) {
	b64_1 := strings.Repeat("BBBB", 30) + "AAAA" // > 100 chars
	b64_2 := strings.Repeat("CCCC", 30) + "BBBB" // > 100 chars

	markdown := fmt.Sprintf(
		"![图片1](data:image/png;base64,%s)\n\n一些文本\n\n![图片2](data:image/jpeg;base64,%s)",
		b64_1, b64_2,
	)

	images := ExtractBase64Images(markdown)

	if len(images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(images))
	}
	if images[0].Format != "png" {
		t.Errorf("image 0: expected format 'png', got %q", images[0].Format)
	}
	if images[1].Format != "jpeg" {
		t.Errorf("image 1: expected format 'jpeg', got %q", images[1].Format)
	}
}

func TestExtractBase64Images_NoImages(t *testing.T) {
	markdown := "# 标题\n\n这是一段没有图片的文本。\n\n## 结论\n\n数据增长20%。"
	images := ExtractBase64Images(markdown)
	if len(images) != 0 {
		t.Fatalf("expected 0 images, got %d", len(images))
	}
}

func TestExtractBase64Images_MockedDataFiltered(t *testing.T) {
	// "mocked image data" should be filtered
	markdown := "![图表](data:image/png;base64,mocked image data)\n\n结论"
	images := ExtractBase64Images(markdown)
	if len(images) != 0 {
		t.Fatalf("expected mocked image to be filtered, got %d images", len(images))
	}
}

func TestExtractBase64Images_ShortDataFiltered(t *testing.T) {
	// base64 data shorter than 100 characters should be filtered
	markdown := "![小图](data:image/png;base64,abc123)"
	images := ExtractBase64Images(markdown)
	if len(images) != 0 {
		t.Fatalf("expected short data to be filtered, got %d images", len(images))
	}
}

func TestExtractBase64Images_ChineseAlt(t *testing.T) {
	b64 := strings.Repeat("DDDD", 30) + "EEEE" // > 100 chars
	markdown := fmt.Sprintf("![年度销售额趋势折线图，展示2021年至2025年每年的总销售额变化](data:image/png;base64,%s)", b64)
	images := ExtractBase64Images(markdown)
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].Alt != "年度销售额趋势折线图，展示2021年至2025年每年的总销售额变化" {
		t.Errorf("alt text mismatch: %q", images[0].Alt)
	}
}

func TestExtractBase64Images_LargeData(t *testing.T) {
	// Simulate a 200KB+ image
	largeB64 := strings.Repeat("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/", 4000)
	markdown := fmt.Sprintf("![大图](data:image/png;base64,%s)", largeB64)
	images := ExtractBase64Images(markdown)
	if len(images) != 1 {
		t.Fatalf("expected 1 image for large data, got %d", len(images))
	}
	if len(images[0].B64Data) != len(largeB64) {
		t.Errorf("expected b64 data length %d, got %d", len(largeB64), len(images[0].B64Data))
	}
}

func TestExtractBase64Images_MixedRealAndMocked(t *testing.T) {
	realB64 := strings.Repeat("FFFF", 30) + "GGGG" // > 100 chars
	markdown := fmt.Sprintf(
		"![real](data:image/png;base64,%s)\n![mock](data:image/png;base64,mocked image data)",
		realB64,
	)
	images := ExtractBase64Images(markdown)
	if len(images) != 1 {
		t.Fatalf("expected 1 real image (mock filtered), got %d", len(images))
	}
	if images[0].Alt != "real" {
		t.Errorf("expected alt 'real', got %q", images[0].Alt)
	}
}

// assertAction is a test helper that checks the action matches.
func assertAction(t *testing.T, pe ParsedEvent, want Action, msg string) {
	t.Helper()
	if pe.Action != want {
		t.Fatalf("%s: got %s, want %s", msg, pe.Action, want)
	}
}

// task_finish insights carry the detail rows in the "data" field; the parser
// must keep them in the conclusion instead of reducing the result to the
// one-line summary.
func TestParseTaskFinishKeepsDetailData(t *testing.T) {
	content := `[{"title":"销售额TOP5国家","summary":"美国居首","chart_type":"bar",` +
		`"data":"[{\"country\":\"USA\",\"total\":523.06},{\"country\":\"Canada\",\"total\":303.96}]"}]`
	pe := Parse("data", "task_finish", content, "json")
	if pe.Action != ActionConclusion {
		t.Fatalf("action = %v, want conclusion", pe.Action)
	}
	for _, want := range []string{"销售额TOP5国家: 美国居首", "USA", "523.06", "Canada"} {
		if !strings.Contains(pe.Content, want) {
			t.Fatalf("conclusion missing %q:\n%s", want, pe.Content)
		}
	}
}

// Insights without a data field keep the historical summary-only shape.
func TestParseTaskFinishWithoutDataUnchanged(t *testing.T) {
	pe := Parse("data", "task_finish", `[{"title":"t","summary":"s"}]`, "json")
	if pe.Content != "t: s" {
		t.Fatalf("content = %q, want %q", pe.Content, "t: s")
	}
}

// Oversized detail payloads are truncated, not dropped.
func TestParseTaskFinishTruncatesHugeData(t *testing.T) {
	big := strings.Repeat("x", 10000)
	pe := Parse("data", "task_finish", `[{"title":"t","summary":"s","data":"`+big+`"}]`, "json")
	if !strings.Contains(pe.Content, "...(truncated)") {
		t.Fatal("expected truncation marker")
	}
	if len(pe.Content) > 4200 {
		t.Fatalf("conclusion too large: %d bytes", len(pe.Content))
	}
}

// A chart-only insight (data without summary/title) must not be dropped —
// title and summary are both optional in the wire schema (per
// @dmsfe/data-agent-sdk TaskFinishChart: only chart_type/data are present).
func TestParseTaskFinishKeepsChartOnlyInsight(t *testing.T) {
	content := `[{"chart_type":"bar","data":"[{\"c\":\"USA\",\"v\":523.06}]"}]`
	pe := Parse("data", "task_finish", content, "json")
	if pe.Action != ActionConclusion {
		t.Fatalf("action = %v, want conclusion (chart-only insight dropped)", pe.Action)
	}
	for _, want := range []string{"(chart: bar)", "USA", "523.06"} {
		if !strings.Contains(pe.Content, want) {
			t.Fatalf("conclusion missing %q:\n%s", want, pe.Content)
		}
	}
}

// content_type=str means the payload is a markdown report; it must be kept
// verbatim even when it happens to parse as JSON (SDK: str → markdown).
func TestParseTaskFinishStrKeptVerbatim(t *testing.T) {
	md := `[{"title":"looks like json but is markdown"}]`
	pe := Parse("data", "task_finish", md, "str")
	if pe.Content != md {
		t.Fatalf("str content mangled: %q", pe.Content)
	}
}

// output_conclusion events carry {"mission_idx","objective_order","result"};
// the conclusion must expose the result text (trace markers stripped) with a
// dedup key so re-emitted objectives replace instead of duplicate.
func TestParseOutputConclusionExtractsResultAndKey(t *testing.T) {
	content := `{"mission_idx":0,"objective_order":2,"result":" - Rock领先<trace id=\"0-1-3\">，营收826.65</trace>元"}`
	pe := Parse("data", "output_conclusion", content, "json")
	if pe.Action != ActionConclusion {
		t.Fatalf("action = %v", pe.Action)
	}
	if strings.Contains(pe.Content, "<trace") || strings.Contains(pe.Content, "mission_idx") {
		t.Fatalf("content not cleaned: %q", pe.Content)
	}
	if !strings.Contains(pe.Content, "Rock领先") || !strings.Contains(pe.Content, "826.65") {
		t.Fatalf("result text lost: %q", pe.Content)
	}
	if pe.DedupKey != "output_conclusion:0:2" {
		t.Fatalf("dedup key = %q", pe.DedupKey)
	}
}

// file_upload_finish announces a generated artifact; it must be recorded.
func TestParseFileUploadFinish(t *testing.T) {
	pe := Parse("data", "file_upload_finish", "各流派销售明细.xlsx 上传完成", "str")
	if pe.Action != ActionArtifact || len(pe.Artifacts) != 1 || pe.Artifacts[0] != "file:各流派销售明细.xlsx" {
		t.Fatalf("parsed = %+v", pe)
	}
}
