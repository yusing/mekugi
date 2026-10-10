package router

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func mainFollowup(t *testing.T, u *appServerUI, ctx context.Context, id, text string) *orchestrateCommand {
	t.Helper()
	c := &orchestrateCommand{ctx: ctx, workspace: u.orchestrateThreads["child"].batch.Cwd, main: "child", target: "main", followup: true, callID: id, input: orchestrateSpawnInput{Message: text}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(c)
	return c
}

func TestAppServerOrchestrateMainComposer(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	w := u.client.Input.(*appServerTestInput)
	u.draft = "user draft"
	u.settings.begin(nil, "")
	c := mainFollowup(t, u, t.Context(), "idle", "/compact")
	if w.Len() != 0 || len(u.orchestrateJobs) != 0 {
		t.Fatal("follow-up bypassed settings")
	}
	u.unsent = []composerDraft{{text: "user input"}}
	u.settings.finish()
	if err := u.flushInput(); err != nil {
		t.Fatal(err)
	}
	drainOrchestrateWork(t, u)
	r := appServerOneRequest(t, w, "turn/start", "user input")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"user-turn"}}}`, r.ID))
	if w.Len() != 0 {
		t.Fatal("follow-up raced an acknowledged start without turn identity")
	}
	appServerTestTurn(t, u, "user-turn")
	drainOrchestrateWork(t, u)
	r = appServerOneRequest(t, w, "turn/steer", "/compact")
	if r.Params.ExpectedTurnID != "user-turn" || r.Params.ClientUserMessageID == "" || u.draft != "user draft" {
		t.Fatal("follow-up bypassed composer identity or modified draft", r)
	}
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, r.ID))
	if got := <-c.reply; got.err == nil || got.delivery.State != "rejected" || u.draft != "user draft" {
		t.Fatal("rejection changed user draft", got, u.draft)
	}
	appServerTestTurnEnd(t, u, "user-turn", "completed")
	c = mainFollowup(t, u, t.Context(), "start", "wake Main")
	drainOrchestrateWork(t, u)
	r = appServerOneRequest(t, w, "turn/start", "wake Main")
	// Host completion before acknowledgement must not revive the turn.
	appServerTestTurn(t, u, "main-followup")
	appServerTestUserMessage(t, u, "main-input", r.Params.ClientUserMessageID, "wake Main")
	appServerTestTurnEnd(t, u, "main-followup", "completed")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"main-followup"}}}`, r.ID))
	if got := <-c.reply; got.err != nil || got.delivery.TurnID != "main-followup" || u.turn != "" || u.busy() || u.draft != "user draft" {
		t.Fatal("acknowledgement revived Main or changed draft", got, u.draft)
	}
	u.proxy.orchestration.store = &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
	c = mainFollowup(t, u, t.Context(), "start", "wake Main")
	drainOrchestrateWork(t, u)
	if got := <-c.reply; got.err != nil || got.delivery.State != "delivered" || w.Len() != 0 {
		t.Fatal("retained repeat resent input", got)
	}
}

func TestAppServerOrchestrateMainDeliveryCancellation(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	w := u.client.Input.(*appServerTestInput)
	ctx, cancel := context.WithCancel(t.Context())
	c := mainFollowup(t, u, ctx, "cancel-before", "do not send")
	complete := <-u.orchestrateCompletions // Intent is now durable, before dispatch.
	cancel()
	u.completeOrchestrateWork(complete)
	drainOrchestrateWork(t, u)
	if got := <-c.reply; !errors.Is(got.err, context.Canceled) || got.delivery.State != "canceled" || w.Len() != 0 {
		t.Fatal("canceled input dispatched", got)
	}
	c = mainFollowup(t, u, t.Context(), "changed-turn", "do not steer another turn")
	complete = <-u.orchestrateCompletions
	appServerTestTurn(t, u, "running")
	u.completeOrchestrateWork(complete)
	drainOrchestrateWork(t, u)
	if got := <-c.reply; got.err == nil || got.delivery.State != "rejected" || w.Len() != 0 {
		t.Fatal("turn precondition changed during persistence", got)
	}
	ctx, cancel = context.WithCancel(t.Context())
	c = mainFollowup(t, u, ctx, "cancel-after", "late acknowledgement")
	drainOrchestrateWork(t, u)
	r := appServerOneRequest(t, w, "turn/steer", "late acknowledgement")
	cancel()
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"running"}}`, r.ID))
	if got := <-c.reply; got.err != nil || got.delivery.State != "delivered" || u.turn != "running" || w.Len() != 0 {
		t.Fatal("late cancellation interrupted recipient or lost acknowledgement", got)
	}
}

func TestAppServerOrchestrateMainGuardFailure(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	w := u.client.Input.(*appServerTestInput)
	u.guardHookCheck.hash = "guard"
	c := mainFollowup(t, u, t.Context(), "guarded", "wake")
	r := appServerOneRequest(t, w, "hooks/list", "")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"unavailable"}}`, r.ID))
	if got := <-c.reply; got.err == nil || len(u.orchestrateMainFollowups) != 0 || w.Len() != 0 {
		t.Fatal("failed guard retried or left the caller waiting", got)
	}
}

func TestAppServerOrchestrateMainShutdown(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	c := mainFollowup(t, u, t.Context(), "shutdown", "wake")
	drainOrchestrateWork(t, u)
	w := u.client.Input.(*appServerTestInput)
	appServerOneRequest(t, w, "turn/start", "wake")
	if err := u.closeOrchestrateStorage(); err != nil {
		t.Fatal(err)
	}
	if got := <-c.reply; got.err == nil || got.delivery.State != "uncertain" || w.Len() != 0 {
		t.Fatal("shutdown lost uncertain dispatch or sent another request", got)
	}
	store := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
	d, dispatch, err := store.BeginDelivery(t.Context(), c.workspace, c.main, orchestrate.Delivery{ID: c.callID, From: "child", Target: "main", Message: "wake"})
	if err != nil || dispatch || d.State != "uncertain" {
		t.Fatal("restart resent uncertain input", d, err)
	}
}

func TestAppServerOrchestrateMainUncommittedSteerRestore(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	appServerTestTurn(t, u, "running")
	u.draft = "user draft"
	c := mainFollowup(t, u, t.Context(), "restore-probe", "child assignment")
	drainOrchestrateWork(t, u)
	w := u.client.Input.(*appServerTestInput)
	r := appServerOneRequest(t, w, "turn/steer", "child assignment")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"running"}}`, r.ID))
	got := <-c.reply
	if got.err != nil {
		t.Fatal(got.err)
	}
	appServerTestTurnEnd(t, u, "running", "interrupted")
	if u.draft != "user draft" {
		t.Fatalf("child input leaked into draft: %q, delivery=%+v", u.draft, got.delivery)
	}
}
func TestAppServerOrchestrateMainSendFailureRestore(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	u.draft = "user draft"
	u.client.Input = new(sessionTitleFailInput)
	c := mainFollowup(t, u, t.Context(), "write-probe", "child assignment")
	drainOrchestrateWork(t, u)
	got := <-c.reply
	if got.err == nil {
		t.Fatal("expected dispatch failure")
	}
	if u.draft != "user draft" {
		t.Fatalf("child input leaked into draft: %q, delivery=%+v", u.draft, got.delivery)
	}
}
func TestAppServerOrchestrateMainEscapeReplay(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	appServerTestTurn(t, u, "running")
	c := mainFollowup(t, u, t.Context(), "replay-probe", "child assignment")
	drainOrchestrateWork(t, u)
	w := u.client.Input.(*appServerTestInput)
	r := appServerOneRequest(t, w, "turn/steer", "child assignment")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"running"}}`, r.ID))
	got := <-c.reply
	if got.err != nil {
		t.Fatal(got.err)
	}
	appServerTestKeys(t, u, "ordinary user input\r")
	user := appServerOneRequest(t, w, "turn/steer", "ordinary user input")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"running"}}`, user.ID))
	u.draft = "unsent editor text"
	if err := u.keyboardEscape(); err != nil {
		t.Fatal(err)
	}
	interrupt := appServerOneRequest(t, w, "turn/interrupt", "")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, interrupt.ID))
	appServerTestTurnEnd(t, u, "running", "interrupted")
	drainOrchestrateWork(t, u)
	appServerOneRequest(t, w, "turn/start", "ordinary user input")
	if u.draft != "unsent editor text" {
		t.Fatalf("editor changed: %q", u.draft)
	}
}

func TestAppServerOrchestrateMainCanceledQueueDeparture(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"child-turn","status":"completed"}}}`)
	if u.orchestrateBusy() {
		t.Fatal("fixture was not idle")
	}
	u.settings.restoreEffort = true
	ctx, cancel := context.WithCancel(t.Context())
	mainFollowup(t, u, ctx, "canceled-queue", "child assignment")
	cancel()
	if err := u.tickOrchestratedViews(u); err != nil {
		t.Fatal(err)
	}
	if err := u.tickJournalReset(u.now()); err != nil {
		t.Fatal(err)
	}
	u.draft = "/quit"
	quit, err := u.key('\r')
	if err != nil {
		t.Fatal(err)
	}
	if !quit {
		t.Fatalf("canceled queue blocks idle departure: pending=%d notice=%q", len(u.orchestrateMainFollowups), u.notice)
	}
}

func TestAppServerOrchestrateMainLocalSendRejection(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	skill := filepath.Join(t.TempDir(), "SKILL.md")
	writeTestFile(t, skill, "Small skill body")
	u.picker.skillsLoaded, u.picker.skillsCwd = true, u.session.cwd
	u.picker.skills = []composerChoice{{name: "probe", path: skill, enabled: true}}
	text := strings.Repeat("x", composerTextLimit) + " $probe"
	c := mainFollowup(t, u, t.Context(), "local-reject", text)
	drainOrchestrateWork(t, u)
	w := u.client.Input.(*appServerTestInput)
	if w.Len() != 0 {
		t.Fatal("expected composer size guard")
	}
	select {
	case got := <-c.reply:
		if got.err == nil {
			t.Fatalf("local rejection reported success: %+v", got)
		}
	default:
		t.Fatalf("local composer rejection strands delivery: pending=%v notice=%q", u.orchestrateMainInput != nil, u.notice)
	}
}

func TestAppServerOrchestrateMainDeferredComposer(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	w := u.client.Input.(*appServerTestInput)
	u.draft = "user draft"
	input := orchestrate.Delivery{ID: "main-queue", From: "child", Target: "main", Message: "child context", Deferred: true}
	c := &orchestrateCommand{ctx: t.Context(), workspace: u.orchestrateThreads["child"].batch.Cwd, main: "child", target: "main", followup: true, deferred: true, callID: input.ID, input: orchestrateSpawnInput{Message: input.Message}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(c)
	drainOrchestrateWork(t, u)
	if got := <-c.reply; got.err != nil || got.delivery.State != "queued" || w.Len() != 0 || u.busy() {
		t.Fatal("queued input woke Main", got)
	}
	// A fresh storage owner supplies queued input; it is not an in-memory draft.
	store := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
	u.proxy.orchestration.store = store
	appServerTestTurn(t, u, "running")
	u.unsent = []composerDraft{{text: "user steer"}}
	if err := u.flushInput(); err != nil {
		t.Fatal(err)
	}
	r := appServerOneRequest(t, w, "turn/steer", "user steer")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"running"}}`, r.ID))
	appServerTestUserMessage(t, u, "user-input", r.Params.ClientUserMessageID, "user steer")
	appServerTestTurnEnd(t, u, "running", "completed")
	if d, _, err := store.BeginDelivery(t.Context(), u.session.cwd, "main", input); err != nil || d.State != "queued" {
		t.Fatal("steer consumed queued input", d, err)
	}
	// Ordinary input keeps image token positions while child text is prefixed.
	image := filepath.Join(t.TempDir(), "image.png")
	writeTestFile(t, image, "image bytes")
	user := composerDraft{text: "user [Image 1]", images: []composerImage{{start: 5, end: 14, path: image}}}
	if err := u.send([]composerDraft{user}, false); err != nil {
		t.Fatal(err)
	}
	drainOrchestrateWork(t, u)
	r = appServerOneRequest(t, w, "turn/start", "child context\nuser ")
	if len(r.Params.Input) != 2 || r.Params.Input[1].Path != image {
		t.Fatal("queued child input changed the user attachment", r)
	}
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, r.ID))
	if d, _, err := store.BeginDelivery(t.Context(), u.session.cwd, "main", input); err != nil || d.State != "queued" || u.draft != "user [Image 1]\nuser draft" {
		t.Fatal("rejected input lost queue or polluted user draft", d, err, u.draft)
	}
	c = mainFollowup(t, u, t.Context(), "idle-with-queue", "wake Main")
	drainOrchestrateWork(t, u)
	r = appServerOneRequest(t, w, "turn/start", "child context\nwake Main")
	orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turn":{"id":"next"}}}`, r.ID))
	if got := <-c.reply; got.err != nil || got.delivery.State != "delivered" {
		t.Fatal(got)
	}
	if d, _, err := store.BeginDelivery(t.Context(), u.session.cwd, "main", input); err != nil || d.State != "delivered" || d.TurnID != "next" {
		t.Fatal("queued acknowledgement missing", d, err)
	}
}
