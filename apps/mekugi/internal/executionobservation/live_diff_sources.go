package executionobservation

import (
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
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
