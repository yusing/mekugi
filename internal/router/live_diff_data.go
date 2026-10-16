package router

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/yusing/mekugi/internal/livediff"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	previewmodel "github.com/yusing/mekugi/modules/livepreview/preview"
)

type liveDiffAttempt struct {
	change, correlation string
	thread              string
	stream              int
	chunks              []livediff.Chunk
	receipt             *capturedActivityEdit
}

// Only review projections are retained, not whole replay records or scripts.
// A live event loads its exact new attempt once; repeated events reuse it.
type liveDiffData struct {
	attempts map[string]liveDiffAttempt
	order    []string
	bytes    int
}

func newLiveDiffData() *liveDiffData {
	return &liveDiffData{attempts: make(map[string]liveDiffAttempt)}
}

func (d *liveDiffData) apply(ctx context.Context, store *mekugiReplayStore, event reviewChange) error {
	for _, call := range event.Calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := event.Workspace + "\x00" + call
		if old, found := d.attempts[key]; found {
			if old.change != event.ID || old.correlation != event.Correlation || old.stream != event.Stream {
				return errors.New("live diff attempt identity changed")
			}
			continue
		}
		if len(d.attempts) >= 65536 {
			return errors.New("live diff exceeds 65,536 attempts")
		}
		record, found, err := store.read(event.Workspace, call, false)
		if err != nil {
			return err
		}
		if !found || record.History.ChangeID != event.ID || record.History.CorrelationID != event.Correlation {
			return fmt.Errorf("change %s has a missing or inconsistent attempt", event.ID)
		}
		history := authoredChangeHistory(record.History)
		attempt := liveDiffAttempt{thread: cmp.Or(history.ExecutingThread, event.Thread), change: event.ID, correlation: event.Correlation, stream: event.Stream}
		if history.ExecOutcome != nil && history.ExecutingThread != "" {
			attempt.receipt = capturedEditActivity(event.Workspace, history)
			if attempt.receipt != nil {
				d.bytes += len(attempt.receipt.text)
			}
		}
		origin := livediff.Origin{Change: event.ID, Caller: history.Caller, Source: cmp.Or(history.Source, history.ToolName)}
		for n, file := range history.ReviewFiles {
			canonical := func(path string) string {
				if path == "" {
					return ""
				}
				if !filepath.IsAbs(path) {
					path = filepath.Join(event.Workspace, path)
				}
				return filepath.Clean(path)
			}
			file.BeforePath, file.AfterPath = canonical(file.BeforePath), canonical(file.AfterPath)
			d.bytes += len(file.Diff) + len(file.BeforePath) + len(file.AfterPath) + len(key) + len(origin.Caller) + len(origin.Source)
			if d.bytes > changes.MaxReadBytes {
				return errors.New("live diff exceeds 64 MiB; use mchanges with a narrower range")
			}
			attempt.chunks = append(attempt.chunks, livediff.Chunk{
				Key: key + "/" + strconv.Itoa(n), Stream: event.Workspace + "\x00" + event.Namespace + "\x00" + strconv.Itoa(event.Stream),
				Workspace: event.Workspace, CaptureOrder: record.CaptureOrder,
				Review: file, Origin: origin,
			})
		}
		d.attempts[key] = attempt
		d.order = append(d.order, key)
	}
	return nil
}

func (d *liveDiffData) files() []livediff.File {
	var captures []livediff.Chunk
	for _, key := range d.order {
		captures = append(captures, d.attempts[key].chunks...)
	}
	return livediff.GroupCaptures(captures)
}

func (s *mekugiReplayStore) liveDiffSnapshot(ctx context.Context, scope previewmodel.Scope) (*liveDiffData, error) {
	data := newLiveDiffData()
	if err := data.reconcile(ctx, s, scope); err != nil {
		return nil, err
	}
	return data, nil
}

// Scope additions reconcile index membership and receipts without reloading
// immutable attempts. Transport gaps use a fresh snapshot instead.
func (d *liveDiffData) reconcile(ctx context.Context, s *mekugiReplayStore, scope previewmodel.Scope) error {
	present := make(map[string]bool)
	for _, workspace := range slices.Sorted(maps.Keys(scope.Workspaces)) {
		if !filepath.IsAbs(workspace) {
			return errors.New("live diff workspace must be absolute")
		}
		indexes, err := s.changes.InspectVisible(s.changeScope(), workspace, scope.Workspaces[workspace])
		if err != nil {
			return err
		}
		threads := scope.Workspaces[workspace]
		for _, index := range indexes {
			for stream, info := range index.Streams {
				for number := 1; number <= info.Next; number++ {
					id := changes.ID(index.Streams[stream].Name, number)
					change := index.Changes[id]
					change.Calls = slices.Clone(change.Calls)
					change.Calls = slices.DeleteFunc(change.Calls, func(call changes.Call) bool {
						return threads != nil && !threads[cmp.Or(call.Thread, info.Thread)]
					})
					for _, call := range change.Calls {
						present[index.Workspace+"\x00"+call.ID] = true
					}
					if err := d.apply(ctx, s, retainedReviewChange(changes.Update{
						Workspace: index.Workspace, Namespace: index.Namespace, Thread: info.Thread, Stream: stream,
						ID: id, Change: change,
					})); err != nil {
						return err
					}
				}
			}
		}
	}
	for key := range d.attempts {
		if !present[key] {
			return errors.New("change records were removed; restart the live view")
		}
	}
	return nil
}
