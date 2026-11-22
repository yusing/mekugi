package executionobservation

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/outputbudget"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestNativeFinalProjectionUsesOnlySelectedRetainedRanges(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final-projection")
	id := f.reserve(t, "worker", "edit")
	f.publish(t, id, "edit", "edit-call", mekugiHistory{Caller: "/root/worker", ReviewFiles: []diffmodel.ReviewFile{
		diffmodel.RenderReviewFile("seen.txt", "seen.txt", "before\n", "seen-content\n"),
		diffmodel.RenderReviewFile("remaining.txt", "remaining.txt", "before\n", "remaining-content\n"),
	}})
	p := testProxyWithStore(f.store)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
	frozen, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || frozen == nil || frozen.ResourceRef == "" || !strings.Contains(frozen.FullText, "+seen-content") || !strings.Contains(frozen.FullText, "+remaining-content") || reader.reads.Load() != 1 {
		t.Fatalf("full frozen capture=%+v err=%v reads=%d", frozen, err, reader.reads.Load())
	}
	source, _ := finalCaptureSource(t, frozen.FullText, "'mchanges' '"+id+"'")
	explicit, _, status := f.run(t, "mchanges "+id)
	if status != 0 || source != explicit {
		t.Fatalf("attached source and explicit diff do not match: status=%d, source=%q, explicit=%q", status, source, explicit)
	}
	readerScope, err := p.nativeCaptureScope(f.thread, f.thread)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := readerScope.Scope.Store.ReadJSONResource(frozen.ResourceRef)
	if err != nil || strings.Contains(string(metadata), "seen-content") || strings.Contains(string(metadata), "remaining-content") || strings.Contains(string(metadata), "FullText") {
		t.Fatalf("selection duplicated captured evidence: %q, %v", metadata, err)
	}
	record, found, err := f.store.read(f.workspace, "edit-call", false)
	if err != nil || !found {
		t.Fatalf("captured record=%v err=%v", found, err)
	}
	key := changes.CaptureKey(f.workspace, "/root/worker", id, record.CaptureOrder)
	// A late writer extends this same change ID after the final was frozen.
	f.publish(t, id, "edit", "late-call", mekugiHistory{Caller: "/root/worker", ReviewFiles: []diffmodel.ReviewFile{
		diffmodel.RenderReviewFile("late.txt", "late.txt", "before\n", "late-content\n"),
	}})
	projection := appserver.FinalCaptureProjectRequest{RootThreadID: input.RootThreadID, ThreadID: input.ThreadID, OperationID: input.OperationID,
		RecipientThreadID: input.RecipientThreadID, RecipientCwd: input.RecipientCwd, RecipientContextID: "context-one", ResourceRef: frozen.ResourceRef,
		Seen: []appserver.CaptureCursor{{Key: key, ThroughChunk: 1}}}
	for range 2 {
		projected, err := p.ProjectNativeFinalCapture(t.Context(), projection)
		if err != nil || strings.Contains(projected.Text, "+seen-content") || strings.Contains(projected.Text, "late-content") || !strings.Contains(projected.Text, "+remaining-content") || reader.reads.Load() != 1 {
			t.Fatalf("projection reselected or lost frozen chunks: %q err=%v reads=%d", projected.Text, err, reader.reads.Load())
		}
	}
	// Native resets/resume rotate context and supply no acknowledgments. The
	// projection restores the original selection, without reselecting mutable IDs.
	projection.RecipientContextID, projection.Seen = "replacement-context", nil
	projected, err := p.ProjectNativeFinalCapture(t.Context(), projection)
	if err != nil || projected.Text != frozen.FullText || reader.reads.Load() != 1 {
		t.Fatalf("replacement context changed selected evidence: %q err=%v", projected.Text, err)
	}
	projection.Seen = []appserver.CaptureCursor{{Key: key, ThroughChunk: 2}}
	projection.RecipientContextID = ""
	if projected, err := p.ProjectNativeFinalCapture(t.Context(), projection); err != nil || projected.Text != frozen.FullText {
		t.Fatalf("unavailable delivery context suppressed full capture: %q err=%v", projected.Text, err)
	}
	projection.OperationID = "different-operation"
	if _, err := p.ProjectNativeFinalCapture(t.Context(), projection); err == nil {
		t.Fatal("another operation used the frozen resource")
	}
	if err := p.AdmitNativeChild(t.Context(), appserver.ChildHostAdmissionRequest{RootThreadID: f.thread, ParentThreadID: f.thread, ThreadID: "other-recipient"}); err != nil {
		t.Fatal(err)
	}
	projection.OperationID, projection.RecipientThreadID = input.OperationID, "other-recipient"
	if _, err := p.ProjectNativeFinalCapture(t.Context(), projection); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("another admitted recipient used the frozen resource: %v", err)
	}
	projection.OperationID, projection.RecipientThreadID = input.OperationID, "unadmitted"
	if _, err := p.ProjectNativeFinalCapture(t.Context(), projection); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("unadmitted recipient reached resource: %v", err)
	}
	if reader.reads.Load() != 1 {
		t.Fatal("projection or rejection repeated range selection")
	}
}

func TestNativeFinalRangeRecoveryExcludesLaterCaptures(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final-recovery")
	id := f.reserve(t, "worker", "large")
	content := "visible line of retained evidence\n" +
		strings.Repeat("oversized line of retained evidence ", 1600) + "frozen-end\n"
	f.publish(t, id, "large", "large-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", "large.txt", "", content)}})
	p := testProxyWithStore(f.store)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
	frozen, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || frozen == nil || frozen.ResourceRef == "" {
		t.Fatalf("freeze=%+v err=%v", frozen, err)
	}
	_, continuation, found := strings.Cut(frozen.FullText, "Read the remaining output with: mcat ")
	if !found || len(strings.Fields(continuation)) == 0 {
		t.Fatalf("large full capture has no exact recovery: %q", frozen.FullText)
	}
	path := strings.Trim(strings.Fields(continuation)[0], "'")
	readerScope, err := p.nativeCaptureScope(f.thread, f.thread)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := readerScope.Scope.Store.ReadReadOnlyFile(path, history.MaxRecordBytes)
	if err != nil || !strings.Contains(string(recovered), "frozen-end") {
		t.Fatalf("full fallback lost omitted suffix: %v", err)
	}
	command := "'mchanges' '" + id + "'"
	source, payload := finalCaptureSource(t, frozen.FullText, command)
	header := command + ": exit_code=0\n"
	want := header + diffmodel.RenderReviewFile("", "large.txt", "", content).UnifiedDiff() + "\n"
	if string(recovered) != want {
		t.Fatalf("frozen completion file changed original bytes: got %d bytes, want %d", len(recovered), len(want))
	}
	if !strings.HasSuffix(source, "+visible line of retained evidence\n") || !strings.Contains(payload, "+oversized line") || strings.Contains(source, "oversized line") {
		t.Fatal("a partially visible row was marked as a deduplication source")
	}
	if tokens, err := new(outputbudget.Budget).Count(frozen.FullText); err != nil || tokens > CompletionTokens {
		t.Fatalf("framing exceeded completion budget: %d, %v", tokens, err)
	}
	f.publish(t, id, "large", "late-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", "late.txt", "", "late-content\n")}})
	projection := appserver.FinalCaptureProjectRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "work", RecipientThreadID: f.thread,
		RecipientCwd: &f.workspace, RecipientContextID: "replacement", ResourceRef: frozen.ResourceRef}
	projected, err := p.ProjectNativeFinalCapture(t.Context(), projection)
	if err != nil || strings.Contains(projected.Text, "late-content") || reader.reads.Load() != 1 {
		t.Fatalf("projection reselected mutable IDs: %v reads=%d", err, reader.reads.Load())
	}
	_, projectedContinuation, ok := strings.Cut(projected.Text, "Read the remaining output with: mcat ")
	if !ok || len(strings.Fields(projectedContinuation)) == 0 {
		t.Fatal("projected selection lost bounded recovery")
	}
	projectedPath := strings.Trim(strings.Fields(projectedContinuation)[0], "'")
	projectedFull, err := readerScope.Scope.Store.ReadReadOnlyFile(projectedPath, history.MaxRecordBytes)
	if err != nil || string(projectedFull) != string(recovered) {
		t.Fatalf("rematerialized range changed recovery content: %v", err)
	}
	again, err := readerScope.Scope.Store.ReadReadOnlyFile(path, history.MaxRecordBytes)
	if err != nil || string(again) != string(recovered) || strings.Contains(string(again), "late-content") {
		t.Fatalf("retained recovery changed after late writer: %v", err)
	}
}

func TestFrozenCaptureProjectionCountsDiagnosticPrefixes(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-diagnostic-prefix")
	id := f.reserve(t, "worker", "edit")
	diagnostic := diffmodel.RenderReviewFile("git.txt", "git.txt", "before\n", "imported\n")
	diagnostic.Origin = "git checkout"
	f.publish(t, id, "edit", "edit-call", mekugiHistory{Caller: "/root/worker", ReviewFiles: []diffmodel.ReviewFile{
		diagnostic, diffmodel.RenderReviewFile("own.txt", "own.txt", "before\n", "authored\n"),
	}})
	p := testProxyWithStore(f.store)
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
	frozen, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || frozen == nil || frozen.ResourceRef == "" {
		t.Fatalf("freeze=%+v err=%v", frozen, err)
	}
	record, _, err := f.store.read(f.workspace, "edit-call", false)
	if err != nil {
		t.Fatal(err)
	}
	projection := appserver.FinalCaptureProjectRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "work", RecipientThreadID: f.thread,
		RecipientCwd: &f.workspace, RecipientContextID: "context", ResourceRef: frozen.ResourceRef,
		Seen: []appserver.CaptureCursor{{Key: changes.CaptureKey(f.workspace, "/root/worker", id, record.CaptureOrder), ThroughChunk: 1}}}
	if projected, err := p.ProjectNativeFinalCapture(t.Context(), projection); err != nil || !strings.Contains(projected.Text, "+authored") || strings.Contains(projected.Text, "+imported") {
		t.Fatalf("diagnostic prefix consumed an authored chunk: %q err=%v", projected.Text, err)
	}
	projection.Seen[0].ThroughChunk = 2
	if projected, err := p.ProjectNativeFinalCapture(t.Context(), projection); err != nil || strings.Contains(projected.Text, "+authored") {
		t.Fatalf("acknowledged authored chunk repeated: %q err=%v", projected.Text, err)
	}
}

func TestNativeFinalFrozenReadPreservesIndependentCaptureErrors(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-partial-final")
	id := f.reserve(t, "worker", "edit")
	f.publish(t, id, "edit", "edit-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", "before\n", "available-content\n")}})
	stream, number, err := changes.ParseID(id)
	if err != nil {
		t.Fatal(err)
	}
	missing := changes.ID(stream, number+100)
	p := testProxyWithStore(f.store)
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace,
		Sources: []string{"mchanges:" + id + "," + missing}}
	frozen, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || frozen == nil || frozen.ResourceRef == "" || !strings.Contains(frozen.FullText, "+available-content") || !strings.Contains(frozen.FullText, "exit_code=1") {
		t.Fatalf("unavailable target discarded independent captured bytes or its error: %+v err=%v", frozen, err)
	}
	source, _ := finalCaptureSource(t, frozen.FullText, "'mchanges' '"+id+"' '"+missing+"'")
	if !strings.HasSuffix(source, "+available-content\n") || strings.Contains(source, "mchanges:") {
		t.Fatalf("partial capture did not isolate available diff bytes: %q", source)
	}
}

func TestFrozenFinalRangesEnforceBothChunkBoundsWithoutCopyingEvidence(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final-bounds")
	id := f.reserve(t, f.thread, "edit")
	content := strings.Repeat("large capture content is retained only by the capture owner\n", 1000)
	f.publish(t, id, "edit", "edit-call", mekugiHistory{Caller: "/root/worker", ReviewFiles: []diffmodel.ReviewFile{
		diffmodel.RenderReviewFile("", "lower.txt", "", "lower-content\n"),
		diffmodel.RenderReviewFile("", "selected.txt", "", content),
		diffmodel.RenderReviewFile("", "upper.txt", "", "upper-content\n"),
	}})
	p := testProxyWithStore(f.store)
	reader, err := p.nativeCaptureScope(f.thread, f.thread)
	if err != nil {
		t.Fatal(err)
	}
	query, err := p.Changes.Parse([]string{id}, f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := p.Changes.Freeze(t.Context(), reader.Scope, query)
	if err != nil || len(selection.Captures) != 1 || selection.Captures[0].FirstChunk != 1 || selection.Captures[0].LastChunk != 3 {
		t.Fatalf("selection=%+v err=%v", selection, err)
	}
	metadata, err := json.Marshal(&selection)
	if err != nil || len(metadata) > 2048 || strings.Contains(string(metadata), "large capture content") {
		t.Fatalf("selection cost grew with evidence bytes: bytes=%d err=%v", len(metadata), err)
	}
	selection.Captures[0].FirstChunk, selection.Captures[0].LastChunk = 2, 2
	projection, err := p.Changes.ProjectFrozen(t.Context(), reader.Scope, selection, nil)
	if err != nil || !projection.Diff || strings.Contains(projection.Text, "lower-content") || strings.Contains(projection.Text, "upper-content") || !strings.Contains(projection.Text, "+large capture content") {
		t.Fatalf("projection ignored exact lower/upper bounds: err=%v", err)
	}
	capture := selection.Captures[0]
	key := changes.CaptureKey(capture.Workspace, capture.Caller, capture.ChangeID, capture.Order)
	projection, err = p.Changes.ProjectFrozen(t.Context(), reader.Scope, selection, map[string]uint64{key: 2})
	if err != nil || projection.Diff || !strings.Contains(projection.Text, "no new net changes") {
		t.Fatalf("acknowledged selected range repeated: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Changes.ProjectFrozen(ctx, reader.Scope, selection, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled range projection continued: %v", err)
	}
	selection.Captures[0].LastChunk = 4
	if _, err := p.Changes.ProjectFrozen(t.Context(), reader.Scope, selection, nil); err == nil {
		t.Fatal("missing frozen upper bound became complete evidence")
	}
}

func finalCaptureSource(t *testing.T, text, reference string) (string, string) {
	t.Helper()
	header, rest, _ := strings.Cut(text, "\n")
	payload, ok := strings.CutSuffix(rest, "\n</mekugi-source-v1>")
	var length, start, end int
	n, err := fmt.Sscanf(header, "<mekugi-source-v1 bytes=\"%d\" bodies=\"%d:%d\">", &length, &start, &end)
	if !ok || err != nil || n != 3 || len(payload) != length || start < 0 || start >= end || end > length {
		t.Fatalf("invalid single-source framing: header=%q, length=%d", header, len(payload))
	}
	if !strings.HasSuffix(payload[:start], fmt.Sprintf("\nSource %q:\n", reference)) {
		t.Fatalf("source header does not name %q", reference)
	}
	return payload[start:end], payload
}

func TestNativeFinalRetainedVersionOneProjectsWithoutRestoringCoordinator(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final-legacy")
	p := testProxyWithStore(f.store)
	if err := p.Retained.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.InitializeScope(history.Scope{Thread: f.thread, Namespace: f.thread}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	reader, err := p.nativeCaptureScope(f.thread, f.thread)
	if err != nil {
		t.Fatal(err)
	}
	// The old resource owns these bytes. There need not be a live change index,
	// old work generation, or loaded sender to read a retained completion.
	legacy := map[string]any{
		"Version": 1, "Root": f.thread, "Thread": "worker", "Work": "retired-work",
		"Recipient": f.thread, "Workspace": f.workspace, "FullText": "original bounded completion\n",
		"Sources": []map[string]any{{"Command": "mchanges amber1", "Error": "", "Capture": changes.LegacyFrozenRead{
			Workspace: f.workspace, Captures: []changes.LegacyFrozenCapture{{ChangeID: "amber1", Workspace: f.workspace, Order: 7,
				Files: []changes.LegacyFrozenFile{
					{Key: "legacy-key", Chunk: 1, File: diffmodel.RenderReviewFile("", "seen.txt", "", "seen-legacy\n")},
					{Key: "legacy-key", Chunk: 2, File: diffmodel.RenderReviewFile("", "remaining.txt", "", "remaining-legacy\n")},
				}}},
		}}},
	}
	ref, err := publishNativeResource(t.Context(), reader.Scope, &legacy)
	if err != nil {
		t.Fatal(err)
	}
	request := appserver.FinalCaptureProjectRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "current-native-call",
		RecipientThreadID: f.thread, RecipientCwd: &f.workspace, RecipientContextID: "current-context", ResourceRef: ref,
		Seen: []appserver.CaptureCursor{{Key: "legacy-key", ThroughChunk: 1}}}
	result, err := p.ProjectNativeFinalCapture(t.Context(), request)
	if err != nil || strings.Contains(result.Text, "+seen-legacy") || !strings.Contains(result.Text, "+remaining-legacy") {
		t.Fatalf("historical range lost evidence or required old work state: %q, %v", result.Text, err)
	}
	request.Seen = nil
	result, err = p.ProjectNativeFinalCapture(t.Context(), request)
	if err != nil || result.Text != "original bounded completion\n" {
		t.Fatalf("historical full output/recovery changed: %q, %v", result.Text, err)
	}
	request.ThreadID = "another-sender"
	if _, err := p.ProjectNativeFinalCapture(t.Context(), request); err == nil {
		t.Fatal("historical format bypassed retained sender authority")
	}
}
