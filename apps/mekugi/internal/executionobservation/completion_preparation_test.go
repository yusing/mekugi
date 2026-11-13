package executionobservation

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yusing/mekugi/backends/codex/appserver"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

type completionReads struct {
	changes.Service
	reads atomic.Int32
}

func (reader *completionReads) Read(ctx context.Context, selected changes.Scope, query changes.Query) (string, error) {
	reader.reads.Add(1)
	return reader.Service.Read(ctx, selected, query)
}

func (reader *completionReads) Freeze(ctx context.Context, selected changes.Scope, query changes.Query) (changes.FrozenRead, error) {
	reader.reads.Add(1)
	return reader.Service.Freeze(ctx, selected, query)
}

func TestNativeFinalCaptureUsesRecipientStorageWithoutGoCoordination(t *testing.T) {
	f := newMChangesSliceFixture(t, "native-final")
	id := f.reserve(t, f.thread, "edit")
	f.publish(t, id, "edit", "edit-call", mekugiHistory{ReviewFiles: []diffmodel.ReviewFile{diffmodel.RenderReviewFile("file.txt", "file.txt", "before\n", "after\n")}})
	p := testProxyWithStore(f.store)
	reader := &completionReads{Service: p.Changes}
	p.Changes = reader
	input := appserver.FinalCaptureRequest{RootThreadID: f.thread, ThreadID: "child", WorkID: "work", RecipientThreadID: f.thread, RecipientCwd: &f.workspace, Sources: []string{"mchanges:" + id}}
	body, err := p.PrepareNativeFinalCapture(t.Context(), input)
	if err != nil || body == nil || body.ResourceRef == "" || !strings.Contains(body.FullText, "+after") || strings.Contains(body.FullText, "private.txt") || reader.reads.Load() != 1 {
		t.Fatalf("native capture = %v, %v, reads %d", body, err, reader.reads.Load())
	}
	input.Sources = nil
	if body, err := p.PrepareNativeFinalCapture(t.Context(), input); err != nil || body != nil || reader.reads.Load() != 1 {
		t.Fatalf("unselected final acquired sources: %v, %v", body, err)
	}
	input.Sources = []string{"mchanges:amber1.."}
	if body, err := p.PrepareNativeFinalCapture(t.Context(), input); err != nil || body == nil || !strings.Contains(body.FullText, "CONTENT NOT ATTACHED") {
		t.Fatalf("malformed final lost capture diagnostic: %v, %v", body, err)
	}
	input.Sources = []string{"mchanges:" + id}
	input.RecipientCwd = nil
	if body, err := p.PrepareNativeFinalCapture(t.Context(), input); err != nil || body == nil || !strings.Contains(body.FullText, "workspace is unavailable") || reader.reads.Load() != 1 {
		t.Fatalf("unavailable recipient cwd fell back to another workspace: %v, %v", body, err)
	}
	input.RecipientCwd = &f.workspace
	if body, err := new(Preparer).PrepareNativeFinalCapture(t.Context(), input); err != nil || body == nil || !strings.Contains(body.FullText, "storage is unavailable") {
		t.Fatalf("missing storage discarded final: %v, %v", body, err)
	}
	input.RecipientThreadID = "unadmitted-recipient"
	if body, err := p.PrepareNativeFinalCapture(t.Context(), input); err != nil || body == nil || !strings.Contains(body.FullText, "scope is not admitted") || reader.reads.Load() != 1 {
		t.Fatalf("unadmitted recipient used another namespace: %v, %v", body, err)
	}
	if _, found, err := p.Retained.Store.ReadScope(input.RecipientThreadID); found || err != nil {
		t.Fatalf("failed recipient read admitted storage: found=%v err=%v", found, err)
	}
}
