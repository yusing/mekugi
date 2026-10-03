package router

import (
	json "encoding/json/v2"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func runtimeResetJournal(t *testing.T, s *ObservationService, b ObservationBinding) threadJournal {
	t.Helper()
	ctx, err := s.journal.scope(t.Context(), b)
	if err != nil {
		t.Fatal(err)
	}
	j, found, err := readThreadJournal(s.owner.store.scoped(ctx), b.Workspace, observationThread(b))
	if err != nil || !found {
		t.Fatalf("journal unavailable: %v %v", found, err)
	}
	return j
}

func TestRuntimeJournalResetPreservesEvidenceAndIsolatesSource(t *testing.T) {
	s, source, c := runtimeJournalFixture(t)
	if err := s.journal.startTurn(t.Context(), "source-turn"); err != nil {
		t.Fatal(err)
	}
	input := `{"journal":[{"op":"add","kind":"context","title":"Constraint","body":"Keep local state"},{"op":"add","kind":"task","title":"Continue task","state":"working"}]}`
	runtimeJournalReceipt(t, s, source, c, "facts", "journal_batch", input)
	runtimeJournalInvoke(t, s, c, "facts", "journal_batch", input)
	change := runtimeMChangesEdit(t, s, source, "source-edit")
	child := source
	child.Agent = "child"
	runtimeJournalBind(t, s, child, c)
	runtimeJournalAdd(t, s, child, c, "child-task", "Live child")
	nativeParentReceipt(t, s, c, source, "spawn-child")
	if err := s.journal.parent(t.Context(), "spawn-child", child.Agent, "running"); err != nil {
		t.Fatal(err)
	}
	mount := `{"journal":[{"op":"add","kind":"task","title":"Integrate child","agent":"/root/child"}]}`
	runtimeJournalReceipt(t, s, source, c, "mount-child", "journal_batch", mount)
	runtimeJournalInvoke(t, s, c, "mount-child", "journal_batch", mount)
	readInput := runtimeMChangesInput(t, "--mine", "--summary")
	runtimeJournalReceipt(t, s, source, c, "old-read", "mchanges", readInput)
	sourceCtx, err := s.journal.scope(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := s.journal.journals.completedSlice(sourceCtx, s.owner.store, source.Workspace, observationThread(source), "source-turn")
	if err != nil || intent == nil {
		t.Fatalf("source continuation setup: %v %v", intent, err)
	}
	saved := runtimeResetJournal(t, s, source)
	check, err := s.journal.resetSession(t.Context(), "journal_reset_check", ObservationBinding{})
	if err != nil {
		t.Fatal(err)
	}
	if ready := check.(map[string]bool); len(ready) != 1 || !ready["ready"] {
		t.Fatalf("invalid reset check receipt: %+v", ready)
	}
	target := source
	target.Session = "reset-session"
	prepared, err := s.journal.resetSession(t.Context(), "journal_reset", target)
	if err != nil {
		t.Fatal(err)
	}
	text := prepared.(map[string]string)["text"]
	if !strings.Contains(text, "Keep local state") || !strings.Contains(text, "Continue task") || journalSummaryCharacters(text) > 10000 {
		t.Fatalf("missing or unbounded reset context: %q", text)
	}
	for _, b := range []ObservationBinding{source, child} {
		if err := s.owner.bind(t.Context(), b); err == nil {
			t.Fatal("retired binding accepted")
		}
		if err := s.owner.before(t.Context(), ObservationCall{Binding: b, ID: "late-edit", Tool: "Edit", Paths: []string{filepath.Join(source.Workspace, "source-edit.txt")}}); err == nil {
			t.Fatal("retired native edit accepted")
		}
	}
	if _, err := s.journal.invoke(t.Context(), "mchanges", "old-read", readInput); err == nil {
		t.Fatal("old native MCP receipt accepted after rotation")
	}
	runtimeJournalBind(t, s, target, c)
	fresh := runtimeResetJournal(t, s, target)
	expected := append([]journalItem(nil), saved.Items...)
	for i := range expected {
		expected[i].Agent = ""
		expected[i].RootEverReported = false
		// Working tasks keep accruing across the reset; only the total may grow.
		if i < len(fresh.Items) {
			before, after := expected[i].WorkTimer, fresh.Items[i].WorkTimer
			if after.Known != before.Known || after.ElapsedNS < before.ElapsedNS {
				t.Fatalf("reset lost work timer for %s: %+v -> %+v", expected[i].Path, before, after)
			}
			expected[i].WorkTimer = after
		}
	}
	if !reflect.DeepEqual(expected, fresh.Items) || fresh.ResetIntent != nil || fresh.TurnID != "" {
		t.Fatalf("reset lost facts or copied continuation: %+v", fresh)
	}
	targetCtx, err := s.journal.scope(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.journal.journals.listAgent(targetCtx, s.owner.store.scoped(targetCtx), target.Workspace, observationThread(target), "/root/child"); err == nil {
		t.Fatal("reset copied source child authority")
	}
	for range 2 {
		if _, err := s.journal.resetSession(t.Context(), "journal_reset_installed", target); err != nil {
			t.Fatal(err)
		}
	}
	installed := runtimeResetJournal(t, s, target)
	if len(installed.Items) != len(saved.Items)+1 {
		t.Fatalf("installed note is not idempotent: %+v", installed.Items)
	}
	if _, err := s.journal.resetSession(t.Context(), "journal_reset", target); err == nil {
		t.Fatal("same target reset replay accepted")
	}
	if _, err := s.journal.resetSession(t.Context(), "journal_reset", source); err == nil {
		t.Fatal("existing source scope overwritten")
	}
	if err := s.journal.startTurn(t.Context(), "target-turn"); err != nil {
		t.Fatal(err)
	}
	if err := s.journal.startTurn(t.Context(), "target-turn"); err != nil {
		t.Fatal(err)
	}
	if got := runtimeResetJournal(t, s, target); got.TurnID != "target-turn" || got.Turns != installed.Turns+1 {
		t.Fatalf("turn begin not idempotent: %+v", got)
	}
	runtimeJournalReceipt(t, s, target, c, "copied-read", "mchanges", readInput)
	result := runtimeMChangesDecode(t, runtimeJournalInvoke(t, s, c, "copied-read", "mchanges", readInput))
	if result.Stdout != "M\t1\t1\tsource-edit.txt\n" {
		t.Fatalf("own captured changes lost: %q", result.Stdout)
	}
	runtimeMChangesEdit(t, s, target, "target-edit")
	runtimeJournalAdd(t, s, target, c, "target-task", "Target only")
	original, found, err := readThreadJournal(s.owner.store.scoped(sourceCtx), source.Workspace, observationThread(source))
	if err != nil || !found || !reflect.DeepEqual(saved.Items, original.Items) {
		t.Fatalf("source journal changed: %v %v", found, err)
	}
	options, err := parseChangeReadAt([]string{"--mine", "--summary"}, source.Workspace, source.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	oldChanges := executeParsedChanges(sourceCtx, toolWorkerManifest{ReplayDirectory: s.owner.store.directory}, options)
	if oldChanges.ExitCode != 0 || oldChanges.Stderr != "" || oldChanges.Stdout != "M\t1\t1\tsource-edit.txt\n" {
		t.Fatalf("source capture stream changed: %+v", oldChanges)
	}
	// Fresh launch can bind only the target and recover retained facts, not live handles.
	s.owner.close()
	store, err := openMekugiReplayStore(s.owner.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := newNativeObservationOwner(t.Context(), store, target.Runtime, target.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.close()
	restored := &ObservationService{owner: owner}
	restored.EnableJournal()
	if err := owner.bind(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	if err := restored.journal.bind(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	resumed := runtimeResetJournal(t, restored, target)
	if resumed.ResetIntent != nil || owner.pendingCount.Load() != 0 || len(owner.live) != 0 {
		t.Fatal("restart revived process or reset continuation")
	}
	if recovery, err := restored.journal.recover(t.Context(), target); err != nil || !strings.Contains(recovery, "Keep local state") || !strings.Contains(recovery, "Target only") {
		t.Fatalf("restart lost facts: %q %v", recovery, err)
	}
	args := runtimeMChangesInput(t, "--summary", change)
	call := ObservationCall{Binding: target, ID: "resumed-read", Tool: "mcp__mekugi__mchanges", Input: args}
	if err := restored.journal.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	body, err := restored.journal.invoke(t.Context(), "mchanges", call.ID, args)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if result := runtimeMChangesDecode(t, string(data)); result.Stdout != "M\t1\t1\tsource-edit.txt\n" {
		t.Fatalf("restart lost copied capture: %q", result.Stdout)
	}
}

func TestRuntimeJournalResetRejectsInvalidTargetsWithoutTransfer(t *testing.T) {
	s, source, c := runtimeJournalFixture(t)
	runtimeJournalAdd(t, s, source, c, "saved", "Keep task")
	valid := source
	valid.Session = "new-session"
	for _, mutate := range []func(*ObservationBinding){
		func(b *ObservationBinding) { b.Workspace = t.TempDir() },
		func(b *ObservationBinding) { b.Agent = "child" },
		func(b *ObservationBinding) { b.Runtime = "codex" },
		func(b *ObservationBinding) { b.Branch = "fork" },
		func(b *ObservationBinding) { b.Session = source.Session },
	} {
		target := valid
		mutate(&target)
		if _, err := s.journal.resetSession(t.Context(), "journal_reset", target); err == nil {
			t.Fatal("invalid reset target accepted")
		}
		if s.journal.rootBinding() != source {
			t.Fatal("invalid target rotated source")
		}
		if _, exists, err := s.owner.store.readHandleScope(observationThread(valid)); err != nil || exists {
			t.Fatalf("invalid target transferred scope: %v %v", exists, err)
		}
	}
	if nodes := runtimeJournalRead(t, s, source, c, "still-bound", `{}`); len(nodes) != 1 || nodes[0].Title != "Keep task" {
		t.Fatalf("source authority lost: %+v", nodes)
	}
}

func TestRuntimeJournalResetWaitsForPendingObservation(t *testing.T) {
	s, source, c := runtimeJournalFixture(t)
	runtimeJournalAdd(t, s, source, c, "saved", "Keep task")
	path := filepath.Join(source.Workspace, "pending.txt")
	nativeObservationWrite(t, path, "before\n")
	call := ObservationCall{Binding: source, ID: "pending", Tool: "Edit", Paths: []string{path}}
	if err := s.owner.before(t.Context(), call); err != nil {
		t.Fatal(err)
	}
	target := source
	target.Session = "new-session"
	for _, op := range []string{"journal_reset_check", "journal_reset"} {
		if _, err := s.journal.resetSession(t.Context(), op, target); err == nil {
			t.Fatal("pending capture allowed reset")
		}
		if s.journal.rootBinding() != source {
			t.Fatal("pending capture rotated session")
		}
	}
	if _, exists, err := s.owner.store.readHandleScope(observationThread(target)); err != nil || exists {
		t.Fatalf("pending reset transferred scope: %v %v", exists, err)
	}
	nativeObservationWrite(t, path, "after\n")
	if _, err := s.owner.after(t.Context(), call, ObservationTerminal{Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.journal.resetSession(t.Context(), "journal_reset", target); err != nil {
		t.Fatal(err)
	}
	if status, body := observationPost(t, s, c, s.Endpoint().Token, observationRequest{Operation: "before", Call: call}); status != http.StatusUnprocessableEntity {
		t.Fatalf("settled source call accepted: %d %s", status, body)
	}
}

func TestRuntimeJournalResetStopIsSerializedWithRotation(t *testing.T) {
	for _, rotated := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[rotated], func(t *testing.T) {
			s, ancestor, c := runtimeJournalFixture(t)
			runtimeJournalAdd(t, s, ancestor, c, "work", "Keep working")
			source := ancestor
			source.Session = "source-fork"
			if _, err := s.journal.resetSession(t.Context(), "journal_reset", source); err != nil {
				t.Fatal(err)
			}
			runtimeJournalBind(t, s, source, c)
			target := source
			target.Session = "target-fork"
			if rotated {
				if _, err := s.journal.resetSession(t.Context(), "journal_reset", target); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.journal.cancelReset(t.Context(), "user-stop", source); err != nil {
				t.Fatal(err)
			}
			check := func(b ObservationBinding, want bool) {
				j, found, err := readThreadJournal(s.owner.store, b.Workspace, observationThread(b))
				if err != nil || !found {
					t.Fatalf("read stopped branch: %v %v", found, err)
				}
				if (j.StoppedTasks["/1"] != "") == !want {
					t.Fatalf("wrong branch pause for %s: %+v", b.Session, j.StoppedTasks)
				}
			}
			check(ancestor, false)
			check(source, true)
			if !rotated {
				if _, err := s.journal.resetSession(t.Context(), "journal_reset", target); err != nil {
					t.Fatal(err)
				}
			}
			check(target, true)
		})
	}
}
