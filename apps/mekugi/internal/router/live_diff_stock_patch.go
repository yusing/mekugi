package router

import (
	"context"

	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"

	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
	"github.com/yusing/mekugi/modules/livepreview/projection"
)

// projectStockPatchPreview follows the former hpatch preview model: read the
// bounded source, calculate a disposable in-memory result, and hand the real
// before/after pair to the shared review renderer. It never writes a file or
// treats a speculative projection as evidence of host execution.
func projectStockPatchPreview(ctx context.Context, workspace string, preview previewmodel.Preview) previewmodel.Preview {
	const limit = 256 << 10
	if len(preview.Input) > limit {
		preview.Input = ""
		preview.Status = previewmodel.PreviewUnavailable + "patch exceeds projection capacity"
		return preview
	}
	files, err := projection.Patch(ctx, workspace, preview.Input, preview.Complete, changecapture.ReadPatchFile)
	preview.Input = ""
	if err != nil {
		preview.Status = previewmodel.PreviewUnavailable + "patch cannot be projected"
	} else {
		preview.Files = files
		if len(files) == 0 {
			// Preserve an earlier projected card while this patch has no hunk.
			preview.Input = "\n"
		} else {
			preview.DiffText = false // ReviewFiles use the normal language-aware renderer.
		}
	}
	return preview
}
