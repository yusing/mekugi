package router

import (
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
)

// operationCaller is the canonical agent path of this request's tool calls.
// Older clients may omit a child's name, leaving it unknown rather than main.
func (t *mekugiResponseTransform) operationCaller() string {
	if !t.subagentTurn {
		return "/root"
	}
	return t.commentaryAuthor
}

func (t *mekugiResponseTransform) previewStockDelta(itemID, kind, delta string) {
	if delta == "" {
		return
	}
	auto := t.review.Preview()
	if auto == nil || !auto.Enabled() {
		return
	}
	if t.previews == nil {
		t.previews = make(map[string]*previewbinding.Stream)
	}
	worker := t.previews[itemID]
	if worker == nil {
		worker = previewbinding.Start(t.ctx, auto, previewbinding.Identity{
			Workspace: t.directory, Thread: t.threadID, Kind: kind, Caller: t.operationCaller(), Turn: t.shellTurnID,
		}, t.previewTiming, changecapture.ReadPatchFile)
		t.previews[itemID] = worker
	}
	worker.Append(delta)
}

func (t *mekugiResponseTransform) endPreview(itemID string) {
	if worker := t.previews[itemID]; worker != nil {
		worker.Stop()
		delete(t.previews, itemID)
	}
}

func (t *mekugiResponseTransform) finishPreview(itemID, input string) {
	if worker := t.previews[itemID]; worker != nil {
		worker.Finish(input)
	}
}
