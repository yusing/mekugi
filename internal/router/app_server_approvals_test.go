package router

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/coder/websocket"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/uisnapshot"
	"github.com/yusing/mekugi/internal/vcsguard"
)

func approvalTestCommand(t *testing.T, u *appServerUI, id any, params map[string]any) {
	t.Helper()
	if u.proxy == nil {
		u.proxy = &mekugiProxy{}
	}
	request := map[string]any{"threadId": "main", "turnId": "turn", "itemId": "cmd", "command": "git push origin main"}
	for key, value := range params {
		request[key] = value
	}
	questionTestMessage(t, u, id, "item/commandExecution/requestApproval", request)
}

func approvalTestChoices(u *appServerUI) []string {
	if len(u.approvals.pending) == 0 {
		return nil
	}
	var labels []string
	for _, choice := range u.approvals.pending[0].choices {
		labels = append(labels, choice.label)
	}
	return labels
}

func approvalTestTranscript(t *testing.T, u *appServerUI) string {
	t.Helper()
	var rows []string
	for _, entry := range u.view.entries {
		if entry.Agent == "Session" {
			t.Fatal("approval added a Session entry")
		}
		for _, block := range entry.blocks {
			rows = append(rows, u.view.painter.Block(block, 100)...)
		}
	}
	return ansi.Strip(strings.Join(rows, "\n"))
}

func TestNativeApprovalCommandReturnsTheOfferedDecision(t *testing.T) {
	u, input := newAppServerTestUI()
	u.turn = "turn"
	approvalTestCommand(t, u, 7, map[string]any{
		"reason": "needs the remote",
		"availableDecisions": []any{"accept", "acceptForSession",
			map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": []string{"git", "push"}}}, "cancel"},
	})
	if !u.approvals.open || !slices.Equal(approvalTestChoices(u), []string{
		"Yes, proceed",
		"Yes, and don't ask again for this command in this session",
		"Yes, and don't ask again for commands that start with `git push`",
		"No, continue without running it",
	}) {
		t.Fatalf("open=%v choices=%q", u.approvals.open, approvalTestChoices(u))
	}
	paint := ansi.Strip(questionTestPaint(t, u, 80))
	for _, want := range []string{"Run this command?", "git push origin main", "Reason: needs the remote", "› 1. Yes, proceed", "enter confirm"} {
		if !strings.Contains(paint, want) {
			t.Fatalf("dock lacks %q:\n%s", want, paint)
		}
	}
	appServerTestKeys(t, u, "3\r")
	if got := input.String(); !strings.Contains(got, `"id":7`) || !strings.Contains(got, `"decision":{"acceptWithExecpolicyAmendment":{"execpolicy_amendment":["git","push"]}}`) {
		t.Fatalf("response = %s", got)
	}
	if len(u.approvals.pending) != 0 || u.approvals.open {
		t.Fatal("answered approval remains")
	}
	if got := approvalTestTranscript(t, u); !strings.Contains(got, "git push origin main · Approved") || len(u.view.entries) != 1 {
		t.Fatalf("transcript = %q", got)
	}
}

// Without availableDecisions, Codex offers accept, the proposed amendment,
// and cancel.
func TestNativeApprovalCommandDefaultDecisions(t *testing.T) {
	u, input := newAppServerTestUI()
	u.turn = "turn"
	approvalTestCommand(t, u, "default", map[string]any{"proposedExecpolicyAmendment": []string{"git", "push"}})
	if len(approvalTestChoices(u)) != 3 {
		t.Fatalf("choices = %q", approvalTestChoices(u))
	}
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "\x0e\x0e\r")
	if !strings.Contains(input.String(), `"decision":"decline"`) {
		t.Fatalf("response = %s", input.String())
	}
}

func TestNativeApprovalTerminalInput(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.turn = "turn"
	approvalTestCommand(t, u, 12, map[string]any{"kind": "writeStdin", "command": "write_stdin --session-id 7 'y\n'"})
	if a := u.approvals.pending[0]; a.title != "Send input to terminal 7?" || a.subject != `Input: "y\n"` {
		t.Fatalf("stdin approval = %q %q", a.title, a.subject)
	}
}

func TestNativeApprovalFileChangeAndPermissions(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/work")
	input := u.client.Input.(*appServerTestInput)
	u.turn = "turn"
	questionTestMessage(t, u, nil, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": map[string]any{
		"id": "patch", "type": "fileChange", "changes": []any{map[string]any{"path": "/work/a.go", "kind": map[string]any{"type": "update"}, "diff": ""}},
	}})
	questionTestMessage(t, u, 8, "item/fileChange/requestApproval", map[string]any{"threadId": "main", "turnId": "turn", "itemId": "patch"})
	if a := u.approvals.pending[0]; a.title != "Make these edits?" || a.subject != "a.go" {
		t.Fatalf("edit approval = %q %q", a.title, a.subject)
	}
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "2\r")
	if !strings.Contains(input.String(), `"decision":"acceptForSession"`) {
		t.Fatalf("edit response = %s", input.String())
	}
	input.Reset()
	permissions := map[string]any{"network": map[string]any{"enabled": true}, "fileSystem": map[string]any{"write": []string{"/tmp/out"}}}
	questionTestMessage(t, u, 9, "item/permissions/requestApproval", map[string]any{"threadId": "main", "turnId": "turn", "itemId": "perm", "cwd": "/work", "permissions": permissions})
	if a := u.approvals.pending[0]; a.subject != "network; write /tmp/out" {
		t.Fatalf("permission subject = %q", a.subject)
	}
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "3\r")
	if got := input.String(); !strings.Contains(got, `"scope":"session"`) || !strings.Contains(got, `"write":["/tmp/out"]`) {
		t.Fatalf("permission response = %s", got)
	}
	input.Reset()
	questionTestMessage(t, u, 10, "item/permissions/requestApproval", map[string]any{"threadId": "main", "turnId": "turn", "itemId": "perm", "cwd": "/work", "permissions": permissions})
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "4\r")
	if got := input.String(); !strings.Contains(got, `"permissions":{}`) || !strings.Contains(got, `"scope":"turn"`) {
		t.Fatalf("permission denial = %s", got)
	}
}

func TestNativeApprovalEndsWhenResolvedElsewhere(t *testing.T) {
	u, input := newAppServerTestUI()
	u.turn = "turn"
	approvalTestCommand(t, u, "stale", nil)
	approvalTestCommand(t, u, "ended", nil)
	if !strings.Contains(ansi.Strip(questionTestPaint(t, u, 80)), "1 of 2") {
		t.Fatal("dock lacks the queue position")
	}
	questionTestMessage(t, u, nil, "serverRequest/resolved", map[string]any{"threadId": "main", "requestId": "stale"})
	questionTestMessage(t, u, nil, "turn/completed", map[string]any{"threadId": "main", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
	if len(u.approvals.pending) != 0 || u.approvals.open {
		t.Fatal("ended approvals remain")
	}
	if strings.Contains(input.String(), `"decision"`) {
		t.Fatalf("ended approval was answered: %s", input.String())
	}
	if got := approvalTestTranscript(t, u); strings.Contains(got, "Pending Approval") || len(u.view.entries) != 1 || u.view.entries[0].native.approval != "Turn ended before an answer" {
		t.Fatalf("transcript = %q", got)
	}
}

// An approval arriving mid-typing waits behind a banner; hiding it keeps the
// draft, and reopening shows it again.
func TestNativeApprovalWaitsForAnEmptyComposer(t *testing.T) {
	u, input := newAppServerTestUI()
	u.turn = "turn"
	appServerTestKeys(t, u, "draft")
	approvalTestCommand(t, u, 11, nil)
	if u.approvals.open {
		t.Fatal("approval took over a draft")
	}
	if paint := ansi.Strip(questionTestPaint(t, u, 80)); !strings.Contains(paint, "! 1 approval pending · ctrl+b q review") {
		t.Fatalf("banner missing:\n%s", paint)
	}
	appServerTestKeys(t, u, "1")
	if u.draft != "draft1" || strings.Contains(input.String(), `"decision"`) {
		t.Fatalf("draft=%q response=%s", u.draft, input.String())
	}
	if !u.openApprovals() {
		t.Fatal("approval did not reopen")
	}
	questionTestPaint(t, u, 80)
	if handled, err := u.approvalKey("\x1b"); !handled || err != nil || u.approvals.open {
		t.Fatalf("esc: handled=%v err=%v open=%v", handled, err, u.approvals.open)
	}
	u.openApprovals()
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "x\x7f\x17\t\x1b[200~pasted\x1b[201~2\r")
	if u.draft != "draft1" || !strings.Contains(input.String(), `"decision":"decline"`) {
		t.Fatalf("draft=%q response=%s", u.draft, input.String())
	}
}

func TestNativeApprovalGuardedWrite(t *testing.T) {
	u := newAppServerSessionTestUI(t, "/work")
	id := 0
	newRequest := func() *vcsApproval {
		id++
		item := fmt.Sprintf("cmd-%d", id)
		appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "main", "turnId": "turn", "item": appServerItem{ID: item, Type: "commandExecution", Command: "git push origin main", Status: "inProgress"}})
		return &vcsApproval{thread: "main", item: item, cwd: "/work", argv: []string{"git", "push", "origin", "main"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})}
	}
	approved, denied, expired := newRequest(), newRequest(), newRequest()
	for _, request := range []*vcsApproval{approved, denied, expired} {
		u.addGuardApproval(request)
	}
	paint := ansi.Strip(questionTestPaint(t, u, 80))
	for _, want := range []string{"Allow this remote write?", "git push origin main", "Denying fails only this command with exit status 1.", "1. Yes, run it", "2. Yes, for this exact command", "3. No, fail this command"} {
		if !strings.Contains(paint, want) {
			t.Fatalf("dock lacks %q:\n%s", want, paint)
		}
	}
	appServerTestKeys(t, u, "\r")
	if reply := <-approved.reply; !reply.OK {
		t.Fatalf("approval reply = %+v", reply)
	}
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "3\r")
	if reply := <-denied.reply; reply.OK || reply.Reason != "user denied this command without a reason" {
		t.Fatalf("denial reply = %+v", reply)
	}
	expired.outcome = "timed out"
	close(expired.done)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "\r") // The command has its answer; nothing is sent.
	if len(expired.reply) != 0 || len(u.approvals.pending) != 0 {
		t.Fatal("expired write was answered")
	}
	for i, want := range []string{"Approved", "Denied", "Auto Denied: no answer within 5 minutes"} {
		if len(u.view.entries) != 3 || u.view.entries[i].blocks[0].Approval != want+"\nCommand: git push origin main" {
			t.Fatalf("approval %d = %+v", i, u.view.entries)
		}
	}
	withdrawn := newRequest()
	u.addGuardApproval(withdrawn)
	withdrawn.outcome = "withdrawn"
	close(withdrawn.done)
	if !u.expireApprovals() || strings.Contains(approvalTestTranscript(t, u), "Approval") || u.view.entries[3].native.approval != "Withdrawn: the command stopped\nCommand: git push origin main" {
		t.Fatalf("transcript = %q", approvalTestTranscript(t, u))
	}
}

func TestNativeApprovalThreadPermissions(t *testing.T) {
	u, _ := newAppServerTestUI()
	if got := u.threadPermissions(map[string]any{}); got["approvalPolicy"] != "never" || got["sandbox"] != "danger-full-access" {
		t.Fatalf("yolo params = %v", got)
	}
	u.approvalMode = true
	if got := u.threadPermissions(map[string]any{}); len(got) != 0 {
		t.Fatalf("approval-mode params = %v", got)
	}
}

func TestNativeApprovalGuardSessionMatchesExactCommandAndWorkdir(t *testing.T) {
	u, _ := newAppServerTestUI()
	request := func(cwd string, argv ...string) *vcsApproval {
		return &vcsApproval{cwd: cwd, executable: "/bin/git", argv: argv, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})}
	}
	first, queued := request("/work", "git", "push", "origin", "main"), request("/work", "git", "push", "origin", "main")
	u.addGuardApproval(first)
	u.addGuardApproval(queued)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "2\r")
	for _, r := range []*vcsApproval{first, queued, request("/work", "git", "push", "origin", "main")} {
		if r != first && r != queued {
			u.addGuardApproval(r)
		}
		if len(r.reply) != 1 || !(<-r.reply).OK || len(u.approvals.pending) != 0 {
			t.Fatal("exact session approval was not reused")
		}
	}
	otherExecutable := request("/work", "git", "push", "origin", "main")
	otherExecutable.executable = "/other/git"
	for _, r := range []*vcsApproval{request("/other", "git", "push", "origin", "main"), request("/work", "git", "push", "origin", "other"), request("/work", "git", "push", "origin main"), otherExecutable} {
		u.addGuardApproval(r)
		if len(r.reply) != 0 {
			t.Fatal("session approval broadened to another command or workdir")
		}
	}
	fresh, _ := newAppServerTestUI()
	fresh.addGuardApproval(request("/work", "git", "push", "origin", "main"))
	if len(fresh.approvals.pending) != 1 {
		t.Fatal("approval survived a new UI session")
	}
}

func TestNativeApprovalTypedDenialReachesCommand(t *testing.T) {
	for _, keys := range []string{"3", "!do not publish\x7f\x1b[200~h yet\x1b[201~"} {
		t.Run(keys, func(t *testing.T) {
			shell := newVCSGuardShell(t)
			result := make(chan execTrackRun, 1)
			go func() { result <- runExecTrackShell(t, shell.env, "git push origin main") }()
			u, _ := newAppServerTestUI()
			u.insertDraft("kept draft")
			image := answerImageFixture(t)
			u.attachImage(image)
			kept := u.draft
			request := <-shell.hub.approvals
			if request.executable != shell.real+"/git" {
				t.Fatalf("resolved executable = %q", request.executable)
			}
			u.addGuardApproval(request)
			u.openApprovals()
			questionTestPaint(t, u, 80)
			appServerTestKeys(t, u, keys)
			if u.approvals.pending[0].selected != 2 || u.shellMode() || u.picker.open {
				t.Fatal("typing did not select plain-text denial")
			}
			u.hideApprovals()
			if u.draft != kept {
				t.Fatal("approval changed parked draft")
			}
			if _, err := os.Stat(image); err != nil {
				t.Fatal("approval reclaimed parked image", err)
			}
			u.openApprovals()
			questionTestPaint(t, u, 80)
			appServerTestKeys(t, u, "\r")
			run := <-result
			want := "user denied this command without a reason"
			if keys != "3" {
				want = "user denied this command with a reason: !do not publish yet"
			}
			if run.code != 1 || run.stderr != "mekugi: remote write denied: "+want+"\n" || len(shell.invoked(t)) != 0 || u.draft != kept || len(u.images) != 1 {
				t.Fatalf("result=%+v draft=%q", run, u.draft)
			}
		})
	}
}

func TestNativeApprovalDenialProjectsFeedbackInSameTurn(t *testing.T) {
	for _, tc := range [][2]string{{"main", "cancel"}, {"child", "decline"}} {
		thread, decision := tc[0], tc[1]
		t.Run(thread+"/"+decision, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.turn = "turn"
			approvalTestCommand(t, u, 7, map[string]any{"threadId": thread, "availableDecisions": []string{"accept", decision}})
			questionTestPaint(t, u, 80)
			appServerTestKeys(t, u, "wait for review\r")
			if strings.Count(strings.TrimSpace(wire.String()), "\n") != 0 || !strings.Contains(wire.String(), `"decision":"decline"`) {
				t.Fatalf("denial steered or started a turn: %s", wire.String())
			}
			for _, scope := range [][2]string{{thread, "other"}, {"other", "turn"}, {thread, "turn"}, {thread, "turn"}} {
				request := &parsedResponsesRequest{fields: map[string]jsontext.Value{"input": jsontext.Value(`[{"type":"custom_tool_call_output","call_id":"call","output":"exec command rejected by user"}]`)}}
				if err := u.proxy.projectApprovalFeedback(request, scope[0], scope[1]); err != nil {
					t.Fatal(err)
				}
				got := string(request.fields["input"])
				want := scope == [2]string{thread, "turn"}
				if strings.Contains(got, "user denied this command with a reason: wait for review") != want || !strings.Contains(got, "exec command rejected by user") {
					t.Fatalf("feedback scope %v: %s", scope, got)
				}
			}
			questionTestMessage(t, u, nil, "turn/completed", map[string]any{"threadId": thread, "turn": map[string]any{"id": "turn", "status": "completed"}})
			if len(u.proxy.approvalFeedback) != 0 {
				t.Fatal("completed-turn feedback retained")
			}
		})
	}
}

func TestNativeApprovalQuestionEditorOwnership(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.session.start("main", t.TempDir())
	u.turn = "turn"
	approvalTestCommand(t, u, 7, nil)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "wait for review")
	questionTestSync(t, u, "question", false)
	questionTestPaint(t, u, 80)
	u.ensureShell()
	defer u.shell.diffScreen.Close()
	u.shell.layout.codex = terminalRect{0, 0, 80, 28}
	if err := u.shell.mouse(fmt.Sprintf("\x1b[<0;%d;%dM", u.questions.rect.x+1, u.questions.rect.y+1)); err != nil {
		t.Fatal(err)
	}
	if u.approvals.open || u.currentQuestion() == nil || u.draft != "" {
		t.Fatal("question and approval share the composer")
	}
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "answer to question\r")
	if !strings.Contains(wire.String(), "answer to question") || strings.Contains(wire.String(), `"id":7`) {
		t.Fatalf("question answer sent to approval: %s", wire.String())
	}
	u.openApprovals()
	if u.draft != "wait for review" {
		t.Fatalf("approval reason lost: %q", u.draft)
	}
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "\r")
	feedback := strings.Join(u.proxy.approvalFeedback[[2]string{"main", "turn"}], "\n")
	if !strings.Contains(feedback, "wait for review") || strings.Contains(feedback, "answer to question") {
		t.Fatalf("wrong denial reason: %s", feedback)
	}
}

func TestNativeApprovalWithoutProxyKeepsHostDecisions(t *testing.T) {
	u, wire := newAppServerTestUI()
	questionTestMessage(t, u, 7, "item/commandExecution/requestApproval", map[string]any{
		"threadId": "main", "turnId": "turn", "itemId": "cmd", "command": "echo example",
	})
	if strings.Contains(questionTestPaint(t, u, 80), "deny with reason") {
		t.Fatal("proxy-free UI offers unavailable feedback")
	}
	appServerTestKeys(t, u, "ignored reason2\r")
	if !strings.Contains(wire.String(), `"decision":"cancel"`) || len(u.approvals.pending) != 0 || u.draft != "" {
		t.Fatalf("proxy-free native denial did not complete: %s", wire.String())
	}
}

func TestNativeApprovalFeedbackWebSocketContinuation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	proxy := newManagedMekugiProxy(t)
	const feedback = "user denied this command with a reason: keep the existing result"
	if err := proxy.answerWithApprovalFeedback("thread-1", "turn", feedback, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	headers := codexAuthHeaders()
	headers.Set(sessionIDHeader, "approval-session")
	headers.Set(threadIDHeader, "thread-1")
	headers.Set(codexTurnMetadataHeader, string(mustMarshalJSON(codexTurnMetadata{
		RequestKind: "turn", TurnID: "turn", Directories: map[string]jsontext.Value{t.TempDir(): nil},
	})))
	conn := testResponsesSocket(t, ctx, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.CloseNow()
		for _, id := range []string{"first", "second", "third"} {
			request, err := providerSocketRead(ctx, upstream)
			if err != nil {
				t.Error(err)
				return
			}
			// Provider projections are not native WebSocket history. The next
			// request must rebase, retaining exactly one denial and feedback.
			var input []map[string]jsontext.Value
			if err := json.Unmarshal(request["input"], &input); err != nil {
				t.Error(err)
				return
			}
			denials, reasons := 0, 0
			for _, item := range input {
				if jsonString(item, "call_id") == "denied" && jsonString(item, "output") == "exec command rejected by user" {
					denials++
				}
				if jsonString(item, "role") == "user" && strings.Contains(string(item["content"]), feedback) {
					reasons++
				}
			}
			if denials != 1 || reasons != 1 || jsonString(request, "previous_response_id") != "" {
				t.Errorf("%s: denial=%d feedback=%d parent=%s", id, denials, reasons, request["previous_response_id"])
			}
			if err := providerSocketWrite(ctx, upstream, socketEvent("response.completed", id)); err != nil {
				t.Error(err)
				return
			}
		}
	}), proxy, headers)
	input := []any{map[string]any{"type": "custom_tool_call_output", "call_id": "denied", "output": "exec command rejected by user"}}
	for _, parent := range []string{"", "first", "second"} {
		socketWrite(t, ctx, conn, map[string]any{
			"type": "response.create", "model": "gpt-test", "tools": testExecResponsesTools(), "input": input, "previous_response_id": parent,
		})
		if got := socketRead(t, ctx, conn); jsonString(got, "type") != "response.completed" {
			t.Fatalf("continuation failed: %s", mustMarshalJSON(got))
		}
		input = []any{map[string]string{"role": "user", "content": "continue"}}
	}
}

func TestUISnapshotNativeApprovalDock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
		setup func(*testing.T, *appServerUI)
	}{
		{"approval-command", 80, func(t *testing.T, u *appServerUI) {
			approvalTestCommand(t, u, 1, map[string]any{"cwd": "/workspace/app", "reason": "Publish the release branch.", "proposedExecpolicyAmendment": []string{"git", "push"}})
			approvalTestCommand(t, u, 2, nil)
		}},
		{"approval-command-narrow", 36, func(t *testing.T, u *appServerUI) {
			approvalTestCommand(t, u, 1, map[string]any{"reason": "Publish the release branch."})
		}},
		{"approval-guarded-write", 80, func(t *testing.T, u *appServerUI) {
			u.addGuardApproval(&vcsApproval{thread: "main", cwd: "/workspace", argv: []string{"gh", "pr", "merge", "12", "--squash"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})})
		}},
		{"approval-guarded-denial", 80, func(t *testing.T, u *appServerUI) {
			u.addGuardApproval(&vcsApproval{cwd: "/workspace", argv: []string{"git", "push"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})})
			questionTestPaint(t, u, 80)
			appServerTestKeys(t, u, "do not publish yet")
		}},
		{"approval-banner", 80, func(t *testing.T, u *appServerUI) {
			u.draft = "Keep this draft."
			approvalTestCommand(t, u, 1, nil)
			approvalTestCommand(t, u, 2, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _ := newAppServerTestUI()
			u.view.painter.Theme = livediff.DarkTheme
			u.status, u.model, u.reasoningEffort = "Ready", "snapshot-model", "high"
			u.session.start("main", "/workspace")
			u.turn = "turn"
			tc.setup(t, u)
			rows, _ := u.mainFrame(tc.width, 20, 0)
			assertNativeUISnapshot(t, "native-"+tc.name, rows)
		})
	}
}

func TestUISnapshotNativeApprovalOutcomes(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	for _, tc := range []struct {
		name, outcome       string
		guard, early, child bool
		choice              int
	}{
		{name: "native-approved", outcome: "Approved"},
		{name: "native-denied", outcome: "Declined", early: true, choice: 1},
		{name: "guard-approved", outcome: "Approved", guard: true, early: true},
		{name: "guard-denied-child", outcome: "Denied", guard: true, child: true, early: true, choice: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, "/workspace")
			u.clock = func() time.Time { return time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC) }
			u.view.clock, u.agents.clock = u.clock, u.clock
			u.view.painter.Theme, u.agents.painter.Theme = livediff.DarkTheme, livediff.DarkTheme
			u.turn = "turn"
			thread, view := "main", u.view
			if tc.child {
				u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
				thread, view = "child", u.agents
			}
			item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "git push origin main", Status: "inProgress"}
			notify := func(method string) {
				appServerTestNotify(t, u, method, map[string]any{"threadId": thread, "turnId": "turn", "item": item})
			}
			if !tc.early {
				notify("item/started")
				u.shell.openEntry(view, view.entries[len(view.entries)-1].Seq)
				u.shell.paintOutput(make([]string, 18), 80, 18)
			}
			if tc.guard {
				u.addGuardApproval(&vcsApproval{thread: thread, item: "cmd", argv: []string{"git", "push", "origin", "main"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})})
			} else {
				approvalTestCommand(t, u, 7, map[string]any{"threadId": thread, "availableDecisions": []string{"accept", "decline"}})
			}
			a := u.approvals.pending[0]
			if tc.guard && tc.early && !tc.child {
				notify("item/started")
				native := *view.entries[0].native
				native.phase = "item/commandExecution/outputDelta"
				native.segments = []commandSegment{{source: item.Command, text: toolActivityShell(item.Command), running: true}}
				u.applyActivity([]activityPaneEntry{{Seq: u.session.next(), Agent: u.session.path(thread), Kind: "tool", Text: toolActivityShell(item.Command), native: &native}}, nil)
				if len(view.entries) != 1 || len(view.entries[0].blocks) != 1 || !strings.HasPrefix(view.entries[0].native.approval, "Pending Approval") {
					t.Fatal("early guard pending state was not reconciled")
				}
			}
			if tc.name == "guard-approved" {
				uisnapshot.AssertTerminal(t, "testdata/snapshots/approval-pending-"+tc.name+".txt", append(view.renderFeed(48, 30).lines, "plain after approval"), 48)
			}
			if err := u.answerApproval(a, a.choices[tc.choice]); err != nil {
				t.Fatal(err)
			}
			if !tc.guard && !tc.early {
				if frame := drawOutputDialog(u.shell); !strings.Contains(frame, "Approval: Approved") {
					t.Fatalf("painted dialog did not refresh before host output: %s", frame)
				}
			}
			if tc.early && (!tc.guard || tc.child) {
				notify("item/started")
				if tc.guard && len(u.approvals.unbound) != 0 {
					t.Fatal("early guard decision was not reconciled")
				}
			}
			item.Status, item.ExitCode = "completed", new(1) // Approval does not mean execution succeeded.
			if tc.choice == 1 && !tc.guard {
				item.Status, item.ExitCode = "declined", nil
			}
			notify("item/completed")
			count := 1
			outcome := tc.outcome
			if tc.guard {
				outcome += "\nCommand: git push origin main"
			}
			if len(view.entries) != count || view.entries[count-1].native.approval != outcome {
				t.Fatalf("entries = %+v", view.entries)
			}
			entry := view.entries[count-1]
			feed := view.renderFeed(48, 30)
			rows := feed.lines
			color, label := activityui.Green, "Approved"
			if tc.choice != 0 {
				color, label = activityui.Red, "Denied"
			}
			if !strings.Contains(strings.Join(rows, "\n"), color+label+activityui.Reset) || entry.Agent == "Session" {
				t.Fatalf("approval rows = %q", rows)
			}
			assertNativeUISnapshot(t, "approval-outcome-"+tc.name, rows)
			row := slices.IndexFunc(feed.snippets, func(s liveActivitySnippet) bool {
				block, ok := view.snippetBlock(s)
				return ok && block.Source == entry.Seq
			})
			if row < 0 || !u.shell.openOutput(view, feed.snippets[row]) {
				t.Fatal("approval command has no clickable output dialog")
			}
			rows = make([]string, 18)
			u.shell.paintOutput(rows, 80, len(rows))
			if !strings.Contains(u.shell.output.laid.Title, color+label+activityui.Reset) {
				t.Fatalf("dialog lost the decision: %q", rows)
			}
			assertNativeUISnapshot(t, "approval-dialog-"+tc.name, rows)
		})
	}
}

func TestUISnapshotNativeAutoApprovalStates(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	for _, status := range []string{"approved", "denied", "timedOut", "aborted"} {
		t.Run(status, func(t *testing.T) {
			u := newAppServerSessionTestUI(t, "/workspace")
			u.clock = func() time.Time { return time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC) }
			u.view.clock, u.agents.clock = u.clock, u.clock
			u.session.registerThread(appServerThreadInfo{ID: "child", AgentNickname: "worker"})
			item := appServerItem{ID: "cmd", Type: "commandExecution", Command: "git push", Status: "inProgress"}
			appServerTestNotify(t, u, "item/started", map[string]any{"threadId": "child", "turnId": "turn", "item": item})
			review := func(method, state, target string) {
				appServerTestNotify(t, u, method, map[string]any{"threadId": "child", "turnId": "turn", "reviewId": "review", "targetItemId": target, "review": map[string]any{"status": state}, "decisionSource": "agent"})
			}
			review("item/autoApprovalReview/started", "inProgress", "cmd")
			if got := u.agents.entries[0].blocks[0].Approval; got != "Pending Approval" {
				t.Fatalf("pending state = %q", got)
			}
			review("item/autoApprovalReview/completed", status, "cmd")
			want := map[string]string{"approved": "Approved", "denied": "Auto Denied", "timedOut": "Auto Denied", "aborted": "Review aborted"}[status]
			item.Status, item.ExitCode = "completed", new(1)
			appServerTestNotify(t, u, "item/completed", map[string]any{"threadId": "child", "turnId": "turn", "item": item})
			// Untargeted network reviews and missing items cannot add a row.
			review("item/autoApprovalReview/completed", "approved", "")
			review("item/autoApprovalReview/completed", "approved", "missing")
			if len(u.agents.entries) != 1 || len(u.view.entries) != 0 || u.agents.entries[0].blocks[0].Approval != want {
				t.Fatalf("review changed another record: %+v", u.agents.entries)
			}
			if status == "approved" || status == "denied" {
				uisnapshot.AssertTerminal(t, "testdata/snapshots/approval-auto-"+status+".txt", append(u.agents.renderFeed(48, 30).lines, "plain after approval"), 48)
			}
		})
	}
}
