package router

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
)

// Encoder retained solely to construct historical fixtures for read compatibility.
func (b *snapshotBatch) putFiles(files snapshotFiles, refs *replaySnapshots) (string, error) {
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
// write: one ownership update and admission, then every missing blob, then one
// directory sync before the envelope that references them.
type snapshotBatch struct {
	store   *mekugiReplayStore
	missing []managedFile
	seen    map[string]bool
}

// Called under store.lock. Existing blobs are validated, including on retries
// after a failed sync; the record write syncs the directory before relying on them.
func (b *snapshotBatch) put(value any) (string, error) {
	data, err := json.Marshal(value, json.Deterministic(true), json.FormatNilSliceAsNull(true))
	if err != nil {
		return "", err
	}
	if len(data) > maxReplayRecordBytes {
		return "", errors.New("snapshot exceeds size limit")
	}
	name := snapshotName(data)
	if b.seen[name] {
		return name, nil
	}
	if b.seen == nil {
		b.seen = make(map[string]bool)
	}
	b.seen[name] = true
	_, exists, err := b.store.snapshotData(name)
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
	refs := new(replaySnapshots)
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
	if len(refs.names()) > 0 {
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
	return store.scoped(ctx).locked(ctx, func() error {
		record, found, err := store.read("/w", call, false)
		if err != nil || !found {
			return err
		}
		blobs, err := store.compactSnapshots(&record)
		if err != nil {
			return err
		}
		data, err := marshalProtocolJSON(record)
		if err != nil {
			return err
		}
		return store.writeManagedFiles(managedFile{name: replayRecordName("/w", call, false), pattern: "call-pending-", data: data}, record.Snapshots.names(), blobs)
	})
}
