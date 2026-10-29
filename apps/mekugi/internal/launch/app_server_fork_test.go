package launch

import (
	json "encoding/json/v2"
	"fmt"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessioncontrol"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"
	"github.com/yusing/mekugi/libraries/diffrender"
	changepresentation "github.com/yusing/mekugi/modules/changehistory/presentation"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

func TestUISnapshotNativeFork(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	u.controlObservations = new(sessioncontrol.Observations)
	u.shell.Transcript.Painter.Theme = diffrender.DarkTheme
	u.clock = func() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local) }
	u.model, u.reasoningEffort, u.serviceTier = "model", "high", "priority"
	appServerTestKeys(t, u, "/fork\r")
	fork := resumeTestOne(t, w, "thread/fork")
	if fork.Params.ThreadID != "main" || !u.Replacement.Forking() {
		t.Fatal("fork did not target Main")
	}
	appServerTestKeys(t, u, "next draft")
	rows, _ := u.testMainFrame(100, 12, 0)
	assertNativeUISnapshot(t, "native-fork-pending", rows)
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","serviceTier":"priority","thread":{"id":"branch","cwd":"/work","turns":[{"id":"done","status":"completed","items":[{"type":"agentMessage","id":"answer","text":"Inherited answer"}]}]}}}`, fork.ID))
	methods := resumeTestSettle(t, u, w)
	if u.controlObservations.ContextRevision("branch") != 1 {
		t.Fatal("accepted fork did not refresh context")
	}
	if u.Thread != "branch" || u.Text != "next draft" || u.Replacement.Pending() || u.model != "model" || u.reasoningEffort != "high" || u.serviceTier != "priority" {
		t.Fatalf("fork state: %s %q %+v", u.Thread, u.Text, u.Live)
	}
	for _, method := range methods {
		if method == "turn/start" || method == "thread/archive" {
			t.Fatalf("fork caused %s", method)
		}
	}
	appServerTestMessage(t, u, `{"method":"item/completed","params":{"threadId":"main","turnId":"late","item":{"type":"agentMessage","id":"late","text":"Late original answer"}}}`)
	rows, _ = u.testMainFrame(100, 12, 0)
	assertNativeUISnapshot(t, "native-fork-ready", rows)
	// The original is still selectable through the established resume path.
	u.Editor.Take()
	appServerTestKeys(t, u, "/resume main\r")
	if read := resumeTestOne(t, w, "thread/read"); read.Params.ThreadID != "main" {
		t.Fatal("original was not resumable")
	}
}

func TestAppServerForkRejectsAndRestoresInput(t *testing.T) {
	for _, response := range []string{`"error":{"code":-1,"message":"unavailable"}`, `"result":{"thread":{"id":"main"}}`, `"result":{"thread":{}}`} {
		t.Run(response, func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			appServerTestKeys(t, u, "/fork\r")
			fork := resumeTestOne(t, w, "thread/fork")
			appServerTestKeys(t, u, "waiting\rnewer")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,%s}`, fork.ID, response))
			if u.Thread != "main" || u.Replacement.Pending() || u.Text != "waiting\nnewer" || !strings.Contains(u.attached.feedback.Text, "Could not fork") || w.Len() != 0 {
				t.Fatalf("failure lost original/input: %s %q %q %s", u.Thread, u.Text, u.attached.feedback.Text, w.Bytes())
			}
		})
	}
	for _, command := range []string{"/fork extra", "/fork", "/rewind extra", "/rewind"} {
		u, w := newResumeSessionTestUI(t)
		if command == "/fork" || command == "/rewind" {
			u.Turn = "busy"
		}
		appServerTestKeys(t, u, command+"\r")
		if w.Len() != 0 || !u.attached.feedback.Alert || strings.TrimSpace(u.Text) != command {
			t.Fatal("invalid/busy fork was sent or consumed")
		}
	}
}

func TestAppServerForkRetainsEvidenceBeforeSwitch(t *testing.T) {
	for _, command := range []string{"/fork", "/rewind"} {
		t.Run(command, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "file.go")
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			parent, releaseParent := retentionTestSession(t, store, "main", 0)
			parent = bindTestHandleScope(t, store, parent, "", "")
			store = store.scoped(parent)
			output, err := store.putShellOutput(parent, strings.Repeat("retained output\n", 100), "", 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("after"), 0600); err != nil {
				t.Fatal(err)
			}
			liveDiffScopeCapture(t, store, workspace, "main", "edit", path, "before", "after")
			child, releaseChild := retentionTestSession(t, store, "child", 0)
			child = bindTestHandleScope(t, store, child, "main", "")
			liveDiffScopeCapture(t, store.scoped(child), workspace, "child", "child-edit", filepath.Join(workspace, "child.go"), "before", "after")
			other := t.TempDir()
			liveDiffScopeCapture(t, store.scoped(child), other, "child", "other-child-edit", filepath.Join(other, "other.go"), "before", "after")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			u, w := newResumeSessionTestUI(t)
			u.startSession("main", workspace)
			gate := &forkTestChanges{Service: store.changes, entered: make(chan struct{}), release: make(chan struct{})}
			u.retained, u.changeSource = store.changeScope(), gate
			u.reasoningEffort = "high"
			if command == "/rewind" {
				u.changeSource = store.changes
				rewindTestTurns(t, u, w)
				appServerTestKeys(t, u, "\r\r")
			} else {
				appServerTestKeys(t, u, "/fork\r")
			}
			fork := resumeTestOne(t, w, "thread/fork")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","thread":{"id":"branch","cwd":%q}}}`, fork.ID, workspace))
			if u.Thread != "main" || u.Startup.ForkRetention == nil {
				t.Fatal("switched before retained admission")
			}
			if command == "/fork" {
				<-gate.entered
			}
			appServerTestKeys(t, u, "preserved while retaining")
			if command == "/fork" {
				close(gate.release)
			}
			pending := u.Startup.ForkRetention
			pending.Err, pending.Complete = <-pending.Done, true
			if err := u.message(pending.Message); err != nil {
				t.Fatal(err)
			}
			resumeTestSettle(t, u, w)
			identity, exists, err := store.publication.ReadScope("branch")
			if err != nil || !exists || identity.Fork != "main" || u.Thread != "branch" || u.Text != "preserved while retaining" {
				t.Fatalf("durable fork: %+v %v %v %s %q", identity, exists, err, u.Thread, u.Text)
			}
			wantNamespace := "branch"
			if command == "/rewind" {
				wantNamespace = "main"
			}
			if identity.Namespace != wantNamespace {
				t.Fatalf("retained namespace: %+v", identity)
			}
			// A fresh store and projection need no live parent to recover inherited review.
			fresh, err := openMekugiReplayStore(store.publication.Directory())
			if err != nil {
				t.Fatal(err)
			}
			projection := sessionreview.Projection{Changes: fresh.changes, Retained: fresh.changeScope(), Data: new(changepresentation.Snapshot)}
			scope := previewmodel.Scope{Workspaces: map[string]map[string]bool{workspace: {"branch": true}}}
			if _, err := projection.Apply(t.Context(), sessionreview.Event{Kind: "scope", Scope: &scope, Resync: true}); err != nil {
				t.Fatal(err)
			}
			if len(projection.Data.Publication.Files) != 3 {
				t.Fatalf("fresh fork lost retained review: %+v", projection.Data.Publication)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatal("fork modified workspace files")
			}
			// Rewind keeps the live family, while an ordinary fork freezes evidence.
			liveDiffScopeCapture(t, store, workspace, "main", "later", filepath.Join(workspace, "later.go"), "a", "b")
			liveDiffScopeCapture(t, store.scoped(child), workspace, "child", "later-child", filepath.Join(workspace, "later-child.go"), "a", "b")
			if _, err := projection.Apply(t.Context(), sessionreview.Event{Kind: "scope", Scope: &scope, Resync: true}); err != nil {
				t.Fatal(err)
			}
			wantFiles := 3
			if command == "/rewind" {
				wantFiles = 5
			}
			if len(projection.Data.Publication.Files) != wantFiles {
				t.Fatalf("late family edits: got %d files, want %d", len(projection.Data.Publication.Files), wantFiles)
			}
			releaseParent()
			releaseChild()
			retentionTestAge(t, store, "child", 30*24*time.Hour)
			retentionTestAge(t, store, "main", 30*24*time.Hour)
			branch, _ := retentionTestSession(t, fresh, "branch", 0)
			if err := fresh.cleanupSessions(branch); err != nil {
				t.Fatal(err)
			}
			if _, err := fresh.recoverOutput(branch, output); err != nil {
				t.Fatalf("fresh fork lost inherited output: %v", err)
			}
		})
	}
}

// Stall the existing publication participant to exercise input during storage.
type forkTestChanges struct {
	changes.Service
	entered, release chan struct{}
}

func (s *forkTestChanges) PrepareFork(selected changes.Scope, source, dest, fork string) (changes.ForkPlan, error) {
	close(s.entered)
	<-s.release
	return s.Service.PrepareFork(selected, source, dest, fork)
}

func TestAppServerForkRetentionFailureKeepsOriginal(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A non-file destination makes publication fail after host acceptance.
	if err := os.Mkdir(filepath.Join(store.publication.Directory(), history.ScopeName("branch")), 0700); err != nil {
		t.Fatal(err)
	}
	u, w := newResumeSessionTestUI(t)
	u.retained, u.changeSource = store.changeScope(), store.changes
	appServerTestKeys(t, u, "/fork\r")
	fork := resumeTestOne(t, w, "thread/fork")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"thread":{"id":"branch","cwd":"/work"}}}`, fork.ID))
	pending := u.Startup.ForkRetention
	pending.Err, pending.Complete = <-pending.Done, true
	if pending.Err == nil {
		t.Fatal("publication unexpectedly succeeded")
	}
	if err := u.message(pending.Message); err != nil {
		t.Fatal(err)
	}
	if u.Thread != "main" || u.Replacement.Pending() || !strings.Contains(u.attached.feedback.Text, "retaining history") {
		t.Fatal("failed retention replaced or blocked original")
	}
	if cleanup := resumeTestOne(t, w, "thread/unsubscribe"); cleanup.Params.ThreadID != "branch" {
		t.Fatal("failure released original")
	}
	appServerTestMessage(t, u, `{"method":"thread/started","params":{"thread":{"id":"branch","cwd":"/work"}}}`)
	appServerTestKeys(t, u, "continue original\r")
	if start := resumeTestOne(t, w, "turn/start"); start.Params.ThreadID != "main" {
		t.Fatal("original unusable after retention failure")
	}

}

func TestAppServerForkDefaultEffortAdmission(t *testing.T) {
	for _, outcome := range []string{"ack first", "confirmation first", "rejected", "mode unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			u, w := newResumeSessionTestUI(t)
			u.model, u.reasoningEffort = "model", ""
			appServerTestKeys(t, u, "/fork\r")
			fork := resumeTestOne(t, w, "thread/fork")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","thread":{"id":"branch","cwd":"/work"}}}`, fork.ID))
			read := resumeTestOne(t, w, "thread/resume")
			if read.Params.ThreadID != "branch" || u.Thread != "main" {
				t.Fatal("fork switched before obtaining default-effort settings")
			}
			appServerTestKeys(t, u, "waiting\rnewer")
			mode := `,"collaborationMode":{"mode":"default","settings":{"model":"model","reasoning_effort":"high","developer_instructions":"retained"}}`
			if outcome == "mode unavailable" {
				mode = ""
			}
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","thread":{"id":"branch","cwd":"/work"}%s}}`, read.ID, mode))
			if outcome != "mode unavailable" {
				var update struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
					Params struct {
						ThreadID string `json:"threadId"`
						Mode     struct {
							Settings map[string]any `json:"settings"`
						} `json:"collaborationMode"`
					} `json:"params"`
				}
				if err := json.Unmarshal(w.Bytes(), &update); err != nil {
					t.Fatal(err)
				}
				w.Reset()
				if update.Method != "thread/settings/update" || update.Params.ThreadID != "branch" || update.Params.Mode.Settings["reasoning_effort"] != nil || update.Params.Mode.Settings["developer_instructions"] != "retained" || u.Thread != "main" {
					t.Fatalf("fork correction: %+v", update)
				}
				confirm := func() {
					appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"branch","threadSettings":{"effort":null}}}`)
				}
				if outcome == "confirmation first" {
					confirm()
					if w.Len() != 0 || u.Thread != "main" {
						t.Fatal("confirmation escaped acknowledgement gate")
					}
				}
				if outcome == "rejected" {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"rejected"}}`, update.ID))
				} else {
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, update.ID))
					if outcome == "ack first" {
						if w.Len() != 0 || u.Thread != "main" {
							t.Fatal("acknowledgement escaped confirmation gate")
						}
						confirm()
					}
				}
			}
			if outcome == "rejected" || outcome == "mode unavailable" {
				cleanup := resumeTestOne(t, w, "thread/unsubscribe")
				if cleanup.Params.ThreadID != "branch" || u.Thread != "main" || u.Replacement.Pending() || u.Text != "waiting\nnewer" {
					t.Fatal("default-effort failure did not preserve original and pending input")
				}
				return
			}
			ready := resumeTestOne(t, w, "thread/resume")
			appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":null,"thread":{"id":"branch","cwd":"/work"}}}`, ready.ID))
			var started bool
			for range 8 {
				requests := resumeTestRequests(t, w)
				if len(requests) == 0 {
					break
				}
				for _, request := range requests {
					if request.Method == "turn/start" {
						started = true
						if request.Params.ThreadID != "branch" {
							t.Fatal("pending input reached original")
						}
						continue
					}
					appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"data":[],"nextCursor":null}}`, request.ID))
				}
			}
			if u.Thread != "branch" || u.reasoningEffort != "" || u.Settings.BlocksInput() || !started || u.Text != "newer" {
				t.Fatalf("default fork did not settle: %s effort=%s pending=%v started=%v draft=%q", u.Thread, u.reasoningEffort, u.Settings.BlocksInput(), started, u.Text)
			}
		})
	}
}

func TestAppServerForkRetiresFailedCorrection(t *testing.T) {
	u, w := newResumeSessionTestUI(t)
	u.model, u.reasoningEffort = "model", ""
	prepare := func(branch string) resumeTestRequest {
		appServerTestKeys(t, u, "/fork\r")
		fork := resumeTestOne(t, w, "thread/fork")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","thread":{"id":%q,"cwd":"/work"}}}`, fork.ID, branch))
		read := resumeTestOne(t, w, "thread/resume")
		appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{"model":"model","reasoningEffort":"high","collaborationMode":{"mode":"default","settings":{"model":"model","reasoning_effort":"high"}},"thread":{"id":%q,"cwd":"/work"}}}`, read.ID, branch))
		return resumeTestOne(t, w, "thread/settings/update")
	}
	old := prepare("branch1")
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"branch1","threadSettings":{"effort":"high"}}}`)
	resumeTestOne(t, w, "thread/unsubscribe")
	current := prepare("branch2")
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"error":{"code":-1,"message":"old branch rejected"}}`, old.ID))
	if !u.Replacement.Forking() || u.Replacement.Target != "branch2" || w.Len() != 0 {
		t.Fatal("old acknowledgement settled newer fork")
	}
	appServerTestMessage(t, u, fmt.Sprintf(`{"id":%d,"result":{}}`, current.ID))
	appServerTestMessage(t, u, `{"method":"thread/settings/updated","params":{"threadId":"branch2","threadSettings":{"effort":null}}}`)
	if ready := resumeTestOne(t, w, "thread/resume"); ready.Params.ThreadID != "branch2" {
		t.Fatal("current correction lost correlation")
	}
}
