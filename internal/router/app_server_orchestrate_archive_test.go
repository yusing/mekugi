package router

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
)

func completeOrchestratedArchive(t *testing.T, u *appServerUI) {
	t.Helper()
	for _, r := range u.orchestrateRequests {
		if r.method == "thread/archive" {
			w := u.client.Input.(*appServerTestInput)
			request := btwTestRequest(t, w, "thread/archive", "child")
			orchestrateTestReply(t, u, request, `{}`)
			return
		}
	}
}

func TestAppServerOrchestrateArchiveUncertain(t *testing.T) {
	for _, response := range []string{"lost", "rejected"} {
		t.Run(response, func(t *testing.T) {
			u := orchestrateCleanupUI(t)
			if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "accept", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
				t.Fatal(err)
			}
			u.flushOrchestratedEvents()
			drainOrchestrateWork(t, u)
			w := u.client.Input.(*appServerTestInput)
			request := btwTestRequest(t, w, "thread/archive", "child")
			if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("working")}}); err == nil {
				t.Fatal("archive intent did not retain accepted binding")
			}
			if response == "rejected" {
				orchestrateTestMessage(t, u, `{"id":`+strconv.Itoa(request.ID)+`,"error":{"code":-32603,"message":"archive failed"}}`)
				if !strings.Contains(u.notice, "archive failed") {
					t.Fatal("archive failure was not visible", u.notice)
				}
			}
			store := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
			batches, err := store.Snapshot(u.session.cwd, "main")
			if err != nil || batches[0].Archive == nil || batches[0].Archive.State == "archived" || batches[0].Integration == nil {
				t.Fatal("uncertain archive claimed success or lost integration", batches, err)
			}
			if _, err := store.BeginArchive(t.Context(), u.session.cwd, "main", "batch", func(func() error) error { t.Fatal("replayed archive"); return nil }); err == nil {
				t.Fatal("uncertain archive allowed another effect")
			}
			if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "accept", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil || len(u.proxy.orchestration.journalEvents) != 0 {
				t.Fatal("acceptance replay queued archival", err)
			}
		})
	}
}

func TestAppServerOrchestrateArchiveUnsentRetry(t *testing.T) {
	for _, blocker := range []string{"canceled", "pending input"} {
		t.Run(blocker, func(t *testing.T) {
			u := orchestrateCleanupUI(t)
			if _, err := u.proxy.applyJournal(u.ctx, u.session.cwd, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			c := &orchestrateCommand{ctx: ctx, workspace: u.session.cwd, main: "main", target: "batch", tool: "cleanup", reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(c)
			joined := *c
			joined.ctx, joined.reply = t.Context(), make(chan orchestrateResult, 1)
			u.startOrchestratedChild(&joined)
			for step := range 2 {
				select {
				case complete := <-u.orchestrateCompletions:
					// First remove the checkout, then pause after durable archive
					// intent but before the UI callback can dispatch to Codex.
					if step == 1 {
						if blocker == "canceled" {
							cancel()
						} else {
							u.navigation.views["child"].draft = "keep this input"
						}
					}
					u.completeOrchestrateWork(complete)
				case <-time.After(5 * time.Second):
					t.Fatal("cleanup storage timeout")
				}
			}
			drainOrchestrateWork(t, u)
			result := <-c.reply
			if result.err == nil || result.batch.Archive == nil || result.batch.Archive.State != "not_sent" || u.client.Input.(*appServerTestInput).Len() != 0 {
				t.Fatal("known-unsent archive was not retained correctly", result)
			}
			if got := <-joined.reply; got.err != result.err || got.batch.Archive.State != "not_sent" {
				t.Fatal("joined caller lost the owner's failure", got)
			}
			// A fresh store owner must permit an explicit retry, once idle.
			store := &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
			u.proxy.orchestration.store = store
			u.navigation.views["child"].draft = ""
			orchestrateTargetMCPClient(t, u, "cleanup")("main", false)
			if u.orchestrateThreads["child"].batch.Archive.State != "archived" {
				t.Fatal("explicit retry did not archive child")
			}
			// Even the already-archived no-effect path must recheck authority.
			denied := errors.New("acceptance unavailable")
			if _, err := store.BeginArchive(t.Context(), u.session.cwd, "main", "batch", func(func() error) error { return denied }); !errors.Is(err, denied) {
				t.Fatal("archived no-op skipped authorization", err)
			}
		})
	}
}

func TestAppServerOrchestrateArchivedHistory(t *testing.T) {
	old := orchestrateCleanupUI(t)
	child := old.orchestrateThreads["child"]
	if _, err := old.proxy.applyJournal(old.ctx, child.batch.Cwd, "child", "", []journalMutation{{Op: "add", Kind: "note", Title: new("Retained child result")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.proxy.applyJournal(old.ctx, old.session.cwd, "main", "", []journalMutation{{Op: "set", P: "/1", State: new("accepted")}}); err != nil {
		t.Fatal(err)
	}
	orchestrateTargetMCPClient(t, old, "cleanup")("main", false)
	store, workspace, replay := old.proxy.orchestration.store, old.session.cwd, old.proxy.replayStore
	batch := old.orchestrateThreads["child"].batch
	old.closeOrchestratedViews()
	u, w := resumeOrchestrationMain(t, store, workspace, replay)
	drainOrchestrateWork(t, u)
	w.Reset()
	var ready bool
	u.resumeOrchestratedBatch("batch", func(err error) {
		if err != nil {
			t.Fatal(err)
		}
		ready = true
	})
	drainOrchestrateWork(t, u)
	read := btwTestRequest(t, w, "thread/read", "child")
	thread := map[string]any{"id": "child", "cwd": batch.Cwd, "turns": []any{map[string]any{"id": "initial", "status": "completed", "items": []any{map[string]any{"id": "answer", "type": "agentMessage", "text": "Archived batch answer", "channel": "final_answer"}}}}}
	btwTestReply(t, u, read, string(mustMarshalJSON(map[string]any{"thread": thread})))
	for range 2 {
		list := btwTestRequest(t, w, "thread/list", "")
		btwTestReply(t, u, list, `{"data":[]}`)
	}
	if !ready || w.Len() != 0 || u.viewedUI() != u {
		t.Fatal("archived history resumed host execution or lost navigation", ready, w.String())
	}
	v := u.navigation.views["child"]
	if v.journal.tree == nil || !slices.ContainsFunc(v.journal.tree.Items, func(item journalItem) bool { return item.Title == "Retained child result" }) {
		t.Fatal("archived journal was not restored")
	}
	if !slices.ContainsFunc(v.view.entries, func(e liveActivityRecord) bool { return e.Text == "Archived batch answer" }) || !v.orchestrateCheckoutUnavailable() {
		t.Fatal("archived transcript or input guard was lost", v.view.entries)
	}
}
