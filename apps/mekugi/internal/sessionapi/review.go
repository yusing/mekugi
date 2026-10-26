package sessionapi

import (
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/diffmodel"
	changes "github.com/yusing/mekugi/modules/changehistory/presentation"
)

type savedReview struct {
	Page      *PageCursor    `json:"page,omitzero"`
	FileCount int            `json:"fileCount"`
	Revision  uint64         `json:"revision"`
	Workspace string         `json:"workspace"`
	Coverage  string         `json:"coverage"`
	Failure   string         `json:"failure,omitzero"`
	Callers   []callerReview `json:"callers"`
	Counts    changes.Counts `json:"counts"`
}

type reviewFile struct {
	Content *ContentReference `json:"content,omitzero"`
	Key     string            `json:"key"`
	Path    string            `json:"path"`
	Chunks  []changes.Chunk   `json:"chunks"`
	Origins []changes.Origin  `json:"origins"`
	Counts  diffmodel.Counts  `json:"counts"`
}

// callerReview is one caller's files as the terminal's caller filter shows
// them: other callers' captures compose as baseline, so each diff is exact.
type callerReview struct {
	Caller string `json:"caller"`
	Count  int    `json:"count"`
}

// projectReview shares the saved pane's policy and already-composed chunks.
// The server calls it only when the admitted review revision changes.
func projectReview(view *changes.ReviewView) savedReview {
	p := view.Publication
	r := savedReview{Revision: view.Revision, Workspace: view.Workspace, Coverage: view.Coverage, Failure: view.Failure, Counts: p.Counts}
	r.FileCount = len(p.Files)
	for _, caller := range changes.Callers(&changes.View{Files: p.Files}) {
		filtered := changes.View{Caller: caller}
		c := callerReview{Caller: caller}
		for _, file := range p.Files {
			if slices.ContainsFunc(file.Chunks, filtered.Shows) {
				c.Count++
			}
		}
		r.Callers = append(r.Callers, c)
	}
	return r
}

func projectReviewFile(key string, composed changes.File) reviewFile {
	return reviewFile{Key: reviewIdentity(key), Path: composed.Path, Chunks: composed.Chunks, Origins: composed.Origins, Counts: composed.NetCounts()}
}

// Long paths remain fully readable without making their metadata unpageable.
func reviewIdentity(key string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(key)))
}

func reviewPage(view *changes.ReviewView, attachment string, cursor PageCursor) (any, *RPCError) {
	if view == nil || attachment != cursor.Attachment || view.Revision != cursor.Revision || cursor.Before > uint64(len(view.Files)) {
		return nil, rpcFailure(409, "Saved review changed; reopen its current metadata.")
	}
	page := PageResult{Files: []reviewFile{}}
	used := 0
	for index := int(cursor.Before); index < len(view.Files); index++ {
		file := view.Files[index]
		if cursor.Caller != "" && !slices.ContainsFunc(file.Chunks, (&changes.View{Caller: cursor.Caller}).Shows) {
			continue
		}
		composed := composeReviewFile(view, file, cursor.Caller)
		key := reviewIdentity(file.Key())
		ref := &ContentReference{Attachment: attachment, Kind: "review", Revision: view.Revision, Caller: cursor.Caller, Key: key}
		path := composed.Path
		if len(path) > 1024 {
			path = strings.ToValidUTF8(path[:1024], "") + "…"
		}
		metadata := reviewFile{Key: key, Path: path, Counts: composed.NetCounts(), Content: ref}
		data, err := json.Marshal(metadata, encoding)
		if err != nil {
			return nil, rpcFailure(500, "Saved file metadata unavailable.")
		}
		if len(page.Files) == pageEntries || used+len(data) > feedBytes {
			if len(page.Files) == 0 {
				return nil, rpcFailure(413, "Saved file identity exceeds the page budget.")
			}
			cursor.Before = uint64(index)
			page.Next = &cursor
			break
		}
		page.Files = append(page.Files, metadata)
		used += len(data)
	}
	return page, nil
}

func reviewContent(view *changes.ReviewView, attachment string, ref ContentReference) (any, *RPCError) {
	if view == nil || attachment != ref.Attachment || view.Revision != ref.Revision {
		return nil, rpcFailure(409, "Saved review changed; reopen its current file.")
	}
	for _, file := range view.Files {
		if reviewIdentity(file.Key()) != ref.Key {
			continue
		}
		if ref.Caller != "" && !slices.ContainsFunc(file.Chunks, (&changes.View{Caller: ref.Caller}).Shows) {
			break
		}
		full := projectReviewFile(file.Key(), composeReviewFile(view, file, ref.Caller))
		return ContentResult{Review: &full}, nil
	}
	return nil, rpcFailure(404, "The requested caller has no such saved file.")
}

func composeReviewFile(view *changes.ReviewView, file changes.File, caller string) changes.File {
	if caller == "" {
		return view.Unfiltered[file.Key()]
	}
	filtered := changes.View{Caller: caller, Files: []changes.File{file}}
	filtered.RefreshVisible()
	return filtered.Visible[file.Key()]
}
