package executionobservation

import (
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/livepreview/projection"
)

// Prime the final preview with the bounded baseline captured before the host
// receives a completed call. The preview worker may still be pacing its input
// when Codex applies the patch, so its first filesystem read can be too late.
func (t *mekugiResponseTransform) primePreviewSources(itemID string, patches []changecapture.PatchObservation) {
	worker := t.previews[itemID]
	if worker == nil || len(patches) == 0 {
		return
	}
	for _, patch := range patches {
		for _, file := range patch.Files {
			if file.BeforeRef != nil {
				if file.BeforeRef.Size > projection.FileLimit {
					file.Error = "baseline exceeds preview bound"
				} else if text, err := file.BeforeRef.Read(); err != nil {
					file.Error = err.Error()
				} else {
					file.Before = text
				}
			}
			if file.TargetBeforeRef != nil {
				if file.TargetBeforeRef.Size > projection.FileLimit {
					file.TargetError = "baseline exceeds preview bound"
				} else if text, err := file.TargetBeforeRef.Read(); err != nil {
					file.TargetError = err.Error()
				} else {
					file.TargetBefore = text
				}
			}
			if file.Error == "" {
				worker.Prime(file.BeforePath, file.Before, file.Exists)
			}
			if file.AfterPath != file.BeforePath {
				if file.BeforePath == "" {
					if file.Error == "" {
						worker.Prime(file.AfterPath, file.Before, file.Exists)
					}
				} else if file.TargetError == "" {
					worker.Prime(file.AfterPath, file.TargetBefore, file.TargetExists)
				}
			}
		}
	}
}
