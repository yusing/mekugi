package launch

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/conversation/prompt"
)

func TestDelegateApprovalDockPreservesChildAuthorityAndParentDraft(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Turn, u.Text = "parent-turn", "parked parent input"
	parent := u.Editor.Park()
	owner := sessioncontrol.NewDelegateApprovals(t.Context())
	defer owner.Close()
	result := make(chan error, 1)
	go func() {
		choice, reason, err := owner.Request(t.Context(), observation.Thread{ID: "claude-child", Path: "/root/reviewer", NonAttachable: true}, &prompt.Approval{Thread: "claude-child", Turn: "native-turn", Item: "native-tool", Title: "Allow this tool?", Subject: "Write a workspace file", Details: []string{"Agent: agent:main/reviewer"}, Choices: []prompt.Choice{{ID: 0, Label: "Allow once", Approve: true, Outcome: "Approved"}, {ID: 1, Label: "Deny", Deny: true, Outcome: "Denied"}}})
		if err == nil && (choice.Approve || reason != "wait for review") {
			err = errors.New("native runtime received a different decision or reason")
		}
		result <- err
	}()
	request := <-owner.Requests()
	addDelegateApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, false, u.notifications, u.review, &u.pendingEditReceipts, request)
	if u.approvals.Open || u.Text != parent.Snapshot.Text {
		t.Fatal("approval took over a nonempty parent composer")
	}
	if !openApprovals(&u.approvals, &u.questions, u.attached.conversation, u.shell, &u.picker, &u.Editor, u.Thread) {
		t.Fatal("delegate approval did not enter the existing dock")
	}
	view := u.approvals.View().Approval
	if view.Thread != "claude-child" || view.Turn != "native-turn" || view.Item != "native-tool" {
		t.Fatalf("approval source changed: %+v", view)
	}
	paint := ansi.Strip(questionTestPaint(t, u, 90))
	for _, want := range []string{"Allow this tool?", "Write a workspace file", "Agent: agent:main/reviewer", "Allow once", "Deny"} {
		if !strings.Contains(paint, want) {
			t.Fatalf("approval dock lacks %q:\n%s", want, paint)
		}
	}
	appServerTestKeys(t, u, "wait for review\r")
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || u.Turn != "parent-turn" || !reflect.DeepEqual(u.Editor.Park(), parent) || len(u.approvals.Pending) != 0 {
		t.Fatalf("approval changed parent work: wire=%s turn=%q draft=%+v", wire.String(), u.Turn, u.Editor.Park())
	}
	if feedback := u.controlObservations.Feedback("claude-child", "native-turn"); feedback != "" {
		t.Fatalf("native denial became Codex feedback: %q", feedback)
	}
}

func TestDelegateApprovalCancellationWithdrawsDock(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.startSession("main", t.TempDir())
	u.Text = "parked parent input"
	owner := sessioncontrol.NewDelegateApprovals(t.Context())
	defer owner.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := owner.Request(ctx, observation.Thread{ID: "child", Path: "/root/child"}, &prompt.Approval{Thread: "child", Turn: "turn", Item: "tool", Title: "Allow this tool?", Choices: []prompt.Choice{{ID: 0, Approve: true}, {ID: 1, Deny: true}}})
		result <- err
	}()
	request := <-owner.Requests()
	addDelegateApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, false, u.notifications, u.review, &u.pendingEditReceipts, request)
	openApprovals(&u.approvals, &u.questions, u.attached.conversation, u.shell, &u.picker, &u.Editor, u.Thread)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request = %v", err)
	}
	if !expireApprovals(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.notifications, u.review, &u.pendingEditReceipts) || len(u.approvals.Pending) != 0 || u.approvals.Open || u.Text != "parked parent input" {
		t.Fatal("canceled request remained visible or lost the parent draft")
	}
	addDelegateApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, false, u.notifications, u.review, &u.pendingEditReceipts, request)
	if wire.Len() != 0 || len(u.approvals.Pending) != 0 {
		t.Fatal("canceled request was answered or reopened")
	}
}
