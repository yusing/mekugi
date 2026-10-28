package launch

import (
	"encoding/json/jsontext"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/backends/claude"
	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestClaudeApprovalProducerDockAndAPI(t *testing.T) {
	f := newClaudeHookFixture(t)
	parent, err := f.bridge.identity("spawn-reviewer")
	if err != nil {
		t.Fatal(err)
	}
	settings := f.bridge.settings
	settings.Role = "reviewer"
	spawn, err := f.bridge.execution.shared.ReserveSpawn(t.Context(), parent, control.SpawnRequest{Name: "reviewer", Message: "review work", Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	const thread = "claude:00000000-1111-2222-3333-444444444444"
	for _, phase := range []string{"create", "assignment_input", "admit", "initial"} {
		claimed, err := f.bridge.execution.shared.ClaimEffect(t.Context(), parent.Thread, spawn.ID)
		if err != nil || claimed == nil || claimed.Phase != phase {
			t.Fatalf("claim %s: %+v, %v", phase, claimed, err)
		}
		if err := f.bridge.execution.shared.EffectAcknowledged(t.Context(), parent.Thread, spawn.ID, control.Receipt{Phase: phase, Thread: thread, Session: "review-session", Turn: "review-turn"}); err != nil {
			t.Fatal(err)
		}
	}
	// A nested nonattachable runtime borrows the nearest ancestor's dock. Its
	// authenticated logical name differs from its opaque native thread ID.
	owner := sessioncontrol.NewDelegateApprovals(t.Context())
	defer owner.Close()
	f.bridge.execution.approvals = owner
	bridge := &claudeBridge{execution: f.bridge.execution, info: control.ThreadInfo{Thread: thread, Session: "review-session"}, settings: settings}
	for _, tc := range []struct {
		name, reason, outcome string
		choice                int
		allowed               bool
	}{
		{name: "approve", outcome: "Approved", allowed: true},
		{name: "deny", choice: 1, outcome: "Denied"},
		{name: "deny with reason", choice: 1, reason: "wait for review", outcome: "Denied: wait for review"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.Thread, u.Turn = "parent", "parent-turn"
			u.startSession("parent", t.TempDir())
			u.attached.session.RegisterThread(observation.Thread{ID: "parent", Path: "/root"})
			u.Text = "parent draft"
			identity := control.Identity{Thread: thread, Session: "review-session", Turn: "review-turn", Call: "native-approval", Item: "native-tool"}
			result := make(chan struct {
				allowed bool
				reason  string
				err     error
			}, 1)
			go func() {
				allowed, reason, err := bridge.approve(t.Context(), identity, "Bash", jsontext.Value(`{"command":"echo approval"}`), claude.Decision{Ask: true, Reason: "Native tool permission requested"})
				result <- struct {
					allowed bool
					reason  string
					err     error
				}{allowed, reason, err}
			}()
			request := <-owner.Requests()
			addDelegateApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, false, u.notifications, u.review, &u.pendingEditReceipts, request)
			projected := apiPrompts(&u.attached, &u.Client)
			if len(projected.Approvals) != 1 || projected.Approvals[0].Agent != "/root/child/reviewer" {
				t.Fatalf("approval lost authenticated logical attribution: %+v", projected.Approvals)
			}
			member := u.attached.session.Agent("/root/child/reviewer")
			if member == nil || member.Role != "reviewer" || !member.NonAttachable {
				t.Fatalf("approval source metadata = %+v", member)
			}
			view := u.approvals.View().Approval
			if view.Thread != thread || view.Turn != identity.Turn || view.Item != identity.Item {
				t.Fatalf("dock changed native request identity: %+v", view)
			}
			approval := u.approvals.handles[request.Approval]
			if err := applyApproval(&u.attached, u.shell, &u.Client, &u.picker, &u.Editor, &u.questions, &u.approvals, u.clock, u.notifications, u.review, &u.pendingEditReceipts, u.controlObservations, approval, approval.Choices[tc.choice], tc.reason); err != nil {
				t.Fatal(err)
			}
			got := <-result
			if got.err != nil || got.allowed != tc.allowed || got.reason != tc.reason {
				t.Fatalf("native decision = %+v", got)
			}
			item := approvalItem(&u.attached, thread, identity.Turn, identity.Item)
			if item == nil || item.Approval != tc.outcome {
				t.Fatalf("recorded approval outcome = %+v, want %q", item, tc.outcome)
			}
			if wire.Len() != 0 || u.Turn != "parent-turn" || u.Text != "parent draft" || len(u.approvals.Pending) != 0 {
				t.Fatal("native approval changed ancestor work or remained pending")
			}
		})
	}
}
