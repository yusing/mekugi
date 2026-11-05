package launch

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"

	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
)

func emissionTestEvent(t *testing.T, transform requestexecution.Prepared, event map[string]any) {
	t.Helper()
	if _, err := transform.TransformSSE(responses.MustMarshalJSON(event)); err != nil {
		t.Fatal(err)
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
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": item})
			questionTestAsync(t, u, "question", "Who receives it?")
			openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
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
			// No item/completed or turn/completed is needed: execution can wait
			// indefinitely after generation finishes.
			if err := u.nativeSession.flushInput(&u.attached, u.shell, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
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
				openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
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
	openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
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

func TestAssistantMessageDefersInputUntilBoundary(t *testing.T) {
	for _, thread := range []string{"main", "child"} {
		for _, answer := range []bool{false, true} {
			t.Run(thread+map[bool]string{false: "/steer", true: "/answer"}[answer], func(t *testing.T) {
				u, wire := newAppServerTestUI()
				u.Thread, u.Turn = thread, "turn"
				proxy := newManagedMekugiProxy(t)
				setTestAppServerIntegration(u, proxy)
				transform := prepareEmissionTest(t, proxy, "", thread)
				defer transform.Close()
				item := map[string]any{"id": "message", "type": "message", "role": "assistant", "status": "in_progress"}
				emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": item})
				emissionTestEvent(t, transform, map[string]any{"type": "response.output_text.delta", "item_id": "message", "delta": "The latest trace"})
				appServerTestKeys(t, u, "next turn\t")
				if answer {
					questionTestAsync(t, u, "question", "Who receives it?")
					openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
					questionTestPaint(t, u, 70)
					appServerTestKeys(t, u, "1\r")
				} else {
					appServerTestKeys(t, u, "change direction\r")
				}
				if requests := appServerTurnRequests(t, wire); len(requests) != 0 || len(u.attached.conversation.Unsent) != 1 || len(u.attached.conversation.Queued) != 1 {
					t.Fatalf("message interrupted or input lost: requests=%+v", requests)
				}
				// A text part can end before the whole assistant message ends.
				emissionTestEvent(t, transform, map[string]any{"type": "response.output_text.done", "item_id": "message", "text": "The latest trace is complete."})
				flush := func() {
					t.Helper()
					if err := u.nativeSession.flushInput(&u.attached, u.shell, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
						t.Fatal(err)
					}
				}
				flush()
				if requests := appServerTurnRequests(t, wire); len(requests) != 0 {
					t.Fatal("text part completion released the message gate")
				}
				item["status"] = "completed"
				emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": item})
				flush()
				requests := appServerTurnRequests(t, wire)
				if len(requests) != 1 || requests[0].Method != "turn/steer" || len(u.attached.conversation.Queued) != 1 || u.Turn != "turn" {
					t.Fatalf("boundary did not steer independently of turn completion: %+v", requests)
				}
				flush()
				if requests := appServerTurnRequests(t, wire); len(requests) != 0 {
					t.Fatal("input delivered twice")
				}
			})
		}
	}
}

func TestAssistantMessageBoundaryIsNotRearmed(t *testing.T) {
	for _, answer := range []bool{false, true} {
		name := "steer"
		if answer {
			name = "answer"
		}
		t.Run(name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.Turn = "turn"
			proxy := newManagedMekugiProxy(t)
			setTestAppServerIntegration(u, proxy)
			transform := prepareEmissionTest(t, proxy, "", u.Thread)
			defer transform.Close()
			first := map[string]any{"id": "first", "type": "message", "role": "assistant"}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": first})
			if answer {
				questionTestAsync(t, u, "question", "Who receives it?")
				openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
				questionTestPaint(t, u, 70)
				appServerTestKeys(t, u, "1\r")
			} else {
				appServerTestKeys(t, u, "change direction\r")
			}
			if wire.Len() != 0 {
				t.Fatal("setup delivered during first message")
			}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": first})
			// These events can be in the same provider read, with no UI tick between.
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "second", "type": "message", "role": "assistant"}})
			appServerTestKeys(t, u, "later instruction\r")
			requests := appServerTurnRequests(t, wire)
			if len(requests) != 1 {
				t.Fatalf("first message completed, but next message reblocked the accepted %s: requests=%+v unsent=%d", name, requests, len(u.attached.conversation.Unsent))
			}
			if strings.Contains(requests[0].text(), "later instruction") || len(u.attached.conversation.Unsent) != 1 {
				t.Fatal("input waiting for a later boundary joined the earlier batch")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"turn"}}`, requests[0].ID))
			if err := u.nativeSession.flushInput(&u.attached, u.shell, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
				t.Fatal(err)
			}
			if wire.Len() != 0 {
				t.Fatal("later input did not retain its own message boundary")
			}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": map[string]any{"id": "second", "type": "message"}})
			if err := u.nativeSession.flushInput(&u.attached, u.shell, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
				t.Fatal(err)
			}
			appServerOneRequest(t, wire, "turn/steer", "later instruction")
		})
	}
}

func TestExecutionFreeMessagesObserveInputBoundary(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.Turn = "turn"
	proxy := newManagedMekugiProxy(t)
	setTestAppServerIntegration(u, proxy)
	request := serverRequest(t, func(fields map[string]any) {
		fields["tools"] = []any{}
		fields["input"] = []any{map[string]any{"role": "user", "content": "task"}}
	})
	if !codexresponses.ExecutionFree(&request) {
		t.Fatal("fixture is not execution-free")
	}
	prepared, err := proxy.prepareRequest(t.Context(), &request, "session", u.Thread, codexresponses.TurnMetadata{ThreadID: u.Thread, RequestKind: "turn"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil {
		t.Fatal("execution-free turn has no message observer")
	}
	defer prepared.Close()
	if string(request.Fields["tools"]) != "[]" {
		t.Fatal("message observation changed the execution-free tool catalog")
	}
	item := map[string]any{"id": "message", "type": "message", "role": "assistant"}
	emissionTestEvent(t, prepared, map[string]any{"type": "response.output_item.added", "item": item})
	appServerTestKeys(t, u, "change direction\r")
	if wire.Len() != 0 {
		t.Fatal("execution-free assistant message was interrupted")
	}
	emissionTestEvent(t, prepared, map[string]any{"type": "response.output_item.done", "item": item})
	if err := u.nativeSession.flushInput(&u.attached, u.shell, &u.picker, u.clock, &u.Editor, u.ctx); err != nil {
		t.Fatal(err)
	}
	appServerOneRequest(t, wire, "turn/steer", "change direction")
}
