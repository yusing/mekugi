package sessionapi

import (
	"slices"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

type savedReview struct {
	Revision  uint64         `json:"revision"`
	Workspace string         `json:"workspace"`
	Coverage  string         `json:"coverage"`
	Failure   string         `json:"failure,omitzero"`
	Files     []reviewFile   `json:"files"`
	Callers   []callerReview `json:"callers"`
	Counts    changes.Counts `json:"counts"`
}

type reviewFile struct {
	Key     string           `json:"key"`
	Path    string           `json:"path"`
	Chunks  []changes.Chunk  `json:"chunks"`
	Origins []changes.Origin `json:"origins"`
	Counts  diffmodel.Counts `json:"counts"`
}

// callerReview is one caller's files as the terminal's caller filter shows
// them: other callers' captures compose as baseline, so each diff is exact.
type callerReview struct {
	Caller string       `json:"caller"`
	Files  []reviewFile `json:"files"`
}

// projectReview shares the saved pane's policy and already-composed chunks.
// The server calls it only when the admitted review revision changes.
func projectReview(view *changes.ReviewView) savedReview {
	p := view.Publication.ForDiff(view.Workspace)
	r := savedReview{Revision: view.Revision, Workspace: view.Workspace, Coverage: view.Coverage, Failure: view.Failure, Counts: p.Counts}
	for _, file := range p.Files {
		r.Files = append(r.Files, projectReviewFile(file.Key(), p.Unfiltered[file.Key()]))
	}
	for _, caller := range changes.Callers(&changes.View{Files: p.Files}) {
		// Only the caller's own files compose; the rest would be all baseline.
		filtered := changes.View{Caller: caller}
		for _, file := range p.Files {
			if slices.ContainsFunc(file.Chunks, filtered.Shows) {
				filtered.Files = append(filtered.Files, file)
			}
		}
		filtered.RefreshVisible()
		c := callerReview{Caller: caller}
		for _, file := range filtered.Files {
			c.Files = append(c.Files, projectReviewFile(file.Key(), filtered.Visible[file.Key()]))
		}
		r.Callers = append(r.Callers, c)
	}
	return r
}

func projectReviewFile(key string, composed changes.File) reviewFile {
	return reviewFile{Key: key, Path: composed.Path, Chunks: composed.Chunks, Origins: composed.Origins, Counts: composed.NetCounts()}
}
