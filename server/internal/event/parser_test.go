package event

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSSEFinish(t *testing.T) {
	r := Parse(EventSSEFinish, "", "", "")
	assertAction(t, r, ActionStreamEnded, "SSE_FINISH -> ActionStreamEnded")
	if r.Action.IsTerminal() {
		t.Fatal("ActionStreamEnded should not be terminal")
	}
	if r.Action.NeedsConfirmation() {
		t.Fatal("ActionStreamEnded should not need confirmation")
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
	cases := []struct {
		name, content, contentType string
	}{
		{"question", "Which database?", "text"},
		{"plan", `{"result_type":"plan","plans":[{"plan":{"steps":[]}}]}`, "json"},
		{"sql", `{"sql":"SELECT 1"}`, "json"},
		{"report", `{"report":"data"}`, "json"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Parse(EventChatFinish, CatAskHuman, tc.content, tc.contentType)
			assertAction(t, r, ActionHumanInput, "chat_finish/ask_human -> ActionHumanInput")
			if r.Content != tc.content {
				t.Fatalf("expected question %q, got %q", tc.content, r.Content)
			}
			if !r.Action.NeedsConfirmation() {
				t.Fatal("ActionHumanInput should need confirmation")
			}
		})
	}
}

func TestChatFinishChat(t *testing.T) {
	r := Parse(EventChatFinish, CatChat, "", "")
	assertAction(t, r, ActionCompleted, "chat_finish/chat -> ActionCompleted")
	if !r.Action.IsTerminal() {
		t.Fatal("ActionCompleted should be terminal")
	}
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
	assertPlanPreview(t, r, tcrPlan, 1)
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

func TestLLMFallback(t *testing.T) {
	approval := `{"action":"approved","reason":"plan confirmed","answers":{}}`
	plan := `<requirement><plan><step>Compute totals</step></plan><question>Revenue?</question></requirement>`
	cases := []struct {
		name, content string
		control       bool
	}{
		{"empty", " \n\t", true},
		{"approval_json", approval, true},
		{"approval_wrapper", "<result>" + approval + "</result>", true},
		{"approval_fence", "```json\n" + approval + "\n```", true},
		{"approval_wrapped_fence", " \n```xml\n<result>" + approval + "</result>\n```\n", true},
		{"approval_inner_fence", "<result>\n```json\n" + approval + "\n```\n</result>", true},
		{"control_schema", `{"action":"revised","reason":"change grouping","answers":{"period":"quarter"}}`, true},
		{"plan", plan, true},
		{"plan_fence", "```xml\n" + plan + "\n```", true},
		{"nested_plan", `<requirement><analysis><plan/></analysis></requirement>`, true},
		{"answer", "  September revenue is 360. Total profit is 405.\n", false},
		{"numeric_json", " {\"value\":9007199254740993.1234567890123456789}\n", false},
		{"numeric_scalar", "405.000000000000000001", false},
		{"numeric_array", `[{"value":360},{"value":405}]`, false},
		{"fenced_json_answer", "```json\n{\"value\":405}\n```", false},
		{"approved_mention", "The approved budget is 405.", false},
		{"arbitrary_result", "<result>Total profit is 405.</result>", false},
		{"result_numeric_json", `<result>{"profit":405}</result>`, false},
		{"unrelated_json", `{"action":"approved","value":405}`, false},
		{"extra_result_field", `{"action":"approved","reason":"budget","answers":{},"value":405}`, false},
		{"numeric_action", `{"action":405,"reason":"budget","answers":{}}`, false},
		{"numeric_reason", `{"action":"approved","reason":405,"answers":{}}`, false},
		{"null_answers", `{"action":"approved","reason":"budget","answers":null}`, false},
		{"array_answers", `{"action":"approved","reason":"budget","answers":[405]}`, false},
		{"missing_answers", `{"action":"approved","reason":"budget"}`, false},
		{"approval_with_answer", "<result>" + approval + "</result>\nTotal profit is 405.", false},
		{"plan_mention", "The <plan> element describes the approved budget of 405.", false},
		{"other_xml_root", `<report><requirement><plan>405</plan></requirement></report>`, false},
		{"requirement_without_plan", `<requirement><value>405</value></requirement>`, false},
		{"escaped_plan", `<requirement>&lt;plan&gt;405&lt;/plan&gt;</requirement>`, false},
		{"invalid_plan_xml", `<requirement><plan>405</requirement>`, false},
		{"plan_with_answer", plan + "\nTotal profit is 405.", false},
		{"plan_with_sibling", plan + "<result>405</result>", false},
	}
	for _, eventType := range []string{EventData, EventContentFinish} {
		for _, tc := range cases {
			t.Run(eventType+"/"+tc.name, func(t *testing.T) {
				pe := Parse(eventType, CatLLM, tc.content, "text")
				wantAction, wantContent := ActionFallback, tc.content
				if tc.control {
					wantAction, wantContent = ActionNone, ""
				}
				assertAction(t, pe, wantAction, "llm fallback")
				if pe.Category != CatLLM || pe.Content != wantContent {
					t.Fatalf("parsed = %+v, want content %q", pe, wantContent)
				}
			})
		}
	}
}

func TestToolCallResponseJupyterFallback(t *testing.T) {
	stdout := `{"output_type":"stream","name":"stdout","text":"metric value\nSeptember revenue 360\nTotal profit 405\n"}`
	plain := `{"output_type":"execute_result","data":{"text/plain":"405.000000000000000001"}}`
	markdown := `{"output_type":"display_data","data":{"text/markdown":["| metric | value |\n","| profit | 405 |\n"],"text/plain":"unformatted"}}`
	executing := `{"output_type":"display_data","metadata":{"content_type":"dms/executing"},"data":{"text/plain":"Executing...","text/markdown":"Running..."}}`
	failure := `{"output_type":"error","ename":"ValueError","evalue":"synthetic failure","traceback":["Traceback: synthetic failure"]}`
	cases := []struct {
		name, cell, want string
	}{
		{"observed_stdout", `{"content_type":"code","cell_id":"cell-example","content":"print(summary.to_string(index=False))","nb_file_outputs":[` + stdout + `],"outputs":[]}`, "metric value\nSeptember revenue 360\nTotal profit 405\n"},
		{"stdout_lines", `{"content_type":"code","nb_file_outputs":[{"output_type":"stream","name":"stdout","text":["metric value\n","profit 405.000000000000000001\n"]}]}`, "metric value\nprofit 405.000000000000000001\n"},
		{"stdout_chunks", `{"content_type":"code","nb_file_outputs":[{"output_type":"stream","name":"stdout","text":"405."},{"output_type":"stream","name":"stdout","text":"000000000000000001\n"}]}`, "405.000000000000000001\n"},
		{"plain_result", `{"content_type":"code","nb_file_outputs":[` + plain + `]}`, "405.000000000000000001"},
		{"plain_display_lines", `{"content_type":"code","outputs":[{"output_type":"display_data","data":{"text/plain":["profit ","405\n"]}}]}`, "profit 405\n"},
		{"markdown_preferred", `{"content_type":"code","nb_file_outputs":[` + markdown + `]}`, "| metric | value |\n| profit | 405 |\n"},
		{"markdown_result", `{"content_type":"code","outputs":[{"output_type":"execute_result","data":{"text/markdown":"**Profit: 405**","text/plain":"405"}}]}`, "**Profit: 405**"},
		{"blank_markdown_uses_plain", `{"content_type":"code","outputs":[{"output_type":"display_data","data":{"text/markdown":[],"text/plain":"405"}}]}`, "405"},
		{"result_order", `{"content_type":"code","outputs":[` + plain + `,` + markdown + `]}`, "405.000000000000000001\n| metric | value |\n| profit | 405 |\n"},
		{"outputs_fallback", `{"content_type":"code","nb_file_outputs":[],"outputs":[` + plain + `]}`, "405.000000000000000001"},
		{"null_nb_outputs", `{"content_type":"code","nb_file_outputs":null,"outputs":[` + plain + `]}`, "405.000000000000000001"},
		{"nb_outputs_preferred", `{"content_type":"code","nb_file_outputs":[` + plain + `],"outputs":[` + markdown + `]}`, "405.000000000000000001"},
		{"executing", `{"content_type":"code","nb_file_outputs":[` + executing + `]}`, ""},
		{"executing_then_result", `{"content_type":"code","nb_file_outputs":[` + executing + `,` + plain + `]}`, "405.000000000000000001"},
		{"placeholder_not_replaced_by_outputs", `{"content_type":"code","nb_file_outputs":[` + executing + `],"outputs":[` + plain + `]}`, ""},
		{"markdown_cell", `{"content_type":"markdown","content":"Compute revenue and profit","nb_file_outputs":[{"output_type":"execute_result","data":{"text/plain":"Markdown content saved successfully"}}]}`, ""},
		{"missing_content_type", `{"nb_file_outputs":[` + stdout + `]}`, ""},
		{"source_only", `{"content_type":"code","content":"print(405)","nb_file_outputs":[],"outputs":[]}`, ""},
		{"missing_outputs", `{"content_type":"code","content":"print(405)"}`, ""},
		{"empty_stdout", `{"content_type":"code","outputs":[{"output_type":"stream","name":"stdout","text":" \n"}]}`, ""},
		{"stderr", `{"content_type":"code","outputs":[{"output_type":"stream","name":"stderr","text":"Traceback: synthetic failure"}]}`, ""},
		{"stderr_and_stdout", `{"content_type":"code","outputs":[{"output_type":"stream","name":"stderr","text":"warning"},` + stdout + `]}`, "metric value\nSeptember revenue 360\nTotal profit 405\n"},
		{"error", `{"content_type":"code","outputs":[` + failure + `]}`, ""},
		{"stdout_then_error", `{"content_type":"code","nb_file_outputs":[` + stdout + `,` + failure + `]}`, ""},
		{"error_then_result", `{"content_type":"code","outputs":[` + failure + `,` + plain + `]}`, ""},
		{"error_in_secondary_outputs", `{"content_type":"code","nb_file_outputs":[` + stdout + `],"outputs":[` + failure + `]}`, ""},
		{"images_only", `{"content_type":"code","outputs":[{"output_type":"display_data","data":{"image/png":"c3ludGhldGlj","image/svg+xml":"<svg/>","text/html":"<img src='example'>"}}]}`, ""},
		{"plain_without_image_data", `{"content_type":"code","outputs":[{"output_type":"display_data","data":{"text/plain":"405","image/png":"c3ludGhldGlj"}}]}`, "405"},
		{"unknown_output_type", `{"content_type":"code","outputs":[{"output_type":"unknown","text":"405","data":{"text/plain":"405"}}]}`, ""},
		{"malformed_text", `{"content_type":"code","outputs":[{"output_type":"stream","name":"stdout","text":["405",42]}]}`, ""},
		{"numeric_text_not_coerced", `{"content_type":"code","outputs":[{"output_type":"execute_result","data":{"text/plain":9007199254740993}}]}`, ""},
		{"malformed_outputs", `{"content_type":"code","outputs":[null,42,"405",{}]}`, ""},
		{"null_result", `null`, ""},
		{"scalar_result", `405`, ""},
	}
	for _, eventType := range []string{EventData, EventContentFinish} {
		for _, stringResult := range []bool{false, true} {
			for _, tc := range cases {
				t.Run(fmt.Sprintf("%s/string=%t/%s", eventType, stringResult, tc.name), func(t *testing.T) {
					var result interface{} = json.RawMessage(tc.cell)
					if stringResult {
						result = tc.cell
					}
					content, err := json.Marshal(map[string]interface{}{"result_type": "jupyter_cell", "result": result})
					if err != nil {
						t.Fatal(err)
					}
					pe := Parse(eventType, CatToolCallResponse, string(content), "json")
					wantAction := ActionFallback
					if tc.want == "" {
						wantAction = ActionNone
					}
					assertAction(t, pe, wantAction, "jupyter fallback")
					if pe.Category != CatToolCallResponse || pe.Content != tc.want {
						t.Fatalf("parsed = %+v, want content %q", pe, tc.want)
					}
				})
			}
		}
	}
}

func TestFallbackOnlyOnCompletedContent(t *testing.T) {
	cases := []struct {
		category, content string
		action            Action
	}{
		{CatLLM, "<result>{\"action\":\"approved\",\"reason\":\"plan confirmed\",\"answers\":{}}</result>", ActionNone},
		{CatLLM, "```xml\n<requirement><plan>Compute totals</plan></requirement>\n```", ActionNone},
		{CatLLM, "Total profit is 405.", ActionFallback},
		{CatToolCallResponse, `{"result_type":"jupyter_cell","result":{"content_type":"code","content":"print(405)","nb_file_outputs":[{"output_type":"stream","name":"stdout","text":"405\n"}]}}`, ActionFallback},
	}
	for _, tc := range cases {
		assertAction(t, Parse(EventContentStart, tc.category, tc.content, "json"), ActionNone, "content_start")
		var accumulated strings.Builder
		for start := 0; start < len(tc.content); start += 7 {
			end := start + 7
			if end > len(tc.content) {
				end = len(tc.content)
			}
			chunk := tc.content[start:end]
			assertAction(t, Parse(EventDelta, tc.category, chunk, "json"), ActionNone, "delta fragment")
			accumulated.WriteString(chunk)
		}
		assertAction(t, Parse(EventContentFinish, tc.category, accumulated.String(), "json"), tc.action, "completed content")
	}
}

func TestFallbackOutputLimit(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"at_limit", strings.Repeat("x", 4096), strings.Repeat("x", 4096)},
		{"ascii", strings.Repeat("x", 5000), strings.Repeat("x", 4096)},
		{"multibyte", strings.Repeat("数", 2000), strings.Repeat("数", 1365)},
		{"four_byte_boundary", strings.Repeat("x", 4095) + "\U00020000", strings.Repeat("x", 4095)},
	}
	for _, tc := range cases {
		for _, category := range []string{CatLLM, CatToolCallResponse} {
			t.Run(tc.name+"/"+category, func(t *testing.T) {
				content := tc.text
				if category == CatToolCallResponse {
					textJSON, err := json.Marshal(tc.text)
					if err != nil {
						t.Fatal(err)
					}
					content = `{"result_type":"jupyter_cell","result":{"content_type":"code","outputs":[{"output_type":"stream","name":"stdout","text":` + string(textJSON) + `}]}}`
				}
				for _, eventType := range []string{EventData, EventContentFinish} {
					pe := Parse(eventType, category, content, "text")
					assertAction(t, pe, ActionFallback, "bounded fallback")
					if pe.Content != tc.want || len(pe.Content) > 4096 || !utf8.ValidString(pe.Content) {
						t.Fatalf("invalid bounded fallback: %d bytes, want %d", len(pe.Content), len(tc.want))
					}
				}
			})
		}
	}
	approval := `{"action":"approved","reason":"` + strings.Repeat("x", 5000) + `","answers":{}}`
	assertAction(t, Parse(EventContentFinish, CatLLM, approval, "json"), ActionNone, "filter before truncation")
	failedCell := `{"result_type":"jupyter_cell","result":{"content_type":"code","outputs":[{"output_type":"stream","name":"stdout","text":"` + strings.Repeat("x", 5000) + `"},{"output_type":"error"}]}}`
	assertAction(t, Parse(EventContentFinish, CatToolCallResponse, failedCell, "json"), ActionNone, "error after large output")
}

func TestDataToolCallResponsePlan(t *testing.T) {
	cases := []struct {
		content string
		steps   int
	}{
		{`{"result_type":"plan","result":"{\"plans\":[{\"plan\":{\"steps\":[{\"order\":1,\"name\":\"Compute totals\"}]}}]}"}`, 1},
		{`{"result_type":"plan","result":{"plans":[{"plan":{"steps":[{"order":1,"name":"Compute totals"}]}}]}}`, 1},
		{`{"result_type":"plan"}`, 0},
		{`{"result_type":"plan","result":"Plan preview"}`, 0},
	}
	for _, tc := range cases {
		assertPlanPreview(t, Parse(EventData, CatToolCallResponse, tc.content, "json"), tc.content, tc.steps)
	}
}

func TestDataAskReportRender(t *testing.T) {
	for _, content := range []string{`{"report":"data"}`, "Report preview", ""} {
		r := Parse(EventData, CatAskReportRender, content, "json")
		assertAction(t, r, ActionNone, "data/ask_report_render -> ActionNone")
		if r.Content != content {
			t.Fatalf("preview content = %q, want %q", r.Content, content)
		}
	}
}

func TestDataAskPlan(t *testing.T) {
	planJSON := `{"plan_id":"xyz","plans":[{"plan":{"steps":[{"order":1,"name":"Only step"}]}}]}`
	r := Parse(EventData, CatAskPlan, planJSON, "json")
	assertPlanPreview(t, r, planJSON, 1)
}

func TestProtocolMatrix(t *testing.T) {
	cases := []struct {
		category, content               string
		data, contentFinish, chatFinish Action
	}{
		{CatAskPlan, `{"plans":[{"plan":{"steps":[{"order":1,"name":"Preview"}]}}]}`, ActionStepProgress, ActionNone, ActionConfirmPlan},
		{CatAskSQL, `{"sql":"SELECT 1"}`, ActionNone, ActionNone, ActionConfirmSQL},
		{CatAskReportRender, `{"report":"preview"}`, ActionNone, ActionNone, ActionConfirmReport},
		{CatAskHuman, `{"result_type":"plan","sql":"SELECT 1"}`, ActionNone, ActionNone, ActionHumanInput},
		{CatToolCallResponse, `{"result_type":"plan","result":{"plans":[{"plan":{"steps":[]}}]}}`, ActionStepProgress, ActionStepProgress, ActionNone},
		{CatLLM, "Total profit is 405.", ActionFallback, ActionFallback, ActionNone},
		{CatPlan, `{"current_step":1,"plans":[{"plan":{"steps":[{"order":1,"name":"Query"}]}}]}`, ActionStepProgress, ActionNone, ActionNone},
		{CatChat, "", ActionNone, ActionNone, ActionCompleted},
		{CatOutputConclusion, `{"mission_idx":0,"objective_order":1,"result":"Conclusion"}`, ActionConclusion, ActionConclusion, ActionNone},
		{"task_finish", `[{"title":"Title","summary":"Summary","data":[{"value":1}]}]`, ActionConclusion, ActionNone, ActionNone},
		{"unknown", `{"result_type":"plan"}`, ActionNone, ActionNone, ActionNone},
	}
	eventTypes := []string{
		EventChatStart, EventContentStart, EventDelta, EventData, EventContentFinish,
		EventStatusChange, EventChatFinish, EventChatCanceled, EventSSEFinish,
		EventSSEFailure, EventSSEVersion, EventHeartbeat, EventStream, "unknown",
	}
	for _, tc := range cases {
		for _, eventType := range eventTypes {
			t.Run(eventType+"/"+tc.category, func(t *testing.T) {
				want := ActionNone
				switch eventType {
				case EventData:
					want = tc.data
				case EventContentFinish:
					want = tc.contentFinish
				case EventChatFinish:
					want = tc.chatFinish
				case EventChatCanceled:
					want = ActionCanceled
				case EventSSEFinish:
					want = ActionStreamEnded
				case EventSSEFailure:
					want = ActionError
				}
				r := Parse(eventType, tc.category, tc.content, "json")
				assertAction(t, r, want, "protocol action")
				if r.Category != tc.category {
					t.Fatalf("category = %q, want %q", r.Category, tc.category)
				}
				isAsk := tc.category == CatAskPlan || tc.category == CatAskSQL ||
					tc.category == CatAskReportRender || tc.category == CatAskHuman
				wantConfirmation := eventType == EventChatFinish && isAsk
				if r.Action.NeedsConfirmation() != wantConfirmation {
					t.Fatalf("NeedsConfirmation() = %v, want %v", r.Action.NeedsConfirmation(), wantConfirmation)
				}
				wantTerminal := eventType == EventSSEFailure || eventType == EventChatCanceled ||
					(eventType == EventChatFinish && tc.category == CatChat)
				if r.Action.IsTerminal() != wantTerminal {
					t.Fatalf("IsTerminal() = %v, want %v", r.Action.IsTerminal(), wantTerminal)
				}
			})
		}
	}
}

func TestPlanPreviewFallbacks(t *testing.T) {
	cases := []struct {
		name, eventType, category, content string
	}{
		{"plan_text", EventData, CatAskPlan, "Plan preview"},
		{"plan_empty", EventData, CatAskPlan, ""},
		{"plan_invalid_json", EventData, CatAskPlan, `{"plans":`},
		{"plan_null", EventData, CatAskPlan, "null"},
		{"plan_no_steps", EventData, CatAskPlan, `{"plan_id":"preview"}`},
		{"tool_result_text", EventContentFinish, CatToolCallResponse, `{"result_type":"plan","result":"Plan preview"}`},
		{"tool_result_null", EventContentFinish, CatToolCallResponse, `{"result_type":"plan","result":null}`},
		{"tool_result_number", EventContentFinish, CatToolCallResponse, `{"result_type":"plan","result":1}`},
		{"tool_result_no_steps", EventContentFinish, CatToolCallResponse, `{"result_type":"plan","result":{"plan_id":"preview"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Parse(tc.eventType, tc.category, tc.content, "json")
			assertPlanPreview(t, r, tc.content, 0)
			if tc.category == CatAskPlan {
				confirmation := Parse(EventChatFinish, tc.category, tc.content, "json")
				assertAction(t, confirmation, ActionConfirmPlan, "chat_finish/ask_plan fallback")
				if confirmation.Content != tc.content {
					t.Fatalf("confirmation content = %q, want %q", confirmation.Content, tc.content)
				}
			}
		})
	}
}

func TestActionValuesStable(t *testing.T) {
	actions := []Action{
		ActionNone, ActionConfirmPlan, ActionConfirmSQL, ActionConfirmReport,
		ActionHumanInput, ActionStepProgress, ActionConclusion, ActionCompleted,
		ActionError, ActionCanceled, ActionRecommendedQuestion, ActionReportGenerated,
		ActionArtifact, ActionStreamEnded, ActionFallback,
	}
	for want, action := range actions {
		if int(action) != want {
			t.Fatalf("%s = %d, want %d", action, action, want)
		}
	}
}

func TestActionString(t *testing.T) {
	cases := map[Action]string{
		ActionNone:                "none",
		ActionConfirmPlan:         "confirm_plan",
		ActionConfirmSQL:          "confirm_sql",
		ActionConfirmReport:       "confirm_report",
		ActionHumanInput:          "human_input",
		ActionStepProgress:        "step_progress",
		ActionConclusion:          "conclusion",
		ActionCompleted:           "completed",
		ActionError:               "error",
		ActionCanceled:            "canceled",
		ActionRecommendedQuestion: "recommended_question",
		ActionReportGenerated:     "report_generated",
		ActionArtifact:            "artifact",
		ActionStreamEnded:         "stream_ended",
		ActionFallback:            "fallback",
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
	assertPlanPreview(t, r, tcrPlan, 2)
}

func TestToolCallResponsePlanNoResult(t *testing.T) {
	tcrPlan := `{"result_type":"plan"}`
	r := Parse(EventContentFinish, CatToolCallResponse, tcrPlan, "json")
	assertPlanPreview(t, r, tcrPlan, 0)
}

func TestNeedsConfirmationExhaustive(t *testing.T) {
	confirm := []Action{ActionConfirmPlan, ActionConfirmSQL, ActionConfirmReport, ActionHumanInput}
	noConfirm := []Action{ActionNone, ActionStepProgress, ActionConclusion, ActionCompleted, ActionError, ActionCanceled,
		ActionRecommendedQuestion, ActionReportGenerated, ActionArtifact, ActionStreamEnded, ActionFallback}

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
	nonTerminal := []Action{ActionNone, ActionConfirmPlan, ActionConfirmSQL, ActionConfirmReport, ActionHumanInput, ActionStepProgress, ActionConclusion,
		ActionRecommendedQuestion, ActionReportGenerated, ActionArtifact, ActionStreamEnded, ActionFallback}

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

func assertPlanPreview(t *testing.T, pe ParsedEvent, content string, stepTotal int) {
	t.Helper()
	assertAction(t, pe, ActionStepProgress, "plan preview")
	if pe.Action.NeedsConfirmation() || pe.Action.IsTerminal() {
		t.Fatal("plan preview must not confirm or terminate the turn")
	}
	if pe.Content != content {
		t.Fatalf("preview content = %q, want %q", pe.Content, content)
	}
	if pe.StepTotal != stepTotal {
		t.Fatalf("preview step total = %d, want %d", pe.StepTotal, stepTotal)
	}
	var wantRaw map[string]interface{}
	if err := json.Unmarshal([]byte(content), &wantRaw); err != nil {
		wantRaw = nil
	}
	if !reflect.DeepEqual(pe.RawData, wantRaw) {
		t.Fatalf("preview raw data = %#v, want %#v", pe.RawData, wantRaw)
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
