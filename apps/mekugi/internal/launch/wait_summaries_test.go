package launch

import (
	json "encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/scope"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/threadcontrol/control"
)

func TestWaitSummaryOverflowRetainsEveryEntryForExactRecovery(t *testing.T) {
	f := newClaudeHookFixture(t)
	e := f.bridge.execution
	var err error
	e.recovery, err = recovery.New(f.bridge.owners.History, nil)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("Observed changes; validation not run. ", 500)
	full := control.WaitProgress{Agents: []control.AgentProgress{{Task: "/root/first", Summary: long, Changes: "amber1 amber3"}, {Task: "/root/second", Summary: "Missing validation.", Changes: "blue7"}}}
	bounded, err := e.BoundWaitProgress(t.Context(), "parent", full)
	if err != nil || bounded.Recovery == "" || len(bounded.Agents) != 0 {
		t.Fatalf("bound: entries=%d recovery=%q err=%v", len(bounded.Agents), bounded.Recovery, err)
	}
	data, _ := json.Marshal(bounded)
	if len(data) > 12<<10 {
		t.Fatal("combined output exceeded its bound")
	}
	read, err := e.recovery.Execute(t.Context(), command.Request{Args: append(strings.Fields(bounded.Recovery)[1:], "--max-tokens", "15500"), Scope: scope.Retained{Directory: e.retained.Store.Directory(), Namespace: "parent", Thread: "parent"}})
	if err != nil || read.ExitCode != 0 || !strings.Contains(read.Stdout, long) || !strings.Contains(read.Stdout, "blue7") || !strings.Contains(read.Stdout, "amber1 amber3") {
		t.Fatalf("recovery lost an entry/summary/selection: err=%v exit=%d stderr=%s", err, read.ExitCode, read.Stderr)
	}
	small := control.WaitProgress{Agents: []control.AgentProgress{{Task: "/root/first", Summary: strings.Repeat("word ", 400), Changes: "amber1"}}}
	kept, err := e.BoundWaitProgress(t.Context(), "parent", small)
	if err != nil || kept.Recovery != "" || kept.Agents[0].Summary != small.Agents[0].Summary {
		t.Fatal("advisory summary length became a hard limit")
	}
}

func TestClaudeProgressCheckpointUsesNativeCompletedBatchAndReplyRoute(t *testing.T) {
	f := newClaudeHookFixture(t)
	b, shared := f.bridge, f.bridge.execution.shared
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	parent := control.Identity{Thread: "parent", Session: "parent-session", Turn: "parent-turn", Call: "progress", Item: "wait"}
	now := time.Now()
	_, err := shared.RegisterWait(t.Context(), parent, control.WaitAny, nil, new(int64(300000)), now.Add(-5*time.Minute))
	check(err)
	check(shared.Execution(t.Context(), parent.Thread, parent.Turn, parent.Item, "", true))
	_, err = shared.ClaimLocalStop(t.Context(), parent.Thread, parent.Turn, parent.Item)
	check(err)
	check(shared.HostCompleted(t.Context(), parent.Thread, parent.Turn, control.Completed, control.HostFailure{}))
	_, err = shared.PrepareWake(t.Context(), parent.Thread, now)
	check(err)
	_, err = shared.ClaimEffect(t.Context(), parent.Thread, parent.Call)
	check(err)
	check(shared.BeginWaitSummaries(t.Context(), parent.Thread, parent.Call, "context", control.WaitProgress{Agents: []control.AgentProgress{{Task: "/root/child", Changes: "amber1"}}}, map[string]int{"capture": 1}))
	member, _ := shared.Member(parent.Thread)
	slot, err := shared.ClaimEffect(t.Context(), parent.Thread, member.Wait.Summaries[0].ID)
	check(err)
	before, _ := shared.Member("child")
	turn, err := b.runtime.Input(t.Context(), control.NativeInput{Thread: "child", TargetTurn: before.Turn, ID: slot.ID, Kind: "PROGRESS_CHECKPOINT", Message: slot.Message, Run: true})
	check(err)
	check(shared.AcknowledgeSummary(t.Context(), parent.Thread, slot.ID, control.Receipt{Turn: turn}))
	if len(f.inputs) != 1 {
		t.Fatal("checkpoint queued native input that could interrupt/restart work")
	}
	batch, err := b.request(t.Context(), "hook", []byte(`{"session_id":"native-session","hook_event_name":"PostToolBatch","tool_calls":[]}`))
	check(err)
	var output struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	check(json.Unmarshal(batch, &output))
	if output.Hook.Context != slot.Message || strings.Contains(string(batch), `"continue":false`) {
		t.Fatalf("checkpoint stopped the assignment or lost its request: %s", batch)
	}
	reply, _ := json.Marshal(map[string]any{"name": "send_message", "toolUseID": "reply", "arguments": map[string]string{"target": "agent:main", "message": control.ProgressReplyPrefix + slot.ID + "\nChanged behavior for the frozen selection; validation not run."}})
	result, err := b.request(t.Context(), "tool", reply)
	check(err)
	// The composition's scheduler may already have claimed the wake. Assert
	// the reply consumer's acknowledgement instead of racing that claim.
	if !strings.Contains(string(result), `progress_recorded\":true`) {
		t.Fatalf("summary reply did not resolve its retained slot: %s", result)
	}
	after, _ := shared.Member("child")
	if after.Generation != before.Generation || after.Turn != before.Turn || !after.HostRunning || after.LastAnswer != "" {
		t.Fatal("nonterminal reply changed logical work")
	}
	batch, err = b.request(t.Context(), "hook", []byte(`{"session_id":"native-session","hook_event_name":"PostToolBatch","tool_calls":[]}`))
	check(err)
	if strings.Contains(string(batch), control.ProgressReplyPrefix) {
		t.Fatal("native batch repeated the summary request")
	}
}
