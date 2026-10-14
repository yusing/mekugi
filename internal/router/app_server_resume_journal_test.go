package router

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/yusing/mekugi/internal/appserver"
	"github.com/yusing/mekugi/internal/orchestrate"
)

func resumeJournalFixture(t *testing.T, archived bool) (*appServerUI, *appServerUI, *appServerTestInput, appserver.Message) {
	t.Helper()
	replay, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	journals := newJournalStore()
	for _, scope := range []string{workspace, ""} {
		if err := journals.initialize(t.Context(), replay, scope, "saved", "/root", ""); err != nil {
			t.Fatal(err)
		}
		if err := journals.bindIdentity(t.Context(), replay, scope, "saved", "", "/root", true); err != nil {
			t.Fatal(err)
		}
		if _, err := journals.apply(t.Context(), replay, scope, "saved", "", []journalMutation{{Op: "add", Kind: "note", Title: new("Retained " + scope)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := journals.initialize(t.Context(), replay, workspace, "descendant", "/root/child", ""); err != nil {
		t.Fatal(err)
	}
	if err := journals.bindIdentity(t.Context(), replay, workspace, "descendant", "saved", "/root/child", true); err != nil {
		t.Fatal(err)
	}
	if _, err := journals.apply(t.Context(), replay, workspace, "descendant", "", []journalMutation{{Op: "add", Kind: "note", Title: new("Mounted finding")}}); err != nil {
		t.Fatal(err)
	}
	owner, wire := newAppServerTestUI()
	owner.ctx, owner.agents = t.Context(), newLiveActivityView()
	// A fresh journal owner must hydrate retained records, not live memory.
	owner.proxy = &mekugiProxy{journals: newJournalStore(), replayStore: replay, activity: newSubagentActivity()}
	owner.ensureShell()
	v := owner
	method := "thread/resume"
	if archived {
		owner.session.start("main", t.TempDir())
		child := &orchestrateChild{command: &orchestrateCommand{workspace: owner.session.cwd, main: "main"}, batch: orchestrate.Batch{TaskName: "archived", Cwd: workspace, Launch: &orchestrate.Launch{ThreadID: "saved"}, Archive: &orchestrate.Archive{State: "archived"}}}
		owner.orchestrateThreads = map[string]*orchestrateChild{"saved": child}
		if err := owner.addOrchestratedView(child, "saved", mustMarshalJSON(map[string]any{"thread": appServerThreadInfo{ID: "saved", Cwd: workspace}}), func() {}); err != nil {
			t.Fatal(err)
		}
		drainOrchestrateWork(t, owner)
		v = owner.navigation.views["saved"]
		owner.navigation.resumes = map[string]*orchestrateResume{"saved": {view: v}}
		v.restoring = &appServerActivityRestore{}
		method = "archive/history"
	} else {
		owner.thread, owner.resumeThread = "", "saved"
	}
	v.resumeThread = "saved"
	v.requests["99"] = method
	wire.Reset()
	t.Cleanup(func() {
		_ = owner.closeOrchestrateStorage()
		owner.closeOrchestratedViews()
		if owner.waitRelease != nil {
			owner.waitRelease()
		}
		owner.shell.diff.close()
		owner.shell.diffScreen.Close()
	})
	root := appServerThreadInfo{ID: "saved", Cwd: workspace}
	if !archived {
		root.Turns = []appServerHistoryTurn{{ID: "live", Status: "inProgress"}}
	}
	return owner, v, wire, appserver.Message{ID: []byte("99"), Result: mustMarshalJSON(map[string]any{"thread": root})}
}

func resumeJournalHistoryReplies(t *testing.T, owner, v *appServerUI) {
	t.Helper()
	for range 2 {
		found := false
		for id, method := range v.requests {
			if method == "thread/list" {
				found = true
				if err := owner.message(appserver.Message{ID: []byte(id), Result: []byte(`{"data":[]}`)}); err != nil {
					t.Fatal(err)
				}
				break
			}
		}
		if !found {
			t.Fatal("missing consuming roster history request")
		}
	}
}

func TestResumeJournalConsumingStorageResponsiveness(t *testing.T) {
	for _, archived := range []bool{false, true} {
		name := "resume"
		if archived {
			name = "archive"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				owner, v, wire, response := resumeJournalFixture(t, archived)
				release := hostJournalIOHold(t, owner, "state")
				defer release()
				result := make(chan error, 1)
				go func() {
					if err := owner.message(response); err != nil {
						result <- err
						return
					}
					_, err := owner.key('x')
					result <- err
				}()
				synctest.Wait()
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				default:
					release()
					if err := <-result; err != nil {
						t.Fatal(err)
					}
					t.Fatal("actual resume response consumer blocked behind held journal storage; next input key was unreachable")
				}
				resumeJournalHistoryReplies(t, owner, v)
				if v.restoring == nil || archived && len(owner.navigation.resumes) != 1 {
					t.Fatal("history replies bypassed journal restoration admission")
				}
				var frame bytes.Buffer
				if err := owner.paint(&frame, 120, 40); err != nil || owner.draft != "x" || !strings.Contains(appServerComposerScreenText(t, frame.Bytes(), 120, 40), "x") {
					t.Fatal("held journal prevented painting the next input", err)
				}
				if !archived {
					if _, err := owner.key('\r'); err != nil {
						t.Fatal(err)
					}
					if err := owner.keyboardInterrupt(); err != nil || !strings.Contains(wire.String(), "turn/interrupt") {
						t.Fatal("held journal prevented host turn cancellation", err)
					}
					appServerTestMessage(t, owner, `{"method":"turn/started","params":{"threadId":"saved","turn":{"id":"live"}}}`)
					appServerTestMessage(t, owner, `{"method":"turn/completed","params":{"threadId":"saved","turn":{"id":"live","status":"completed"}}}`)
				}
				release()
				drainOrchestrateWork(t, owner)
				if v.restoring != nil || v.thread != "saved" || v.session.cwd != v.journal.workspace || archived && len(owner.navigation.resumes) != 0 {
					t.Fatal("journal completion lost identity or resume admission")
				}
				for _, sink := range []*nativeJournalSink{v.journal, v.unscopedJournal} {
					if sink.tree == nil || !slices.ContainsFunc(sink.tree.Items, func(item journalItem) bool { return item.Title == "Retained "+sink.workspace }) {
						t.Fatal("journal namespace was not restored", sink.workspace)
					}
				}
				if v.journal.mounted == nil || !slices.ContainsFunc(v.journal.mounted.Items, func(item journalItem) bool { return item.Title == "Mounted finding" }) {
					t.Fatalf("restoration did not publish mounted child history: %+v", v.journal.mounted)
				}
				if !archived && v.journal.tree.LifecycleState != "done" {
					t.Fatal("delayed restoration overwrote ordered live lifecycle events")
				}
				if strings.Contains(wire.String(), "turn/start") || strings.Contains(wire.String(), "thread/resume") {
					t.Fatal("journal restoration replayed host execution")
				}
			})
		})
	}
}

func TestResumeJournalFailurePresentation(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "archive"}[archived], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				owner, v, _, response := resumeJournalFixture(t, archived)
				workspace := v.session.cwd
				if !archived {
					var thread struct {
						Thread appServerThreadInfo `json:"thread"`
					}
					if err := json.Unmarshal(response.Result, &thread); err != nil {
						t.Fatal(err)
					}
					workspace = thread.Thread.Cwd
				}
				writeTestFile(t, filepath.Join(owner.proxy.replayStore.directory, journalFilename(workspace, "saved")), "invalid retained journal")
				if err := owner.message(response); err != nil {
					t.Fatal(err)
				}
				resumeJournalHistoryReplies(t, owner, v)
				drainOrchestrateWork(t, owner)
				if !owner.noticeAlert || owner.orchestrateStorageErr == nil {
					t.Fatal("restore error was not presented or retained", owner.notice)
				}
				if archived {
					if owner.navigation.views["saved"] != nil || len(owner.navigation.resumes) != 0 || !strings.Contains(owner.notice, "Orchestration resume:") {
						t.Fatal("failed archived history was admitted", owner.notice)
					}
				} else if v.restoring != nil || !strings.Contains(owner.notice, "Journal restore:") || v.unscopedJournal.tree == nil {
					t.Fatal("auxiliary restore failure lost history or the other namespace", owner.notice)
				}
			})
		})
	}
}

func TestResumeJournalRetiredResultsAndShutdown(t *testing.T) {
	for _, outcome := range []string{"replacement", "failed replacement", "archive retirement", "failed archive retirement", "cancel", "shutdown", "failed shutdown"} {
		t.Run(outcome, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				failed := strings.HasPrefix(outcome, "failed ")
				outcome = strings.TrimPrefix(outcome, "failed ")
				owner, v, wire, response := resumeJournalFixture(t, outcome == "archive retirement")
				if failed {
					var result struct {
						Thread appServerThreadInfo `json:"thread"`
					}
					if err := json.Unmarshal(response.Result, &result); err != nil {
						t.Fatal(err)
					}
					writeTestFile(t, filepath.Join(owner.proxy.replayStore.directory, journalFilename(result.Thread.Cwd, "saved")), "invalid retained journal")
				}
				ctx, cancel := context.WithCancel(owner.ctx)
				defer cancel()
				owner.ctx = ctx
				release := hostJournalIOHold(t, owner, "state")
				defer release()
				if err := owner.message(response); err != nil {
					t.Fatal(err)
				}
				resumeJournalHistoryReplies(t, owner, v)
				var closed chan error
				switch outcome {
				case "archive retirement":
					owner.navigation.finishResume("saved", context.Canceled)
				case "replacement":
					if err := owner.clearSessionPresentation(); err != nil {
						t.Fatal(err)
					}
					owner.thread = "replacement"
					owner.session.start(owner.thread, t.TempDir())
				case "cancel":
					cancel()
				case "shutdown":
					closed = make(chan error, 1)
					go func() { closed <- owner.closeOrchestrateStorage() }()
				}
				synctest.Wait()
				wire.Reset()
				owner.status, owner.notice = "retired", "keep this notice"
				release()
				if closed != nil {
					if err := <-closed; (err != nil) != failed {
						t.Fatalf("shutdown error=%v, restore failed=%t", err, failed)
					}
				} else {
					drainOrchestrateWork(t, owner)
				}
				if wire.Len() != 0 || owner.status != "retired" || owner.notice != "keep this notice" || len(owner.orchestrateJobs) != 0 {
					t.Fatal("retired restoration changed presentation, dispatched, or leaked work")
				}
				if (outcome == "replacement" || outcome == "archive retirement") && owner.orchestrateStorageErr != nil {
					t.Fatal("retired journal failure contaminated shutdown", owner.orchestrateStorageErr)
				}
			})
		})
	}
}
