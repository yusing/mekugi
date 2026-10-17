package router

import (
	"context"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	previewbinding "github.com/yusing/mekugi/modules/livepreview/codexbinding"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	previewview "github.com/yusing/mekugi/modules/livepreview/terminalview"
)

func startLiveDiffPreview(ctx context.Context, broker *previewmodel.Session, workspace, thread string, kind ...string) *previewbinding.Stream {
	id := previewbinding.Identity{Workspace: workspace, Thread: thread}
	if len(kind) != 0 {
		id.Kind = kind[0]
	}
	return previewbinding.Start(ctx, broker, id, previewbinding.Timing{Frame: previewview.PreviewFrameDelay, Unit: previewview.PreviewUnitDelay, Finish: previewview.PreviewFinishDrain}, changecapture.ReadPatchFile)
}
func previewProjector(ctx context.Context, kind string) func(string, string, bool) (previewmodel.Preview, bool) {
	return func(input, workspace string, final bool) (previewmodel.Preview, bool) {
		return previewbinding.Project(ctx, workspace, kind, input, final, changecapture.ReadPatchFile)
	}
}
func projectStockPatchPreview(ctx context.Context, workspace string, p previewmodel.Preview) previewmodel.Preview {
	return previewmodel.AssessPatch(ctx, workspace, p, changecapture.ReadPatchFile)
}
