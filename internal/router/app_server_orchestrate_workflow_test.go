package router

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestAppServerOrchestrateWorkflowComposer(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "steer"}[busy], func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.proxy = &mekugiProxy{orchestration: &orchestrateRuntime{}}
			u.session.cwd = t.TempDir()
			writeTestFile(t, filepath.Join(u.session.cwd, "issue.md"), "attached issue")
			method := "turn/start"
			if busy {
				u.turn, method = "running", "turn/steer"
			}
			appServerTestKeys(t, u, "/orchestrate Issue A\nIssue B ")
			bindComposerFile(u, "@issue.md", "issue.md")
			appServerTestKeys(t, u, "\r")
			request := btwTestRequest(t, wire, method, "main")
			var input string
			for _, part := range request.Params.Input {
				input += part.Text
			}
			if strings.Count(input, orchestrateWorkflow) != 1 || !strings.Contains(input, "Issue A\nIssue B") || strings.Contains(input, "/orchestrate Issue") {
				t.Fatalf("workflow input = %q", input)
			}
			if len(u.submission.files) != 1 || u.submission.text[u.submission.files[0].start:u.submission.files[0].end] != "@issue.md" || !strings.Contains(input, "attached issue") {
				t.Fatal("workflow lost its attached file or token position")
			}
		})
	}
}

func TestAppServerOrchestrateWorkflowUnavailable(t *testing.T) {
	for _, text := range []string{"/orchestrate", "/orchestrate issue"} {
		u, wire := newAppServerTestUI()
		appServerTestKeys(t, u, text+"\r")
		if wire.Len() != 0 || strings.TrimSpace(u.draft) != text || u.notice == "" {
			t.Fatalf("unavailable command lost its draft or dispatched: %q %q %s", u.draft, u.notice, wire.String())
		}
	}
}

func TestAppServerOrchestrateWorkflowChildView(t *testing.T) {
	for _, mode := range []string{"fresh", "resumed", "standalone"} {
		t.Run(mode, func(t *testing.T) {
			var u *appServerUI
			var wire *appServerTestInput
			if mode != "fresh" {
				owner, input, batch := retainedOrchestratedUI(t)
				u, wire = owner, input
				u.switchOrchestratedThread("child")
				drainOrchestrateWork(t, u)
				resumeBatchResponses(t, u, wire, batch, false)
			} else {
				owner, launch := orchestrateIdentityPendingTurn(t)
				u, wire = owner, owner.client.Input.(*appServerTestInput)
				orchestrateTestReply(t, u, launch, `{"turn":{"id":"batch-turn"}}`)
				u.switchOrchestratedThread("child")
			}
			child := u.viewedUI()
			if mode == "standalone" {
				// A fresh session has retained identity without the run navigator.
				child.navigation = nil
			}
			wire.Reset()
			writeTestFile(t, filepath.Join(child.session.cwd, "issue.md"), "attached issue")
			appServerTestKeys(t, child, "/orchestrate Issue ")
			bindComposerFile(child, "@issue.md", "issue.md")
			draft := child.draft
			appServerTestKeys(t, child, "\r")
			if wire.Len() != 0 || child.draft != draft || len(child.files) != 1 || !strings.Contains(child.notice, "Main") {
				t.Fatalf("child workflow dispatched or lost draft: %q %q %s", child.draft, child.notice, wire.String())
			}
		})
	}
}

func TestUISnapshotOrchestrateCommand(t *testing.T) {
	u, _ := newAppServerTestUI()
	appServerTestKeys(t, u, "/orc")
	uisnapshot.Assert(t, "testdata/snapshots/orchestrate-command-picker.txt", strings.Join(u.renderPicker(70, 4), "\n")+"\n")
}
