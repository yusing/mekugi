package retained

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

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

// freezeNetCaptures uses the same validated selection and publication lock as
// ordinary reads. Each decoded file is copied once, before any later projection.
func (s *store) freezeNetCaptures(ctx context.Context, options changeReadOptions, index changeIndex) error {
	frozen := options.freeze
	bytes := 0
changes:
	for _, id := range options.ids {
		start, startBytes := len(frozen.Captures), bytes
		workspace := index.workspaceFor(id)
		canonical := func(path string) string {
			if path == "" {
				return ""
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(workspace, path)
			}
			return filepath.Clean(path)
		}
		for _, call := range index.Changes[id].Calls {
			if err := ctx.Err(); err != nil {
				return err
			}
			record, err := s.readChangeRecord(workspace, id, index.Changes[id].Correlation, call.ID)
			if err != nil {
				if !options.unavailable.skip(id, err) {
					return err
				}
				frozen.Captures, bytes = frozen.Captures[:start], startBytes
				continue changes
			}
			capture := changes.FrozenCapture{ChangeID: id, Workspace: workspace, Order: record.CaptureOrder}
			if history := authoredChangeHistory(record.History); history.ExecOutcome != nil {
				capture.Coverage = history.ExecOutcome.Coverage
			}
			key := changes.CaptureKey(workspace, record.History.Caller, id, record.CaptureOrder)
			var chunk uint64
			for _, review := range record.History.ReviewFiles {
				if changes.AuthoredReview(review.ReviewFile) || changes.GitDiagnosticReview(review.ReviewFile) {
					chunk++
				}
				if !changes.AuthoredReview(review.ReviewFile) {
					continue
				}
				if review.DiffBytes() > changes.MaxReadBytes-bytes {
					return errors.New("change history exceeds 64 MiB")
				}
				file, err := review.Load()
				if err != nil {
					if !options.unavailable.skip(id, fmt.Errorf("change %s: %w", id, err)) {
						return err
					}
					frozen.Captures, bytes = frozen.Captures[:start], startBytes
					continue changes
				}
				bytes += len(file.Diff)
				if bytes > changes.MaxReadBytes {
					return errors.New("change history exceeds 64 MiB")
				}
				file.BeforePath, file.AfterPath = canonical(file.BeforePath), canonical(file.AfterPath)
				frozenFile := changes.FrozenFile{Key: key, Chunk: chunk, File: file}
				if record.CaptureOrder == 0 {
					// Legacy progress keys identify a single original review entry.
					legacy := workspace + "\x00" + call.ID + "/" + strconv.FormatUint(chunk-1, 10)
					frozenFile.Key, frozenFile.Chunk = workspace+"\x00"+record.History.Caller+"\x00"+id+"\x00"+legacy, 1
				}
				capture.Files = append(capture.Files, frozenFile)
			}
			frozen.Captures = append(frozen.Captures, capture)
		}
	}
	slices.SortStableFunc(frozen.Captures, func(a, b changes.FrozenCapture) int { return cmp.Compare(a.Order, b.Order) })
	return nil
}

func (*service) ProjectFrozen(frozen changes.FrozenRead, seen map[string]uint64) (string, error) {
	index := changeIndex{Workspace: frozen.Workspace, workspaces: make(map[string]string)}
	options := changeReadOptions{workspace: frozen.Workspace}
	if len(seen) != 0 {
		options.yielded = map[string]int{"frozen": 1}
	}
	var captures []changeCapture
	for _, source := range frozen.Captures {
		stream, _, err := changes.ParseID(source.ChangeID)
		if err != nil {
			return "", err
		}
		index.workspaces[stream] = source.Workspace
		capture := changeCapture{id: source.ChangeID, order: source.Order, coverage: source.Coverage}
		for _, file := range source.Files {
			if file.Key == "" || file.Chunk == 0 {
				return "", errors.New("invalid frozen capture chunk")
			}
			if seen[file.Key] < file.Chunk {
				capture.files = append(capture.files, file.File)
			}
		}
		captures = append(captures, capture)
	}
	text, err := renderNetCaptures(options, index, captures)
	if frozen.Fallback != "" {
		text = frozen.Fallback
	}
	if frozen.Diagnostic != "" {
		err = errors.Join(errors.New(frozen.Diagnostic), err)
	}
	return text, err
}
