package router

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/internal/livediff"
)

type liveDiffAttempt struct {
	change, correlation string
	stream              int
	confirmed           bool
	chunks              []liveDiffChunk
}

// Only review projections are retained, not whole replay records or scripts.
// A live event loads its exact new attempt once; receipts never reread it.
type liveDiffData struct {
	attempts map[string]liveDiffAttempt
	order    []string
	bytes    int
}

func newLiveDiffData() *liveDiffData {
	return &liveDiffData{attempts: make(map[string]liveDiffAttempt)}
}

func (d *liveDiffData) apply(ctx context.Context, store *mekugiReplayStore, event liveDiffChange) error {
	for _, call := range event.Change.Calls {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := event.Workspace + "\x00" + call.ID
		if old, found := d.attempts[key]; found {
			if old.change != event.ID || old.correlation != event.Change.Correlation || old.stream != event.Stream {
				return errors.New("live diff attempt identity changed")
			}
			if call.Confirmed && !old.confirmed {
				old.confirmed = true
				for i := range old.chunks {
					chunk := &old.chunks[i]
					switch chunk.Status {
					case event.ID + " changes observed", event.ID + " no changes observed", event.ID + " observation incomplete":
						chunk.Status, chunk.Applied = event.ID+" applied", true
					}
				}
				d.attempts[key] = old
			}
			continue
		}
		if len(d.attempts) >= 65536 {
			return errors.New("live diff exceeds 65,536 attempts")
		}
		record, found, err := store.read(event.Workspace, call.ID, false)
		if err != nil {
			return err
		}
		if !found || record.History.ChangeID != event.ID || record.History.CorrelationID != event.Change.Correlation {
			return fmt.Errorf("change %s has a missing or inconsistent attempt", event.ID)
		}
		history := record.History
		status := trackedStatus(history, call.Confirmed)
		attempt := liveDiffAttempt{change: event.ID, correlation: event.Change.Correlation, stream: event.Stream, confirmed: call.Confirmed}
		origin := livediff.Origin{Change: event.ID, Caller: history.Caller, Source: cmp.Or(history.Source, history.ToolName)}
		var managed []string
		for n, file := range history.ReviewFiles {
			if file.Origin != "" {
				managed = append(managed, managedReviewRow(file))
				continue
			}
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
			if d.bytes > maxChangeReadBytes {
				return errors.New("live diff exceeds 64 MiB; use mchanges with a narrower range")
			}
			attempt.chunks = append(attempt.chunks, liveDiffChunk{
				Key: key + "/" + strconv.Itoa(n), Stream: event.Workspace + "\x00" + event.Namespace + "\x00" + strconv.Itoa(event.Stream),
				CaptureOrder: record.CaptureOrder, Status: event.ID + " " + status,
				Review: file, Applied: status == "applied" || history.ExecOutcome != nil && history.Applied, Origin: origin,
			})
		}
		if len(managed) != 0 {
			label := fmt.Sprintf("%s: %d tool-managed files", event.ID, len(managed))
			reason := status + "\n" + strings.Join(managed, "\n")
			d.bytes += len(reason) + len(label)*2 + len(key)
			if d.bytes > maxChangeReadBytes {
				return errors.New("live diff exceeds 64 MiB; use mchanges with a narrower range")
			}
			attempt.chunks = append(attempt.chunks, liveDiffChunk{
				Key: key + "/managed", Stream: event.Workspace + "\x00" + event.Namespace + "\x00" + strconv.Itoa(event.Stream),
				CaptureOrder: record.CaptureOrder, Status: event.ID + " " + status, Applied: true,
				Review: mekugi.ReviewFile{BeforePath: label, AfterPath: label, Origin: "tool-managed", Incomplete: reason}, Origin: origin,
			})
		}
		d.attempts[key] = attempt
		d.order = append(d.order, key)
	}
	return nil
}

func (d *liveDiffData) files() []liveDiffFile {
	var captures []liveDiffChunk
	for _, key := range d.order {
		captures = append(captures, d.attempts[key].chunks...)
	}
	return livediff.GroupCaptures(captures)
}

func (s *mekugiReplayStore) liveDiffSnapshot(ctx context.Context, scope liveDiffScope) (*liveDiffData, error) {
	data := newLiveDiffData()
	if err := data.reconcile(ctx, s, scope); err != nil {
		return nil, err
	}
	return data, nil
}

// Scope additions reconcile index membership and receipts without reloading
// immutable attempts. Transport gaps use a fresh snapshot instead.
func (d *liveDiffData) reconcile(ctx context.Context, s *mekugiReplayStore, scope liveDiffScope) error {
	present := make(map[string]bool)
	for _, workspace := range slices.Sorted(maps.Keys(scope.Workspaces)) {
		if !filepath.IsAbs(workspace) {
			return errors.New("live diff workspace must be absolute")
		}
		indexes, err := s.liveDiffIndexes(workspace, scope.Workspaces[workspace])
		if err != nil {
			return err
		}
		threads := scope.Workspaces[workspace]
		for _, index := range indexes {
			for stream, info := range index.Streams {
				for number := 1; number <= info.Next; number++ {
					id := changeHandle(changeStreamName(stream), number)
					change := index.Changes[id]
					change.Calls = slices.Clone(change.Calls)
					change.Calls = slices.DeleteFunc(change.Calls, func(call trackedCall) bool {
						return threads != nil && !threads[cmp.Or(call.Thread, info.Thread)]
					})
					for _, call := range change.Calls {
						present[index.Workspace+"\x00"+call.ID] = true
					}
					if err := d.apply(ctx, s, liveDiffChange{
						Workspace: index.Workspace, Namespace: index.Namespace, Thread: info.Thread, Stream: stream,
						ID: id, Change: change,
					}); err != nil {
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

func (s *mekugiReplayStore) liveDiffIndexes(workspace string, threads map[string]bool) ([]changeIndex, error) {
	names := map[string]bool{changeIndexName(workspace, ""): true}
	if threads == nil {
		entries, err := os.ReadDir(s.directory)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), changeIndexPrefix) && retainedDataName(entry.Name()) {
				names[entry.Name()] = true
			}
		}
	} else {
		for thread := range threads {
			scope, _, err := s.readHandleScope(thread)
			if err != nil {
				return nil, err
			}
			names[changeIndexName(workspace, scope.Namespace)] = true
		}
	}
	var indexes []changeIndex
	for _, name := range slices.Sorted(maps.Keys(names)) {
		data, err := readManagedOutputFile(filepath.Join(s.directory, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var index changeIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return nil, err
		}
		if index.Version != 1 || index.Changes == nil || changeIndexName(index.Workspace, index.Namespace) != name {
			return nil, errors.New("invalid live diff change index identity")
		}
		if index.Workspace != workspace {
			continue
		}
		if err := validateChangeIndex(index); err != nil {
			return nil, err
		}
		indexes = append(indexes, index)
	}
	return indexes, nil
}
