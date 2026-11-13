package executionobservation

import (
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/persistence"
	"github.com/yusing/mekugi/modules/changehistory/presentation"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func nativeProgressTestPreparer(t *testing.T) (*Preparer, appserver.ProgressFreezeRequest) {
	t.Helper()
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	if err := p.Retained.Store.Transaction(t.Context(), nil, func(tx *history.Transaction) error {
		return tx.InitializeScope(history.Scope{Thread: "root", Namespace: "root"}, nil)
	}); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []string{"worker", "quiet"} {
		if err := p.AdmitNativeChild(t.Context(), appserver.ChildHostAdmissionRequest{RootThreadID: "root", ParentThreadID: "root", ThreadID: thread}); err != nil {
			t.Fatal(err)
		}
	}
	return p, appserver.ProgressFreezeRequest{RootThreadID: "root", CallerThreadID: "root", CallerWorkID: "root-work", WaitID: "wait", BatchID: "batch", RecipientContextID: "context",
		Selected: []appserver.ProgressActor{{ThreadID: "worker", WorkID: "child-work", TaskName: "/root/worker"}, {ThreadID: "quiet", WorkID: "quiet-work", TaskName: "/root/quiet"}}}
}

func TestNativeProgressFreezesExactIntervalsBeforeSummaries(t *testing.T) {
	p, input := nativeProgressTestPreparer(t)
	chunk := presentation.Chunk{Workspace: "/workspace", CaptureOrder: 2, Review: diffmodel.RenderReviewFile("/tmp/scratch", "/tmp/scratch", "before\n", "scratch\n"),
		Origin: presentation.Origin{Caller: "/root/worker", Change: "amber2"}}
	view := &presentation.ReviewView{Publication: presentation.Publication{Files: []presentation.File{{Chunks: []presentation.Chunk{chunk}}}}, Coverage: "complete", Revision: 7}
	borrows := 0
	p.ProgressSnapshot = func(root string) *presentation.ReviewView {
		if root != "root" {
			t.Fatalf("unexpected snapshot root %q", root)
		}
		borrows++
		return view
	}
	frozen, err := p.FreezeNativeProgress(t.Context(), input)
	if err != nil || frozen.SnapshotRef == "" || frozen.PublicationRevision != 7 || len(frozen.Agents) != 1 || len(frozen.Agents[0].Intervals) != 1 || borrows != 1 {
		t.Fatalf("freeze=%+v err=%v borrows=%d", frozen, err, borrows)
	}
	interval := frozen.Agents[0].Intervals[0]
	if interval.RecordOrder != 2 || interval.FirstChunk != 1 || interval.LastChunk != 1 || interval.LegacyRecordKey != nil {
		t.Fatalf("incorrect exact interval: %+v", interval)
	}
	// Lower IDs and further chunks of an old record both count as new evidence.
	late := chunk
	late.CaptureOrder, late.Change, late.Review = 1, "amber1", diffmodel.RenderReviewFile("late", "late", "before\n", "late\n")
	extension := chunk
	extension.Review = diffmodel.RenderReviewFile("extension", "extension", "before\n", "extended\n")
	view = &presentation.ReviewView{Publication: presentation.Publication{Files: []presentation.File{{Chunks: []presentation.Chunk{chunk, late, extension}}}}, Coverage: "complete", Revision: 8}
	input.Seen = []appserver.CaptureCursor{{Key: interval.Key, ThroughChunk: interval.LastChunk}}
	next, err := p.FreezeNativeProgress(t.Context(), input)
	if err != nil || len(next.Agents) != 1 || len(next.Agents[0].Intervals) != 2 || next.Agents[0].Intervals[0].RecordOrder != 1 || next.Agents[0].Intervals[1].FirstChunk != 2 || next.Agents[0].Intervals[1].LastChunk != 2 {
		t.Fatalf("late/extended intervals lost: %+v err=%v", next, err)
	}
	output := appserver.ProgressOutputRequest{RootThreadID: "root", CallerThreadID: "root", CallerWorkID: input.CallerWorkID, WaitID: input.WaitID, BatchID: input.BatchID,
		RecipientContextID: input.RecipientContextID, SnapshotRef: frozen.SnapshotRef, Coverage: frozen.Coverage,
		Agents: []appserver.PreparedProgressAgent{{Actor: frozen.Agents[0].Actor, Intervals: frozen.Agents[0].Intervals, Summary: appserver.SummaryResult{Status: "available", Text: "Changed scratch capture; validation has not run."}}}}
	prepared, err := p.PrepareNativeProgressOutput(t.Context(), output)
	if err != nil || prepared.RecoveryRef != nil || len(prepared.DeliveredIntervals) != 1 || !sameCaptureInterval(prepared.DeliveredIntervals[0], interval) || strings.Contains(prepared.Text, "amber1") || borrows != 2 {
		t.Fatalf("output reacquired or changed frozen intervals: %+v err=%v borrows=%d", prepared, err, borrows)
	}
	output.Agents[0].Intervals = next.Agents[0].Intervals
	if _, err := p.PrepareNativeProgressOutput(t.Context(), output); err == nil {
		t.Fatal("later intervals were accepted into an earlier frozen batch")
	}
	output.Agents[0].Intervals = frozen.Agents[0].Intervals
	output.RecipientContextID = "replacement"
	if _, err := p.PrepareNativeProgressOutput(t.Context(), output); err == nil {
		t.Fatal("old progress batch projected into a replacement context")
	}
	input.Seen = append(input.Seen[:0], appserver.CaptureCursor{Key: next.Agents[0].Intervals[0].Key, ThroughChunk: 1}, appserver.CaptureCursor{Key: interval.Key, ThroughChunk: 2})
	p.Retained.Writes = new(persistence.Counter)
	unchanged, err := p.FreezeNativeProgress(t.Context(), input)
	if err != nil || len(unchanged.Agents) != 0 || unchanged.SnapshotRef != "" || p.Retained.Writes.Snapshot().Bytes != 0 {
		t.Fatalf("unchanged interval created summary work: %+v err=%v", unchanged, err)
	}
}

func TestNativeProgressBoundingRetainsExactOutputWithoutAcknowledgingRecovery(t *testing.T) {
	p, input := nativeProgressTestPreparer(t)
	chunk := presentation.Chunk{Workspace: "/workspace", CaptureOrder: 3, Review: diffmodel.RenderReviewFile("file", "file", "before\n", "after\n"),
		Origin: presentation.Origin{Caller: "/root/worker", Change: "amber3"}}
	p.ProgressSnapshot = func(string) *presentation.ReviewView {
		return &presentation.ReviewView{Publication: presentation.Publication{Files: []presentation.File{{Chunks: []presentation.Chunk{chunk}}}}, Coverage: "partial", Failure: "One capture is unavailable"}
	}
	frozen, err := p.FreezeNativeProgress(t.Context(), input)
	if err != nil || len(frozen.Agents) != 1 {
		t.Fatalf("freeze=%+v err=%v", frozen, err)
	}
	summary := strings.Repeat("已改動 captures; validation not run. ", 600)
	request := appserver.ProgressOutputRequest{RootThreadID: input.RootThreadID, CallerThreadID: input.CallerThreadID, CallerWorkID: input.CallerWorkID, WaitID: input.WaitID,
		BatchID: input.BatchID, RecipientContextID: input.RecipientContextID, SnapshotRef: frozen.SnapshotRef, Coverage: frozen.Coverage,
		Agents: []appserver.PreparedProgressAgent{{Actor: frozen.Agents[0].Actor, Intervals: frozen.Agents[0].Intervals, Summary: appserver.SummaryResult{Status: "available", Text: summary}}}}
	bounded, err := p.PrepareNativeProgressOutput(t.Context(), request)
	if err != nil || bounded.RecoveryRef == nil || len(bounded.Text) > nativeProgressBytes || len(bounded.DeliveredIntervals) != 0 {
		t.Fatalf("bounded output acknowledged omitted evidence: %+v err=%v", bounded, err)
	}
	_, retained, err := p.nativeCaptureScope("root", "root")
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := retained.Snapshot(*bounded.RecoveryRef)
	if err != nil || !strings.Contains(recovered.Stdout, summary) || !strings.Contains(recovered.Stdout, "amber3 record=3 chunks=1..1") || !strings.Contains(recovered.Stdout, "One capture is unavailable") {
		t.Fatalf("full output recovery lost summary/selection/coverage: %+v err=%v", recovered, err)
	}
	request.Agents[0].Summary = appserver.SummaryResult{Status: "unavailable", Reason: "Target completed before auxiliary sampling"}
	missing, err := p.PrepareNativeProgressOutput(t.Context(), request)
	if err != nil || missing.RecoveryRef != nil || len(missing.DeliveredIntervals) != 0 || !strings.Contains(missing.Text, "Target completed") {
		t.Fatalf("unavailable summary suppressed final evidence: %+v err=%v", missing, err)
	}
	request.Coverage.Status = "complete"
	if _, err := p.PrepareNativeProgressOutput(t.Context(), request); err == nil {
		t.Fatal("caller silently upgraded partial capture coverage")
	}
}

func TestNativeProgressRequiresAdmittedSelectedActors(t *testing.T) {
	p, input := nativeProgressTestPreparer(t)
	p.ProgressSnapshot = func(string) *presentation.ReviewView {
		t.Fatal("unadmitted actor reached capture publication")
		return nil
	}
	input.Selected[0].ThreadID = "missing"
	if _, err := p.FreezeNativeProgress(t.Context(), input); err == nil || !strings.Contains(err.Error(), "not admitted") {
		t.Fatalf("missing actor received an allocation-default namespace: %v", err)
	}
	if _, found, err := p.Retained.Store.ReadScope("missing"); found || err != nil {
		t.Fatalf("progress read admitted an actor: found=%v err=%v", found, err)
	}
}

func TestNativeProgressDiagnosticOnlyRetainsExplicitCoverage(t *testing.T) {
	p, input := nativeProgressTestPreparer(t)
	p.ProgressSnapshot = func(string) *presentation.ReviewView {
		return &presentation.ReviewView{Coverage: "partial", Failure: "Capture hydration is incomplete"}
	}
	frozen, err := p.FreezeNativeProgress(t.Context(), input)
	if err != nil || frozen.SnapshotRef == "" || len(frozen.Agents) != 0 || frozen.Coverage.Status != "partial" {
		t.Fatalf("coverage failure became unchanged progress: %+v err=%v", frozen, err)
	}
	request := appserver.ProgressOutputRequest{RootThreadID: input.RootThreadID, CallerThreadID: input.CallerThreadID, CallerWorkID: input.CallerWorkID,
		WaitID: input.WaitID, BatchID: input.BatchID, RecipientContextID: input.RecipientContextID, SnapshotRef: frozen.SnapshotRef, Coverage: frozen.Coverage}
	output, err := p.PrepareNativeProgressOutput(t.Context(), request)
	if err != nil || len(output.DeliveredIntervals) != 0 || !strings.Contains(output.Text, "Capture hydration is incomplete") {
		t.Fatalf("diagnostic output lost coverage or acknowledged nonexistent captures: %+v err=%v", output, err)
	}
}
