package sessionapi

import (
	"github.com/yusing/mekugi/libraries/diffmodel"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

type savedReview struct {
	Revision  uint64         `json:"revision"`
	Workspace string         `json:"workspace"`
	Coverage  string         `json:"coverage"`
	Failure   string         `json:"failure,omitzero"`
	Files     []reviewFile   `json:"files"`
	Counts    changes.Counts `json:"counts"`
}

type reviewFile struct {
	Key     string           `json:"key"`
	Path    string           `json:"path"`
	Chunks  []changes.Chunk  `json:"chunks"`
	Origins []changes.Origin `json:"origins"`
	Counts  diffmodel.Counts `json:"counts"`
}

// projectReview shares the saved pane's policy and already-composed chunks.
// The server calls it only when the admitted review revision changes.
func projectReview(view *changes.ReviewView) savedReview {
	p := view.Publication.ForDiff(view.Workspace)
	r := savedReview{Revision: view.Revision, Workspace: view.Workspace, Coverage: view.Coverage, Failure: view.Failure, Counts: p.Counts}
	for _, file := range p.Files {
		composed := p.Unfiltered[file.Key()]
		r.Files = append(r.Files, reviewFile{Key: file.Key(), Path: composed.Path, Chunks: composed.Chunks, Origins: composed.Origins, Counts: composed.NetCounts()})
	}
	return r
}
