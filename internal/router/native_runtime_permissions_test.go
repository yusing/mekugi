package router

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/session"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func runtimePermissionState(t *testing.T, u *appServerUI, id, want string) {
	t.Helper()
	for _, view := range []*liveActivityView{u.view, u.agents} {
		found := false
		for _, row := range view.entries {
			if row.CallID != id {
				continue
			}
			found = true
			if row.native == nil || row.native.approval != want {
				t.Fatalf("%s view approval = %+v, want %q", id, row.native, want)
			}
		}
		if !found {
			t.Fatalf("%s has no command in one shared view", id)
		}
	}
}

func TestUISnapshotNativeRuntimeCommandPermissions(t *testing.T) {
	previous := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = previous })
	u, client := runtimeTestUI(t)
	u.clock = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }
	feed := runtimeDecodedFrames(t, u)
	tool := func(id, caller string) {
		feed(fmt.Sprintf(`{"kind":"event","event":{"type":"assistant","parent_tool_use_id":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"printf permission"}}]}}}`, caller, id))
	}
	prompt := func(id, tool, child string) {
		feed(fmt.Sprintf(`{"kind":"permission","id":%q,"toolUseID":%q,"agentID":%q,"tool":"Bash","input":{"command":"printf permission"}}`, id, tool, child))
	}
	state := func(id, want string) { t.Helper(); runtimePermissionState(t, u, id, want) }
	tool("other", "") // Identical text must never receive another call's decision.
	prompt("p", "early", "")
	if u.approvalItem(u.thread, "", "early") != nil {
		t.Fatal("permission fabricated a command row")
	}
	tool("early", "")
	state("early", "Pending Approval")
	state("other", "")
	uisnapshot.Assert(t, "testdata/snapshots/native-runtime-command-permission-pending.txt", strings.Join(u.view.renderFeed(80, 30).lines, "\n"))
	runtimeFrame(t, u, 100, 28)
	runtimeKeys(t, u, "1\r")
	if len(client.decisions) != 1 || !client.decisions[0].Allow {
		t.Fatal("shared dock did not send Allow once")
	}
	state("early", "Pending Approval") // Send success is not a native receipt.
	for _, entry := range u.view.entries {
		if entry.CallID == "early" {
			u.shell.openEntry(u.view, entry.Seq)
		}
	}
	u.shell.paintOutput(make([]string, 18), 80, 18)
	feed(`{"kind":"permission_decision","id":"p","toolUseID":"early","allow":true}`)
	state("early", "Pending Approval")
	feed(`{"kind":"event","event":{"type":"tool_progress","tool_use_id":"early","tool_name":"Bash","elapsed_time_seconds":1}}`)
	state("early", "Approved")
	if frame := drawOutputDialog(u.shell); !strings.Contains(frame, "Approval: Approved") {
		t.Fatalf("open dialog missed the native choice: %s", frame)
	}
	u.shell.output = nil
	feed(`{"kind":"event","event":{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"early","content":"exit 1","is_error":true}]}}}`)
	state("early", "Approved") // Execution failure is independent of permission.
	u.runtimeEvent(session.Event{Kind: "task", Task: &session.Task{ID: "child", ToolID: "agent-call", Kind: "local_agent", Status: "running"}})
	prompt("deny", "child-call", "child")
	runtimeFrame(t, u, 100, 28)
	runtimeKeys(t, u, "\x03")
	feed(`{"kind":"permission_decision","id":"deny","toolUseID":"child-call","allow":false}`)
	tool("child-call", "agent-call")
	state("child-call", "Pending Approval")
	feed(`{"kind":"event","event":{"type":"user","parent_tool_use_id":"agent-call","message":{"content":[{"type":"tool_result","tool_use_id":"child-call","content":"Native denial","is_error":true}]}}}`)
	state("child-call", "Declined")
	state("other", "")
	tool("cancelled", "")
	prompt("cancel", "cancelled", "")
	runtimeFrame(t, u, 100, 28)
	runtimeKeys(t, u, "1\r") // Cancellation wins before this choice is consumed.
	feed(`{"kind":"permission_decision","id":"cancel","toolUseID":"cancelled","allow":true}`)
	state("cancelled", "Pending Approval")
	feed(`{"kind":"permission_cancelled","id":"cancel"}`)
	feed(`{"kind":"permission_cancelled","id":"cancel"}`)
	state("cancelled", "Cancelled")
	if u.questions.active != nil || u.questions.calls[2].questions[0].outcome != "cancelled" {
		t.Fatal("native cancellation left a sent choice as confirmed")
	}
	tool("auto", "agent-call")
	feed(`{"kind":"event","event":{"type":"system","subtype":"permission_denied","tool_use_id":"auto","agent_id":"child","message":"Native rule denies this call"}}`)
	state("auto", "Auto Denied")
	tool("terminal", "")
	feed(`{"kind":"event","event":{"type":"result","permission_denials":[{"tool_use_id":"terminal"},{"tool_use_id":"child-call"}]}}`)
	state("terminal", "Auto Denied")
	state("child-call", "Declined")
	state("other", "")
	for _, id := range []string{"child-call", "cancelled", "auto", "terminal"} {
		caller := ""
		if id == "child-call" || id == "auto" {
			caller = "agent-call"
		}
		feed(fmt.Sprintf(`{"kind":"event","event":{"type":"user","parent_tool_use_id":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"Native denial or cancellation","is_error":true}]}}}`, caller, id))
	}
	for _, row := range u.agents.entries {
		if row.CallID == "child-call" && row.Agent != "/root/child" {
			t.Fatal("child permission moved the command to Main")
		}
	}
	uisnapshot.Assert(t, "testdata/snapshots/native-runtime-command-permission-outcomes.txt", strings.Join(u.view.renderFeed(80, 40).lines, "\n"))
	prompt("retired", "unseen", "")
	if err := u.clearSessionPresentation(); err != nil {
		t.Fatal(err)
	}
	feed(`{"kind":"history","event":{"type":"assistant","message":{"content":[{"type":"tool_use","id":"unseen","name":"Bash","input":{"command":"printf permission"}}]}}}`)
	if len(u.approvals.unbound) != 0 || u.questions.active != nil {
		t.Fatal("retired permission survived native presentation replacement")
	}
}
