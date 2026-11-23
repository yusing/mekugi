package launch

import (
	"encoding/json"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func emissionTestEvent(t *testing.T, transform requestexecution.Prepared, event map[string]any) {
	t.Helper()
	visible, err := transform.TransformSSE(responses.MustMarshalJSON(event))
	if err != nil {
		t.Fatal(err)
	}
	for _, payload := range visible {
		codexresponses.ConfirmDelivery(transform, payload)
	}
}

func TestQuestionAnswerWaitsForToolInput(t *testing.T) {
	for _, kind := range []string{"custom_tool_call", "function_call"} {
		t.Run(kind, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			proxy := newManagedMekugiProxy(t)
			setTestAppServerIntegration(u, proxy)
			u.Turn = "turn"
			u.attached.cwd = t.TempDir()
			// Installed Codex may omit cwd in provider requests. Native thread
			// identity must still protect the matching UI's question answer.
			transform := prepareEmissionTest(t, proxy, "", u.Thread)
			defer transform.Close()
			item := map[string]any{"id": "emitting", "type": kind, "name": "unobserved", "call_id": "call", "status": "in_progress"}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": kind, "name": "unobserved"}})
			delta := "response.custom_tool_call_input.delta"
			if kind == "function_call" {
				delta = "response.function_call_arguments.delta"
			}
			emissionTestEvent(t, transform, map[string]any{"type": delta, "output_index": 0, "item_id": "emitting", "delta": "input"})
			questionTestAsync(t, u, "question", "Who receives it?")
			openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
			questionTestPaint(t, u, 70)
			appServerTestKeys(t, u, "1\r")
			if requests := appServerTurnRequests(t, wire); len(requests) != 0 || len(u.attached.conversation.Unsent) != 1 {
				t.Fatalf("answer interrupted tool input: requests=%+v unsent=%+v", requests, u.attached.conversation.Unsent)
			}
			done := "response.custom_tool_call_input.done"
			if kind == "function_call" {
				done = "response.function_call_arguments.done"
			}
			emissionTestEvent(t, transform, map[string]any{"type": done, "item_id": "emitting"})
			if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
				t.Fatal(err)
			}
			if requests := appServerTurnRequests(t, wire); len(requests) != 0 {
				t.Fatal("argument completion released an undispatched tool item")
			}
			// Neither native execution nor turn completion is needed, but the
			// host must receive the complete item before steering can preempt it.
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": item})
			if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
				t.Fatal(err)
			}
			appServerOneRequest(t, wire, "turn/steer", u.attached.conversation.Submitted[u.Submission().ID].Text)
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
			proxy := newManagedMekugiProxy(t)
			setTestAppServerIntegration(u, proxy)
			u.Turn = "turn"
			workspace := tc.workspace
			if workspace != "" {
				workspace = t.TempDir()
			}
			transform := prepareEmissionTest(t, proxy, workspace, tc.thread)
			defer transform.Close()
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "emitting", "type": "custom_tool_call"}})
			if tc.answer {
				questionTestAsync(t, u, "question", "Who receives it?")
				openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
				questionTestPaint(t, u, 70)
			}
			appServerTestKeys(t, u, "1\r")
			appServerOneRequest(t, wire, "turn/steer", u.attached.conversation.Submitted[u.Submission().ID].Text)
		})
	}
}

func TestNormalPromptBypassesDeferredQuestionAnswer(t *testing.T) {
	u, wire := newAppServerTestUI()
	proxy := newManagedMekugiProxy(t)
	setTestAppServerIntegration(u, proxy)
	u.Turn = "turn"
	transform := prepareEmissionTest(t, proxy, u.attached.cwd, u.Thread)
	defer transform.Close()
	emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "emitting", "type": "custom_tool_call"}})
	questionTestAsync(t, u, "question", "Who receives it?")
	openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 70)
	appServerTestKeys(t, u, "1\r")
	appServerTestKeys(t, u, "Stop and change direction\r")
	if requests := appServerTurnRequests(t, wire); len(requests) != 1 || requests[0].text() != "Stop and change direction" {
		t.Fatalf("normal prompt blocked behind deferred answer; queued entries=%d", len(u.attached.conversation.Unsent))
	}
	if len(u.attached.conversation.Unsent) != 1 || u.attached.conversation.Unsent[0].Question == nil {
		t.Fatal("normal prompt consumed the deferred answer")
	}
}

func prepareEmissionTest(t *testing.T, p *observationFixture, workspace, thread string) requestexecution.Prepared {
	t.Helper()
	request := serverRequest(t, nil)
	metadata := codexresponses.TurnMetadata{RequestKind: "turn", ThreadID: thread}
	if workspace != "" {
		metadata.Directories = map[string]json.RawMessage{workspace: nil}
	}
	prepared, err := p.prepareRequest(t.Context(), &request, "emission", thread, metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestInputBoundaryWaitsForDelivery(t *testing.T) {
	for _, kind := range []string{"function_call", "custom_tool_call"} {
		t.Run(kind, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.Turn = "turn"
			proxy := newManagedMekugiProxy(t)
			setTestAppServerIntegration(u, proxy)
			transform := prepareEmissionTest(t, proxy, "", u.Thread)
			defer transform.Close()
			// The host permits an absent item ID. Completion can acquire an ID
			// without changing the generation's indexed boundary.
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "output_index": 2, "item": map[string]any{"type": kind, "role": "assistant"}})
			questionTestAsync(t, u, "question", "Who receives it?")
			openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
			questionTestPaint(t, u, 70)
			appServerTestKeys(t, u, "1\r")
			if wire.Len() != 0 || len(u.attached.conversation.Unsent) != 1 {
				t.Fatal("index-addressed generation did not hold accepted input")
			}
			visible, err := transform.TransformSSE(responses.MustMarshalJSON(map[string]any{"type": "response.output_item.done", "output_index": 2, "item": map[string]any{"id": "assigned-at-completion", "type": kind, "role": "assistant"}}))
			if err != nil {
				t.Fatal(err)
			}
			flush := func() {
				t.Helper()
				if err := u.nativeSession.flushInput(&u.attached, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
					t.Fatal(err)
				}
			}
			flush()
			if wire.Len() != 0 {
				t.Fatal("transformation released an undelivered boundary")
			}
			for _, payload := range visible {
				codexresponses.ConfirmDelivery(transform, payload)
			}
			flush()
			if requests := appServerTurnRequests(t, wire); len(requests) != 1 || requests[0].Method != "turn/steer" {
				t.Fatalf("delivered boundary did not release exactly one steer: %+v", requests)
			}
		})
	}
}
