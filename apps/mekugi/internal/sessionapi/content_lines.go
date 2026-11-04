package sessionapi

import (
	json "encoding/json/v2"
	"errors"
	"strings"

	"fmt"
	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/libraries/pathdisplay"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

const (
	contentPageLines = 1000
	pageBytes        = 256 << 10
)

// One selected source belongs to a physical frontend, not a page-sized cache.
// Independent terminals retain their own index without evicting each other.
type selectedContent struct {
	server *Server
	ref    ContentReference
	source string
}

func (s *Server) selectedContent(ref ContentReference, offset uint64, cached **selectedContent) (any, *RPCError) {
	s.mu.Lock()
	valid := ref.Attachment == s.readAttachment
	switch ref.Kind {
	case "review":
		valid = valid && s.reviewSource != nil && ref.Revision == s.reviewSource.Revision
	case "live":
		valid = valid && ref.Revision == s.contentVersions["live"]
	default:
		valid = false
	}
	s.mu.Unlock()
	if !valid {
		return nil, rpcFailure(409, "Selected content changed; reopen its current reference.")
	}
	if *cached == nil || (*cached).server != s || (*cached).ref != ref {
		body, _ := json.Marshal(&ref)
		value, failure := s.content(body)
		if failure != nil {
			return nil, failure
		}
		full := value.(ContentResult)
		var pieces []string
		if full.Review != nil {
			s.mu.Lock()
			workspace := s.reviewWorkspace
			s.mu.Unlock()
			for _, chunk := range full.Review.Chunks {
				if chunk.Status != "" {
					pieces = append(pieces, chunk.Status)
				}
				pieces = append(pieces, chunk.Review.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(workspace, chunk.Review.BeforePath), pathdisplay.ForWorkspace(workspace, chunk.Review.AfterPath)))
			}
		} else {
			found := false
			for _, preview := range full.Live {
				if preview.ID != ref.Key {
					continue
				}
				found = true
				for _, file := range preview.Files {
					pieces = append(pieces, file.UnifiedDiffWithPaths(pathdisplay.ForWorkspace(preview.Workspace, file.BeforePath), pathdisplay.ForWorkspace(preview.Workspace, file.AfterPath)))
				}
				if len(pieces) == 0 {
					pieces = append(pieces, preview.Input)
				}
				break
			}
			if !found {
				return nil, rpcFailure(404, "Live source is unavailable.")
			}
		}
		for i, piece := range pieces {
			pieces[i] = strings.TrimSuffix(piece, "\n") + "\n"
		}
		*cached = &selectedContent{server: s, ref: ref, source: strings.Join(pieces, "")}
	}
	page, err := linePage((*cached).source, offset)
	if err != nil {
		return nil, rpcFailure(413, err.Error())
	}
	return page, nil
}

type Lines struct {
	Lines []string `json:"lines"`
	Next  *uint64  `json:"next,omitempty"`
}

// Offsets are source bytes at line boundaries, so continuation never scans an
// already delivered prefix or needs a separate index.
func linePage(source string, offset uint64) (Lines, error) {
	out := Lines{Lines: []string{}}
	if offset > uint64(len(source)) || offset > 0 && source[offset-1] != '\n' {
		return out, errors.New("diff offset is unavailable")
	}
	used := 0
	for offset < uint64(len(source)) {
		line, rest, _ := strings.Cut(source[offset:], "\n")
		data, _ := json.Marshal(&line)
		if len(data) > pageBytes {
			return Lines{}, errors.New("diff line exceeds the page budget")
		}
		if len(out.Lines) == contentPageLines || used+len(data) > pageBytes {
			out.Next = new(offset)
			break
		}
		out.Lines = append(out.Lines, line)
		used += len(data)
		offset = uint64(len(source) - len(rest))
	}
	return out, nil
}

// Live metadata uses the shared Preview shape and source revision. Selected
// source reads retain every file; the selector needs only its first file path.
func livePage(source []preview.Preview, attachment string, revision uint64, cursor PageCursor) (any, *RPCError) {
	if cursor.Attachment != attachment || cursor.Revision != revision || cursor.Before > uint64(len(source)) {
		return nil, rpcFailure(409, "Live previews changed; reopen current metadata.")
	}
	page := PageResult{Live: []preview.Preview{}}
	used := 0
	short := func(path string) string {
		if len(path) <= 1024 {
			return path
		}
		return strings.ToValidUTF8(path[:1024], "") + "…"
	}
	for index := int(cursor.Before); index < len(source); index++ {
		full := source[index]
		if cursor.Caller != "" && full.Caller != cursor.Caller {
			continue
		}
		metadata := preview.Preview{ID: full.ID, Caller: full.Caller, Thread: full.Thread, Workspace: short(full.Workspace), Status: full.Status, Complete: full.Complete, Truncated: full.Truncated, DiffText: full.DiffText}
		if len(full.Files) > 0 {
			metadata.Files = []diffmodel.ReviewFile{{BeforePath: short(full.Files[0].BeforePath), AfterPath: short(full.Files[0].AfterPath)}}
		}
		data, err := json.Marshal(&metadata, encoding)
		if err != nil {
			return nil, rpcFailure(500, fmt.Sprintf("Live metadata unavailable: %v", err))
		}
		if len(page.Live) == pageEntries || used+len(data) > feedBytes {
			if len(page.Live) == 0 {
				return nil, rpcFailure(413, "Live preview identity exceeds metadata budget.")
			}
			cursor.Before = uint64(index)
			page.Next = &cursor
			break
		}
		page.Live = append(page.Live, metadata)
		used += len(data)
	}
	return page, nil
}
