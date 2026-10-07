package router

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func seedNativeJournalResetPreview(p *nativePreview) {
	p.steps = nil
	d, _ := resetDriverFixture(p.t, "slice")
	// Leave enough time to inspect a terminal frame and press Escape.
	d.deadline = time.Now().Add(30 * time.Second)
	p.ui.ctx, p.ui.proxy, p.ui.client, p.ui.thread, p.ui.reset = p.t.Context(), d.proxy, d.client, d.thread, d
	p.ui.journal = d.proxy.journals.attachNative(d.workspace, d.thread)
	p.t.Cleanup(func() { d.proxy.journals.detachNative(p.ui.journal) })
	p.ui.shell.focus, p.ui.shell.journalOpen = 0, false
}

func TestNativeJournalResetCountdownEscape(t *testing.T) {
	d, wire := resetDriverFixture(t, "slice")
	d.deadline = time.Now().Add(3 * time.Second)
	u, _ := newAppServerTestUI()
	u.ctx, u.proxy, u.client, u.thread, u.reset = t.Context(), d.proxy, d.client, d.thread, d
	u.ensureShell()
	frame, _ := u.mainFrame(100, 24, 0)
	if text := ansi.Strip(strings.Join(frame, "\n")); !strings.Contains(text, "Resetting context") || !strings.Contains(text, "/2 Second") || !strings.Contains(text, "Esc cancels") {
		t.Fatalf("countdown is not visible: %s", text)
	}
	if err := u.shell.send("\x1b"); err != nil {
		t.Fatal(err)
	}
	if err := d.tick(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	resetDriverRequireMethods(t, wire)
	intent, err := d.proxy.replayStore.resetIntent(t.Context(), d.workspace, d.thread)
	if err != nil || intent != nil || d.active() {
		t.Fatalf("Esc left active intent: %+v %v", intent, err)
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
			appServerTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"`+thread+`","turn":{"id":"turn","status":"`+status+`"}}}`)
			resetDriverRequireMethods(t, wire)
			if u.reset.active() != (status == "completed") {
				t.Fatalf("status=%s active=%t", status, u.reset.active())
			}
		})
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
