package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/router/toolplugin"
)

func boundedSummaryForTest(t *testing.T, ctx context.Context, store *mekugiReplayStore, workspace, thread string, limit int) (journalSummary, error) {
	t.Helper()
	store = store.scoped(ctx)
	var summary journalSummary
	err := store.locked(ctx, func() error {
		j, exists, err := readThreadJournal(store, workspace, thread)
		if err != nil {
			return err
		}
		if !exists {
			t.Fatalf("journal for %q was not persisted", thread)
		}
		summary, err = store.journalSummaryBoundedLocked(ctx, j, limit)
		return err
	})
	return summary, err
}

func boundedSummaryUnits(text string) int { return len(utf16.Encode([]rune(text))) }

func assertBoundedSummary(t *testing.T, summary journalSummary, limit int) {
	t.Helper()
	if !utf8.ValidString(summary.Text) || boundedSummaryUnits(summary.Text) > limit {
		t.Fatalf("invalid or oversized summary: %d UTF-16 units, limit %d: %q", boundedSummaryUnits(summary.Text), limit, summary.Text)
	}
}

func TestJournalSummaryBoundedUTF16ExactBoundary(t *testing.T) {
	for _, body := range []string{strings.Repeat("界", 1800), strings.Repeat("🚀", 1800)} {
		t.Run(body[:len(string([]rune(body)[0]))], func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			ctx, store, thread := transform.ctx, proxy.replayStore, transform.shellThreadID
			if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Unicode constraint"), Body: new(body)}}); err != nil {
				t.Fatal(err)
			}
			full, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(full.Text, body) {
				t.Fatal("mandatory Unicode body was truncated")
			}
			limit := boundedSummaryUnits(full.Text)
			exact, err := boundedSummaryForTest(t, ctx, store, workspace, thread, limit)
			if err != nil {
				t.Fatalf("exact UTF-16 boundary rejected: %v", err)
			}
			assertBoundedSummary(t, exact, limit)
			if exact.Text != full.Text {
				t.Fatal("exact boundary changed a summary containing only mandatory facts")
			}
			// The node alone exceeds this limit. UTF-8 byte and rune counting
			// disagree with UTF-16 for the two inputs above.
			overflow, err := boundedSummaryForTest(t, ctx, store, workspace, thread, boundedSummaryUnits(body)-1)
			if err == nil || overflow.Text != "" {
				t.Fatalf("mandatory overflow produced text: %+v, %v", overflow, err)
			}
			paddedBody := body + strings.Repeat("x", 10000-limit)
			if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "set", P: "/1", Body: new(paddedBody)}}); err != nil {
				t.Fatal(err)
			}
			packet, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
			if err != nil || boundedSummaryUnits(packet.Text) != 10000 || !strings.Contains(packet.Text, paddedBody) {
				t.Fatalf("exact 10000-unit packet rejected or changed: %d units, %v", boundedSummaryUnits(packet.Text), err)
			}
			// Leave no room even after optional empty section headings are
			// omitted. Their admission is not a mandatory-content guarantee.
			if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "set", P: "/1", Body: new(paddedBody + strings.Repeat("x", 200))}}); err != nil {
				t.Fatal(err)
			}
			packet, err = boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
			if err == nil || packet.Text != "" {
				t.Fatalf("mandatory overflow accepted: %d output units, %v", boundedSummaryUnits(packet.Text), err)
			}
		})
	}
}

func TestJournalSummaryBoundedPreservesAllMandatoryNodes(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, store, thread := transform.ctx, proxy.replayStore, transform.shellThreadID
	var mutations []journalMutation
	var facts []string
	for i := range 12 {
		title, body := fmt.Sprintf("Constraint %02d", i), fmt.Sprintf("Entire constraint %02d: %s", i, strings.Repeat("界", 35))
		mutations = append(mutations, journalMutation{Op: "add", Kind: "context", Title: new(title), Body: new(body)})
		facts = append(facts, fmt.Sprintf("/%d %s", i+1, title), body)
	}
	for i, state := range []string{"working", "pending", "blocked"} {
		title, body, reason := fmt.Sprintf("Open task %d", i), fmt.Sprintf("Complete task body %d: %s", i, strings.Repeat("x", 1200)), fmt.Sprintf("Full reason %d", i)
		mutations = append(mutations, journalMutation{Op: "add", Kind: "task", State: new(state), Title: new(title), Body: new(body), Reason: new(reason)})
		facts = append(facts, fmt.Sprintf("/%d [%s]", 13+i, state), title, body, reason)
	}
	for i := range 20 {
		state := []string{"working", "pending", "blocked"}[i%3]
		title, body, reason := fmt.Sprintf("Task %02d", i), fmt.Sprintf("Task body %02d", i), fmt.Sprintf("Reason %02d", i)
		mutations = append(mutations, journalMutation{Op: "add", Kind: "task", State: new(state), Title: new(title), Body: new(body), Reason: new(reason)})
		facts = append(facts, fmt.Sprintf("/%d [%s] %s", 16+i, state, title), body, reason)
	}
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", mutations); err != nil {
		t.Fatal(err)
	}
	summary, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedSummary(t, summary, 10000)
	for _, fact := range facts {
		if !strings.Contains(summary.Text, fact) {
			t.Errorf("mandatory fact omitted: %q", fact)
		}
	}
	if !strings.Contains(summary.Text, "continue /13") {
		t.Fatalf("working task lost resume priority: %s", summary.Text)
	}
	legacy, err := summaryForTest(t, ctx, store, workspace, thread)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(legacy.Text, "Complete task body 0: "+strings.Repeat("x", 1200)) || !strings.Contains(legacy.Text, "[read the retained node for full detail]") {
		t.Fatal("legacy open-task body excerpt policy changed")
	}
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Mandatory overflow"), Body: new(strings.Repeat("界", 4500))}}); err != nil {
		t.Fatal(err)
	}
	summary, err = boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
	if err == nil || summary.Text != "" {
		t.Fatalf("mandatory overflow produced text: %+v, %v", summary, err)
	}
	summary, err = boundedSummaryForTest(t, ctx, store, workspace, thread, 20000)
	if err != nil {
		t.Fatalf("larger caller budget rejected fitting mandatory facts: %v", err)
	}
	assertBoundedSummary(t, summary, 20000)
	if !strings.Contains(summary.Text, strings.Repeat("界", 4500)) {
		t.Fatal("larger caller budget truncated mandatory body")
	}
}

func TestJournalSummaryBoundedOptionalResultsRecoverable(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, store, thread := transform.ctx, proxy.replayStore, transform.shellThreadID
	mutations := []journalMutation{{Op: "add", Kind: "task", Title: new("Keep mandatory task"), State: new("working"), Body: new(strings.Repeat("m", 6500))}}
	for i := range 30 {
		mutations = append(mutations, journalMutation{Op: "add", Kind: "note", Title: new(fmt.Sprintf("Optional result %02d", i)), Body: new(strings.Repeat("界🚀", 900))})
	}
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", mutations); err != nil {
		t.Fatal(err)
	}
	summary, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
	if err != nil {
		t.Fatalf("optional results prevented synthesis: %v", err)
	}
	assertBoundedSummary(t, summary, 10000)
	for _, fact := range []string{"Keep mandatory task", strings.Repeat("m", 6500), "omitted", "durable journal reads"} {
		if !strings.Contains(summary.Text, fact) {
			t.Errorf("lost mandatory fact or recovery notice %q", fact)
		}
	}
	if strings.Contains(summary.Text, "Optional result 00") {
		t.Fatal("oldest optional result displaced newer results")
	}
	scoped := store.scoped(ctx)
	if err := scoped.locked(ctx, func() error {
		j, exists, err := readThreadJournal(scoped, workspace, thread)
		if err != nil {
			return err
		}
		if !exists || len(j.Items) != 31 || j.Items[1].Title != "Optional result 00" || j.Items[1].Body != strings.Repeat("界🚀", 900) {
			t.Fatal("omitted results were not recoverable from the durable journal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestJournalSummaryBoundedFailuresAndObservations(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, store, thread := transform.ctx, proxy.replayStore, transform.shellThreadID
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Repair"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	ref, err := store.putTypedOutput(ctx, toolplugin.OmittedOutput{Stdout: "FAIL retained detail"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 14 {
		if err := store.put(ctx, workspace, map[string]mekugiHistory{fmt.Sprintf("failed-%02d", i): {ExecutingThread: thread, Script: fmt.Sprintf("test failure %02d ", i) + strings.Repeat("x", 500), Report: strings.Repeat("noise", 200) + "FAIL retained detail", ExecOutcome: &execOutcome{Status: execStatusFailed, Exit: new(1), OutputRef: ref}}}); err != nil {
			t.Fatal(err)
		}
	}
	histories := map[string]mekugiHistory{"pending": {ExecutingThread: thread, Script: "unconfirmed command", ExecObservation: &execObservation{}}}
	for _, codeMode := range []bool{false, true} {
		call := fmt.Sprintf("finished-%t", codeMode)
		histories[call] = mekugiHistory{ExecutingThread: thread, Script: "already finished", ExecObservation: &execObservation{CodeMode: codeMode}}
		histories[execDerivedCallID(call, codeMode)] = mekugiHistory{ExecutingThread: thread, ExecOutcome: &execOutcome{Status: execStatusCompleted}}
	}
	histories["shared-failure"] = mekugiHistory{ExecutingThread: thread, ExecOutcome: &execOutcome{Status: execStatusFailed, SharedWith: "failed-13"}}
	histories["foreign"] = mekugiHistory{ExecutingThread: "other-thread", Script: "foreign command", ExecOutcome: &execOutcome{Status: execStatusFailed}}
	if err := store.put(ctx, workspace, histories); err != nil {
		t.Fatal(err)
	}
	change, err := store.reserveChange(ctx, workspace, thread, "edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"edit": {ChangeID: change, CorrelationID: "edit", ExecutingThread: thread, ReviewFiles: []mekugi.ReviewFile{mekugi.RenderReviewFile("repair.go", "repair.go", "before\n", "after\n")}}}); err != nil {
		t.Fatal(err)
	}
	summary, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedSummary(t, summary, 10000)
	if summary.Failures != 14 || summary.Changes != 1 {
		t.Fatalf("omission changed evidence counts: %+v", summary)
	}
	for _, fact := range []string{"omitted", "mread", "mchanges", "unconfirmed command", "not proof of a running process", "No continuation handle or Code Mode store value is restored"} {
		if !strings.Contains(summary.Text, fact) {
			t.Errorf("missing recovery fact %q: %s", fact, summary.Text)
		}
	}
	if strings.Contains(summary.Text, "already finished") || strings.Contains(summary.Text, "foreign command") {
		t.Fatal("completed or foreign execution reported as unconfirmed")
	}
	// A tighter successful summary must preserve counts even if optional
	// failure details cannot all be displayed.
	short, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 2000)
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedSummary(t, short, 2000)
	if short.Failures != 14 || short.Changes != 1 || !strings.Contains(short.Text, "omitted") || !strings.Contains(short.Text, "mchanges") || !strings.Contains(short.Text, "durable journal reads") {
		t.Fatalf("optional evidence omitted without truthful counts/recovery: %+v", short)
	}
	scoped := store.scoped(ctx)
	manifestPath := filepath.Join(scoped.directory, storageSessionName(thread))
	if !strings.Contains(short.Text, manifestPath) {
		t.Fatalf("omitted failure records lack an exact discoverable manifest: %s", short.Text)
	}
	if err := scoped.locked(ctx, func() error {
		manifest, err := scoped.readRetainedSession(storageSessionName(thread))
		if err != nil {
			return err
		}
		if _, ok := manifest.Files[replayRecordName(workspace, "failed-13", false)]; !ok {
			t.Fatal("recovery manifest omitted newest failed record")
		}
		record, exists, err := scoped.read(workspace, "failed-13", false)
		if err != nil {
			return err
		}
		if !exists || record.History.ExecOutcome == nil || record.History.ExecOutcome.OutputRef != ref {
			t.Fatal("manifest failed record lost its mread reference")
		}
		index, err := scoped.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		if _, ok := index.Changes[change]; !ok {
			t.Fatal("omitted change was not recoverable from the retained index")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	output, err := scoped.readShellOutput(ctx, ref)
	if err != nil || output.Stdout != "FAIL retained detail" || output.ExitCode != 1 {
		t.Fatalf("discovered mread reference could not recover actual failure: %+v, %v", output, err)
	}
}

func TestJournalSummaryBoundedRejectsCorruptEvidence(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, store, thread := transform.ctx, proxy.replayStore.scoped(transform.ctx), transform.shellThreadID
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Investigate")}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.directory, changeIndexName(workspace, store.handleNamespace())), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	summary, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
	if err == nil || summary.Text != "" {
		t.Fatalf("corrupt retained evidence became a usable summary: %+v, %v", summary, err)
	}
}

func TestJournalSummaryBoundedUnconfirmedOverflowRecoverable(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, store, thread := transform.ctx, proxy.replayStore, transform.shellThreadID
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Check current host state"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		call := fmt.Sprintf("observed-%02d", i)
		if err := store.put(ctx, workspace, map[string]mekugiHistory{call: {ExecutingThread: thread, Script: "unconfirmed " + strings.Repeat("🚀", 300), ExecObservation: &execObservation{}}}); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := boundedSummaryForTest(t, ctx, restarted, workspace, thread, 1500)
	if err != nil {
		t.Fatalf("unconfirmed observations prevented bounded recovery: %v", err)
	}
	assertBoundedSummary(t, summary, 1500)
	if summary.Failures != 0 || summary.Changes != 0 {
		t.Fatalf("pre-execution observations became completed evidence: %+v", summary)
	}
	for _, fact := range []string{"Check current host state", "omitted", "durable journal reads"} {
		if !strings.Contains(summary.Text, fact) {
			t.Errorf("missing mandatory task or omission recovery %q: %s", fact, summary.Text)
		}
	}
	if strings.Contains(summary.Text, "unconfirmed 🚀") && (!strings.Contains(summary.Text, "not proof of a running process") || !strings.Contains(summary.Text, "No continuation handle")) {
		t.Fatalf("displayed observations lost lifecycle disclaimer: %s", summary.Text)
	}
}

func TestJournalSummaryBoundedFailureWithoutOutput(t *testing.T) {
	transform, proxy, _, workspace := newDurableTreeTransform(t)
	ctx, store, thread := transform.ctx, proxy.replayStore, transform.shellThreadID
	if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "task", Title: new("Investigate"), State: new("working")}}); err != nil {
		t.Fatal(err)
	}
	if err := store.put(ctx, workspace, map[string]mekugiHistory{"failed": {ExecutingThread: thread, Script: "go test", Report: "FAIL parser", ExecOutcome: &execOutcome{Status: execStatusFailed, Exit: new(1)}}}); err != nil {
		t.Fatal(err)
	}
	summary, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
	if err != nil {
		t.Fatal(err)
	}
	assertBoundedSummary(t, summary, 10000)
	if summary.Failures != 1 {
		t.Fatalf("unretained failure not counted: %+v", summary)
	}
	for _, fact := range []string{"go test", "Exit: 1", "output not retained", "FAIL parser"} {
		if !strings.Contains(summary.Text, fact) {
			t.Errorf("missing unretained failure fact %q: %s", fact, summary.Text)
		}
	}
}

// Empty optional headings and a no-change view are not omitted evidence. They
// must yield their space to mandatory content, including at the exact boundary.
func TestJournalSummaryBoundedMandatoryOnlyExactCap(t *testing.T) {
	for _, foreignChange := range []bool{false, true} {
		t.Run(fmt.Sprint(foreignChange), func(t *testing.T) {
			transform, proxy, _, workspace := newDurableTreeTransform(t)
			ctx, store, thread := transform.ctx, proxy.replayStore, transform.shellThreadID
			if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "add", Kind: "context", Title: new("Constraint"), Body: new("x")}}); err != nil {
				t.Fatal(err)
			}
			if foreignChange {
				if _, err := store.reserveChange(ctx, workspace, "foreign-thread", "foreign-edit"); err != nil {
					t.Fatal(err)
				}
			}
			full, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
			if err != nil {
				t.Fatal(err)
			}
			optionalStart := strings.Index(full.Text, "\nEstablished results and completed work:")
			footerStart := strings.Index(full.Text, "\nResume:")
			if optionalStart < 0 || footerStart < optionalStart {
				t.Fatal("missing summary section boundaries")
			}
			mandatory := full.Text[:optionalStart] + full.Text[footerStart:]
			body := strings.Repeat("x", 10000-boundedSummaryUnits(mandatory)+1)
			if _, err := proxy.journals.apply(ctx, store, workspace, thread, "", []journalMutation{{Op: "set", P: "/1", Body: new(body)}}); err != nil {
				t.Fatal(err)
			}
			result, err := boundedSummaryForTest(t, ctx, store, workspace, thread, 10000)
			if err != nil {
				t.Fatalf("mandatory facts plus footer exactly fit: %v", err)
			}
			want := strings.Replace(mandatory, "\nx\n", "\n"+body+"\n", 1)
			if result.Text != want || boundedSummaryUnits(result.Text) != 10000 {
				t.Fatal("exact-fit mandatory packet changed or acquired a false omission")
			}
			for _, limit := range []int{9999, 0, -1} {
				result, err := boundedSummaryForTest(t, ctx, store, workspace, thread, limit)
				if err == nil || result.Text != "" {
					t.Fatalf("insufficient capacity %d exposed text", limit)
				}
			}
		})
	}
}
