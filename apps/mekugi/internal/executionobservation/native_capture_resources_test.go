package executionobservation

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func TestNativeFinalProjectionUsesOnlySelectedRetainedRanges(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final-projection")
	id := f.reserve(t, f.thread, "edit")
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
	_, retained, err := p.nativeCaptureScope(f.thread, f.thread)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := retained.Snapshot(frozen.ResourceRef)
	if err != nil || strings.Contains(metadata.Stdout, "seen-content") || strings.Contains(metadata.Stdout, "remaining-content") || strings.Contains(metadata.Stdout, "FullText") {
		t.Fatalf("selection duplicated captured evidence: %q, %v", metadata.Stdout, err)
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
	id := f.reserve(t, f.thread, "large")
	content := strings.Repeat("unchanged capture evidence belongs to this retained range\n", 1500) + "frozen-end\n"
	f.publish(t, id, "large", "large-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", "large.txt", "", content)}})
	p := testProxyWithStore(f.store)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
	frozen, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || frozen == nil || frozen.ResourceRef == "" {
		t.Fatalf("freeze=%+v err=%v", frozen, err)
	}
	_, continuation, found := strings.Cut(frozen.FullText, "next_call: mread ")
	if !found || len(strings.Fields(continuation)) == 0 {
		t.Fatalf("large full capture has no exact recovery: %q", frozen.FullText)
	}
	ref := strings.Fields(continuation)[0]
	_, retained, err := p.nativeCaptureScope(f.thread, f.thread)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := retained.Snapshot(ref)
	if err != nil || !strings.Contains(recovered.Stdout, "+frozen-end") {
		t.Fatalf("full fallback lost omitted suffix: %v", err)
	}
	f.publish(t, id, "large", "late-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", "late.txt", "", "late-content\n")}})
	projection := appserver.FinalCaptureProjectRequest{RootThreadID: f.thread, ThreadID: "worker", OperationID: "work", RecipientThreadID: f.thread,
		RecipientCwd: &f.workspace, RecipientContextID: "replacement", ResourceRef: frozen.ResourceRef}
	projected, err := p.ProjectNativeFinalCapture(t.Context(), projection)
	if err != nil || strings.Contains(projected.Text, "late-content") || reader.reads.Load() != 1 {
		t.Fatalf("projection reselected mutable IDs: %v reads=%d", err, reader.reads.Load())
	}
	_, projectedContinuation, ok := strings.Cut(projected.Text, "next_call: mread ")
	if !ok || len(strings.Fields(projectedContinuation)) == 0 {
		t.Fatal("projected selection lost bounded recovery")
	}
	projectedSuffix, err := retained.Snapshot(strings.Fields(projectedContinuation)[0])
	if err != nil || projectedSuffix.Stdout != recovered.Stdout {
		t.Fatalf("rematerialized range changed recovery content: %v", err)
	}
	again, err := retained.Snapshot(ref)
	if err != nil || again.Stdout != recovered.Stdout || strings.Contains(again.Stdout, "late-content") {
		t.Fatalf("retained recovery changed after late writer: %v", err)
	}
}

func TestFrozenCaptureProjectionCountsDiagnosticPrefixes(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-diagnostic-prefix")
	id := f.reserve(t, f.thread, "edit")
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
	id := f.reserve(t, f.thread, "edit")
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
	reader, _, err := p.nativeCaptureScope(f.thread, f.thread)
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
	text, err := p.Changes.ProjectFrozen(t.Context(), reader.Scope, selection, nil)
	if err != nil || strings.Contains(text, "lower-content") || strings.Contains(text, "upper-content") || !strings.Contains(text, "+large capture content") {
		t.Fatalf("projection ignored exact lower/upper bounds: err=%v", err)
	}
	capture := selection.Captures[0]
	key := changes.CaptureKey(capture.Workspace, capture.Caller, capture.ChangeID, capture.Order)
	text, err = p.Changes.ProjectFrozen(t.Context(), reader.Scope, selection, map[string]uint64{key: 2})
	if err != nil || strings.Contains(text, "+large capture content") {
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

func TestNativeFinalRetainedVersionOneProjectsWithoutRestoringCoordinator(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final-legacy")
	p := testProxyWithStore(f.store)
	if err := p.Retained.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.InitializeScope(history.Scope{Thread: f.thread, Namespace: f.thread}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	_, retained, err := p.nativeCaptureScope(f.thread, f.thread)
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
	ref, err := publishNativeResource(t.Context(), retained, &legacy)
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
