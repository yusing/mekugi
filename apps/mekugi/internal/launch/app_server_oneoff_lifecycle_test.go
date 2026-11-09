package launch

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/apps/mekugi/internal/terminalsession"
	"github.com/yusing/mekugi/backends/codex/binding"
	activitypresentation "github.com/yusing/mekugi/modules/activity/presentation"
)

func oneoffTestChild(t *testing.T, u *appServerUI) {
	appServerTestNotify(t, u, "thread/started", map[string]any{"thread": map[string]any{"id": "oneoff-child", "parentThreadId": "oneoff", "agentNickname": "worker"}})
}

func TestOneoffEscapeInterruptRetainsConversation(t *testing.T) {
	for _, order := range []string{"ack-first", "completion-first", "rejected", "close-before-ack", "close-after-ack"} {
		t.Run(order, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.startSession("main", "/workspace")
			u.Turn = "main-turn"
			oneoffTestStart(t, u, wire)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "Retained partial result", "phase": "commentary"}})
			appServerTestKeys(t, u, "keep this draft")
			original := u.side
			// Exercise the actual native renderer's key-to-Session-API operation.
			c := nativeTestPromptClient(u)
			defer c.renderer.terminal.Diff.Close()
			c.dispatch(terminalsession.Input{Kind: terminalsession.KeyInput, Text: "\x1b"})
			if len(c.calls) != 1 || c.calls[0].operation.Interrupt == nil || c.calls[0].operation.Side != nil {
				t.Fatalf("One-off Escape did not issue semantic interruption: %+v", c.calls)
			}
			intent := *c.calls[0].operation.Interrupt
			interrupt := func() {
				t.Helper()
				if err := u.nativeSession.interruptSession(&u.attached, &u.picker, &u.Editor, u.clock, &u.side, false, intent); err != nil {
					t.Fatal(err)
				}
			}
			interrupt()
			r := btwTestRequest(t, wire, "turn/interrupt", "oneoff")
			if r.Params.TurnID != "oneoff-turn" {
				t.Fatalf("interrupted wrong turn: %+v", r)
			}
			interrupt()
			if wire.Len() != 0 || u.side != original || original.Closed || u.Text != "keep this draft" || u.Turn != "main-turn" {
				t.Fatal("interruption repeated, closed One-off, lost text, or affected Main")
			}
			if order == "rejected" {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"message":"not now"}}`, r.ID))
				if original.Interruption.Target != "" || original.Turn != "oneoff-turn" || !original.Busy || wire.Len() != 0 {
					t.Fatal("rejection ended work or archived the conversation")
				}
				if err := terminalTest(u).send("\x1b"); err != nil {
					t.Fatal(err)
				}
				_ = btwTestRequest(t, wire, "turn/interrupt", "oneoff")
				return
			}
			if order == "close-before-ack" {
				sideTestClose(t, u)
				if u.side != nil || wire.Len() != 0 {
					t.Fatal("explicit close failed or duplicated pending interruption")
				}
				btwTestReply(t, u, r, `{}`)
				_ = btwTestRequest(t, wire, "thread/archive", "oneoff")
				return
			}
			if order == "close-after-ack" {
				btwTestReply(t, u, r, `{}`)
				sideTestClose(t, u)
				if u.side != nil || wire.Len() != 0 {
					t.Fatal("explicit close failed or duplicated acknowledged interruption")
				}
				appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "interrupted"}})
				_ = btwTestRequest(t, wire, "thread/archive", "oneoff")
				return
			}
			if order == "ack-first" {
				btwTestReply(t, u, r, `{}`)
				if !original.Busy || original.Turn != "oneoff-turn" || wire.Len() != 0 {
					t.Fatal("interrupt acknowledgement completed or archived work")
				}
			}
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "interrupted"}})
			frame := strings.Join(u.sideFrame(100, 24), "\n")
			for _, text := range []string{"One-off", "Retained partial result", "keep this draft", "interrupted"} {
				if !strings.Contains(frame, text) {
					t.Fatalf("interrupted frame lost %q:\n%s", text, frame)
				}
			}
			if original.Busy || original.Turn != "" || u.side != original || wire.Len() != 0 {
				t.Fatal("completion did not preserve idle One-off")
			}
			appServerTestKeys(t, u, "\r")
			start := btwTestRequest(t, wire, "turn/start", "oneoff")
			btwTestReply(t, u, start, `{"turn":{"id":"followup"}}`)
			if order == "completion-first" {
				btwTestReply(t, u, r, `{}`)
			}
			interrupt() // Delayed original intent must not interrupt the successor.
			if original.Turn != "followup" || wire.Len() != 0 || original.Closed {
				t.Fatal("late interruption crossed into a successor turn")
			}
		})
	}
}

func TestOneoffEscapeDuringAdmission(t *testing.T) {
	for _, stage := range []string{"fork", "start", "fork-error", "start-error"} {
		t.Run(stage, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			appServerTestKeys(t, u, "/oneoff preserve task\r")
			pending := btwTestRequest(t, wire, "thread/fork", "main")
			if strings.HasPrefix(stage, "start") {
				btwTestReply(t, u, pending, `{"thread":{"id":"oneoff"}}`)
				pending = btwTestRequest(t, wire, "turn/start", "oneoff")
			}
			appServerTestKeys(t, u, "later draft")
			if err := terminalTest(u).send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if u.side == nil || !u.side.Interruption.BeforeStart || wire.Len() != 0 {
				t.Fatal("pending admission closed or interrupted before identity arrived")
			}
			if strings.HasSuffix(stage, "-error") {
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"message":"rejected admission"}}`, pending.ID))
				if u.side.Interruption.BeforeStart || u.side.Interruption.Target != "" || u.side.Busy || wire.Len() != 0 || !strings.Contains(u.Text, "preserve task") || !strings.Contains(u.Text, "later draft") {
					t.Fatal("rejected admission left interruption pending or lost retry text")
				}
				return
			}
			if stage == "fork" {
				btwTestReply(t, u, pending, `{"thread":{"id":"oneoff"}}`)
				if wire.Len() != 0 || u.side.Busy || !strings.Contains(u.Text, "preserve task") || !strings.Contains(u.Text, "later draft") {
					t.Fatal("fork cancellation started the task or lost draft text")
				}
			} else {
				btwTestReply(t, u, pending, `{"turn":{"id":"pending-turn"}}`)
				request := btwTestRequest(t, wire, "turn/interrupt", "oneoff")
				if request.Params.TurnID != "pending-turn" || u.Text != "later draft" {
					t.Fatal("late start cancellation changed identity or text")
				}
			}
		})
	}
}

func TestOneoffEscapePendingReplyFailure(t *testing.T) {
	for _, failure := range []string{"rejected", "missing-turn", "malformed-turn"} {
		t.Run(failure, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.startSession("main", "/workspace")
			oneoffTestStart(t, u, wire)
			oneoffTestAsync(t, u, "oneoff", "oneoff-turn", "followup-question")
			appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
			selectSideThread(t, u, u.attached.presentation.View().Main)
			oneoffTestAnswer(t, u)
			start := btwTestRequest(t, wire, "turn/start", "oneoff")
			if !strings.Contains(fmt.Sprint(start.Params.Input), "Small") {
				t.Fatal("follow-up answer did not reach its own pending start")
			}
			selectSideThread(t, u, u.attached.presentation.View().SideThread)
			appServerTestKeys(t, u, "newer draft")
			if err := terminalTest(u).send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if !u.side.Interruption.BeforeStart || wire.Len() != 0 {
				t.Fatal("Escape did not defer cancellation of the pending reply")
			}
			wantNotice := "Codex returned no one-off reply turn identity"
			switch failure {
			case "rejected":
				wantNotice = "reply rejected"
				appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"message":"reply rejected"}}`, start.ID))
			case "missing-turn":
				btwTestReply(t, u, start, `{}`)
			case "malformed-turn":
				btwTestReply(t, u, start, `{"turn":{"id":7}}`)
			}
			if u.side.Interruption.BeforeStart || u.side.Interruption.Target != "" || u.side.Starting || u.side.Busy {
				t.Fatalf("failed follow-up retained interruption/admission state: %+v", u.side)
			}
			if wire.Len() != 0 || u.side.Closed || u.side.Drafts.Editor.Text != "newer draft" {
				t.Fatal("failed follow-up resubmitted, closed One-off, or lost the newer draft")
			}
			if !u.attached.feedback.Alert || !strings.Contains(u.attached.feedback.Text, wantNotice) {
				t.Fatalf("failed follow-up lost error feedback: %+v", u.attached.feedback)
			}
			call := u.questions.calls()[0]
			if call.Sent || u.questions.Count("oneoff") != 1 || !strings.Contains(strings.Join(call.Questions[0].Answer, "\n"), "Small") {
				t.Fatal("failed follow-up lost its rejected answer or retry admission")
			}
			selectSideThread(t, u, u.attached.presentation.View().Main)
			oneoffTestAnswer(t, u)
			_ = btwTestRequest(t, wire, "turn/start", "oneoff")
		})
	}
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
			sideTestClose(t, u)
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
	if err := closeSide(&u.attached, &u.Client, &u.Editor, &u.questions, &u.approvals, &u.side, &u.ownedImages); err != nil {
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
	openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
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
			openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
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
	openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
	if wire.Len() != 0 || len(u.side.Drafts.Unsent) != 1 {
		t.Fatalf("answer interrupted tool generation: %s", wire.String())
	}
	// Main remains independently usable while this one-off answer is held.
	appServerTestKeys(t, u, "main request\r")
	_ = btwTestRequest(t, wire, "turn/start", "main")
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	appServerTestKeys(t, u, "stop this approach\r")
	steer := btwTestRequest(t, wire, "turn/steer", "oneoff")
	if len(steer.Params.Input) != 1 || steer.Params.Input[0].Text != "stop this approach" || len(u.side.Drafts.Unsent) != 1 || u.side.Drafts.Unsent[0].Question == nil {
		t.Fatalf("ordinary steering did not pass the waiting companion: %+v", steer)
	}
	emissionTestEvent(t, transform, map[string]any{"type": "response.function_call_arguments.done", "item_id": "emitting"})
	emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": map[string]any{"id": "emitting", "type": "function_call"}})
	if err := flushBTW(&u.attached, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.settings, &u.side, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations); err != nil {
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
	sideTestClose(t, u)
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
	openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
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
	applyObservedActivity(&u.attached, &u.Client, u.usage, u.review, &u.approvals, &u.pendingEditReceipts, u.clock, u.restoring, u.audience)
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

func TestOneoffInputWaitsForItsAssistantMessage(t *testing.T) {
	for _, answer := range []bool{false, true} {
		t.Run(map[bool]string{false: "steer", true: "answer"}[answer], func(t *testing.T) {
			u, wire := newAppServerTestUI()
			proxy := newManagedMekugiProxy(t)
			setTestAppServerIntegration(u, proxy)
			u.startSession("main", "/workspace")
			oneoffTestStart(t, u, wire)
			transform := prepareEmissionTest(t, proxy, "", "oneoff")
			defer transform.Close()
			item := map[string]any{"id": "message", "type": "message"}
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message"}})
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_text.delta", "output_index": 0, "item_id": "message", "delta": "Working"})
			if answer {
				appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "async", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Which?", "questions": []any{map[string]any{"title": "Which?", "options": []string{"Small", "Big"}}}}})
				selectSideThread(t, u, u.attached.presentation.View().Main)
				openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
				questionTestPaint(t, u, 80)
				appServerTestKeys(t, u, "1\r")
			} else {
				appServerTestKeys(t, u, "change approach\r")
			}
			if wire.Len() != 0 || len(u.side.Drafts.Unsent) != 1 {
				t.Fatal("oneoff input interrupted its message")
			}
			selectSideThread(t, u, u.attached.presentation.View().Main)
			appServerTestKeys(t, u, "main request\r")
			_ = btwTestRequest(t, wire, "turn/start", "main")
			emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "item": item})
			if err := flushBTW(&u.attached, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.settings, &u.side, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations); err != nil {
				t.Fatal(err)
			}
			_ = btwTestRequest(t, wire, "turn/steer", "oneoff")
			if len(u.side.Drafts.Unsent) != 0 {
				t.Fatal("oneoff input remained queued after its boundary")
			}
		})
	}
}

func TestOneoffSteerPassesChildAnswerWaitingForMessage(t *testing.T) {
	u, wire := newAppServerTestUI()
	proxy := newManagedMekugiProxy(t)
	setTestAppServerIntegration(u, proxy)
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	oneoffTestChild(t, u)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "oneoff-child", "turn": map[string]any{"id": "child-turn"}})
	transform := prepareEmissionTest(t, proxy, "", "oneoff-child")
	defer transform.Close()
	item := map[string]any{"id": "child-message", "type": "message"}
	emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.added", "item": item})
	emissionTestEvent(t, transform, map[string]any{"type": "response.output_text.delta", "output_index": 0, "item_id": "child-message", "delta": "Working"})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff-child", "turnId": "child-turn", "item": map[string]any{"id": "async", "type": "agentMessage", "delivery": "async", "phase": "finalAnswer", "text": "Which?", "questions": []any{map[string]any{"title": "Which?", "options": []string{"Small", "Big"}}}}})
	selectSideThread(t, u, u.attached.presentation.View().Main)
	openQuestions(&u.attached, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
	if wire.Len() != 0 || len(u.side.Drafts.Unsent) != 1 {
		t.Fatal("child answer was not deferred")
	}
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	appServerTestKeys(t, u, "change parent direction\r")
	_ = btwTestRequest(t, wire, "turn/steer", "oneoff")
	if len(u.side.Drafts.Unsent) != 1 || u.side.Drafts.Unsent[0].Question.Thread != "oneoff-child" {
		t.Fatal("parent steer consumed the child answer")
	}
	emissionTestEvent(t, transform, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "message"}})
	if err := flushBTW(&u.attached, &u.Client, &u.picker, &u.questions, &u.approvals, u.clock, u.settings, &u.side, &u.ownedImages, u.skillEnvironment, &u.guardHookCheck, u.controlObservations); err != nil {
		t.Fatal(err)
	}
	_ = btwTestRequest(t, wire, "turn/steer", "oneoff-child")
}
