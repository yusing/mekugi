package launch

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/testsupport/uisnapshot"
)

func oneoffTestStart(t *testing.T, u *appServerUI, wire *appServerTestInput) {
	t.Helper()
	appServerTestKeys(t, u, "/oneoff inspect the workspace\r")
	var fork map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(wire.Bytes()), &fork); err != nil {
		t.Fatal(err)
	}
	params := fork["params"].(map[string]any)
	for _, key := range []string{"sandbox", "approvalPolicy", "permissions", "developerInstructions"} {
		if _, exists := params[key]; exists {
			t.Fatalf("oneoff overrides inherited %s", key)
		}
	}
	request := btwTestRequest(t, wire, "thread/fork", "main")
	if !request.Params.Ephemeral || !request.Params.ExcludeTurns || !u.attached.presentation.DisplayingSide() || !u.side.OneOff {
		t.Fatal("oneoff did not create and select ephemeral work fork")
	}
	btwTestReply(t, u, request, `{"thread":{"id":"oneoff"}}`)
	if strings.Contains(wire.String(), `"environments"`) {
		t.Fatal("oneoff disabled inherited environments")
	}
	request = btwTestRequest(t, wire, "turn/start", "oneoff")
	if len(request.Params.Input) != 2 || !strings.Contains(request.Params.Input[0].Text, "Perform only requests submitted in this one-off conversation") || strings.TrimSpace(request.Params.Input[1].Text) != "inspect the workspace" {
		t.Fatalf("wrong task input: %+v", request.Params.Input)
	}
	btwTestReply(t, u, request, `{"turn":{"id":"oneoff-turn"}}`)
}

func TestOneoffNativeEventsAndMainIsolation(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "main-turn"
	oneoffTestStart(t, u, wire)
	appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn"}})
	item := map[string]any{"id": "command", "type": "commandExecution", "command": "printf oneoff", "cwd": "/workspace", "status": "inProgress"}
	appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": item})
	appServerTestNotify(t, u, "item/commandExecution/outputDelta", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "command", "delta": "oneoff output\n"})
	item["status"], item["exitCode"], item["aggregatedOutput"] = "completed", 0, "oneoff output\n"
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": item})
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "Isolated result", "phase": "final_answer"}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
	if u.Turn != "main-turn" || u.side.Busy || !u.attached.presentation.DisplayingSide() {
		t.Fatal("oneoff completion affected Main or closed result")
	}
	for _, entry := range u.attached.source.Entries {
		if entry.Source.Native != nil && entry.Source.Native.Thread == "oneoff" {
			t.Fatalf("oneoff leaked into Main: %+v", entry.Source)
		}
	}
	frame := strings.Join(u.sideFrame(100, 24), "\n")
	for _, want := range []string{"One-off", "printf oneoff", "Isolated result"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("missing %q:\n%s", want, frame)
		}
	}
	appServerTestKeys(t, u, "/copy\r")
	if len(u.picker.CopyTexts) == 0 || u.picker.CopyTexts[0] != "Isolated result" {
		t.Fatalf("oneoff copy = %v", u.picker.CopyTexts)
	}
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	selectSideThread(t, u, u.attached.presentation.View().Main)
	appServerTestKeys(t, u, "Main remains independent\r")
	btwTestRequest(t, wire, "turn/steer", "main")
}

func TestOneoffQuestionApprovalAndClose(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	questionTestMessage(t, u, "oneoff-question", "item/tool/requestUserInput", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "ask", "questions": []any{map[string]any{"id": "scope", "header": "Scope", "question": "Which one-off scope?", "options": []any{map[string]any{"label": "Small", "description": "One path"}}}}})
	if u.questions.Count("oneoff") != 1 || u.questions.Count("main") != 0 {
		t.Fatal("question admitted to wrong thread")
	}
	if u.attached.presentation.View().Questions.Count != 1 {
		t.Fatal("oneoff question unavailable in selected view")
	}
	selectSideThread(t, u, u.attached.presentation.View().Main)
	openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
	if !strings.Contains(wire.String(), `"id":"oneoff-question"`) || !strings.Contains(wire.String(), `"Small"`) {
		t.Fatalf("question response lost original identity: %s", wire.String())
	}
	wire.Reset()
	approvalTestCommand(t, u, "oneoff-approval", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "command"})
	if len(u.approvals.calls()) != 1 || u.approvals.calls()[0].Thread != "oneoff" {
		t.Fatal("oneoff approval missing")
	}
	openApprovals(&u.approvals, &u.questions, u.attached.conversation, u.shell, &u.picker, &u.Editor, u.Thread)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "1\r")
	if !strings.Contains(wire.String(), `"id":"oneoff-approval"`) {
		t.Fatalf("approval response lost identity: %s", wire.String())
	}
	wire.Reset()
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	questionTestMessage(t, u, "closing-question", "item/tool/requestUserInput", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "itemId": "closing", "questions": []any{map[string]any{"id": "scope", "header": "Scope", "question": "Close?", "options": []any{map[string]any{"label": "Yes", "description": "Close"}}}}})
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	request := btwTestRequest(t, wire, "turn/interrupt", "oneoff")
	if u.side != nil || u.questions.Count("oneoff") != 0 {
		t.Fatal("closed oneoff retained prompts")
	}
	btwTestReply(t, u, request, `{}`)
	btwTestRequest(t, wire, "thread/unsubscribe", "oneoff")
}

func TestOneoffReplacementCancelAndLateStart(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	oneoffTestStart(t, u, wire)
	appServerTestKeys(t, u, "/oneoff replacement\r")
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if u.picker.sideTask != nil || u.Text != "/oneoff replacement" {
		t.Fatal("dismissal retained clone or lost draft")
	}
	appServerTestKeys(t, u, "\r")
	selectSideThread(t, u, u.attached.presentation.View().Main)
	if u.picker.sideTask != nil {
		t.Fatal("view switch retained replacement clone")
	}
	selectSideThread(t, u, u.attached.presentation.View().SideThread)
	appServerTestKeys(t, u, "\r")
	if wire.Len() != 0 {
		t.Fatal("replacement ran before choice")
	}
	appServerTestKeys(t, u, "\r")
	if u.Text != "/oneoff replacement" || u.side.Thread != "oneoff" {
		t.Fatal("Cancel discarded task or replaced side")
	}
	appServerTestKeys(t, u, "\r\x1b[B\r")
	var requests []btwTestRPC
	for _, line := range bytes.Split(bytes.TrimSpace(wire.Bytes()), []byte{'\n'}) {
		var r btwTestRPC
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, r)
	}
	if len(requests) != 2 || requests[0].Method != "turn/interrupt" || requests[1].Method != "thread/fork" {
		t.Fatalf("replacement requests: %s", wire.String())
	}
	wire.Reset()
	btwTestReply(t, u, requests[1], `{"thread":{"id":"replacement"}}`)
	start := btwTestRequest(t, wire, "turn/start", "replacement")
	if !strings.Contains(fmt.Sprint(start.Params.Input), "replacement") {
		t.Fatal("replacement task lost")
	}
	if err := terminalTest(u).send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 {
		t.Fatal("close during start fabricated turn identity")
	}
	btwTestReply(t, u, start, `{"turn":{"id":"replacement-turn"}}`)
	btwTestRequest(t, wire, "turn/interrupt", "replacement")
}

func TestUISnapshotOneoffCompleted(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
	u.clock = func() time.Time { return now }
	u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
	oneoffTestStart(t, u, wire)
	appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "oneoff", "turnId": "oneoff-turn", "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "Workspace inspected. Main is unchanged.", "phase": "final_answer"}})
	appServerTestNotify(t, u, "turn/completed", map[string]any{"threadId": "oneoff", "turn": map[string]any{"id": "oneoff-turn", "status": "completed"}})
	uisnapshot.Assert(t, "testdata/snapshots/oneoff-completed.txt", strings.Join(u.sideFrame(100, 24), "\n")+"\n")
}

func TestOneoffGuardAdmissionAndFailure(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", "/workspace")
	u.Turn = "main-turn"
	u.guardHookCheck.hash = "guard"
	appServerTestKeys(t, u, "/oneoff guarded task\r")
	fork := btwTestRequest(t, wire, "thread/fork", "main")
	btwTestReply(t, u, fork, `{"thread":{"id":"oneoff"}}`)
	check := appServerOneRequest(t, wire, "hooks/list", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, check.ID))
	if wire.Len() != 0 || u.side.Busy || strings.TrimSpace(u.Text) != "guarded task" || u.Turn != "main-turn" {
		t.Fatalf("guard failure lost task or bypassed check: draft=%q wire=%s", u.Text, wire.String())
	}
	appServerTestKeys(t, u, "\r")
	check = appServerOneRequest(t, wire, "hooks/list", "")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":%s}`, check.ID, guardHookTestResult(t, "/workspace", guardHookTestEntry("guard"))))
	btwTestRequest(t, wire, "turn/start", "oneoff")
}

func TestUISnapshotOneoffSyncQuestionImageCompanion(t *testing.T) {
	for _, child := range []bool{false, true} {
		t.Run(fmt.Sprint("child=", child), func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.startSession("main", "/workspace")
			now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local)
			u.clock = func() time.Time { return now }
			u.shell.Transcript.Clock, u.shell.Agents.Clock = u.clock, u.clock
			oneoffTestStart(t, u, wire)
			thread, turn := "oneoff", "oneoff-turn"
			if child {
				oneoffTestChild(t, u)
				thread, turn = "oneoff-child", "child-turn"
				appServerTestNotify(t, u, "turn/started", map[string]any{"threadId": thread, "turn": map[string]any{"id": turn}})
				wire.Reset()
			}
			questionTestMessage(t, u, "image-question", "item/tool/requestUserInput", map[string]any{"threadId": thread, "turnId": turn, "itemId": "image", "questions": []any{map[string]any{"id": "image", "header": "Image", "question": "Which image?", "isOther": true, "options": []any{map[string]any{"label": "Use image", "description": "Attach image"}}}}})
			selectSideThread(t, u, u.attached.presentation.View().Main)
			openQuestions(&u.attached, u.shell, &u.picker, &u.Editor, &u.questions, &u.approvals, u.Thread)
			questionTestPaint(t, u, 80)
			image := answerImageFixture(t)
			appServerTestKeys(t, u, "\x1b[200~"+image+"\x1b[201~\r")
			lines := bytes.Split(bytes.TrimSpace(wire.Bytes()), []byte{'\n'})
			if len(lines) != 2 {
				t.Fatalf("expected answer and companion, got %s", wire.String())
			}
			var companion struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params struct {
					Thread string           `json:"threadId"`
					Turn   string           `json:"expectedTurnId"`
					Input  []map[string]any `json:"input"`
				} `json:"params"`
			}
			if err := json.Unmarshal(lines[1], &companion); err != nil {
				t.Fatal(err)
			}
			if companion.Method != "turn/steer" || companion.Params.Thread != thread || companion.Params.Turn != turn || len(companion.Params.Input) < 2 || companion.Params.Input[1]["path"] != image {
				t.Fatalf("companion lost scope or image: %s", lines[1])
			}
			wire.Reset()
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, companion.ID))
			if wire.Len() != 0 || len(u.attached.conversation.Editor.Images) != 0 {
				t.Fatal("rejected companion resubmitted or restored to Main")
			}
			if !child {
				if len(u.side.Drafts.Editor.Images) != 1 || len(u.side.Drafts.Queued) != 0 {
					t.Fatal("root companion draft lost")
				}
				return
			}
			if len(u.side.Drafts.Editor.Images) != 0 || len(u.side.Drafts.Queued) != 1 || u.side.Drafts.Queued[0].Question.Thread != thread {
				t.Fatal("descendant companion lost recipient")
			}
			selectSideThread(t, u, u.attached.presentation.View().SideThread)
			uisnapshot.Assert(t, "testdata/snapshots/oneoff-rejected-companion.txt", strings.Join(u.sideFrame(100, 28), "\n")+"\n")
			appServerTestKeys(t, u, "\r")
			retry := btwTestRequest(t, wire, "turn/steer", thread)
			if retry.Params.ExpectedTurnID != turn {
				t.Fatal("retry changed turn")
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected again"}}`, retry.ID))
			if wire.Len() != 0 || len(u.side.Drafts.Queued) != 1 {
				t.Fatal("retry rejection resubmitted or discarded companion")
			}
			closed := u.side
			if err := terminalTest(u).send("\x1b"); err != nil {
				t.Fatal(err)
			}
			if u.side != nil || len(closed.Drafts.Queued) != 0 {
				t.Fatal("close retained retry payload")
			}
			_ = btwTestRequest(t, wire, "turn/interrupt", "oneoff")
		})
	}
}
