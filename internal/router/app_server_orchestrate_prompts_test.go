package router

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func orchestratePromptUI(t *testing.T) (*appServerUI, *appServerUI) {
	t.Helper()
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"batch-turn"}}}`)
	v := u.navigation.views["child"]
	u.modelsLoading, v.modelsLoading = true, true
	u.draft, v.draft = "Main draft", "Batch draft"
	return u, v
}

func orchestratePromptQuestion(t *testing.T, u *appServerUI, sync bool) {
	t.Helper()
	if sync {
		questionTestMessage(t, u, "batch-question", "item/tool/requestUserInput", map[string]any{
			"threadId": "child", "turnId": "batch-turn", "itemId": "question",
			"questions": []any{map[string]any{"id": "scope", "question": "Which batch scope?", "options": []any{map[string]any{"label": "Narrow", "description": "One path"}}}},
		})
	} else {
		questionTestMessage(t, u, nil, "item/completed", map[string]any{
			"threadId": "child", "turnId": "batch-turn", "item": map[string]any{"id": "question", "type": "agentMessage", "delivery": "async", "questions": []any{map[string]any{"title": "Which batch scope?", "options": []string{"Narrow"}}}},
		})
	}
}

func TestAppServerOrchestrateQuestionRouting(t *testing.T) {
	for _, sync := range []bool{true, false} {
		t.Run(map[bool]string{true: "sync", false: "async"}[sync], func(t *testing.T) {
			u, v := orchestratePromptUI(t)
			w := u.client.Input.(*appServerTestInput)
			orchestratePromptQuestion(t, u, sync)
			if v.questions.active != nil {
				t.Fatal("background question captured a nonempty viewed draft")
			}
			if frame := ansi.Strip(questionTestPaint(t, u, 80)); !strings.Contains(frame, "main › batch asks") {
				t.Fatal("pending banner omitted its source", frame)
			}
			for _, key := range []byte{2, 'q'} {
				if err := u.shell.key(key); err != nil {
					t.Fatal(err)
				}
			}
			questionTestPaint(t, u, 80)
			if u.promptEditor() != v || u.draft != "Main draft" {
				t.Fatal("foreign prompt replaced Main's editor")
			}
			if err := u.shell.key(5); err != nil {
				t.Fatal(err)
			}
			for _, key := range []byte("\x1b[1;2A") {
				if err := u.shell.key(key); err != nil {
					t.Fatal(err)
				}
			}
			if u.view.expansion != 1 || v.view.expansion != 0 || u.reasoningKey == nil || v.reasoningKey != nil {
				t.Fatal("presentation command changed the hidden source")
			}
			for _, key := range []byte("custom answer") {
				if err := u.shell.key(key); err != nil {
					t.Fatal(err)
				}
			}
			u.switchOrchestratedThread("child")
			questionTestPaint(t, v, 80)
			u.switchOrchestratedThread("main")
			if !strings.Contains(questionTestPaint(t, u, 80), "custom answer") {
				t.Fatal("switching lost the answer editor")
			}
			if err := u.shell.key('\r'); err != nil {
				t.Fatal(err)
			}
			if u.draft != "Main draft" || v.draft != "Batch draft" {
				t.Fatal("answer merged into an ordinary draft")
			}
			if sync {
				if !strings.Contains(w.String(), `"id":"batch-question","result":{"answers":{"scope":{"answers":["user_note: custom answer"]}}}`) {
					t.Fatal("wrong synchronous recipient", w.String())
				}
			} else {
				request := btwTestRequest(t, w, "turn/steer", "child")
				if encoded := string(mustMarshalJSON(request.Params)); !strings.Contains(encoded, "request_user_input_async") || strings.Contains(encoded, "Main draft") {
					t.Fatal("async envelope crossed roots", encoded)
				}
				orchestrateTestMessage(t, u, `{"id":`+string(mustMarshalJSON(request.ID))+`,"error":{"code":-1,"message":"rejected"}}`)
				u.openQuestions()
				if v.questions.active == nil || !strings.Contains(questionTestPaint(t, u, 80), "custom answer") {
					t.Fatal("rejected answer did not reopen on the viewed shell")
				}
			}
		})
	}
}

func TestAppServerOrchestratePromptResolution(t *testing.T) {
	u, v := orchestratePromptUI(t)
	u.draft = ""
	orchestratePromptQuestion(t, u, true)
	// Keys arriving before the dock is painted keep their viewed-composer meaning.
	if err := u.shell.key('x'); err != nil {
		t.Fatal(err)
	}
	if u.draft != "x" || v.questions.active != nil {
		t.Fatal("unpainted prompt captured a keystroke")
	}
	u.openQuestions()
	questionTestPaint(t, u, 80)
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "child", "requestId": "batch-question"})
	if u.promptEditor() != u || v.questionCount() != 0 || u.draft != "x" || v.draft != "Batch draft" {
		t.Fatal("external resolution did not restore drafts")
	}
	orchestratePromptQuestion(t, u, false)
	u.openQuestions()
	questionTestPaint(t, u, 80)
	if err := u.shell.key(3); err != nil {
		t.Fatal(err)
	}
	btwTestRequest(t, u.client.Input.(*appServerTestInput), "turn/interrupt", "child")
}

func TestAppServerOrchestrateApprovalOrder(t *testing.T) {
	u, v := orchestratePromptUI(t)
	approvalTestCommand(t, u, "batch-approval", map[string]any{"threadId": "child", "turnId": "batch-turn", "command": "echo batch"})
	approvalTestCommand(t, u, "main-approval", map[string]any{"command": "echo main"})
	u.openApprovals()
	if frame := ansi.Strip(questionTestPaint(t, u, 80)); !strings.Contains(frame, "echo batch") {
		t.Fatal("approval order or label lost", frame)
	}
	for _, key := range []byte("wait for review") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	u.switchOrchestratedThread("child")
	questionTestPaint(t, v, 80)
	u.switchOrchestratedThread("main")
	questionTestPaint(t, u, 80)
	if err := u.shell.key('\r'); err != nil {
		t.Fatal(err)
	}
	w := u.client.Input.(*appServerTestInput)
	if !strings.Contains(w.String(), `"id":"batch-approval","result":{"decision":"decline"}`) {
		t.Fatal("approval used another request ID", w.String())
	}
	if feedback := strings.Join(u.proxy.approvalFeedback[[2]string{"child", "batch-turn"}], "\n"); !strings.Contains(feedback, "wait for review") || len(u.proxy.approvalFeedback) != 1 {
		t.Fatal("denial feedback crossed source turns", feedback)
	}
	if u.promptEditor() != u || !u.approvals.open || v.draft != "Batch draft" {
		t.Fatal("next source did not own its editor")
	}
	questionTestPaint(t, v, 80)
	questionTestMessage(t, u, nil, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
	if len(u.approvals.pending) != 0 || u.draft != "Main draft" {
		t.Fatal("turn cancellation kept a stale foreign approval")
	}
}

func TestAppServerOrchestratePromptPasteResolution(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "question", true: "approval"}[approval], func(t *testing.T) {
			u, v := orchestratePromptUI(t)
			request := "batch-question"
			if approval {
				request = "batch-approval"
				approvalTestCommand(t, u, request, map[string]any{"threadId": "child", "turnId": "batch-turn", "command": "echo batch"})
				u.openApprovals()
			} else {
				orchestratePromptQuestion(t, u, true)
				u.openQuestions()
			}
			questionTestPaint(t, u, 80)
			for _, key := range []byte("\x1b[200~first") {
				if err := u.shell.key(key); err != nil {
					t.Fatal(err)
				}
			}
			questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "child", "requestId": request})
			for _, key := range []byte("tail\x1b[201~") {
				if err := u.shell.key(key); err != nil {
					t.Fatal(err)
				}
			}
			if u.draft != "Main draft" || v.draft != "Batch draft" || v.paste || u.shell.paste || len(v.pasted) != 0 {
				t.Fatal("resolved answer paste migrated into an ordinary editor")
			}
		})
	}
}

func TestAppServerOrchestratePromptEditorReturn(t *testing.T) {
	u, _ := orchestratePromptUI(t)
	orchestratePromptQuestion(t, u, true)
	u.openQuestions()
	var first bytes.Buffer
	if err := u.paint(&first, 100, 36); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", "true")
	file, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	u.promptEditor().openComposerEditor(file, file)
	var resumed bytes.Buffer
	if err := u.paint(&resumed, 100, 36); err != nil {
		t.Fatal(err)
	}
	if rows := strings.Count(resumed.String(), "\x1b[2K"); rows != 36 {
		t.Fatalf("editor return repainted %d/36 rows", rows)
	}
}

func TestAppServerOrchestratePromptErrorDetails(t *testing.T) {
	u, v := orchestratePromptUI(t)
	orchestratePromptQuestion(t, u, true)
	u.openQuestions()
	v.setNotice("Question error\nDetails belong to the batch", true)
	var frame bytes.Buffer
	if err := u.paint(&frame, 100, 36); err != nil {
		t.Fatal(err)
	}
	r := v.noticeDetails
	if r.w == 0 {
		t.Fatal("borrowed error omitted its details control")
	}
	x, y := u.shell.layout.codex.x+r.x, u.shell.layout.codex.y+r.y
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", x+1, y+1)); err != nil {
		t.Fatal(err)
	}
	if u.shell.output == nil || u.shell.output.view != v.view || v.shell.output != nil {
		t.Fatal("source error details opened on the hidden shell")
	}
}

func TestAppServerOrchestrateSecretEditor(t *testing.T) {
	u, v := orchestratePromptUI(t)
	questionTestMessage(t, u, "secret", "item/tool/requestUserInput", map[string]any{
		"threadId": "child", "turnId": "batch-turn", "itemId": "secret",
		"questions": []any{map[string]any{"id": "token", "question": "Secret value?", "isSecret": true}},
	})
	u.openQuestions()
	questionTestPaint(t, u, 80)
	for _, key := range []byte("private answer") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if frame := questionTestPaint(t, u, 80); strings.Contains(frame, "private answer") || !strings.Contains(frame, "•••") {
		t.Fatal("borrowed secret editor was not masked")
	}
	_, _ = u.questionKey("\x1b")
	u.draft = ""
	questionTestAsync(t, u, "new-main-question", "New question")
	if u.promptEditor() != v {
		t.Fatal("new source question did not reopen a hidden pending call")
	}
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "child", "requestId": "secret"})
	if strings.Contains(u.draft, "private answer") || strings.Contains(v.draft, "private answer") {
		t.Fatal("secret resolution copied the answer into a draft")
	}
}

func TestUISnapshotOrchestrationPrompts(t *testing.T) {
	u, v := orchestratePromptUI(t)
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	u.status, v.status = "Ready", "Working"
	u.orchestrateThreads["child"].batch.Branch = "mekugi/batch"
	for _, view := range []*appServerUI{u, v} {
		view.clock = func() time.Time { return now }
		view.view.clock, view.agents.clock = view.clock, view.clock
		agent := view.session.agent("/root")
		agent.Started, agent.LastResponse = now, now
		agent.WorkTimer = activeWorkTimer{Since: now, Known: true}
		view.agents.apply(activityPaneEvent{Kind: "agents", Agents: view.session.agents})
	}
	orchestratePromptQuestion(t, u, true)
	u.openQuestions()
	screen := vt.NewEmulator(100, 36)
	defer screen.Close()
	for _, name := range []string{"question", "approval"} {
		if name == "approval" {
			approvalTestCommand(t, u, "batch-approval", map[string]any{"threadId": "child", "turnId": "batch-turn", "command": "echo batch"})
			u.openApprovals()
		}
		var frame bytes.Buffer
		if err := u.paint(&frame, 100, 36); err != nil {
			t.Fatal(err)
		}
		if _, err := screen.Write(frame.Bytes()); err != nil {
			t.Fatal(err)
		}
		uisnapshot.Assert(t, "testdata/snapshots/orchestration-prompt-"+name+".txt", screen.String()+"\n")
	}
}
