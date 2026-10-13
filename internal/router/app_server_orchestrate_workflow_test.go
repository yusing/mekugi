package router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/orchestrate"
	"github.com/yusing/mekugi/internal/uisnapshot"
)

func TestAppServerOrchestrateWorkflowComposer(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "steer"}[busy], func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ctx = t.Context()
			replay, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			u.proxy = &mekugiProxy{journals: newJournalStore(), replayStore: replay, orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: t.TempDir()}}}
			u.session.start("main", t.TempDir())
			t.Cleanup(func() { u.closeOrchestrateStorage() })
			writeTestFile(t, filepath.Join(u.session.cwd, "issue.md"), "attached issue")
			method := "turn/start"
			if busy {
				u.turn, method = "running", "turn/steer"
			}
			appServerTestKeys(t, u, "/orchestrate Issue A\nIssue B ")
			bindComposerFile(u, "@issue.md", "issue.md")
			appServerTestKeys(t, u, "\r")
			if wire.Len() != 0 {
				t.Fatal("input sent before run identity was saved")
			}
			drainOrchestrateWork(t, u)
			if batches, err := u.proxy.orchestration.store.Snapshot(u.session.cwd, "main"); err != nil || len(batches) != 0 {
				t.Fatal("initial command did not retain an empty run", batches, err)
			}
			fresh := &mekugiProxy{journals: newJournalStore(), replayStore: replay}
			projected := parsedResponsesRequest{fields: map[string]json.RawMessage{}}
			if _, err := fresh.projectOrchestrateInstructions(t.Context(), &projected, "main", codexTurnMetadata{}, true); err != nil || !strings.Contains(jsonString(projected.fields, "instructions"), orchestrateOwnershipInstructions) {
				t.Fatal("first turn lacks durable policy", err)
			}
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
			drainOrchestrateWork(t, child)
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

func TestAppServerOrchestrateWorkflowStartupInput(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "canceled"}[canceled], func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ctx = t.Context()
			u.proxy = &mekugiProxy{journals: newJournalStore(), orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: t.TempDir()}}}
			u.session.start("main", t.TempDir())
			t.Cleanup(func() { u.closeOrchestrateStorage() })
			appServerTestKeys(t, u, "/orchestrate first\r")
			if canceled {
				appServerTestKeys(t, u, "\x03")
			} else {
				appServerTestKeys(t, u, "second\r")
			}
			if wire.Len() != 0 {
				t.Fatal("startup input escaped before identity persistence")
			}
			drainOrchestrateWork(t, u)
			if canceled {
				if wire.Len() != 0 || !strings.Contains(u.draft, "/orchestrate first") {
					t.Fatal("canceled startup dispatched or lost draft", u.draft, wire.String())
				}
				return
			}
			request := btwTestRequest(t, wire, "turn/start", "main")
			var text string
			for _, part := range request.Params.Input {
				text += part.Text
			}
			if first, second := strings.Index(text, "first"), strings.Index(text, "second"); first < 0 || second < first {
				t.Fatal("input order changed", text)
			}
		})
	}
}

func TestAppServerOrchestrateWorkflowQueuedPosition(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	u.proxy = &mekugiProxy{journals: newJournalStore(), orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: t.TempDir()}}}
	u.session.start("main", t.TempDir())
	t.Cleanup(func() { u.closeOrchestrateStorage() })
	u.turn = "running"
	appServerTestKeys(t, u, "inflight\r")
	first := appServerOneRequest(t, wire, "turn/steer", "inflight")
	appServerTestKeys(t, u, "older\r/orchestrate newer\r")
	drainOrchestrateWork(t, u)
	if wire.Len() != 0 {
		t.Fatal("startup bypassed the pending steer")
	}
	pending := parsedResponsesRequest{fields: map[string]json.RawMessage{}}
	metadata := codexTurnMetadata{Directories: map[string]json.RawMessage{u.session.cwd: nil}}
	if _, err := u.proxy.projectOrchestrateInstructions(t.Context(), &pending, "main", metadata, true); err != nil || strings.Contains(jsonString(pending.fields, "instructions"), orchestrateOwnershipInstructions) {
		t.Fatal("queued input activated masking before admission", err)
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"turnId":"running"}}`, first.ID))
	drainOrchestrateWork(t, u)
	requests := appServerTurnRequests(t, wire)
	if len(requests) != 1 || !strings.HasPrefix(requests[0].text(), "older\n"+orchestrateWorkflow) || !strings.HasSuffix(requests[0].text(), "newer") {
		t.Fatalf("startup reordered queued input: %+v", requests)
	}
}

func TestAppServerOrchestrateWorkflowCancelAfterPersistence(t *testing.T) {
	for _, state := range []string{"new", "existing", "locator"} {
		existing := state != "new"
		t.Run(state, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ctx = t.Context()
			replay, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			store := &orchestrate.Store{Directory: t.TempDir()}
			u.proxy = &mekugiProxy{journals: newJournalStore(), replayStore: replay, orchestration: &orchestrateRuntime{store: store}}
			u.session.start("main", t.TempDir())
			t.Cleanup(func() { u.closeOrchestrateStorage() })
			if existing {
				for _, err := range []error{
					u.proxy.journals.initialize(u.ctx, replay, u.session.cwd, "main", "/root", ""),
					u.proxy.journals.bindIdentity(u.ctx, replay, u.session.cwd, "main", "", "/root", true),
					store.Start(u.ctx, u.session.cwd, "main"),
					u.proxy.journals.bindRun(u.ctx, replay, u.session.cwd, "main", journalRun{Directory: store.Directory, Workspace: u.session.cwd, Main: "main"}),
				} {
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if state == "locator" {
				if err := u.proxy.journals.transaction(u.ctx, replay, u.session.cwd, "main", func(j *threadJournal, _ bool) error {
					j.Orchestration = nil
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			appServerTestKeys(t, u, "/orchestrate canceled\r")
			var complete func()
			select {
			case complete = <-u.orchestrateCompletions:
			case <-time.After(5 * time.Second):
				t.Fatal("startup storage timeout")
			}
			// The worker has finished all persistence. The UI has not admitted input.
			appServerTestKeys(t, u, "\x03")
			u.completeOrchestrateWork(complete)
			drainOrchestrateWork(t, u)
			if wire.Len() != 0 || strings.Count(u.draft, "/orchestrate canceled") != 1 {
				t.Fatal("cancellation dispatched or duplicated input", u.draft, wire.String())
			}
			scope, _, err := replay.readHandleScope("main")
			if err != nil || (scope.Coordinator != nil) != existing {
				t.Fatal("cancellation changed retained coordinator authority", scope.Coordinator, err)
			}
			fresh := &mekugiProxy{journals: newJournalStore(), replayStore: replay}
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{}}
			if _, err := fresh.projectOrchestrateInstructions(t.Context(), &request, "main", codexTurnMetadata{Directories: map[string]json.RawMessage{u.session.cwd: nil}}, true); err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(jsonString(request.fields, "instructions"), orchestrateOwnershipInstructions); got != existing {
				t.Fatalf("cancellation changed durable membership: active=%v existing=%v", got, existing)
			}
		})
	}
}

func TestAppServerOrchestrateStartupFailureRestoresStack(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	storage := filepath.Join(t.TempDir(), "blocked")
	writeTestFile(t, storage, "not a directory")
	u.proxy = &mekugiProxy{journals: newJournalStore(), orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: storage}}}
	u.session.start("main", t.TempDir())
	t.Cleanup(func() { u.closeOrchestrateStorage() })
	appServerTestKeys(t, u, "/orchestrate first\rsecond\r")
	drainOrchestrateWork(t, u)
	if wire.Len() != 0 || len(u.unsent)+len(u.queued) != 0 || u.draft != "/orchestrate first\nsecond" {
		t.Fatal("startup failure lost stack order", u.draft, u.unsent, u.queued, wire.String())
	}
}

func TestAppServerOrchestrateSubmitFollowsTranscript(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	u.proxy = &mekugiProxy{journals: newJournalStore(), orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: t.TempDir()}}}
	u.session.start("main", t.TempDir())
	t.Cleanup(func() { u.closeOrchestrateStorage() })
	u.view.following, u.view.unseen = false, 3
	appServerTestKeys(t, u, "/orchestrate first\r")
	if !u.view.following || u.view.unseen != 0 {
		t.Fatal("native command did not resume transcript follow")
	}
	drainOrchestrateWork(t, u)
	if wire.Len() == 0 || !u.view.following {
		t.Fatal("submitted input remains below paused viewport")
	}
}

func TestAppServerOrchestrateAdmissionKeepsUIResponsive(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	u.proxy = &mekugiProxy{journals: newJournalStore(), orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: t.TempDir()}}}
	u.session.start("main", t.TempDir())
	t.Cleanup(func() { u.closeOrchestrateStorage() })
	release, err := u.proxy.journals.lockState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	returned := make(chan struct{})
	go func() {
		appServerTestKeys(t, u, "/orchestrate first\rsecond\r\x03")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		release()
		<-returned
		t.Fatal("UI blocked on admission journal lock")
	}
	release()
	drainOrchestrateWork(t, u)
	if wire.Len() != 0 || u.draft != "/orchestrate first\nsecond" {
		t.Fatal("admission cancellation lost input", u.draft, wire.String())
	}
}

func TestAppServerOrchestrateOversizedInputDoesNotBindRun(t *testing.T) {
	u, wire := newAppServerTestUI()
	u.ctx = t.Context()
	u.proxy = &mekugiProxy{journals: newJournalStore(), orchestration: &orchestrateRuntime{store: &orchestrate.Store{Directory: t.TempDir()}}}
	u.session.start("main", t.TempDir())
	t.Cleanup(func() { u.closeOrchestrateStorage() })
	writeTestFile(t, filepath.Join(u.session.cwd, "issue.md"), "attached issue")
	u.loadDraft(composerDraft{text: "/orchestrate " + strings.Repeat("x", composerTextLimit) + " "})
	bindComposerFile(u, "@issue.md", "issue.md")
	u.submitOrchestrate()
	drainOrchestrateWork(t, u)
	if entries, err := os.ReadDir(u.proxy.orchestration.store.Directory); err != nil || len(entries) != 0 {
		t.Fatal("rejected input created run storage", entries, err)
	}
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{}}
	if _, err := u.proxy.projectOrchestrateInstructions(t.Context(), &request, "main", codexTurnMetadata{Directories: map[string]json.RawMessage{u.session.cwd: nil}}, true); err != nil {
		t.Fatal(err)
	}
	if wire.Len() != 0 || !strings.HasPrefix(u.draft, "/orchestrate ") || !u.noticeAlert || strings.Contains(jsonString(request.fields, "instructions"), orchestrateOwnershipInstructions) {
		t.Fatal("rejected input activated masking or lost the command")
	}
}

func TestAppServerOrchestrateHostRejectionReleasesOwnedRun(t *testing.T) {
	for _, state := range []string{"new", "existing", "prepared"} {
		t.Run(state, func(t *testing.T) {
			u, wire := newAppServerTestUI()
			u.ctx = t.Context()
			replay, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			store := &orchestrate.Store{Directory: t.TempDir()}
			u.proxy = &mekugiProxy{journals: newJournalStore(), replayStore: replay, orchestration: &orchestrateRuntime{store: store}}
			u.session.start("main", orchestrateVCSWorkspace(t, "git"))
			t.Cleanup(func() { u.closeOrchestrateStorage() })
			if state == "existing" {
				for _, err := range []error{
					u.proxy.journals.initialize(u.ctx, replay, u.session.cwd, "main", "/root", ""),
					u.proxy.journals.bindIdentity(u.ctx, replay, u.session.cwd, "main", "", "/root", true),
					store.Start(u.ctx, u.session.cwd, "main"),
					u.proxy.journals.bindRun(u.ctx, replay, u.session.cwd, "main", journalRun{Directory: store.Directory, Workspace: u.session.cwd, Main: "main"}),
				} {
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			original := "/orchestrate " + strings.Repeat("x", composerTextLimit)
			u.loadDraft(composerDraft{text: original})
			u.submitOrchestrate()
			drainOrchestrateWork(t, u)
			start := btwTestRequest(t, wire, "turn/start", "main")
			if state == "prepared" {
				if _, err := store.Prepare(t.Context(), u.session.cwd, "main", "used"); err != nil {
					t.Fatal(err)
				}
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-32602,"message":"input too large"}}`, start.ID))
			if u.draft != original {
				t.Fatal("host rejection lost the original command")
			}
			u.loadDraft(composerDraft{text: "ordinary"})
			appServerTestKeys(t, u, "\r")
			if state != "existing" && wire.Len() != 0 {
				t.Fatal("input escaped before binding cleanup")
			}
			drainOrchestrateWork(t, u)
			appServerOneRequest(t, wire, "turn/start", "ordinary")
			scope, _, err := replay.readHandleScope("main")
			if err != nil || (scope.Coordinator != nil) != (state != "new") {
				t.Fatal("rejection changed retained coordinator authority", scope.Coordinator, err)
			}
			fresh := &mekugiProxy{journals: newJournalStore(), replayStore: replay}
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{}}
			if _, err := fresh.projectOrchestrateInstructions(t.Context(), &request, "main", codexTurnMetadata{Directories: map[string]json.RawMessage{u.session.cwd: nil}}, true); err != nil {
				t.Fatal(err)
			}
			if active := strings.Contains(jsonString(request.fields, "instructions"), orchestrateOwnershipInstructions); active != (state != "new") {
				t.Fatalf("rejected %s run retains masking=%v", state, active)
			}
		})
	}
}
