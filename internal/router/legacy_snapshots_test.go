package router

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json/v2"
	"errors"
	"slices"

	"github.com/yusing/mekugi"
	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/internal/responses"
)

type snapshotFiles struct {
	Exec   []execFileSnapshot        `json:",omitempty"`
	Native []nativePatchFileSnapshot `json:",omitempty"`
	Review []mekugi.ReviewFile       `json:",omitempty"`
	Texts  map[int]string            `json:",omitempty"`
}

func (files *snapshotFiles) textSlots() []*string {
	var slots []*string
	for i := range files.Exec {
		slots = append(slots, &files.Exec[i].Content)
	}
	for i := range files.Native {
		slots = append(slots, &files.Native[i].Before, &files.Native[i].TargetBefore)
	}
	for i := range files.Review {
		slots = append(slots, &files.Review[i].Diff)
	}
	return slots
}

// Encoder retained solely to construct historical fixtures for read compatibility.
func (b *snapshotBatch) putFiles(files snapshotFiles, refs *legacycall.Snapshots) (string, error) {
	for i, slot := range files.textSlots() {
		if len(*slot) < 1024 {
			continue
		}
		name, err := b.put(*slot)
		if err != nil {
			return "", err
		}
		if files.Texts == nil {
			files.Texts = make(map[int]string)
		}
		files.Texts[i] = name
		refs.Contents = append(refs.Contents, name)
		*slot = ""
	}
	return b.put(&files)
}

// A record's snapshot blobs are encoded together and published by the record
// write: one ownership update and admission, then every missing blob before
// the envelope that references them.
type snapshotBatch struct {
	store   *mekugiReplayStore
	missing []managedFile
	seen    map[string]bool
}

// Called under store.lock. Existing blobs are validated before reuse.
func (b *snapshotBatch) put(value any) (string, error) {
	data, err := json.Marshal(value, json.Deterministic(true), json.FormatNilSliceAsNull(true))
	if err != nil {
		return "", err
	}
	if len(data) > maxReplayRecordBytes {
		return "", errors.New("snapshot exceeds size limit")
	}
	name := legacycall.SnapshotName(data)
	if b.seen[name] {
		return name, nil
	}
	if b.seen == nil {
		b.seen = make(map[string]bool)
	}
	b.seen[name] = true
	_, exists, err := legacycall.SnapshotData(b.store.directory, name, maxReplayRecordBytes)
	if err != nil || exists {
		return name, err
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(data); err != nil {
		return "", err
	}
	if err := z.Close(); err != nil {
		return "", err
	}
	b.missing = append(b.missing, managedFile{name: name, pattern: "snapshot-pending-", data: compressed.Bytes()})
	return name, nil
}

// compactSnapshots moves filesystem evidence into snapshot blobs and returns
// the blobs that still need publication.
func (s *mekugiReplayStore) compactSnapshots(r *replayRecord) ([]managedFile, error) {
	refs := new(legacycall.Snapshots)
	batch := &snapshotBatch{store: s}
	var err error
	h := &r.History
	if h.ExecObservation != nil && len(h.ExecObservation.Files) > 0 {
		refs.ExecFiles, err = batch.putFiles(snapshotFiles{Exec: slices.Clone(h.ExecObservation.Files)}, refs)
		if err != nil {
			return nil, err
		}
		observation := *h.ExecObservation
		observation.Files = nil
		h.ExecObservation = &observation
	}
	if h.ExecObservation != nil && h.ExecObservation.Inventory != nil && len(h.ExecObservation.Inventory.Files) > 0 {
		inventory := *h.ExecObservation.Inventory
		refs.ExecInventoryFiles, err = batch.putFiles(snapshotFiles{Exec: slices.Clone(inventory.Files)}, refs)
		if err != nil {
			return nil, err
		}
		inventory.Files = nil
		observation := *h.ExecObservation
		observation.Inventory = &inventory
		h.ExecObservation = &observation
	}
	if h.ResolvedBaseline != nil && len(h.ResolvedBaseline.Files) > 0 {
		refs.BaselineFiles, err = batch.putFiles(snapshotFiles{Exec: slices.Clone(h.ResolvedBaseline.Files)}, refs)
		if err != nil {
			return nil, err
		}
		baseline := *h.ResolvedBaseline
		baseline.Files = nil
		h.ResolvedBaseline = &baseline
	}
	if h.ResolvedBaseline != nil && h.ResolvedBaseline.Inventory != nil && len(h.ResolvedBaseline.Inventory.Files) > 0 {
		inventory := *h.ResolvedBaseline.Inventory
		refs.BaselineInventoryFiles, err = batch.putFiles(snapshotFiles{Exec: slices.Clone(inventory.Files)}, refs)
		if err != nil {
			return nil, err
		}
		inventory.Files = nil
		baseline := *h.ResolvedBaseline
		baseline.Inventory = &inventory
		h.ResolvedBaseline = &baseline
	}
	h.NativePatches = slices.Clone(h.NativePatches)
	for i := range h.NativePatches {
		if len(h.NativePatches[i].Files) == 0 {
			continue
		}
		if refs.NativeFiles == nil {
			refs.NativeFiles = make([]string, len(h.NativePatches))
		}
		refs.NativeFiles[i], err = batch.putFiles(snapshotFiles{Native: slices.Clone(h.NativePatches[i].Files)}, refs)
		if err != nil {
			return nil, err
		}
		h.NativePatches[i].Files = nil
	}
	if len(h.ReviewFiles) > 0 {
		refs.ReviewFiles, err = batch.putFiles(snapshotFiles{Review: slices.Clone(h.ReviewFiles)}, refs)
		if err != nil {
			return nil, err
		}
		h.ReviewFiles = nil
	}
	if len(refs.Names()) > 0 {
		slices.Sort(refs.Contents)
		refs.Contents = slices.Compact(refs.Contents)
		r.Version, r.Snapshots = 2, refs
	}
	return batch.missing, nil
}

// Manufacture old storage envelopes, never production snapshots.
func putLegacySnapshotFixture(store *mekugiReplayStore, ctx context.Context, call string, history mekugiHistory) error {
	if err := store.put(ctx, "/w", map[string]mekugiHistory{call: history}); err != nil {
		return err
	}
	return store.scoped(ctx).locked(ctx, func(store *mekugiReplayStore) error {
		record, found, err := store.read("/w", call, false)
		if err != nil || !found {
			return err
		}
		blobs, err := store.compactSnapshots(&record)
		if err != nil {
			return err
		}
		data, err := responses.MarshalJSON(record)
		if err != nil {
			return err
		}
		return store.writeLegacySnapshotFiles(managedFile{name: legacycall.Name("/w", call, false), pattern: "call-pending-", data: data}, record.Snapshots.Names(), blobs)
	})
}

type managedFile struct {
	name    string
	pattern string
	data    []byte
}

// Called under store.lock. The record and every dependency it references are
// retained in one catalog update and admitted together before any write. The
// missing dependencies are published before the record referencing them.
func (s *mekugiReplayStore) writeLegacySnapshotFiles(record managedFile, dependencies []string, missing []managedFile) error {
	if err := s.retainFiles(append([]string{record.name}, dependencies...)...); err != nil {
		return storageIOError(err)
	}
	size := int64(len(record.data))
	for _, file := range missing {
		size += int64(len(file.data))
	}
	if err := s.maintainStorage(record.name, size); err != nil {
		return err
	}
	for _, file := range missing {
		if err := s.transaction.Write(file.name, file.pattern, file.data); err != nil {
			return storageIOError(err)
		}
	}
	return storageIOError(s.transaction.Write(record.name, record.pattern, record.data))
}
