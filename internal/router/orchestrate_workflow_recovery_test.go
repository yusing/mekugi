package router

import (
	"strings"
	"testing"
)

func TestOrchestrateWorkflowRecoveryContinuity(t *testing.T) {
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u, launch := orchestrateIdentityPendingTurnWithReplay(t, replay)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"initial"}}`)
	childCwd := u.orchestrateThreads["child"].batch.Cwd
	for thread, cwd := range map[string]string{"main": u.session.cwd, "child": childCwd} {
		t.Run(thread, func(t *testing.T) {
			fresh := reopenV2ContinuityProxy(t, u.proxy)
			want := orchestrateCoordinatorInstructions
			if thread == "child" {
				want = orchestrateChildInstructions(cwd)
			}
			// Reconstruct recovery using durable records, without live controllers.
			hook, err := fresh.replayStore.postCompactContext(t.Context(), cwd, thread)
			if err != nil || strings.Count(hook, want) != 1 || strings.Contains(hook, "Assignment:") || strings.Contains(hook, "Issues:") {
				t.Fatal("incorrect run guidance after restart", err, hook)
			}
			item, recovery := deliverV2ContinuityReset(t, fresh, cwd, thread)
			if strings.Count(recovery.Text, want) != 1 {
				t.Fatal("reset omitted or duplicated run guidance", recovery.Text)
			}
			requireV2ContinuityForward(t, reopenV2ContinuityProxy(t, fresh), cwd, thread, "", "", item, recovery.Text)
			_, next := deliverV2ContinuityReset(t, fresh, cwd, thread, item)
			if strings.Count(next.Text, want) != 1 {
				t.Fatal("second reset repeated historical workflow", next.Text)
			}
		})
	}
	ctx, release, err := replay.beginSession(t.Context(), "native", "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for _, err := range []error{
		u.proxy.journals.initialize(ctx, replay, childCwd, "native", "/root/native", "child"),
		u.proxy.journals.bindIdentity(ctx, replay, childCwd, "native", "child", "/root/native", true),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	summary, err := summaryForTest(t, ctx, replay, childCwd, "native")
	if err != nil || strings.Contains(summary.Text, "Orchestration workflow") {
		t.Fatal("native descendant acquired independent-root guidance", err, summary.Text)
	}
}
