package router

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestAppServerOrchestratePickerSwitch(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	u.modelsLoading, v.modelsLoading = true, true
	wire := u.client.Input.(*appServerTestInput)
	if _, err := u.proxy.orchestration.store.Prepare(t.Context(), u.session.cwd, "main", "prepared"); err != nil {
		t.Fatal(err)
	}
	// A different coordinator's run cannot enter this picker.
	if _, err := u.proxy.orchestration.store.Prepare(t.Context(), u.session.cwd, "other", "private"); err != nil {
		t.Fatal(err)
	}
	v.draft = "child draft"
	appServerTestKeys(t, u, "/orchestrate\r")
	if !u.picker.loading || u.draft != "" || wire.Len() != 0 {
		t.Fatal("picker did not open without host input")
	}
	drainOrchestrateWork(t, u)
	if len(u.picker.choices) != 3 || u.picker.choices[2].display != "prepared" {
		t.Fatal("picker lost prepared work or exposed another run", u.picker.choices)
	}
	for _, key := range []byte("\x1b[200~hidden draft\x1b[201~") {
		if err := u.shell.key(key); err != nil {
			t.Fatal(err)
		}
	}
	if u.draft != "" {
		t.Fatal("picker paste changed a hidden draft")
	}
	appServerTestKeys(t, u, "\x1b[B\x1b[B\r")
	if u.viewedUI() != u || u.picker.modal != "orchestrate" || wire.Len() != 0 {
		t.Fatal("prepared selection dispatched host work")
	}
	// Live completion supersedes the retained launch state in an open picker.
	orchestrateTestMessage(t, u, `{"method":"turn/completed","params":{"threadId":"child","turn":{"id":"batch-turn","status":"failed"}}}`)
	u.renderPicker(100, 6)
	if !strings.Contains(u.picker.choices[1].description, v.status) || v.status == "Starting" {
		t.Fatal("picker did not consume the subscribed lifecycle")
	}
	appServerTestKeys(t, u, "\x1b[A\r")
	if u.viewedUI() != v || v.draft != "child draft" || wire.Len() != 0 || u.picker.modal != "" {
		t.Fatal("selection lost the child draft or started a turn")
	}
	// Opening from a child selects the same coordinator run and preserves Main's draft.
	u.draft = "main draft"
	v.draft = "/orchestrate"
	appServerTestKeys(t, v, "\r")
	drainOrchestrateWork(t, u)
	appServerTestKeys(t, v, "\r")
	if u.viewedUI() != u || u.draft != "main draft" || wire.Len() != 0 {
		t.Fatal("child picker did not return to Main without dispatch")
	}
	// Reopening only retained storage must not revive the prior subscribed view.
	fresh, freshWire := newAppServerTestUI()
	fresh.ctx, fresh.proxy = t.Context(), u.proxy
	fresh.session.start("main", u.session.cwd)
	fresh.ensureShell()
	t.Cleanup(func() { fresh.closeOrchestratedViews(); fresh.shell.diff.close(); fresh.shell.diffScreen.Close() })
	appServerTestKeys(t, fresh, "/orchestrate\r")
	drainOrchestrateWork(t, fresh)
	if description := fresh.picker.choices[1].description; !strings.Contains(description, "not subscribed") || !strings.Contains(description, "failed") {
		t.Fatal("retained thread lost its known lifecycle", description)
	}
	appServerTestKeys(t, fresh, "\x1b[B\r")
	drainOrchestrateWork(t, fresh)
	if fresh.navigation == nil || fresh.viewedUI() != fresh || fresh.picker.modal != "" || strings.Contains(freshWire.String(), "turn/start") {
		t.Fatal("retained selection skipped resume admission")
	}
	btwTestRequest(t, freshWire, "thread/read", "child")
}

func TestAppServerOrchestratePickerPromptPriority(t *testing.T) {
	for _, approval := range []bool{false, true} {
		t.Run(map[bool]string{false: "question", true: "approval"}[approval], func(t *testing.T) {
			u, v := orchestratePromptUI(t)
			u.draft = "/orchestrate"
			appServerTestKeys(t, u, "\r")
			drainOrchestrateWork(t, u)
			if approval {
				approvalTestCommand(t, u, "batch-approval", map[string]any{"threadId": "child", "turnId": "batch-turn", "command": "echo batch"})
				u.openApprovals()
			} else {
				orchestratePromptQuestion(t, u, true)
				u.openQuestions()
			}
			questionTestPaint(t, u, 80)
			orchestrateTestMessage(t, u, `{"method":"thread/tokenUsage/updated","params":{"threadId":"main","tokenUsage":{"total":{"inputTokens":2,"outputTokens":1}}}}`)
			if frame := questionTestPaint(t, u, 80); u.promptEditor() != v || u.picker.open || strings.Contains(frame, "Orchestration") {
				t.Fatal("picker obscured the source-owned prompt", frame)
			}
		})
	}
}

func TestAppServerOrchestratePickerReadLifetime(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	u.session.start("main", t.TempDir())
	u.proxy = &mekugiProxy{orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: t.TempDir()}}}
	// Closing while storage is queued invalidates only that picker instance.
	appServerTestKeys(t, u, "/orchestrate\r")
	appServerTestKeys(t, u, "\x1b")
	appServerTestKeys(t, u, "/orchestrate\r")
	select {
	case complete := <-u.orchestrateCompletions:
		u.completeOrchestrateWork(complete)
	case <-time.After(5 * time.Second):
		t.Fatal("picker read completion timeout")
	}
	if !u.picker.loading {
		t.Fatal("closed picker read replaced the new picker")
	}
	drainOrchestrateWork(t, u)
	if len(u.picker.choices) != 0 || !strings.Contains(u.picker.problem, "No batches") || wire.Len() != 0 {
		t.Fatal("empty run did not offer workflow entry", u.picker.problem)
	}
	appServerTestKeys(t, u, "\x1b")
	// A storage read error stays visible instead of becoming an empty run.
	u.proxy.orchestration.store.Directory = "relative"
	appServerTestKeys(t, u, "/orchestrate\r")
	drainOrchestrateWork(t, u)
	if !strings.Contains(u.picker.problem, "Read orchestration:") || len(u.picker.choices) != 0 || wire.Len() != 0 {
		t.Fatal("read failure became a successful empty picker", u.picker.problem)
	}
}

func TestUISnapshotOrchestrateRunPicker(t *testing.T) {
	u, launch := orchestrateIdentityPendingTurn(t)
	orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
	v := u.navigation.views["child"]
	at := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	u.clock, v.clock = func() time.Time { return at }, func() time.Time { return at }
	u.status = "Ready"
	u.proxy.usage = newThreadUsage()
	u.proxy.usage.observation("child", "child", "gpt-6-luna", "").observe(tokenCounts{InputTokens: 1200, UncachedInputTokens: 1200, OutputTokens: 30})
	orchestrateTestMessage(t, u, `{"method":"turn/started","params":{"threadId":"child","turn":{"id":"batch-turn"}}}`)
	orchestrateTestMessage(t, u, `{"method":"thread/tokenUsage/updated","params":{"threadId":"child","tokenUsage":{"total":{"inputTokens":1200,"outputTokens":30}}}}`)
	appServerTestKeys(t, u, "/orchestrate\r")
	drainOrchestrateWork(t, u)
	// The branch includes a temporary-workspace hash; fix only its display input.
	branch := "mekugi/0123456789abcdef0123456789abcdef/batch"
	u.orchestrateThreads["child"].batch.Branch = branch
	for _, test := range []struct {
		name  string
		width int
	}{{"wide", 110}, {"narrow", 45}} {
		t.Run(test.name, func(t *testing.T) {
			uisnapshot.Assert(t, filepath.Join("testdata/snapshots", "orchestration-run-picker-"+test.name+".txt"), strings.Join(u.renderPicker(test.width, 5), "\n")+"\n")
		})
	}
}
