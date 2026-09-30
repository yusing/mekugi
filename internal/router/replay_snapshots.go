package router

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi"
)

// Version 2 moves only filesystem evidence out of the call envelope. Inputs,
// outcomes, identity and ordering remain call-local. Hashes identify the exact
// uncompressed JSON, not a compressed spelling or a live workspace file.
type replaySnapshots struct {
	ExecFiles     string   `json:",omitempty"`
	BaselineFiles string   `json:",omitempty"`
	NativeFiles   []string `json:",omitempty"`
	ReviewFiles   string   `json:",omitempty"`
	Contents      []string `json:",omitempty"`
}

func (refs *replaySnapshots) names() []string {
	if refs == nil {
		return nil
	}
	names := []string{refs.ExecFiles, refs.BaselineFiles, refs.ReviewFiles}
	names = append(names, refs.NativeFiles...)
	names = append(names, refs.Contents...)
	return slices.DeleteFunc(names, func(name string) bool { return name == "" })
}

// Large text is shared independently of path and collection identity. Explicit
// slot indexes distinguish references from literal file contents that happen
// to look like a hash. Small strings stay inline to avoid tiny disk objects.
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

func (s *mekugiReplayStore) putSnapshotFiles(files snapshotFiles, refs *replaySnapshots) (string, error) {
	for i, slot := range files.textSlots() {
		if len(*slot) < 1024 {
			continue
		}
		name, err := s.putSnapshot(*slot)
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
	return s.putSnapshot(&files)
}

func snapshotName(data []byte) string {
	return fmt.Sprintf("snapshot-%x.json.gz", sha256.Sum256(data))
}

func validSnapshotName(name string) bool {
	hash, ok := strings.CutPrefix(name, "snapshot-")
	if !ok {
		return false
	}
	hash, ok = strings.CutSuffix(hash, ".json.gz")
	return ok && len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
}

func (s *mekugiReplayStore) snapshotData(name string) ([]byte, bool, error) {
	if !validSnapshotName(name) {
		return nil, false, errors.New("invalid snapshot reference")
	}
	path := filepath.Join(s.directory, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxReplayRecordBytes {
		return nil, false, errors.New("invalid snapshot file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	z, err := gzip.NewReader(io.LimitReader(f, maxReplayRecordBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("decode snapshot: %w", err)
	}
	defer z.Close()
	data, err := io.ReadAll(io.LimitReader(z, maxReplayRecordBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read snapshot: %w", err)
	}
	if len(data) > maxReplayRecordBytes || snapshotName(data) != name {
		return nil, false, errors.New("snapshot size/hash mismatch")
	}
	return data, true, nil
}

// Called under store.lock. Each blob is synced before any envelope references
// it. Existing blobs are validated, including on retries after a failed sync.
func (s *mekugiReplayStore) putSnapshot(value any) (string, error) {
	data, err := json.Marshal(value, json.Deterministic(true), json.FormatNilSliceAsNull(true))
	if err != nil {
		return "", err
	}
	if len(data) > maxReplayRecordBytes {
		return "", errors.New("snapshot exceeds size limit")
	}
	name := snapshotName(data)
	if s.snapshotPins != nil {
		s.snapshotPins[name] = true
	}
	_, exists, err := s.snapshotData(name)
	if err != nil {
		return "", err
	}
	if exists {
		if err := s.retainFiles(name); err != nil {
			return "", err
		}
		return name, syncReplayDirectory(s.directory)
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(data); err != nil {
		return "", err
	}
	if err := z.Close(); err != nil {
		return "", err
	}
	if err := s.writeManagedFile(name, "snapshot-pending-", compressed.Bytes()); err != nil {
		return "", err
	}
	return name, nil
}

func (s *mekugiReplayStore) compactSnapshots(r *replayRecord) error {
	refs := new(replaySnapshots)
	var err error
	h := &r.History
	if h.ExecObservation != nil && len(h.ExecObservation.Files) > 0 {
		refs.ExecFiles, err = s.putSnapshotFiles(snapshotFiles{Exec: slices.Clone(h.ExecObservation.Files)}, refs)
		if err != nil {
			return err
		}
		observation := *h.ExecObservation
		observation.Files = nil
		h.ExecObservation = &observation
	}
	if h.ResolvedBaseline != nil && len(h.ResolvedBaseline.Files) > 0 {
		refs.BaselineFiles, err = s.putSnapshotFiles(snapshotFiles{Exec: slices.Clone(h.ResolvedBaseline.Files)}, refs)
		if err != nil {
			return err
		}
		baseline := *h.ResolvedBaseline
		baseline.Files = nil
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
		refs.NativeFiles[i], err = s.putSnapshotFiles(snapshotFiles{Native: slices.Clone(h.NativePatches[i].Files)}, refs)
		if err != nil {
			return err
		}
		h.NativePatches[i].Files = nil
	}
	if len(h.ReviewFiles) > 0 {
		refs.ReviewFiles, err = s.putSnapshotFiles(snapshotFiles{Review: slices.Clone(h.ReviewFiles)}, refs)
		if err != nil {
			return err
		}
		h.ReviewFiles = nil
	}
	if len(refs.names()) > 0 {
		slices.Sort(refs.Contents)
		refs.Contents = slices.Compact(refs.Contents)
		r.Version, r.Snapshots = 2, refs
	}
	return nil
}

func (s *mekugiReplayStore) restoreSnapshots(r *replayRecord) error {
	refs := r.Snapshots
	if refs == nil {
		return nil
	}
	if r.Version != 2 {
		return errors.New("snapshot references require replay version 2")
	}
	// Bound the combined expansion, not just individual compressed objects.
	remaining := maxReplayRecordBytes
	remainingText := maxReplayRecordBytes
	contents := make(map[string]bool, len(refs.Contents))
	for _, name := range refs.Contents {
		if !validSnapshotName(name) {
			return errors.New("invalid snapshot content dependency")
		}
		contents[name] = false
	}
	load := func(name string) (snapshotFiles, error) {
		var files snapshotFiles
		if name == "" {
			return files, nil
		}
		data, exists, err := s.snapshotData(name)
		if err != nil {
			return files, err
		}
		if !exists {
			return files, fmt.Errorf("retained snapshot %s is unavailable", name)
		}
		remaining -= len(data)
		if remaining < 0 {
			return files, errors.New("combined snapshot manifests exceed size limit")
		}
		if err := json.Unmarshal(data, &files, json.RejectUnknownMembers(true)); err != nil {
			return files, err
		}
		slots := files.textSlots()
		for _, i := range slices.Sorted(maps.Keys(files.Texts)) {
			ref := files.Texts[i]
			_, declared := contents[ref]
			if i < 0 || i >= len(slots) || *slots[i] != "" || !declared {
				return files, errors.New("invalid snapshot content slot/dependency")
			}
			contents[ref] = true
			data, exists, err := s.snapshotData(ref)
			if err != nil {
				return files, err
			}
			if !exists {
				return files, fmt.Errorf("retained snapshot content %s is unavailable", ref)
			}
			remainingText -= len(data)
			if remainingText < 0 {
				return files, errors.New("combined snapshot content exceeds size limit")
			}
			if err := json.Unmarshal(data, slots[i]); err != nil {
				return files, err
			}
		}
		return files, nil
	}
	h := &r.History
	if refs.ExecFiles != "" {
		if h.ExecObservation == nil || len(h.ExecObservation.Files) > 0 {
			return errors.New("conflicting exec snapshot evidence")
		}
		files, err := load(refs.ExecFiles)
		if err != nil {
			return err
		}
		if len(files.Native) > 0 || len(files.Review) > 0 {
			return errors.New("invalid exec snapshot kind")
		}
		h.ExecObservation.Files = files.Exec
	}
	if refs.BaselineFiles != "" {
		if h.ResolvedBaseline == nil || len(h.ResolvedBaseline.Files) > 0 {
			return errors.New("conflicting baseline snapshot evidence")
		}
		files, err := load(refs.BaselineFiles)
		if err != nil {
			return err
		}
		if len(files.Native) > 0 || len(files.Review) > 0 {
			return errors.New("invalid baseline snapshot kind")
		}
		h.ResolvedBaseline.Files = files.Exec
	}
	if len(refs.NativeFiles) > 0 {
		if len(refs.NativeFiles) != len(h.NativePatches) {
			return errors.New("native snapshot count mismatch")
		}
		for i, name := range refs.NativeFiles {
			if name != "" && len(h.NativePatches[i].Files) > 0 {
				return errors.New("conflicting native snapshot evidence")
			}
			files, err := load(name)
			if err != nil {
				return err
			}
			if len(files.Exec) > 0 || len(files.Review) > 0 {
				return errors.New("invalid native snapshot kind")
			}
			if name != "" {
				h.NativePatches[i].Files = files.Native
			}
		}
	}
	if refs.ReviewFiles != "" && len(h.ReviewFiles) > 0 {
		return errors.New("conflicting review snapshot evidence")
	}
	files, err := load(refs.ReviewFiles)
	if err != nil {
		return err
	}
	if len(files.Exec) > 0 || len(files.Native) > 0 {
		return errors.New("invalid review snapshot kind")
	}
	if refs.ReviewFiles != "" {
		h.ReviewFiles = files.Review
	}
	for _, used := range contents {
		if !used {
			return errors.New("unreferenced snapshot content dependency")
		}
	}
	expanded := *r
	expanded.Version, expanded.Snapshots = 1, nil
	data, err := marshalProtocolJSON(expanded)
	if err != nil {
		return err
	}
	if len(data) > maxReplayRecordBytes {
		return errors.New("expanded replay record exceeds size limit")
	}
	return nil
}

// Read only envelope references for ownership/cleanup, never decompress all
// baselines just to enumerate dependencies. Identity is checked before use.
func (s *mekugiReplayStore) snapshotDependencies(name string) ([]string, error) {
	if !strings.HasPrefix(name, "call-") {
		return nil, nil
	}
	data, err := readManagedOutputFile(filepath.Join(s.directory, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // Ownership can precede initial publication.
	}
	if err != nil {
		return nil, err
	}
	// Do not decode History here. Its older wire types (for example fixed byte
	// arrays in command-segment evidence) retain their version-1 JSON semantics.
	var r struct {
		Version    int
		Workspace  string
		CallID     string
		Commentary bool
		Snapshots  *replaySnapshots
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if (r.Version != 1 && r.Version != 2) || replayRecordName(r.Workspace, r.CallID, r.Commentary) != name || r.Version == 1 && r.Snapshots != nil {
		return nil, errors.New("invalid snapshot dependency envelope")
	}
	names := r.Snapshots.names()
	for _, ref := range names {
		if !validSnapshotName(ref) {
			return nil, errors.New("invalid snapshot dependency identity")
		}
	}
	return names, nil
}
