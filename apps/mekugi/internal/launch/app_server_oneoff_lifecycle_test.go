package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/codex/binding"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/sessionpreferences/preferences"
)

func oneoffTestChild(t *testing.T, u *appServerUI) {
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "oneoff-child", "parentThreadId": "oneoff", "agentNickname": "worker"}})
}

func TestOneoffChildIsolation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	oneoffTestChild(t, u)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": coordinationTestCall(t, "spawn", "spawn_agent", "oneoff-child", u.attached.session.Caller("oneoff-child"), "ONEOFF_CHILD_TASK")})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff-child", "turnId": "child-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "ONEOFF_CHILD_RESULT", "phase": "final_answer"}})
	for _, e := range u.attached.source.Entries {
		if strings.Contains(e.Source.Text, "ONEOFF_CHILD") || (e.Source.Native != nil && e.Source.Native.Thread == "oneoff") {
			t.Errorf("oneoff descendant leaked to Main: agent=%s kind=%s text=%q native=%+v", e.Source.Agent, e.Source.Kind, e.Source.Text, e.Source.Native)
		}
	}
}

func TestOneoffClosedKnownChild(t *testing.T) {
	for _, closeOrder := range []string{"archive-first", "closed-first", "unexpected"} {
		t.Run(closeOrder, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.startSession("main", "/workspace")
			oneoffTestStart(t, u, wire)
			oneoffTestChild(t, u)
			questionTestMessage(t, u, "child-question", "item/tool/requestUserInput", map[string]any{"threadId": "oneoff-child", "turnId": "child-turn", "itemId": "ask", "questions": []any{map[string]any{"id": "scope", "header": "Scope", "question": "Child scope?", "options": []any{map[string]any{"label": "Small", "description": "One path"}}}}})
			if u.attached.presentation.View().Questions.Count != 1 {
				t.Fatal("descendant question unavailable from one-off view")
			}
			for _, entry := range u.attached.source.Entries {
				if strings.Contains(entry.Source.Text, "Child scope?") {
					t.Fatal("descendant question leaked into Main")
				}
			}
			wire.Reset()
			if closeOrder == "unexpected" {
				appServerTestNotify(t, u, "thread/closed", map[string]any{"threadId": "oneoff"})
				u.sideFrame(120, 24)
				frame := u.attached.presentation.View().Status.State.Text
				if !strings.Contains(frame, "return to Main") || strings.Contains(frame, "/btw starts") || wire.Len() != 0 {
					t.Fatalf("closed side recovery: status=%q frame=\n%s", u.side.Status, frame)
				}
			}
			if err := terminalTest(u).send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if u.side != nil {
				t.Fatal("oneoff did not close")
			}
			if u.questions.Count("oneoff-child") != 0 {
				t.Fatal("closed descendant prompt remained active")
			}
			if closeOrder != "unexpected" {
				interrupt := btwTestRequest(t, wire, "turn/interrupt", "oneoff")
				if closeOrder == "closed-first" {
					appServerTestNotify(t, u, "thread/closed", map[string]any{"threadId": "oneoff"})
				}
				btwTestReply(t, u, interrupt, `{}`)
			}
			archive := btwTestRequest(t, wire, "thread/archive", "oneoff")
			btwTestReply(t, u, archive, `{}`)

			if u.Requests.HasSides() {
				t.Fatal("cleanup retained root or descendant operation mappings")
			}
			appServerTestNotify(t, u, "thread/closed", map[string]any{"threadId": "oneoff-child"})
			questionTestMessage(t, u, "late-child-approval", "item/commandExecution/requestApproval", map[string]any{"threadId": "oneoff-child", "turnId": "child-turn", "itemId": "command", "command": "printf late"})
			if len(u.approvals.calls()) > 0 {
				t.Errorf("approval from known closed oneoff descendant remains active: %+v", u.approvals.calls()[0])
			}
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff-child", "turnId": "child-turn", "item": map[string]any{"id": "late", "type": "agentMessage", "text": "LATE_CHILD_RESULT", "phase": "final_answer"}})
			for _, e := range u.attached.source.Entries {
				if strings.Contains(e.Source.Text, "LATE_CHILD_RESULT") {
					t.Errorf("closed descendant result admitted to Main: %q", e.Source.Text)
				}
			}
		})
	}
}

func TestOneoffUnexpectedClosePreservesDraft(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	appServerTestNotify(t, u, "thread/closed", map[string]any{"threadId": "oneoff"})
	draft := u.side.Drafts.Editor.Text
	appServerTestKeys(t, u, "\r")
	if wire.Len() != 0 || u.side.Drafts.Editor.Text != draft {
		t.Fatal("closed One-off consumed or submitted its restored draft")
	}
}

func TestOneoffUnexpectedCloseRetiresRoot(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	questionTestMessage(t, u, "root-question", "item/tool/requestUserInput", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "ask", "questions": []any{map[string]any{"id": "scope", "header": "Scope", "question": "Root question?", "options": []any{map[string]any{"label": "Yes", "description": "yes"}}}}})
	approvalTestCommand(t, u, "root-approval", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "command"})
	appServerTestNotify(t, u, "thread/closed", map[string]any{"threadId": "oneoff"})
	if err := closeSide(&u.attached, u.shell, &u.Client, &u.Editor, &u.questions, &u.approvals, &u.side, &u.ownedImages); err != nil {
		t.Fatal(err)
	}
	archive := btwTestRequest(t, wire, "thread/archive", "oneoff")
	btwTestReply(t, u, archive, `{}`)
	if u.questions.Count("oneoff") != 0 || len(u.approvals.calls()) != 0 || !u.RetiredThread("oneoff", "") || u.Requests.HasSides() {
		t.Fatal("closed root retained prompts or escaped retirement")
	}
	approvalTestCommand(t, u, "late-root-approval", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "command"})
	if len(u.approvals.calls()) != 0 {
		t.Fatal("retired root admitted a late approval")
	}
}

func TestOneoffAsyncRejectedStart(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "async", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Which?", "questions": []any{map[string]any{"title": "Which?", "options": []string{"Small", "Big"}}}}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
	selectSideThread(t, u, u.attached.presentation.View().Main)
	openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
	request := btwTestRequest(t, wire, "turn/start", "oneoff")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, request.ID))
	c := u.questions.calls()[0]
	if c.Sent || u.questions.Count("oneoff") != 1 {
		t.Errorf("rejected oneoff async answer remains sent/unavailable: sent=%v count=%d state=%+v", c.Sent, u.questions.Count("oneoff"), c.Call)
	}
}

func TestOneoffAsyncCommitIdentity(t *testing.T) {
	for _, idle := range []bool{false, true} {
		t.Run(fmt.Sprint("idle=", idle), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.startSession("main", "/workspace")
			oneoffTestStart(t, u, wire)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "async", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Which?", "questions": []any{map[string]any{"title": "Which?", "options": []string{"Small", "Big"}}}}})
			if idle {
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
			}
			selectSideThread(t, u, u.attached.presentation.View().Main)
			openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
			questionTestPaint(t, u, 80)
			appServerTestKeys(t, u, "1\r")
			method := "turn/steer"
			if idle {
				method = "turn/start"
			}
			_ = btwTestRequest(t, wire, method, "oneoff")
			c := u.questions.calls()[0]
			text := binding.EncodeQuestionReplies([]binding.QuestionReply{{Answer: "Small", QuestionItemID: binding.AsyncQuestionIdentity("async", 0)}})
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "user-answer", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": text}}}})
			if c.Questions[0].Outcome != "answered" {
				t.Errorf("own answer misclassified: outcome=%q submissionID=%q", c.Questions[0].Outcome, c.SubmissionID)
			}
		})
	}
}

func TestOneoffCloseRoster(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "Result", "phase": "final_answer"}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.side != nil {
		t.Fatal("oneoff did not close")
	}
	for _, r := range u.shell.Agents.Roster() {
		if r.Agent.Thread == "oneoff" {
			t.Errorf("closed oneoff survives in rendered roster: %+v", r)
		}
	}
	if u.attached.session.KnownCaller("oneoff") != "" {
		t.Errorf("closed oneoff survives in owning roster")
	}
}

func TestOneoffAnswerDuringToolInput(t *testing.T) {
	u, wire := newAppServerTestUI()
	proxy := newManagedMekugiProxy(t)
	setTestAppServerIntegration(u, proxy)
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "async", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Which?", "questions": []any{map[string]any{"title": "Which?", "options": []string{"Small", "Big"}}}}})
	transform := prepareEmissionTest(t, proxy, "", "oneoff")
	defer transform.Close()
	emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": map[string]any{"id": "emitting", "type": "function_call", "name": "unobserved", "call_id": "call", "status": "in_progress"}})
	selectSideThread(t, u, u.attached.presentation.View().Main)
	openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
	if wire.Len() != 0 || len(u.side.Drafts.Unsent) != 1 {
		t.Fatalf("answer interrupted tool generation: %s", wire.String())
	}
	// Main remains independently usable while this one-off answer is held.
	appServerTestKeys(t, u, "main request\r")
	_ = btwTestRequest(t, wire, "turn/start", "main")
	emissionTestEvent(t, transform, map[string]any{"type": "response.function_call_arguments.done", "item_id": "emitting"})
	if err := flushBTW(&u.attached, u.shell, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, preferences.Applied{Model: u.model, Effort: u.reasoningEffort, Tier: u.serviceTier}, &u.side, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations); err != nil {
		t.Fatal(err)
	}
	_ = btwTestRequest(t, wire, "turn/steer", "oneoff")
	if len(u.side.Drafts.Unsent) != 0 {
		t.Fatal("answered input remained queued")
	}
}

func TestOneoffCloseRunningCommand(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "sleep 10", "status": "inProgress"}})
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	request := btwTestRequest(t, wire, "turn/interrupt", "oneoff")
	btwTestReply(t, u, request, `{}`)
	request = btwTestRequest(t, wire, "thread/archive", "oneoff")
	btwTestReply(t, u, request, `{}`)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "cmd", "type": "commandExecution", "command": "sleep 10", "status": "completed", "exitCode": 0}})
	for e := range u.attached.session.RunningProcesses() {
		if e.Native.Thread == "oneoff" {
			t.Errorf("closed completed command retained as running: %s", e.Native.Command)
		}
	}
}

func oneoffTestAsync(t *testing.T, u *appServerUI, thread, turn, item string) {
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": thread, "turnId": turn, "item": map[string]any{"id": item, "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Which?", "questions": []any{map[string]any{"title": "Which?", "options": []string{"Small", "Big"}}}}})
}
func oneoffTestAnswer(t *testing.T, u *appServerUI) {
	openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
}
func TestOneoffSupplementDrain(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	u.audience = sessioncontrol.NewAudience(nil)
	defer u.audience.Close()
	u.audience.Attach("main")
	u.audience.Observe("oneoff", "", "/root", false)
	u.audience.Collect(activitypresentation.Supplement{Thread: "oneoff", Source: "oneoff-script-failure", Kind: "error", Text: "ONEOFF_EXEC_SCRIPT_FAILED"})
	applyObservedActivity(&u.attached, u.shell, &u.Client, &u.streams, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
	for _, e := range u.attached.childSource.Entries {
		if strings.Contains(e.Source.Text, "ONEOFF_EXEC_SCRIPT_FAILED") {
			return
		}
	}
	t.Fatal("oneoff exec error supplement was never delivered")
}
func TestOneoffIdleReplyHint(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	oneoffTestAsync(t, u, "oneoff", "oneoff-turn", "async")
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
	selectSideThread(t, u, u.attached.presentation.View().Main)
	oneoffTestAnswer(t, u)
	request := btwTestRequest(t, wire, "turn/start", "oneoff")
	if !strings.Contains(fmt.Sprint(request.Params.Input), "Perform only requests submitted in this one-off conversation") {
		t.Fatal("new oneoff turn omitted the per-turn scoped instruction")
	}
}
func TestOneoffChildStartsSerialized(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	oneoffTestChild(t, u)
	wire.Reset()
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "ct"}})
	oneoffTestAsync(t, u, "oneoff-child", "ct", "async1")
	oneoffTestAsync(t, u, "oneoff-child", "ct", "async2")
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "ct", "status": "completed"}})
	wire.Reset()
	selectSideThread(t, u, u.attached.presentation.View().Main)
	oneoffTestAnswer(t, u)
	first := btwTestRequest(t, wire, "turn/start", "oneoff-child")
	oneoffTestAnswer(t, u)
	if strings.Contains(wire.String(), `"method":"turn/start"`) {
		t.Fatalf("second child start sent before first acknowledgement/turn identity: %s", wire.String())
	}
	btwTestReply(t, u, first, `{"turn":{"id":"child-reply"}}`)
	second := btwTestRequest(t, wire, "turn/steer", "oneoff-child")
	if second.Params.ExpectedTurnID != "child-reply" {
		t.Fatalf("queued answer lost settled child identity: %+v", second.Params)
	}
}
func TestOneoffChildGuardFailurePreservesRoot(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	oneoffTestChild(t, u)
	wire.Reset()
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "ct"}})
	oneoffTestAsync(t, u, "oneoff-child", "ct", "async1")
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "ct", "status": "completed"}})
	wire.Reset()
	u.guardHookCheck.hash = "guard"
	selectSideThread(t, u, u.attached.presentation.View().Main)
	oneoffTestAnswer(t, u)
	check := appServerOneRequest(t, wire, "hooks/list", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"child guard unavailable"}}`, check.ID))
	if !u.side.Busy || u.side.Pending.Text == "" || strings.Contains(u.side.Drafts.Editor.Text, "inspect the workspace") {
		t.Fatalf("child guard failure changed running root: busy=%v turn=%q pending=%q restored=%q", u.side.Busy, u.side.Turn, u.side.Pending.Text, u.side.Drafts.Editor.Text)
	}
}
