package router

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func mainFollowup(t *testing.T, u *appServerUI, ctx context.Context, id, text string) *orchestrateCommand {
	t.Helper()
	c := &orchestrateCommand{ctx: ctx, workspace: u.orchestrateThreads["child"].batch.Cwd, main: "child", target: "main", tool: "followup_task", callID: id, input: orchestrateSpawnInput{Message: text}, reply: make(chan orchestrateResult, 1)}
	u.startOrchestratedChild(c)
	return c
}

func TestAppServerOrchestrateMainReportsNeverSubmitUserInput(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprint(deferred), func(t *testing.T) {
			u, launch := orchestrateIdentityPendingTurn(t)
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
			w := u.client.Input.(*appServerTestInput)
			u.draft = "human draft"
			if deferred {
				appServerTestTurn(t, u, "human-turn")
			}
			c := &orchestrateCommand{ctx: t.Context(), workspace: u.orchestrateThreads["child"].batch.Cwd, main: "child", target: "main", tool: "followup_task", callID: "child-report", input: orchestrateSpawnInput{Message: "/compact"}, reply: make(chan orchestrateResult, 1)}
			if deferred {
				c.tool = "send_message"
			}
			u.startOrchestratedChild(c)
			drainOrchestrateWork(t, u)
			if w.Len() != 0 || len(c.reply) != 1 {
				t.Fatal("child report dispatched a user turn or waited for user admission")
			}
			got := <-c.reply
			if got.err != nil || got.delivery.State != "reported" || got.delivery.TurnID != "" || u.draft != "human draft" {
				t.Fatal("child report changed human input", got, u.draft)
			}

			if len(u.view.entries) != 1 || u.view.entries[0].Kind != "reply" || u.view.entries[0].message.from != "/root/batch" || strings.Contains(strings.Join(u.agents.renderFeed(90, 60).lines, "\n"), "/compact") {
				t.Fatal("report lost its transcript attribution or entered native Activity")
			}
			wait := &orchestrateCommand{ctx: t.Context(), workspace: u.session.cwd, main: "main", tool: "wait_agent", callID: "observe", reply: make(chan orchestrateResult, 1)}
			u.startOrchestratedChild(wait)
			drainOrchestrateWork(t, u)
			event := waitTestEvent(t, <-wait.reply)
			if event.Kind != "message" || event.Task != "batch" || event.Thread != "child" || event.Message != "/compact" {
				t.Fatal("report lost sender identity", event)
			}
			// A fresh store owner and repeated host call cannot submit or republish.
			u.proxy.orchestration.store = &orchestrate.Store{Directory: u.proxy.orchestration.store.Directory}
			u.startOrchestratedChild(c)
			drainOrchestrateWork(t, u)
			if got := <-c.reply; got.err != nil || got.delivery.State != "reported" || w.Len() != 0 {
				t.Fatal("repeated report caused another effect", got)
			}
			if deferred {
				appServerTestTurnEnd(t, u, "human-turn", "completed")
			}
			if err := u.send([]composerDraft{{text: "human input"}}, false); err != nil {
				t.Fatal(err)
			}
			appServerOneRequest(t, w, "turn/start", "human input")
		})
	}
}

func TestUISnapshotOrchestrateMainReport(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	u.clock = func() time.Time { return time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local) }
	u.view.clock, u.agents.clock = u.clock, u.clock
	u.view.painter.Theme = livediff.TerminalTheme
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"child-turn"}}`)
	mainFollowup(t, u, t.Context(), "reported", "Need the command or screenshot context.")
	drainOrchestrateWork(t, u)
	rows, _ := u.mainFrame(90, 10, 0)
	assertNativeUISnapshot(t, "orchestration-main-report", rows)
	uisnapshot.AssertTerminal(t, "testdata/snapshots/orchestration-main-report-styles.txt", append(rows, "plain after report"), 90)
}
