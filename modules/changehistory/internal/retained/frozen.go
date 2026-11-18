package retained

import (
	"context"
	"errors"
	"fmt"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"
)

func (*service) Freeze(ctx context.Context, selected changes.Scope, query changes.Query) (changes.FrozenRead, error) {
	frozen := changes.FrozenRead{Workspace: query.Workspace}
	if query.View != "net" || query.Mine || len(query.Paths) != 0 || len(query.IDs) == 0 || len(query.Yielded) != 0 {
		return frozen, errors.New("frozen capture requires explicit unfiltered change IDs")
	}
	options := options(query)
	options.freeze = &frozen
	text, _, err := selectedStore(selected).readChangeView(ctx, options)
	if err != nil {
		if _, partial := errors.AsType[*partialChangeReadError](err); !partial {
			return changes.FrozenRead{}, err
		}
		frozen.Diagnostic = err.Error()
	}
	frozen.Fallback = text
	return frozen, nil
}

// freezeNetCaptures borrows the normal read's publication lock and dependency
// retention. Only record identities and chunk bounds escape, never review text.
func (s *store) freezeNetCaptures(ctx context.Context, options changeReadOptions, index changeIndex) error {
	frozen := options.freeze
changes:
	for _, id := range options.ids {
		start := len(frozen.Captures)
		workspace := index.workspaceFor(id)
		for _, call := range index.Changes[id].Calls {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := s.readChangeRecord(workspace, id, index.Changes[id].Correlation, call.ID)
			if err != nil {
				if !options.unavailable.skip(id, err) {
					return err
				}
				frozen.Captures = frozen.Captures[:start]
				continue changes
			}
			capture := changes.FrozenCapture{ChangeID: id, Workspace: workspace, Correlation: index.Changes[id].Correlation,
				CallID: call.ID, Caller: record.History.Caller, Order: record.CaptureOrder, FirstChunk: 1}
			for _, review := range record.History.ReviewFiles {
				if changes.AuthoredReview(review.ReviewFile) || changes.GitDiagnosticReview(review.ReviewFile) {
					capture.LastChunk++
				}
			}
			frozen.Captures = append(frozen.Captures, capture)
		}
	}
	return nil
}

func (*service) ProjectFrozen(ctx context.Context, selected changes.Scope, frozen changes.FrozenRead, seen map[string]uint64) (changes.ProjectedCapture, error) {
	index := changeIndex{Workspace: frozen.Workspace, workspaces: make(map[string]string), Changes: make(map[string]trackedChange)}
	options := changeReadOptions{workspace: frozen.Workspace, unavailable: make(changeReadFailures)}
	if len(seen) != 0 {
		options.yielded = make(map[string]int, len(seen))
		for key, through := range seen {
			options.yielded[key] = int(min(through, uint64(^uint(0)>>1)))
		}
	}
	ranges := make(map[string]changes.FrozenCapture, len(frozen.Captures))
	for _, capture := range frozen.Captures {
		stream, _, err := changes.ParseID(capture.ChangeID)
		if err != nil {
			return changes.ProjectedCapture{}, err
		}
		if capture.Workspace == "" || capture.Correlation == "" || capture.CallID == "" || capture.FirstChunk == 0 || capture.FirstChunk-1 > capture.LastChunk {
			return changes.ProjectedCapture{}, errors.New("invalid frozen capture range")
		}
		name := legacycall.Name(capture.Workspace, capture.CallID, false)
		if _, exists := ranges[name]; exists {
			return changes.ProjectedCapture{}, errors.New("duplicate frozen capture record")
		}
		ranges[name] = capture
		if workspace, exists := index.workspaces[stream]; exists && workspace != capture.Workspace {
			return changes.ProjectedCapture{}, errors.New("inconsistent frozen capture workspace")
		}
		index.workspaces[stream] = capture.Workspace
		change, exists := index.Changes[capture.ChangeID]
		if exists && change.Correlation != capture.Correlation {
			return changes.ProjectedCapture{}, errors.New("inconsistent frozen capture correlation")
		}
		change.Correlation = capture.Correlation
		change.Calls = append(change.Calls, trackedCall{ID: capture.CallID})
		index.Changes[capture.ChangeID] = change
		if !exists {
			options.ids = append(options.ids, capture.ChangeID)
		}
	}
	var projection changes.ProjectedCapture
	err := selectedStore(selected).locked(ctx, func(store *store) error {
		captures, err := store.loadChangeCaptures(ctx, frozen.Workspace, index, options.ids, nil, options.unavailable, options.yielded, ranges)
		if err != nil {
			return err
		}
		projection, err = renderNetCaptures(options, index, captures)
		for _, id := range options.ids {
			if failure := options.unavailable[id]; failure != nil {
				err = errors.Join(err, fmt.Errorf("change %s: %w", id, failure))
			}
		}
		return err
	})
	if frozen.Fallback != "" {
		projection = changes.ProjectedCapture{Text: frozen.Fallback}
	}
	if frozen.Diagnostic != "" {
		err = errors.Join(errors.New(frozen.Diagnostic), err)
	}
	return projection, err
}

// ProjectLegacyFrozen reads the historical copied format without publishing new
// copies or reselecting IDs. New completions use metadata-only ProjectFrozen.
func (*service) ProjectLegacyFrozen(ctx context.Context, frozen changes.LegacyFrozenRead, seen map[string]uint64) (changes.ProjectedCapture, error) {
	index := changeIndex{Workspace: frozen.Workspace, workspaces: make(map[string]string)}
	options := changeReadOptions{workspace: frozen.Workspace}
	if len(seen) != 0 {
		options.yielded = map[string]int{"legacy": 1}
	}
	var captures []changeCapture
	bytes := 0
	for _, source := range frozen.Captures {
		if err := ctx.Err(); err != nil {
			return changes.ProjectedCapture{}, err
		}
		stream, _, err := changes.ParseID(source.ChangeID)
		if err != nil {
			return changes.ProjectedCapture{}, err
		}
		index.workspaces[stream] = source.Workspace
		capture := changeCapture{id: source.ChangeID, order: source.Order, coverage: source.Coverage}
		for _, file := range source.Files {
			if file.Key == "" || file.Chunk == 0 {
				return changes.ProjectedCapture{}, errors.New("invalid legacy capture chunk")
			}
			if seen[file.Key] < file.Chunk {
				bytes += len(file.File.Diff)
				if bytes > changes.MaxReadBytes {
					return changes.ProjectedCapture{}, errors.New("legacy capture exceeds 64 MiB")
				}
				capture.files = append(capture.files, file.File)
			}
		}
		captures = append(captures, capture)
	}
	projection, err := renderNetCaptures(options, index, captures)
	if frozen.Fallback != "" {
		projection = changes.ProjectedCapture{Text: frozen.Fallback}
	}
	if frozen.Diagnostic != "" {
		err = errors.Join(errors.New(frozen.Diagnostic), err)
	}
	return projection, err
}
