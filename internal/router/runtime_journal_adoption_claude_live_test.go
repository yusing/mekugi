package router

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/internal/claude"
)

// The user prompt requests ordinary implementation, not journal or helper calls.
func TestRuntimeJournalAdoptionClaudeLive(t *testing.T) {
	runtimeOrdinaryClaudeLive(t, "sonnet", true)
}

func TestRuntimeBashOrdinaryClaudeLive(t *testing.T) {
	runtimeOrdinaryClaudeLive(t, "haiku", false)
}

func runtimeOrdinaryClaudeLive(t *testing.T, model string, requireJournal bool) {
	t.Helper()
	if os.Getenv("MEKUGI_TEST_NATIVE_CLAUDE") != "1" {
		t.Skip("requires authenticated native Claude acceptance")
	}
	nativeAcceptanceSettingsUnchanged(t)
	t.Setenv(routerTestWorkerEnvironment, "1")
	t.Setenv("MEKUGI_RUNTIME_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	service, binding, _ := observationHTTPFixture(t)
	trace := traceNativeObservation(t, service)
	presentation, err := service.PrepareCompanion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := filepath.Abs("../claude/bridge/dist/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := exec.LookPath("claude")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := service.Endpoint()
	client, err := claude.Start(ctx, "node", bridge, claude.Config{Cwd: binding.Workspace, Executable: executable, Model: model, Companion: &claude.ObservationEndpoint{
		Socket: endpoint.Socket, Token: endpoint.Token, Plugin: presentation.Plugin, FrontendDirectory: presentation.FrontendDirectory, JournalSchema: presentation.JournalSchema,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	u := newRuntimeUI(ctx, client, "Claude Code", binding.Workspace)
	u.attachRuntimeObservation(service)
	defer u.shell.diff.close()
	defer u.shell.diffScreen.Close()
	const prompt = "Create range.js exporting range(start, end) as a CommonJS function returning consecutive integers from start inclusive to end exclusive. Reject non-integer inputs and end before start. Create range.test.js using node:test to cover normal, empty, invalid and reversed ranges. Run the tests and report the result."
	started := false
	journalCalls := make(map[string]bool)
	for {
		select {
		case <-ctx.Done():
			t.Fatal("ordinary implementation did not complete before the acceptance deadline")
		case e, ok := <-client.Events():
			if !ok {
				t.Fatal("native bridge disconnected")
			}
			if e.Kind == "error" || e.Kind == "done" && e.Failed || e.Kind == "tool_result" && e.Failed {
				t.Fatalf("native operation failed: %s", e.Text)
			}
			if e.Kind == "notice" && (strings.Contains(e.Text, "unavailable") || strings.Contains(e.Text, "rejected")) {
				trace.logDrift(t)
				t.Fatal(e.Text)
			}
			if err := u.runtimeEvent(e); err != nil {
				t.Fatal(err)
			}
			u.journal = service.journal.sink()
			switch e.Kind {
			case "ready":
				if !started {
					started = true
					u.draft = prompt
					if _, _, err := u.runtimeKey('\r'); err != nil || !u.runtime.busy {
						t.Fatalf("ordinary native input not admitted: %v", err)
					}
				}
			case "prompt":
				runtimeFrame(t, u, 120, 40)
				runtimeKeys(t, u, "1\r")
			case "tool":
				if e.Role == "mcp__mekugi__journal_batch" {
					journalCalls[e.ID] = true
				}
			case "done":
				if !started {
					continue
				}
				if !requireJournal {
					trace.mu.Lock()
					bashCalls := 0
					for id, before := range trace.before {
						if before.Tool != "Bash" {
							continue
						}
						bashCalls++
						after, ok := trace.after[id]
						if !ok || !sameObservationCall(&before, &after) {
							t.Fatalf("native Bash has no matching terminal tuple: %s", id)
						}
					}
					trace.mu.Unlock()
					if bashCalls == 0 || service.owner.pendingCount.Load() != 0 {
						t.Fatal("ordinary native task did not settle its Bash observation")
					}
					t.Logf("ordinary %s task settled %d Bash calls without tuple drift", model, bashCalls)
					return
				}
				if len(journalCalls) < 2 {
					t.Fatalf("ordinary task did not maintain a journal: %d native batches", len(journalCalls))
				}
				root := service.journal.rootBinding()
				journalCtx, err := service.journal.scope(ctx, root)
				if err != nil {
					t.Fatal(err)
				}
				j, found, err := readThreadJournal(service.owner.store.scoped(journalCtx), root.Workspace, observationThread(root))
				if err != nil || !found {
					t.Fatalf("native journal not retained: %v", err)
				}
				tasks := 0
				for _, item := range j.Items {
					if item.Kind != "task" {
						continue
					}
					tasks++
					if item.State != "done" {
						t.Fatalf("completed implementation left journal task %s in %s", item.Path, item.State)
					}
				}
				if tasks == 0 || len(j.Events) < 2 || u.journal == nil || len(u.journal.snapshot()) == 0 {
					t.Fatal("native work did not produce retained tasks and shared UI publication")
				}
				u.shell.journalOpen = true
				frame := runtimeFrame(t, u, 120, 40)
				if strings.Contains(frame, "No journal entries.") {
					t.Fatal("ordinary native journal did not appear in the shared pane")
				}
				grader := exec.CommandContext(ctx, "node", "-e", `const assert = require('node:assert/strict'); const api = require('./range.js'); const range = typeof api === 'function' ? api : api.range; assert.equal(typeof range, 'function'); assert.deepEqual(range(2,5), [2,3,4]); assert.deepEqual(range(2,2), []); assert.throws(() => range('2',5)); assert.throws(() => range(2,2.5)); assert.throws(() => range(5,2));`)
				grader.Dir = binding.Workspace
				if output, err := grader.CombinedOutput(); err != nil {
					t.Fatalf("independent behavior grader failed: %v\n%s", err, output)
				}
				t.Logf("ordinary native task authored %d journal batches and %d completed tasks; shared pane and behavior grader passed", len(journalCalls), tasks)
				return
			}
		}
	}
}
