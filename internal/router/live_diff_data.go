package router

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
					if chunk.status == event.ID+" prepared (application unconfirmed)" {
						chunk.status, chunk.applied = event.ID+" applied", true
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
		// Historical private-script edits are not workspace edits.
		if !history.Applied || !strings.HasPrefix(strings.TrimLeft(history.recoveryBaseline(), "\r\n"), "in @shell/") {
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
				d.bytes += len(file.Diff) + len(file.BeforePath) + len(file.AfterPath) + len(key)
				if d.bytes > maxChangeReadBytes {
					return errors.New("live diff exceeds 64 MiB; use hchanges with a narrower range")
				}
				attempt.chunks = append(attempt.chunks, liveDiffChunk{
					key: key + "/" + strconv.Itoa(n), stream: event.Workspace + "\x00" + strconv.Itoa(event.Stream),
					captureOrder: record.CaptureOrder, status: event.ID + " " + status,
					review: file, applied: status == "applied",
				})
			}
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
	return groupLiveDiffCaptures(captures)
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
		index, err := s.readChangeIndex(workspace)
		if err != nil {
			return err
		}
		threads := scope.Workspaces[workspace]
		for stream, info := range index.Streams {
			if threads != nil && !threads[info.Thread] {
				continue
			}
			for number := 1; number <= info.Next; number++ {
				id := changeHandle(changeStreamName(stream), number)
				change := index.Changes[id]
				for _, call := range change.Calls {
					present[index.Workspace+"\x00"+call.ID] = true
				}
				if err := d.apply(ctx, s, liveDiffChange{
					Workspace: index.Workspace, Thread: info.Thread, Stream: stream,
					ID: id, Change: change,
				}); err != nil {
					return err
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
