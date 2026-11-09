package executionobservation

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/apps/mekugi/internal/sessionreview"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/commandanalysis"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestBoundedReceiptAdaptersKeepOmissionUncertainty(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := diffmodel.RenderReviewFile("", "/w/large", "", strings.Repeat("01234567890123456789012345678901234567890123456789\n", 1000))
	for _, chunked := range []bool{false, true} {
		review := evidence.Inline(file)
		if chunked {
			budget := 0
			review = evidence.RetainReview(t.Context(), captureSource(store.changeScope()), file, &budget)
		}
		value := mekugiHistory{ToolName: nativeExecCommandToolName, CorrelationID: "writer\x00exec", ExecOutcome: &changecapture.Outcome{Status: changecapture.StatusCompleted}, reviewFiles: []evidence.Review{review}}
		live := presentation.NewReceipt("/w", receiptEvidence(value))
		retained, err := sessionreview.CapturedRetainedReceipt("/w", retainedHistory(value), value.reviews())
		if err != nil || retained == nil {
			t.Fatalf("retained receipt: %v", err)
		}
		for _, receipt := range []*presentation.Receipt{live, retained} {
			if !strings.Contains(receipt.Text, "… more diff content") || !strings.Contains(receipt.Text, "+1000 -0") {
				t.Fatalf("chunked=%v adapter dropped truncation or totals", chunked)
			}
		}
	}
}

func TestLargeNamedCaptureRetainsEveryFileAcrossRestart(t *testing.T) {
	f := newMChangesSliceFixture(t, "large-named")
	selected := f.store.scoped(f.ctx).changeScope()
	data := "before\n" + strings.Repeat("unchanged text with enough rows to keep a small changed region...\n", 6144)
	var paths []string
	for i := range 27 {
		path := filepath.Join(f.workspace, fmt.Sprintf("file-%02d.txt", i))
		writeTestFile(t, path, data)
		paths = append(paths, path)
	}
	// Isolate the aggregate-byte regression from the independent 500 ms native
	// forwarding deadline. The same acquisition owner is used by native tools.
	observation := changecapture.NamedUntil(paths, f.workspace, time.Now().Add(10*time.Second), captureSource(selected))
	resident := 0
	for _, file := range observation.Files {
		resident += len(file.Content)
		if file.Error != "" || file.ContentRef == nil && file.Content != data {
			t.Fatalf("baseline %s: error=%q retained=%v", file.Path, file.Error, file.ContentRef != nil)
		}
	}
	if len(observation.Files) != len(paths) || len(observation.Omitted) != 0 || resident > changecapture.MaxContentBytes {
		t.Fatalf("bounded acquisition lost files: files=%d omissions=%v resident=%d", len(observation.Files), observation.Omitted, resident)
	}
	script := "printf 'run\\n' >> executions; sed -i '1s/before/after/' -- file-*.txt"
	observation.Commands = []commandanalysis.Command{{Command: script, Workdir: f.workspace, Shell: "bash"}}
	original := mekugiHistory{ToolName: nativeExecCommandToolName, Script: script, ExecutingThread: f.thread, ExecObservation: observation, CarrierPayload: `{"cmd":"original native input"}`}
	if err := f.store.put(f.ctx, f.workspace, map[string]mekugiHistory{"large": original}); err != nil {
		t.Fatal(err)
	}
	host := exec.Command(execTrackShellExecutable(t, "bash"), "-c", script)
	host.Dir = f.workspace
	if output, err := host.CombinedOutput(); err != nil {
		t.Fatalf("host: %s, %v", output, err)
	}
	p := testProxyWithStore(f.store)
	members := []execCompletion{{callID: "large", history: original, result: binding.DecodeExecutionResult(nativeExecCommandToolName, responses.MustMarshalJSON(map[string]any{"exit_code": 0}))}}
	for range 2 {
		if err := finalizeExecObservations(p.Retained, p.Changes, p.Checkpoints, p.Recovery, p.PublishChanges, p.Windows, p.Audience, p.Notice, f.ctx, f.workspace, members); err != nil {
			t.Fatal(err)
		}
	}
	if runs, err := os.ReadFile(filepath.Join(f.workspace, "executions")); err != nil || string(runs) != "run\n" {
		t.Fatalf("capture repeated host execution: %q, %v", runs, err)
	}
	reopened, err := openMekugiReplayStore(f.store.directory)
	if err != nil {
		t.Fatal(err)
	}
	cold := reopened.scoped(f.ctx).changeScope()
	base, found, err := lookupObservation(f.ctx, cold, f.workspace, "large")
	if err != nil || !found || base.Script != script || base.CarrierPayload != original.CarrierPayload || len(base.ExecObservation.Files) != 27 {
		t.Fatalf("cold original host facts: found=%v error=%v", found, err)
	}
	for _, file := range base.ExecObservation.Files {
		if file.ContentRef == nil {
			t.Fatalf("published baseline was not chunked: %s", file.Path)
		}
	}
	completed, found, err := lookupObservation(f.ctx, cold, f.workspace, legacycall.ExecCallID("large", false))
	if err != nil || !found || len(completed.reviews()) != 27 || completed.ExecOutcome.Coverage != changecapture.CoverageExact {
		t.Fatalf("cold complete evidence: found=%v reviews=%d error=%v", found, len(completed.reviews()), err)
	}
	query := changes.Query{Workspace: f.workspace, IDs: []string{completed.ChangeID}, Paths: []string{paths[26]}}
	text, err := reopened.changes.Read(f.ctx, cold, query)
	if err != nil || !strings.Contains(text, "-before\n") || !strings.Contains(text, "+after\n") || strings.Contains(text, "file-00") {
		t.Fatalf("cold selected last file: bytes=%d prefix=%q error=%v", len(text), text[:min(len(text), 256)], err)
	}
	out, stderr, status := f.run(t, "mchanges revert "+completed.ChangeID+" -- file-26.txt")
	if status != 0 {
		t.Fatalf("revert: %s %s (%d)", out, stderr, status)
	}
	if restored, err := os.ReadFile(paths[26]); err != nil || string(restored) != data {
		t.Fatalf("large captured baseline not reversible: %v", err)
	}
	if untouched, err := os.ReadFile(paths[0]); err != nil || !strings.HasPrefix(string(untouched), "after\n") {
		t.Fatalf("unselected sibling changed: %v", err)
	}
	out, stderr, status = f.run(t, "mchanges apply "+completed.ChangeID+" -- file-26.txt")
	if status != 0 {
		t.Fatalf("apply: %s %s (%d)", out, stderr, status)
	}
	t.Logf("source_bytes=%d files=%d retained_inline_bytes=%d complete_reviews=%d", len(data)*27, len(observation.Files), resident, len(completed.reviews()))
}

func TestMissingChunkPreservesIndependentReadTargetsAndRejectsMutation(t *testing.T) {
	f := newMChangesSliceFixture(t, "missing-chunk")
	badPath, goodPath := filepath.Join(f.workspace, "bad.txt"), filepath.Join(f.workspace, "good.txt")
	before, after := strings.Repeat("before\n", 10000), strings.Repeat("after\n", 10000)
	writeTestFile(t, badPath, after)
	writeTestFile(t, goodPath, "new\n")
	bad, good := f.reserve(t, f.thread, "bad"), f.reserve(t, f.thread, "good")
	f.publish(t, bad, "bad", "bad-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(badPath, badPath, before, after)}})
	f.publish(t, good, "good", "good-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile(goodPath, goodPath, "old\n", "new\n")}})
	record, found, err := f.store.publication.ReadAttachedHostCall(f.workspace, "bad-call", false)
	if err != nil || !found {
		t.Fatalf("retained call: %v %v", found, err)
	}
	reviews, err := evidence.Reviews(&evidence.Source{Store: f.store.publication}, record)
	if err != nil || len(reviews) != 1 || reviews[0].Text == nil {
		t.Fatalf("large review was not retained: %v", err)
	}
	missing := reviews[0].Text.Parts[0]
	if err := os.Remove(filepath.Join(f.store.directory, missing)); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"", "--history ", "--summary ", "--list "} {
		out, stderr, status := f.run(t, "mchanges "+view+bad+" "+good)
		wanted, unwanted := "good.txt", "bad.txt"
		if view == "--list " {
			wanted, unwanted = good, bad
		}
		if status != 1 || !strings.Contains(stderr, missing) || !strings.Contains(out, wanted) || strings.Contains(out, unwanted) {
			t.Fatalf("%q lost independent target: %q %q (%d)", view, out, stderr, status)
		}
	}
	_, stderr, status := f.run(t, "mchanges revert "+bad+" "+good)
	if status != 1 || !strings.Contains(stderr, missing) {
		t.Fatalf("missing mutation source accepted: %q (%d)", stderr, status)
	}
	for path, want := range map[string]string{badPath: after, goodPath: "new\n"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("rejected mutation changed %s: %v", path, err)
		}
	}
}

func TestCaptureStorageFailureClassificationAndRetryCause(t *testing.T) {
	for _, captured := range []bool{false, true} {
		t.Run(fmt.Sprintf("captured=%v", captured), func(t *testing.T) {
			store, err := openMekugiReplayStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			selected := store.changeScope()
			selected.Limits.Total = 1
			value := mekugiHistory{ToolName: "shell", Script: "true"}
			if captured {
				value.ReviewFiles = []diffmodel.ReviewFile{diffmodel.RenderReviewFile("/w/a", "/w/a", "before\n", "after\n")}
			}
			err = publishObservations(t.Context(), selected, store.changes, nil, "/w", map[string]mekugiHistory{"call": value})
			diagnostic, ok := errors.AsType[*requestexecution.DiagnosticError](err)
			if !ok || diagnostic.Code != "storage_capacity" {
				t.Fatalf("storage failure lost classification: %T %v", err, err)
			}
			if _, ok := errors.AsType[*history.CapacityError](err); !ok {
				t.Fatalf("retry cause lost: %v", err)
			}
		})
	}
}
