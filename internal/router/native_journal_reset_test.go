package router

import (
	"strings"
	"testing"
)

func seedNativeJournalResetPreview(p *nativePreview) {
	p.steps = nil
	d, _ := resetDriverFixture(p.t, "slice")
	if err := d.start(); err != nil {
		p.t.Fatal(err)
	}
	p.ui.ctx, p.ui.proxy, p.ui.client, p.ui.thread, p.ui.reset = p.t.Context(), d.proxy, d.client, d.thread, d
	p.ui.journal = d.proxy.journals.attachNative(d.workspace, d.thread)
	p.t.Cleanup(func() { d.proxy.journals.detachNative(p.ui.journal) })
	p.ui.shell.focus, p.ui.shell.journalOpen = 0, false
}

func TestUISnapshotJournalSubsliceContinuation(t *testing.T) {
	d, wire := resetDriverPlanFixture(t, "auto", "/1")
	u := newAppServerSessionTestUI(t, d.workspace)
	u.ctx, u.proxy, u.client, u.thread, u.reset = t.Context(), d.proxy, d.client, d.thread, d
	u.session.start(d.thread, d.workspace)
	appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{
		"threadId": d.thread, "tokenUsage": map[string]any{
			"total": map[string]any{"totalTokens": 900_000},
			"last":  map[string]any{"totalTokens": 149_999}, "modelContextWindow": 400_000,
		},
	})
	if err := d.start(); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "turn/start")
	assertNativeUISnapshot(t, "journal-subslice-continuation", []string{u.journalResetStrip(100)})
}

func TestNativeJournalSubsliceResumeUsesRestoredContext(t *testing.T) {
	d, wire := resetDriverPlanFixture(t, "slice", "/1")
	reopened, err := openMekugiReplayStore(d.proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{journals: newJournalStore(), replayStore: reopened, journalCompaction: "slice"}
	u, _ := newAppServerTestUI()
	u.proxy = proxy
	rollout := writeTestRollout(t, d.thread, map[string]any{"type": "event_msg", "payload": map[string]any{
		"type": "token_count", "info": map[string]any{
			"last_token_usage":  map[string]any{"total_tokens": 149_999},
			"total_token_usage": map[string]any{"total_tokens": 900_000}, "model_context_window": 400_000,
		},
	}})
	u.restoreContextUsage(&activityPaneAgent{}, appServerThreadInfo{ID: d.thread, Path: rollout})
	next := &journalResetDriver{ctx: t.Context(), proxy: proxy, client: d.client, workspace: d.workspace, thread: d.thread}
	if err := next.restore(); err != nil {
		t.Fatal(err)
	}
	if err := next.start(); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire, "turn/start")
}

func TestNativeJournalResetPreservesSelectedServiceTier(t *testing.T) {
	for _, tier := range []string{"priority", "default", ""} {
		t.Run("selection="+tier, func(t *testing.T) {
			d, _ := resetDriverFixture(t, "auto")
			u := newAppServerSessionTestUI(t, d.workspace)
			u.proxy, u.thread, u.model = d.proxy, d.thread, "gpt-6.1-sol"
			u.serviceTiers = &serviceTierSettings{configured: map[string]string{u.model: "fast"}}
			wire := u.client.Input.(*appServerTestInput)
			if tier != "" {
				appServerTestKeys(t, u, "/tier "+tier+"\r")
				if tier == "priority" {
					assertAppServerSettingsRequest(t, wire, "thread/settings/update", map[string]any{"threadId": d.thread, "serviceTier": tier})
					appServerTestNotify(t, u, "thread/settings/updated", map[string]any{"threadId": d.thread, "threadSettings": map[string]any{"model": u.model, "serviceTier": tier}})
				}
			}
			u.journal = d.proxy.journals.attachNative(d.workspace, d.thread)
			t.Cleanup(func() { d.proxy.journals.detachNative(u.journal) })
			if err := d.proxy.journals.restoreNative(t.Context(), d.proxy.replayStore, u.journal); err != nil {
				t.Fatal(err)
			}
			u.ensureJournalReset()
			d = u.reset
			if err := d.start(); err != nil {
				t.Fatal(err)
			}
			appServerOneRequest(t, wire, "thread/compact/start", "")
			resetDriverEvent(t, d, "turn/started", `{"threadId":"`+d.thread+`","turn":{"id":"reset"}}`)
			deliverRecoveryCompaction(t, d.proxy, d.workspace, d.thread, "reset")
			resetDriverEvent(t, d, "item/completed", `{"threadId":"`+d.thread+`","turnId":"reset","item":{"type":"contextCompaction"}}`)
			resetDriverEvent(t, d, "turn/completed", `{"threadId":"`+d.thread+`","turn":{"id":"reset","status":"completed"}}`)
			resetDriverReply(t, d, `{}`)
			start := appServerOneRequest(t, wire, "turn/start", journalContinuationText(d.intent))
			if start.Params.ServiceTier != tier {
				t.Fatalf("continuation tier=%q, want %q", start.Params.ServiceTier, tier)
			}
		})
	}
}

func TestNativeJournalResetRequiresSuccessfulHostCompletion(t *testing.T) {
	for _, status := range []string{"failed", "interrupted", "completed"} {
		t.Run(status, func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			thread := transform.shellThreadID
			resetPlan(t, proxy, workspace, thread)
			if err := proxy.journals.beginJournalTurn(t.Context(), proxy.replayStore, workspace, thread, "turn"); err != nil {
				t.Fatal(err)
			}
			if _, err := proxy.journals.apply(t.Context(), proxy.replayStore, workspace, thread, "", []journalMutation{{Op: "set", P: "/1", State: new("done")}}); err != nil {
				t.Fatal(err)
			}
			u, wire := newAppServerTestUI()
			u.ctx, u.proxy, u.thread, u.turn = t.Context(), proxy, thread, "turn"
			u.reset = &journalResetDriver{ctx: t.Context(), proxy: proxy, client: u.client, workspace: workspace, thread: thread}
			orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+thread+`","turn":{"id":"turn","status":"`+status+`"}}}`)
			if status == "completed" {
				resetDriverRequireMethods(t, wire, "turn/start")
			} else {
				resetDriverRequireMethods(t, wire)
			}
			if u.reset.active() != (status == "completed") {
				t.Fatalf("status=%s active=%t", status, u.reset.active())
			}
		})
	}
}

func TestUISnapshotJournalContinuationPause(t *testing.T) {
	d, wire := autoResumeFixture(t)
	if _, err := d.proxy.journals.apply(t.Context(), d.proxy.replayStore, d.workspace, d.thread, "", []journalMutation{
		{Op: "add", Kind: "task", Title: new("Check approval"), State: new("blocked"), Reason: new("Awaiting **approval**")},
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.completed("answer"); err != nil || d.active() {
		t.Fatalf("blocked work continued: active=%v err=%v", d.active(), err)
	}
	resetDriverRequireMethods(t, wire)
	store, err := openMekugiReplayStore(d.proxy.replayStore.directory)
	if err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{journals: newJournalStore(), replayStore: store}
	u := newAppServerSessionTestUI(t, d.workspace)
	u.proxy, u.thread = proxy, d.thread
	u.journal = proxy.journals.attachNative(d.workspace, d.thread)
	t.Cleanup(func() { proxy.journals.detachNative(u.journal) })
	if err := proxy.journals.restoreNative(t.Context(), store, u.journal); err != nil {
		t.Fatal(err)
	}
	assertNativeUISnapshot(t, "journal-continuation-paused", []string{u.journalPlanStrip(100), u.journalPlanStrip(50)})
	if !strings.Contains(u.journalPlanStrip(100), "\x1b[1mapproval") {
		t.Fatal("pause reason lost its Markdown style")
	}
	summary, err := summaryForTest(t, t.Context(), store, d.workspace, d.thread)
	if err != nil || !strings.Contains(summary.Text, "Continuation paused: /2: Awaiting **approval**") || strings.Contains(summary.Text, "Resume: continue") {
		t.Fatalf("recovery lost the pause: %s, %v", summary.Text, err)
	}
	if _, err := proxy.journals.apply(t.Context(), store, d.workspace, d.thread, "", []journalMutation{{Op: "set", P: "/2", State: new("done")}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u.journalPlanStrip(100), "Continuation paused") {
		t.Fatal("resolved blocker left a stale pause strip")
	}
	if err := proxy.journals.beginJournalTurn(t.Context(), store, d.workspace, d.thread, "unblocked"); err != nil {
		t.Fatal(err)
	}
	next := &journalResetDriver{ctx: t.Context(), proxy: proxy, client: d.client, workspace: d.workspace, thread: d.thread}
	if err := next.completed("unblocked"); err != nil || next.intent == nil || next.intent.Path != "/1" {
		t.Fatalf("resolved blocker did not release local work: %+v %v", next.intent, err)
	}
}

func TestNativeJournalAutoContinueUsesClientProvenance(t *testing.T) {
	for _, id := range []string{"ordinary", journalContinuationPrefix + "fixture"} {
		view := newLiveActivityView()
		view.applyAppServerItem(true, "", "main", "main", "turn", "item", "item/completed", "", appServerItem{
			Type: "userMessage", ClientID: id, Content: []byte(`[{"type":"text","text":"Continue the journal plan: /2 Second."}]`),
		})
		if len(view.entries) != 1 {
			t.Fatalf("entries=%d", len(view.entries))
		}
		auto := strings.HasPrefix(id, journalContinuationPrefix)
		if (view.entries[0].Agent == "You") == auto || strings.Contains(view.entries[0].Text, "Auto-continue") != auto {
			t.Fatalf("wrong continuation provenance: %+v", view.entries[0])
		}
	}
}
