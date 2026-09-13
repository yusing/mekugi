package router

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHpatchMixedChangesSurviveRepairAndRestart(t *testing.T) {
	t.Parallel()
	transform, overrides := mixedTestTransform(t)
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transform.proxy.replayStore = store
	run := func(callID, source string) mixedScriptResult {
		t.Helper()
		history, err := transform.translate(callID, source, nil)
		if err != nil || history.TranslationError != "" {
			t.Fatalf("translate: %v, %s", err, history.TranslationError)
		}
		if history.ChangeID != "hp_a1" {
			t.Fatalf("change ID: %q", history.ChangeID)
		}
		if err := store.put(t.Context(), transform.directory, map[string]mekugiHistory{callID: history}); err != nil {
			t.Fatal(err)
		}
		var result mixedScriptResult
		runShellCatJavaScript(t, transform.proxy.registry.NodeExecutable, transform.directory, history.carrierInput(), &result, overrides)
		if result.ChangeID != history.ChangeID {
			t.Fatalf("result identity: %q", result.ChangeID)
		}
		return result
	}
	first := run("mixed-change", "new first.txt\ntype \"first\\n\"\nshell printf untracked > incidental.txt; test -f fixed.txt\nnew last.txt\ntype \"last\\n\"")
	if first.Sequence.Stopped != "nonzero_exit" {
		t.Fatalf("first: %+v", first)
	}
	last := run("mixed-repair", "resume "+first.ResumeHandle+" repair\nnew fixed.txt\ntype \"fixed\\n\"")
	if last.Sequence.Stopped != "" {
		t.Fatalf("repair: %+v", last)
	}
	invalid, err := transform.translateRecovery("wrong-recovery", `type "first" "other"`, nil)
	if err != nil || invalid.ChangeID != "hp_a1" || !invalid.Unevaluated || !strings.Contains(invalid.TranslationError, "checkpoints") {
		t.Fatalf("invalid recovery: %+v, %v", invalid, err)
	}
	badResume, err := transform.translate("bad-resume", "resume "+first.ResumeHandle+" unknown", nil)
	if err != nil || badResume.ChangeID != "hp_a1" || badResume.TranslationError == "" {
		t.Fatalf("invalid resume: %+v, %v", badResume, err)
	}
	if err := store.put(t.Context(), transform.directory, map[string]mekugiHistory{"wrong-recovery": invalid, "bad-resume": badResume}); err != nil {
		t.Fatal(err)
	}
	// Review evidence survives both a new store instance and later workspace edits.
	if err := os.WriteFile(filepath.Join(transform.directory, "first.txt"), []byte("later\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	output, err := reopened.readChanges(t.Context(), changeReadOptions{workspace: transform.directory, ids: []string{"hp_a1"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"+first", "+fixed", "+last"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q: %s", expected, output)
		}
	}
	historyOutput, err := reopened.readChanges(t.Context(), changeReadOptions{workspace: transform.directory, ids: []string{"hp_a1"}, view: "history"})
	if err != nil || !strings.Contains(historyOutput, `type "first" "other"`) || !strings.Contains(historyOutput, " unknown") {
		t.Fatalf("history: %s, %v", historyOutput, err)
	}
	if strings.Count(output, " applied\n") != 3 || strings.Contains(output, "incidental.txt") || strings.Contains(output, "+later") {
		t.Fatalf("review: %s", output)
	}
}

func TestHpatchMixedChangesRequireApplicationReceipt(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := hpatchResumeState{Handle: "Mtest", Root: t.TempDir(), CorrelationID: "mixed", ReplayDirectory: store.directory}
	state.ChangeID, err = store.reserveChange(t.Context(), state.Root, "thread", state.CorrelationID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := state.translateTracked(t.Context(), "new target.txt\ntype \"value\\n\"")
	if err != nil {
		t.Fatal(err)
	}
	options := changeReadOptions{workspace: state.Root, ids: []string{state.ChangeID}}
	output, err := store.readChanges(t.Context(), options)
	if err != nil || !strings.Contains(output, "prepared (application unconfirmed)") {
		t.Fatalf("prepared: %s, %v", output, err)
	}
	if err := state.confirmTracked(t.Context(), "another-handle-attempt"); err == nil {
		t.Fatal("accepted another handle")
	}
	if err := state.confirmTracked(t.Context(), result.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := state.confirmTracked(t.Context(), result.AttemptID); err != nil {
		t.Fatal(err)
	}
	rejected, err := state.translateTracked(t.Context(), "in missing.txt\ntype \"a\" \"b\"")
	if err != nil || rejected.Diagnostic == "" {
		t.Fatalf("rejection: %+v, %v", rejected, err)
	}
	if err := state.confirmTracked(t.Context(), rejected.AttemptID); err == nil {
		t.Fatal("confirmed rejection")
	}
	output, err = store.readChanges(t.Context(), options)
	if err != nil || strings.Count(output, " applied\n") != 1 || !strings.Contains(output, " rejected\n") {
		t.Fatalf("receipts: %s, %v", output, err)
	}
}
