package router

import (
	"fmt"
	"strings"
	"testing"
)

func TestAppServerOrchestrateAncestryReadRace(t *testing.T) {
	for _, order := range []string{"identity-before-error", "error-before-identity", "metadata-before-error", "metadata-read", "completion-before-identity", "unrelated-notice"} {
		t.Run(order, func(t *testing.T) {
			replay, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			u, launch := orchestrateIdentityPendingTurnWithReplay(t, replay)
			u.shell.diff.store = replay
			orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
			u.proxy.activity = newSubagentActivity()
			u.proxy.activity.attachNativePane("child")
			v := u.navigation.views["child"]
			auto, stop := newAutoLiveDiff(t.Context(), "")
			t.Cleanup(stop)
			auto.enable()
			u.proxy.autoLiveDiff, u.shell.auto, v.shell.auto = auto, auto, auto
			sub := auto.events.subscribe()
			for _, thread := range []string{"child", "native", "foreign"} {
				auto.includeThread(v.session.cwd, thread, thread == "child")
			}
			for len(sub.events) > 0 {
				u.applyOrchestrationDiff(t.Context(), <-sub.events)
			}
			if v.shell.diff.scope.Workspaces[v.session.cwd]["native"] {
				t.Fatal("unknown descendant entered Diff scope")
			}
			orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"native","turn":{"id":"native-turn"}}}`)
			read := btwTestRequest(t, u.client.Input.(*appServerTestInput), "thread/read", "native")
			for _, name := range []string{"first.go", "second.go"} {
				orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"item/completed","params":{"threadId":"native","turnId":"native-turn","item":{"id":%q,"type":"commandExecution","command":%q,"status":"completed"}}}`, name, "cat "+name))
			}
			fail := func() {
				orchestrateTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"metadata not ready"}}`, read.ID))
			}
			if order == "error-before-identity" || order == "completion-before-identity" || order == "unrelated-notice" {
				fail()
				if !strings.Contains(u.notice, "ancestry unavailable") {
					t.Fatal("unresolved ancestry lost its notice", u.notice)
				}
			}
			if order == "completion-before-identity" {
				orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"native","turn":{"id":"native-turn","status":"completed"}}}`)
			}
			if order == "metadata-before-error" {
				orchestrateTestMessage(t, u, `{"method":"thread/started","params":{"thread":{"id":"native","parentThreadId":"child","agentNickname":"worker"}}}`)
			} else if order == "metadata-read" {
				orchestrateTestReply(t, u, read, `{"thread":{"id":"native","parentThreadId":"child","agentNickname":"worker"}}`)
			} else {
				u.proxy.activity.observe("native", "child", "/root/worker", true)
			}
			if order == "identity-before-error" || order == "metadata-before-error" {
				fail()
			}
			if order == "unrelated-notice" {
				u.setNotice("Unrelated failure", true)
			}
			if err := u.tickOrchestratedViews(u); err != nil {
				t.Fatal(err)
			}
			if len(u.navigation.pending) != 0 || len(u.navigation.unavailable) != 0 || strings.Contains(u.notice, "ancestry unavailable") {
				t.Fatal("resolved ancestry retained pending events or stale failure", len(u.navigation.pending), u.notice)
			}
			if order == "unrelated-notice" && u.notice != "Unrelated failure" {
				t.Fatal("ancestry recovery cleared another owner's notice", u.notice)
			}
			if scope := v.shell.diff.scope.Workspaces[v.session.cwd]; !scope["native"] || scope["foreign"] || u.shell.diff.scope.Workspaces[v.session.cwd]["native"] {
				t.Fatal("late ancestry lost or crossed the existing Diff scope", scope)
			}
			feed := strings.Join(v.agents.renderFeed(90, 60).lines, "\n")
			if a, b := strings.Index(feed, "first.go"), strings.Index(feed, "second.go"); a < 0 || b <= a || strings.Count(feed, "first.go") != 1 || strings.Count(feed, "second.go") != 1 {
				t.Fatal("buffered native Activity was empty, duplicated or reordered", feed)
			}
			if v.session.agent("/root/worker").Responding != (order != "completion-before-identity") || strings.Contains(strings.Join(u.agents.renderFeed(90, 60).lines, "\n"), "first.go") {
				t.Fatal("native lifecycle or Activity crossed roots")
			}
		})
	}
}

func TestAppServerOrchestrateAncestryIsolation(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	u.proxy.activity = newSubagentActivity()
	u.proxy.activity.attachNativePane("child")
	u.proxy.activity.observe("foreign", "", "/root", false)
	u.proxy.activity.observe("incomplete", "missing", "/root/worker", true)
	u.proxy.activity.observe("unsubscribed", "foreign", "/root/worker", true)
	u.proxy.activity.observe("conflicted", "child", "/root/worker", true)
	u.proxy.activity.observe("conflicted", "foreign", "/root/worker", true)
	for _, thread := range []string{"incomplete", "unsubscribed", "conflicted"} {
		orchestrateTestMessage(t, u, fmt.Sprintf(`{"method":"turn/started","params":{"threadId":%q,"turn":{"id":"turn"}}}`, thread))
	}
	if err := u.tickOrchestratedViews(u); err != nil {
		t.Fatal(err)
	}
	if len(u.navigation.pending) != 3 || len(u.navigation.views["child"].session.threads) != 1 {
		t.Fatal("unproven ancestry entered a subscribed tree", u.navigation.views["child"].session.threads)
	}
}
