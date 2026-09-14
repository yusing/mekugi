package router

import (
	"context"
	"errors"
	"fmt"
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
				old.chunks = slices.Clone(old.chunks)
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
		attempt := liveDiffAttempt{change: event.ID, correlation: event.Change.Correlation, stream: event.Stream, confirmed: call.Confirmed}
		// Private retained scripts are not workspace edits.
		if !strings.HasPrefix(strings.TrimLeft(history.recoveryBaseline(), "\r\n"), "in "+shellArtifactPrefix) {
			for n, file := range history.ReviewFiles {
				diff := file.UnifiedDiff()
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
				d.bytes += len(diff) + len(file.BeforePath) + len(file.AfterPath) + len(key)
				if d.bytes > maxChangeReadBytes {
					return errors.New("live diff exceeds 64 MiB; use hchanges with a narrower range")
				}
				attempt.chunks = append(attempt.chunks, liveDiffChunk{
					key: key + "/" + strconv.Itoa(n), stream: event.Workspace + "\x00" + strconv.Itoa(event.Stream),
					captureOrder: record.CaptureOrder, status: event.ID + " " + trackedStatus(history, call.Confirmed),
					review: file, applied: trackedStatus(history, call.Confirmed) == "applied", diff: diff,
				})
			}
		}
		d.attempts[key] = attempt
		d.order = append(d.order, key)
	}
	return nil
}

func (d *liveDiffData) files() ([]liveDiffFile, error) {
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
	indexes, err := s.liveDiffScopeIndexes(scope)
	if err != nil {
		return err
	}
	present := make(map[string]bool)
	for _, index := range indexes {
		for stream, info := range index.Streams {
			for number := 1; number <= info.Next; number++ {
				id := "hp_" + changeStreamName(stream) + strconv.Itoa(number)
				for _, call := range index.Changes[id].Calls {
					present[index.Workspace+"\x00"+call.ID] = true
				}
				if err := d.apply(ctx, s, liveDiffChange{
					Workspace: index.Workspace, Thread: info.Thread, Stream: stream,
					ID: id, Change: index.Changes[id],
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
