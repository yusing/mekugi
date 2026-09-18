package router

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/patchtest"
)

func TestRecoverScriptEditsPreparedText(t *testing.T) {
	const unrelated = "new keep.txt\ntype <<TEXT\n|type <<PATCH\n|PATCH\nTEXT\n"
	for _, test := range []struct {
		name, broken, correction, fixed string
	}{
		{"path", "in wrong.txt\n", `type "wrong.txt" "right.txt"`, "in right.txt\n"},
		{"operation", "wrong command\n", `type "wrong command" "rm"`, "rm\n"},
		{"value", "in f.txt\ntype \"old\" \"bad\"\n", `type "bad" "good"`, "in f.txt\ntype \"old\" \"good\"\n"},
		{"framing", "new f.txt\ntype <<END\nbody\nWRONG\n", `type "WRONG" "END"`, "new f.txt\ntype <<END\nbody\nEND\n"},
		{"missing close", "new f.txt\ntype <<TEXT\n|body\n", `add EOF "TEXT\n"`, "new f.txt\ntype <<TEXT\n|body\nTEXT\n"},
		{"remove conflict", "in f.txt\ntype \"old\" \"new\"\ntype \"old\" \"again\"\n",
			`type "type \"old\" \"again\"\n" ""`, "in f.txt\ntype \"old\" \"new\"\n"},
		{"multiple fields", "in wrong.txt\ntype \"old\" \"bad\"\n",
			"type \"wrong.txt\" \"right.txt\"\ntype \"bad\" \"good\"", "in right.txt\ntype \"old\" \"good\"\n"},
		{"literal protocol value", "new f.txt\ntype \"bad\"\n",
			"type \"type \\\"bad\\\"\\n\" <<'END'\ntype <<PATCH\nvalue\nPATCH\nEND\n", "new f.txt\ntype <<PATCH\nvalue\nPATCH\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A large unrelated prepared value is retained rather than re-emitted.
			prepared := unrelated + "new large.txt\ntype " + strconv.Quote(strings.Repeat("retained ", 10000)) + "\n"
			got, err := recoverScriptDetailed(t.Context(), prepared+test.broken, test.correction, testRecoveryHandles(prepared+test.broken))
			if err != nil || got.script != prepared+test.fixed || got.delta == "" {
				t.Fatalf("recovery error %v; changed unrelated content or failed correction", err)
			}
		})
	}
}

func TestTextRecoveryThroughRouterTranslationAndAncestry(t *testing.T) {
	dataDirectory := t.TempDir()
	outcomePath := filepath.Join(t.TempDir(), "outcomes.txt")
	command := "printf '%s\\n' {{shellquote .Stage}}'|'{{shellquote .Outcome}}'|'{{shellquote .ToolName}}'|'{{.EmittedBytes}}'|'{{.EvaluatedBytes}} >> " + shellQuoteArgument(outcomePath)
	settings, err := json.Marshal(map[string]any{"hooks": map[string]any{"outcome": []string{command}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDirectory, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	transform, _, _, directory := newMekugiTestTransform(t, newInProcessMekugiTranslator(dataDirectory))
	path := filepath.Join(directory, "file.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const base = "in file.txt\ntype \"missing\" \"new\"\nnew keep.txt\ntype \"kept\\n\"\n"
	first, err := transform.translate("original", base, nil)
	if err != nil || !first.EvaluatorRejected || !strings.Contains(first.TranslationError, "Retained rejected-script rows:") {
		t.Fatalf("initial rejection: %v, %+v", err, first)
	}
	partial, err := transform.translateRecovery("partial", `type "missing" "stillmissing"`, nil)
	if err != nil || !partial.EvaluatorRejected || !strings.Contains(partial.recoveryBaseline(), "stillmissing") || partial.Attempt != 2 {
		t.Fatalf("partial correction: %v, %+v", err, partial)
	}
	unchanged, err := transform.translateRecovery("unchanged", `type "stillmissing" "stillmissing"`, nil)
	if err != nil || !unchanged.Unevaluated || unchanged.Attempt != 3 {
		t.Fatalf("unchanged correction: %v, %+v", err, unchanged)
	}
	const payload = `type "stillmissing" "old"`
	fixed, err := transform.translateRecovery("fixed", payload, nil)
	if err != nil || fixed.TranslationError != "" || fixed.Attempt != 4 || fixed.CorrelationID != "original" ||
		fixed.ToolName != mekugiRecoveryToolName || fixed.Script != payload || !strings.Contains(fixed.Evaluated, "new keep.txt") {
		t.Fatalf("fixed correction: %v, %+v", err, fixed)
	}
	tree, err := patchtest.Apply(map[string]string{"file.txt": "old\n"}, fixed.Patch)
	if err != nil || tree["file.txt"] != "new\n" || tree["keep.txt"] != "kept\n" {
		t.Fatalf("host patch = %+v, %v", tree, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "old\n" {
		t.Fatalf("translation applied a workspace mutation: %q, %v", content, err)
	}
	outcomes, err := os.ReadFile(outcomePath)
	wantOutcome := fmt.Sprintf("translated|succeeded|hpatch_recover|%d|%d\n", len(payload), len(fixed.Evaluated))
	if err != nil || !strings.Contains(string(outcomes), wantOutcome) || strings.Count(string(outcomes), "\n") != 4 {
		t.Fatalf("outcome identity/count = %q, %v; want final %q and four attempts", outcomes, err, wantOutcome)
	}
	storePath := t.TempDir()
	store, err := openMekugiReplayStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.put(t.Context(), directory, map[string]mekugiHistory{"fixed": fixed, "partial": partial}); err != nil {
		t.Fatal(err)
	}
	reopened, err := openMekugiReplayStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	replayed, ok, err := reopened.lookup(t.Context(), directory, "fixed")
	if err != nil || !ok || replayed.Script != payload || replayed.Evaluated != fixed.Evaluated ||
		replayed.ToolName != mekugiRecoveryToolName || replayed.CorrelationID != fixed.CorrelationID {
		t.Fatalf("replay lost original correction or rebuilt script: %v, found %v", err, ok)
	}
	rejected, ok, err := reopened.lookup(t.Context(), directory, "partial")
	if err != nil || !ok || rejected.recoveryBaseline() != partial.recoveryBaseline() || !rejected.EvaluatorRejected {
		t.Fatalf("replay lost partial correction baseline: %v, found %v", err, ok)
	}
	afterSuccess, err := transform.translateRecovery("too-late", `type "old" "missing"`, nil)
	if err != nil || !afterSuccess.Unevaluated || !strings.Contains(afterSuccess.TranslationError, "most recent mekugi call succeeded") {
		t.Fatalf("recovered an older rejection after success: %v, %+v", err, afterSuccess)
	}
}

func TestTextRecoveryCannotUseNonvisibleOrCrossWorktreeHistory(t *testing.T) {
	for _, crossWorktree := range []bool{false, true} {
		transform, proxy, _, directory := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
		history := mekugiHistory{
			ToolName: mekugiToolName, Script: "in f.txt\ntype \"bad\" \"value\"\n",
			Root: directory + "-other", sequence: 1, EvaluatorRejected: true, TranslationError: "rejected",
		}
		if crossWorktree {
			transform.visible["old"] = history
		} else if err := proxy.rememberBatch(transform.historySessionID, map[string]mekugiHistory{"old": history}); err != nil {
			t.Fatal(err)
		}
		result, err := transform.translateRecovery("new", `type "bad" "good"`, nil)
		if err != nil || !result.Unevaluated {
			t.Fatalf("used inaccessible history: %v, %+v", err, result)
		}
	}
}
func TestRecoverScriptTextRejectsInvalidOrUnchangedEdits(t *testing.T) {
	const baseline = "new f.txt\ntype \"bad\"\n"
	row := strings.Fields(mekugi.TextReferences(baseline, 2))[0]
	for _, payload := range []string{
		`type "bad" "bad"`,
		`type "missing" "good"`,
		"type 2:ffff \"good\"",
		`type "new f.txt\ntype \"bad\"\n" ""`,
		"type " + row + " \"good\"\ntype " + row + " \"other\"",
		"type \"bad\" \"good\"\nC2:aaaa 1:bbbb",
		"type \"bad\" \"good\"\nrm",
		"type \"bad\" \"good\"\nin other.txt",
	} {
		got, err := recoverScriptDetailed(t.Context(), baseline, payload, testRecoveryHandles(baseline))
		if err == nil || got.script != "" {
			t.Fatalf("accepted invalid correction %q: %v", payload, err)
		}
	}
}

func TestRecoverScriptTextBoundsExpansion(t *testing.T) {
	baseline := "new f.txt\ntype \"xx\"\n"
	payload := `type "x" 2 ` + strconv.Quote(strings.Repeat("v", maxMekugiScriptBytes/2+1))
	got, err := recoverScriptDetailed(t.Context(), baseline, payload, testRecoveryHandles(baseline))
	if err == nil || got.script != "" || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expanded recovery accepted: %d bytes, %v", len(got.script), err)
	}
}

func TestTextCorrectionInvalidatesUnchangedCommandHandles(t *testing.T) {
	transform, _, _, _ := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	first, err := transform.translate("original", "in old.txt\ntype 1:aaaa \"value\"\n", nil)
	if err != nil || !first.EvaluatorRejected {
		t.Fatalf("initial rejection: %+v, %v", first, err)
	}
	changed, err := transform.translateRecovery("changed", `type "old.txt" "new.txt"`, nil)
	if err != nil || !changed.EvaluatorRejected {
		t.Fatalf("changed rejection: %+v, %v", changed, err)
	}
	result, err := transform.translateRecovery("stale", first.RecoveryHandles[1]+" 2:bbbb", nil)
	if err != nil || !result.Unevaluated || !strings.Contains(result.TranslationError, "stale") {
		t.Fatalf("old handle crossed changed file context: %+v, %v", result, err)
	}
}
