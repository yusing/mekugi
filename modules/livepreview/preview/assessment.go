package preview

import (
	"context"

	"github.com/yusing/mekugi/modules/livepreview/projection"
)

// AssessPatch reads bounded source, calculates a disposable in-memory result,
// and supplies before/after values for preview publication. It never writes a file or
// treats a speculative projection as evidence of host execution.
// Source: apps/mekugi/internal/router/live_diff_stock_patch.go:12:37@72549ff1.
func AssessPatch(ctx context.Context, workspace string, preview Preview, read func(string) (string, bool, error)) Preview {
	const limit = 256 << 10
	if len(preview.Input) > limit {
		preview.Input = ""
		preview.Status = PreviewUnavailable + "patch exceeds projection capacity"
		return preview
	}
	files, err := projection.Patch(ctx, workspace, preview.Input, preview.Complete, read)
	preview.Input = ""
	if err != nil {
		preview.Status = PreviewUnavailable + "patch cannot be projected"
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
