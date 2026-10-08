package router

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/router/toolplugin"
)

func summaryForTest(t *testing.T, ctx context.Context, store *mekugiReplayStore, workspace, thread string) (journalSummary, error) {
	t.Helper()
	store = store.scoped(ctx)
	var summary journalSummary
	err := store.locked(ctx, func() error {
		journal, exists, err := readThreadJournal(store, workspace, thread)
		if err != nil {
			return err
		}
		if !exists {
			t.Fatalf("journal for %q was not persisted", thread)
		}
		summary, err = store.journalSummaryLocked(ctx, journal)
		return err
	})
	return summary, err
}

func TestJournalSummaryOrdersOpenTasksAndIndexesContext(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	thread := transform.shellThreadID
	_, err := proxy.journals.apply(transform.ctx, proxy.replayStore, workspace, thread, "", []journalMutation{
		{Op: "add", Kind: "context", Title: new("Keep protocol v1 stable"), Body: new("No new dependencies")},
		{Op: "add", Kind: "task", Title: new("Pending renderer")},
		{Op: "add", Kind: "task", Title: new("Blocked CLI"), State: new("blocked"), Reason: new("Awaiting API decision")},
		{Op: "add", Kind: "task", Title: new("Working parser"), State: new("working")},
	})
	if err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, transform.ctx, proxy.replayStore, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{"Keep protocol v1 stable", "Read relevant context paths before acting", "Continuation paused: /3: Awaiting API decision"} {
		if !strings.Contains(summary.Text, fact) {
			t.Errorf("summary omitted %q: %s", fact, summary.Text)
		}
	}
	if strings.Contains(summary.Text, "Resume: continue") {
		t.Fatal("blocked journal directed unrelated work to continue")
	}
	working, pending, blocked := strings.Index(summary.Text, "/4 [working]"), strings.Index(summary.Text, "/2 [pending]"), strings.Index(summary.Text, "/3 [blocked]")
	if working < 0 || pending < working || blocked < pending {
		t.Fatalf("open tasks not ordered working, pending, blocked: %s", summary.Text)
	}
}

func TestJournalSummarySeparatesEarlierEvidenceFromUnreportedFailure(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Repair renderer"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	changeID, err := store.reserveChange(ctx, workspace, thread, "earlier-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"earlier-edit": {
		ChangeID: changeID, CorrelationID: "earlier-edit", ExecutingThread: thread,
		ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("render.go", "render.go", "before\n", "after\n")},
	}}); err != nil {
		t.Fatal(err)
	}
	firstRef, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: "old failure"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"earlier-fail": {
		ExecutingThread: thread, Script: "old test", Report: "old failure",
		ExecOutcome: &execOutcome{Status: execStatusFailed, OutputRef: firstRef, Exit: new(1)},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.commentary.journalPublisher(t.Context(), workspace+"\x00runtime", thread, "checkpoint", []journalMutation{{Op: "log", P: "/1", Text: new("Earlier test result reviewed")}}); err != nil {
		t.Fatal(err)
	}
	before, err := summaryForTest(t, ctx, store, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if before.Changes != 0 || before.Failures != 0 || !strings.Contains(before.Text, changeID) || strings.Contains(before.Text, "Since the last journal event:") {
		t.Fatalf("covered evidence leaked into resume section: %+v", before)
	}
	ref, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: "--- FAIL: TestWrap\nwant 3 rows, got 4"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"later-fail": {
		ExecutingThread: thread, Script: "go test ./internal/ui/...", Report: "--- FAIL: TestWrap: want 3 rows, got 4",
		ExecOutcome: &execOutcome{Status: execStatusFailed, OutputRef: ref, Exit: new(1)},
	}}); err != nil {
		t.Fatal(err)
	}
	laterChange, err := store.reserveChange(ctx, workspace, thread, "later-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"later-edit": {ChangeID: laterChange, CorrelationID: "later-edit", ExecutingThread: thread, ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("new.go", "new.go", "", "package new\n")}}}); err != nil {
		t.Fatal(err)
	}
	after, err := summaryForTest(t, ctx, store, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if after.Changes != 1 || after.Failures != 1 {
		t.Fatalf("wrong evidence counts: %+v", after)
	}
	for _, fact := range []string{"Since the last journal event:", "go test ./internal/ui/...", "Exit: 1", "mread " + ref, laterChange, "new.go", "want 3 rows, got 4", "continue /1"} {
		if !strings.Contains(after.Text, fact) {
			t.Errorf("missing failure recovery fact %q: %s", fact, after.Text)
		}
	}
	retained, delta, ok := strings.Cut(after.Text, "Since the last journal event:")
	// Recorded work keeps only its ranges; unrecorded changes keep file statistics.
	if !ok || !strings.Contains(retained, changeID+".."+laterChange) || strings.Contains(retained, "render.go") || strings.Contains(retained, "new.go") ||
		strings.Contains(delta, "render.go") || !strings.Contains(delta, "new.go") {
		t.Fatalf("cumulative and delta changes lost their evidence boundary: %s", after.Text)
	}
}

func TestJournalSummaryRejectsCorruptChangeIndex(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Repair renderer")}}); err != nil {
		t.Fatal(err)
	}
	scoped := store.scoped(ctx)
	if err := os.WriteFile(filepath.Join(scoped.directory, changeIndexName(workspace, scoped.handleNamespace())), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, ctx, store, workspace, thread)
	if err == nil || summary.Text != "" {
		t.Fatalf("corrupt evidence produced a recovery summary: %+v, %v", summary, err)
	}
}

func TestJournalSummaryListsFailureWithoutRetainedOutput(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Investigate"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"unretained-fail": {
		ExecutingThread: thread, Script: "go test", Report: "FAIL parser", ExecOutcome: &execOutcome{Status: execStatusFailed, Exit: new(1)},
	}}); err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, ctx, store, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Failures != 1 || !strings.Contains(summary.Text, "Exit: 1; output not retained\nFAIL parser") {
		t.Fatalf("unretained output was not reported as unavailable: %s", summary.Text)
	}
}

func TestJournalSummaryTreatsUnorderedFailureAsCoveredByKnownBoundary(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"legacy-fail": {
		ExecutingThread: thread, Script: "legacy test", ExecOutcome: &execOutcome{Status: execStatusFailed, Exit: new(1)},
	}}); err != nil {
		t.Fatal(err)
	}
	// Records written before failures were capture-ordered carry no order.
	name := filepath.Join(store.directory, replayRecordName(workspace, "legacy-fail", false))
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var record replayRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.CaptureOrder = 0
	if data, err = marshalProtocolJSON(record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Resume"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, ctx, store, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Failures != 0 || strings.Contains(summary.Text, "legacy test") {
		t.Fatalf("unordered pre-boundary failure never aged out: %+v", summary)
	}
}

func TestJournalSummaryBoundsFailuresKeepingNewest(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Debug"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	for i := range 14 {
		output := strings.Repeat("noise\n", 400) + fmt.Sprintf("FAIL attempt %02d", i)
		ref, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: output}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.put(ctx, workspace, map[string]mekugiHistory{fmt.Sprintf("fail-%02d", i): {
			ExecutingThread: thread, Script: fmt.Sprintf("go test attempt %02d ", i) + strings.Repeat("-run X ", 300), Report: output,
			ExecOutcome: &execOutcome{Status: execStatusFailed, OutputRef: ref, Exit: new(1)},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := summaryForTest(t, ctx, store, workspace, thread)
	if err != nil {
		t.Fatalf("a normal debugging loop overflowed the summary: %v", err)
	}
	if summary.Failures != 14 || len(summary.Text) > maxJournalSummaryBytes {
		t.Fatalf("wrong failure bounds: %d failures, %d bytes", summary.Failures, len(summary.Text))
	}
	for _, fact := range []string{"6 earlier failed commands omitted.", "FAIL attempt 13", "FAIL attempt 06", "[command truncated]", "[earlier output omitted; mread "} {
		if !strings.Contains(summary.Text, fact) {
			t.Errorf("missing %q: %s", fact, summary.Text)
		}
	}
	if strings.Contains(summary.Text, "FAIL attempt 05") || strings.Index(summary.Text, "attempt 06") > strings.Index(summary.Text, "attempt 13") {
		t.Fatalf("failures not bounded newest-first in capture order: %s", summary.Text)
	}
}

func TestJournalSummaryLimitsFactsToCurrentWork(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	mutations := []journalMutation{
		{Op: "add", Kind: "task", Title: new("Current task"), State: new("working")},
		{Op: "add", Under: "/1", Kind: "context", Title: new("Current constraint"), Body: new("Preserve the active wire contract")},
		{Op: "add", Kind: "task", Title: new("Later task")},
		{Op: "log", P: "/2", Text: new("Unrelated pending-task result")},
	}
	for i := range 40 {
		mutations = append(mutations,
			journalMutation{Op: "add", Under: "/1", Kind: "note", Title: new(fmt.Sprintf("Result %02d", i)), Body: new(strings.Repeat("x", 1500))},
			journalMutation{Op: "add", Kind: "task", Title: new(fmt.Sprintf("Unrelated completed task %02d", i)), State: new("done")},
			journalMutation{Op: "add", Kind: "note", Title: new("Unrelated root result"), Body: new(strings.Repeat("y", 1500))},
		)
	}
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", mutations); err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, ctx, store, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(summary.Text, "Result 36") || strings.Contains(summary.Text, "Unrelated") || len(summary.Text) > 4096 {
		t.Fatalf("session history inflated recovery: %d bytes; unrelated history=%t", len(summary.Text), strings.Contains(summary.Text, "Unrelated"))
	}
	for _, want := range []string{"Preserve the active wire contract", "Result 37", "Result 38", "Result 39", "37 more current-work facts available by path", `journal({op:"read",view:"outline"})`} {
		if !strings.Contains(summary.Text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Index(summary.Text, "Result 38") > strings.Index(summary.Text, "Result 39") {
		t.Fatal("kept results lost tree order")
	}
}

func TestJournalSummaryIndexesLargeContextForOnDemandReads(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	body := strings.Repeat("Retained constraint detail. ", 500)
	for range 4 {
		if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Constraint"), Body: &body}}); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := summaryForTest(t, ctx, store, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Text, "/4 Constraint") || strings.Contains(summary.Text, "Retained constraint detail") || len(summary.Text) > 1024 {
		t.Fatalf("root context bodies inflated recovery: %s", summary.Text)
	}
	nodes, err := proxy.journals.readTree(ctx, store, workspace, thread, "", "/4", nil, "own")
	if err != nil || len(nodes) != 1 || nodes[0].Body != body {
		t.Fatalf("on-demand read lost context: %+v %v", nodes, err)
	}
}

func TestJournalSummaryIsolatesWorkspaceAndThreadAfterRestart(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	otherWorkspace := t.TempDir()
	otherThread := "unrelated-thread"
	for _, target := range []struct{ workspace, thread, title string }{
		{workspace, thread, "Own working task"},
		{workspace, otherThread, "Other thread secret"},
		{otherWorkspace, thread, "Other workspace secret"},
	} {
		if target.thread != thread || target.workspace != workspace {
			if err := proxy.journals.initialize(ctx, store, target.workspace, target.thread, "/root", ""); err != nil {
				t.Fatal(err)
			}
			if err := proxy.journals.bindIdentity(ctx, store, target.workspace, target.thread, "", "/root", true); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := proxy.journals.apply(ctx, store, target.workspace, target.thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new(target.title), State: new("working")}}); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := summaryForTest(t, ctx, restarted, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Text, "Own working task") || strings.Contains(summary.Text, "Other thread secret") || strings.Contains(summary.Text, "Other workspace secret") {
		t.Fatalf("restarted summary crossed thread/workspace boundary: %s", summary.Text)
	}
}

func TestPostCompactContextUsesJournalV2Summary(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{
		{Op: "add", Kind: "context", Title: new("Keep wire protocol stable")},
		{Op: "add", Kind: "task", Title: new("Finish parser"), State: new("working")},
	}); err != nil {
		t.Fatal(err)
	}
	content, err := store.postCompactContext(ctx, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{"Journal recovery", "Keep wire protocol stable", "/2 [working] Finish parser"} {
		if !strings.Contains(content, fact) {
			t.Errorf("post-compact hook omitted %q: %s", fact, content)
		}
	}
	if strings.Contains(content, "Mekugi post-compaction recovery") {
		t.Fatalf("v2 context used legacy item rendering: %s", content)
	}
}

func TestJournalSummaryRejectsCorruptRetainedFailureOutput(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, thread := transform.ctx, transform.shellThreadID
	store := proxy.replayStore.scoped(ctx)
	ref, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: "failure"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"failure": {ExecutingThread: thread, Script: "test", Report: "failure", ExecOutcome: &execOutcome{Status: execStatusFailed, Exit: new(2), OutputRef: ref}}}); err != nil {
		t.Fatal(err)
	}
	name, err := store.outputName(ref)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{
		"corrupt test output",
		`{"version":1,"id":"` + ref + `"}`,
		`{"version":1,"id":"` + ref + `","stdout":"","stderr":"","exit_code":2,"unknown":true}`,
	} {
		if err := os.WriteFile(filepath.Join(store.directory, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := summaryForTest(t, ctx, store, workspace, thread); err == nil {
			t.Fatal("corrupt output was accepted as resume evidence")
		}
		if _, err := store.readShellOutput(ctx, ref); err == nil {
			t.Fatal("corrupt output was accepted by mread")
		}
	}
}

func TestJournalWriteSurvivesUnreadableRecoveryEvidence(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	store, thread, ctx := proxy.replayStore, transform.shellThreadID, transform.ctx
	if err := os.WriteFile(filepath.Join(store.directory, "capture-order"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Keep journaling"), State: new("working")}}); err != nil {
		t.Fatalf("auxiliary recovery evidence blocked a journal write: %v", err)
	}
	scoped := store.scoped(ctx)
	if err := scoped.locked(ctx, func() error {
		journal, exists, err := readThreadJournal(scoped, workspace, thread)
		if err != nil {
			return err
		}
		if !exists || journal.EvidenceKnown {
			t.Fatalf("journal claimed an unreadable evidence boundary: exists=%t known=%t", exists, journal.EvidenceKnown)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
