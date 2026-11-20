package sessionapi

import (
	json "encoding/json/v2"
	"fmt"
	"strings"

	"github.com/yusing/mekugi/libraries/diffmodel"
	"github.com/yusing/mekugi/modules/livepreview/preview"
)

// Live metadata uses the shared Preview shape and source revision. Selected
// source reads retain every file; the selector needs only its first file path.
func livePage(source []preview.Preview, attachment string, revision uint64, cursor PageCursor) (any, *RPCError) {
	if cursor.Attachment != attachment || cursor.Revision != revision {
		return nil, rpcSuperseded("Live previews changed; reopen current metadata.")
	}
	if cursor.Before > uint64(len(source)) {
		return nil, rpcFailure(409, "The live preview boundary is unavailable.")
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
