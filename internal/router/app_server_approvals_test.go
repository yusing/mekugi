package router

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/internal/livediff"
	activityui "github.com/yusing/mekugi/internal/ui/activity"
	"github.com/yusing/mekugi/internal/vcsguard"
)

func approvalTestCommand(t *testing.T, u *appServerUI, id any, params map[string]any) {
	t.Helper()
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
		"No, and tell Codex what to do differently",
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
	if !strings.Contains(input.String(), `"decision":"cancel"`) {
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
	if got := approvalTestTranscript(t, u); !strings.Contains(got, "git push origin main · Turn ended before an answer") || len(u.view.entries) != 1 {
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
	if u.draft != "draft1" || !strings.Contains(input.String(), `"decision":"cancel"`) {
		t.Fatalf("draft=%q response=%s", u.draft, input.String())
	}
}

func TestNativeApprovalGuardedWrite(t *testing.T) {
	u, _ := newAppServerTestUI()
	u.session.start("main", "/work")
	newRequest := func() *vcsApproval {
		return &vcsApproval{thread: "main", cwd: "/work", argv: []string{"git", "push", "origin", "main"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})}
	}
	approved, denied, expired := newRequest(), newRequest(), newRequest()
	for _, request := range []*vcsApproval{approved, denied, expired} {
		u.addGuardApproval(request)
	}
	paint := ansi.Strip(questionTestPaint(t, u, 80))
	for _, want := range []string{"Allow this remote write?", "git push origin main", "Denying fails only this command with exit status 1.", "1. Yes, run it", "2. No, fail this command"} {
		if !strings.Contains(paint, want) {
			t.Fatalf("dock lacks %q:\n%s", want, paint)
		}
	}
	appServerTestKeys(t, u, "\r")
	if reply := <-approved.reply; !reply.OK {
		t.Fatalf("approval reply = %+v", reply)
	}
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "2\r")
	if reply := <-denied.reply; reply.OK || reply.Reason != "denied in Mekugi" {
		t.Fatalf("denial reply = %+v", reply)
	}
	expired.outcome = "timed out"
	close(expired.done)
	questionTestPaint(t, u, 80)
	appServerTestKeys(t, u, "\r") // The command has its answer; nothing is sent.
	if len(expired.reply) != 0 || len(u.approvals.pending) != 0 {
		t.Fatal("expired write was answered")
	}
	for i, want := range []string{"Approved", "Denied", "Denied: no answer within 5 minutes"} {
		if len(u.view.entries) != 3 || u.view.entries[i].blocks[0].Approval != want {
			t.Fatalf("approval %d = %+v", i, u.view.entries)
		}
	}
	withdrawn := newRequest()
	u.addGuardApproval(withdrawn)
	withdrawn.outcome = "withdrawn"
	close(withdrawn.done)
	if !u.expireApprovals() || !strings.Contains(approvalTestTranscript(t, u), "git push origin main · Withdrawn: the command stopped") {
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
		{name: "guard-approved", outcome: "Approved", guard: true},
		{name: "guard-denied-child", outcome: "Denied", guard: true, child: true, choice: 1},
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
				u.addGuardApproval(&vcsApproval{thread: thread, argv: []string{"git", "push", "origin", "main"}, reply: make(chan vcsguard.Reply, 1), done: make(chan struct{})})
			} else {
				approvalTestCommand(t, u, 7, map[string]any{"threadId": thread, "availableDecisions": []string{"accept", "decline"}})
			}
			a := u.approvals.pending[0]
			if err := u.answerApproval(a, a.choices[tc.choice]); err != nil {
				t.Fatal(err)
			}
			if !tc.guard && !tc.early {
				if frame := drawOutputDialog(u.shell); !strings.Contains(frame, "Approval: Approved") {
					t.Fatalf("painted dialog did not refresh before host output: %s", frame)
				}
			}
			if tc.early {
				notify("item/started")
			}
			item.Status, item.ExitCode = "completed", new(1) // Approval does not mean execution succeeded.
			if tc.choice == 1 && !tc.guard {
				item.Status, item.ExitCode = "declined", nil
			}
			notify("item/completed")
			count := 1
			if tc.guard {
				count = 2 // The guard names its argv, not an outer host item.
				if view.entries[0].native.approval != "" {
					t.Fatal("guard decision changed the outer host command")
				}
			}
			if len(view.entries) != count || view.entries[count-1].native.approval != tc.outcome {
				t.Fatalf("entries = %+v", view.entries)
			}
			entry := view.entries[count-1]
			feed := view.renderFeed(48, 30)
			rows := feed.lines
			color, label := activityui.Green, "Approved"
			if tc.choice == 1 {
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
