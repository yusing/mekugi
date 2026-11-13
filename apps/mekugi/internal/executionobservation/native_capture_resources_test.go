package executionobservation

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func TestNativeFinalProjectionUsesOnlyFrozenCaptureBytes(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final-projection")
	id := f.reserve(t, f.thread, "edit")
	f.publish(t, id, "edit", "edit-call", mekugiHistory{Caller: "/root/worker", ReviewFiles: []diffmodel.ReviewFile{
		diffmodel.RenderReviewFile("seen.txt", "seen.txt", "before\n", "seen-content\n"),
		diffmodel.RenderReviewFile("remaining.txt", "remaining.txt", "before\n", "remaining-content\n"),
	}})
	p := testProxyWithStore(f.store)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", WorkID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
	frozen, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || frozen == nil || frozen.ResourceRef == "" || !strings.Contains(frozen.FullText, "+seen-content") || !strings.Contains(frozen.FullText, "+remaining-content") || reader.reads.Load() != 1 {
		t.Fatalf("full frozen capture=%+v err=%v reads=%d", frozen, err, reader.reads.Load())
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
	projection := appserver.FinalCaptureProjectRequest{RootThreadID: input.RootThreadID, ThreadID: input.ThreadID, WorkID: input.WorkID,
		RecipientThreadID: input.RecipientThreadID, RecipientCwd: input.RecipientCwd, RecipientContextID: "context-one", ResourceRef: frozen.ResourceRef,
		Seen: []appserver.CaptureCursor{{Key: key, ThroughChunk: 1}}}
	for range 2 {
		projected, err := p.ProjectNativeFinalCapture(t.Context(), projection)
		if err != nil || strings.Contains(projected.Text, "+seen-content") || strings.Contains(projected.Text, "late-content") || !strings.Contains(projected.Text, "+remaining-content") || reader.reads.Load() != 1 {
			t.Fatalf("projection reacquired or lost frozen chunks: %q err=%v reads=%d", projected.Text, err, reader.reads.Load())
		}
	}
	// Native resets/resume rotate context and supply no acknowledgments. The
	// fallback is exactly the original bytes, not a new read of mutable IDs.
	projection.RecipientContextID, projection.Seen = "replacement-context", nil
	projected, err := p.ProjectNativeFinalCapture(t.Context(), projection)
	if err != nil || projected.Text != frozen.FullText || reader.reads.Load() != 1 {
		t.Fatalf("replacement context changed full fallback: %q err=%v", projected.Text, err)
	}
	projection.Seen = []appserver.CaptureCursor{{Key: key, ThroughChunk: 2}}
	projection.RecipientContextID = ""
	if projected, err := p.ProjectNativeFinalCapture(t.Context(), projection); err != nil || projected.Text != frozen.FullText {
		t.Fatalf("unavailable delivery context suppressed full capture: %q err=%v", projected.Text, err)
	}
	projection.WorkID = "different-work"
	if _, err := p.ProjectNativeFinalCapture(t.Context(), projection); err == nil {
		t.Fatal("another work used the frozen resource")
	}
	if err := p.AdmitNativeChild(t.Context(), appserver.ChildHostAdmissionRequest{RootThreadID: f.thread, ParentThreadID: f.thread, ThreadID: "other-recipient"}); err != nil {
		t.Fatal(err)
	}
	projection.WorkID, projection.RecipientThreadID = input.WorkID, "other-recipient"
	if _, err := p.ProjectNativeFinalCapture(t.Context(), projection); err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("another admitted recipient used the frozen resource: %v", err)
	}
	projection.WorkID, projection.RecipientThreadID = input.WorkID, "unadmitted"
	if _, err := p.ProjectNativeFinalCapture(t.Context(), projection); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("unadmitted recipient reached resource: %v", err)
	}
	if reader.reads.Load() != 1 {
		t.Fatal("projection or rejection repeated source acquisition")
	}
}

func TestNativeFinalFullFallbackKeepsItsOriginalRecovery(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final-recovery")
	id := f.reserve(t, f.thread, "large")
	content := strings.Repeat("unchanged capture evidence belongs to this immutable snapshot\n", 1500) + "frozen-end\n"
	f.publish(t, id, "large", "large-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("", "large.txt", "", content)}})
	p := testProxyWithStore(f.store)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", WorkID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
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
	projection := appserver.FinalCaptureProjectRequest{RootThreadID: f.thread, ThreadID: "worker", WorkID: "work", RecipientThreadID: f.thread,
		RecipientCwd: &f.workspace, RecipientContextID: "replacement", ResourceRef: frozen.ResourceRef}
	projected, err := p.ProjectNativeFinalCapture(t.Context(), projection)
	if err != nil || projected.Text != frozen.FullText || reader.reads.Load() != 1 {
		t.Fatalf("full fallback reacquired content or changed recovery: %v reads=%d", err, reader.reads.Load())
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
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", WorkID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
	frozen, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || frozen == nil || frozen.ResourceRef == "" {
		t.Fatalf("freeze=%+v err=%v", frozen, err)
	}
	record, _, err := f.store.read(f.workspace, "edit-call", false)
	if err != nil {
		t.Fatal(err)
	}
	projection := appserver.FinalCaptureProjectRequest{RootThreadID: f.thread, ThreadID: "worker", WorkID: "work", RecipientThreadID: f.thread,
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
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "worker", WorkID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace,
		Sources: []string{"mchanges:" + id + "," + missing}}
	frozen, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || frozen == nil || frozen.ResourceRef == "" || !strings.Contains(frozen.FullText, "+available-content") || !strings.Contains(frozen.FullText, "exit_code=1") {
		t.Fatalf("unavailable target discarded independent captured bytes or its error: %+v err=%v", frozen, err)
	}
}
