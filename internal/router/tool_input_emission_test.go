package router

import (
	"testing"
)

func emissionTestEvent(t *testing.T, transform *mekugiResponseTransform, event map[string]any) {
	t.Helper()
	if _, err := transform.transformActivitySSE(mustMarshalJSON(event)); err != nil {
		t.Fatal(err)
	}
}

func TestQuestionAnswerWaitsForToolInput(t *testing.T) {
	for _, kind := range []string{"custom_tool_call", "function_call"} {
		t.Run(kind, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.proxy = newManagedMekugiProxy(t)
			u.turn = "turn"
			u.session.cwd = t.TempDir()
			// Installed Codex may omit cwd in provider requests. Native thread
			// identity must still protect the matching UI's question answer.
			transform := &mekugiResponseTransform{proxy: u.proxy, threadID: u.thread}
			defer transform.Close()
			item := map[string]any{"id": "emitting", "type": kind, "name": "unobserved", "call_id": "call", "status": "in_progress"}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": item})
			questionTestAsync(t, u, "question", "Who receives it?")
			u.openQuestions()
			questionTestPaint(t, u, 70)
			appServerTestKeys(t, u, "1\r")
			if requests := appServerTurnRequests(t, wire); len(requests) != 0 || len(u.unsent) != 1 {
				t.Fatalf("answer interrupted tool input: requests=%+v unsent=%+v", requests, u.unsent)
			}
			done := "response.custom_tool_call_input.done"
			if kind == "function_call" {
				done = "response.function_call_arguments.done"
			}
			emissionTestEvent(t, transform, map[string]any{"type": done, "item_id": "emitting"})
			// No item/completed or turn/completed is needed: execution can wait
			// indefinitely after generation finishes.
			if err := u.flushInput(); err != nil {
				t.Fatal(err)
			}
			appServerOneRequest(t, wire, "turn/steer", u.submission.text)
		})
	}
}

func TestToolInputEmissionDoesNotBlockNormalPromptsOrOtherSessions(t *testing.T) {
	for _, tc := range []struct {
		name, workspace, thread string
		answer                  bool
	}{
		{"normal prompt", "", "main", false},
		{"other workspace", "/other", "main", true},
		{"other thread", "", "child", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.proxy = newManagedMekugiProxy(t)
			u.turn = "turn"
			transform := &mekugiResponseTransform{proxy: u.proxy, threadID: tc.thread, directory: tc.workspace}
			defer transform.Close()
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "emitting", "type": "custom_tool_call"}})
			if tc.answer {
				questionTestAsync(t, u, "question", "Who receives it?")
				u.openQuestions()
				questionTestPaint(t, u, 70)
			}
			appServerTestKeys(t, u, "1\r")
			appServerOneRequest(t, wire, "turn/steer", u.submission.text)
		})
	}
}

func TestToolInputEmissionClearsOnItemDoneOrResponseClose(t *testing.T) {
	for _, closeResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "item done", true: "response close"}[closeResponse], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			transform := &mekugiResponseTransform{proxy: proxy, threadID: "main", directory: "/workspace"}
			defer transform.Close()
			item := map[string]any{"id": "emitting", "type": "custom_tool_call", "status": "incomplete"}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": item})
			if !proxy.emittingToolInput("/workspace", "main") {
				t.Fatal("emission was not observed")
			}
			if closeResponse {
				transform.Close()
			} else {
				emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": item})
			}
			if proxy.emittingToolInput("/workspace", "main") {
				t.Fatal("ended response still blocks answers")
			}
		})
	}
}

func TestNormalPromptBypassesDeferredQuestionAnswer(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.proxy = newManagedMekugiProxy(t)
	u.turn = "turn"
	transform := &mekugiResponseTransform{proxy: u.proxy, threadID: u.thread, directory: u.session.cwd}
	defer transform.Close()
	emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "emitting", "type": "custom_tool_call"}})
	questionTestAsync(t, u, "question", "Who receives it?")
	u.openQuestions()
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	appServerTestKeys(t, u, "Stop and change direction\r")
	if requests := appServerTurnRequests(t, wire); len(requests) != 1 || requests[0].text() != "Stop and change direction" {
		t.Fatalf("normal prompt blocked behind deferred answer; queued entries=%d", len(u.unsent))
	}
	if len(u.unsent) != 1 || u.unsent[0].questionCall == nil {
		t.Fatal("normal prompt consumed the deferred answer")
	}
}
func TestRouterLocalToolInputEmission(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	transform := &mekugiResponseTransform{proxy: proxy, threadID: "main", directory: "/workspace", journalActive: true, journalPending: make(map[string]bool)}
	defer transform.Close()
	_, err := transform.transformSSE(mustMarshalJSON(map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "emitting", "type": "function_call", "name": "report_issue"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !proxy.emittingToolInput("/workspace", "main") {
		t.Fatal("report_issue argument generation is invisible to answer deferral")
	}
	_, err = transform.transformSSE(mustMarshalJSON(map[string]any{"type": "response.function_call_arguments.done", "item_id": "emitting", "arguments": "{}"}))
	if err != nil {
		t.Fatal(err)
	}
	if proxy.emittingToolInput("/workspace", "main") {
		t.Fatal("router-local input completion still blocks answers")
	}
}
