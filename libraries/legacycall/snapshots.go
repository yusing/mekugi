package legacycall

import (
	"compress/gzip"
	"crypto/sha256"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yusing/mekugi/libraries/persistence"
)

// Source: internal/router/replay_snapshots.go:21:285@[ff834f05] snapshot decoding
// Version 2 moves only filesystem evidence out of the call envelope. Inputs,
// outcomes, identity and ordering remain call-local. Hashes identify the exact
// uncompressed JSON, not a compressed spelling or a live workspace file.
type Snapshots struct {
	ExecFiles     string `json:",omitempty"`
	BaselineFiles string `json:",omitempty"`
	// Inventory files of the command observation or, for a cell whose
	// observation has none, of the resolved baseline.
	ExecInventoryFiles     string   `json:",omitempty"`
	BaselineInventoryFiles string   `json:",omitempty"`
	NativeFiles            []string `json:",omitempty"`
	ReviewFiles            string   `json:",omitempty"`
	Contents               []string `json:",omitempty"`
}

func (refs *Snapshots) Names() []string {
	if refs == nil {
		return nil
	}
	names := []string{refs.ExecFiles, refs.BaselineFiles, refs.ExecInventoryFiles, refs.BaselineInventoryFiles, refs.ReviewFiles}
	names = append(names, refs.NativeFiles...)
	names = append(names, refs.Contents...)
	return slices.DeleteFunc(names, func(name string) bool { return name == "" })
}

// Large text is shared independently of path and collection identity. Explicit
// slot indexes distinguish references from literal file contents that happen
// to look like a hash. Small strings stay inline to avoid tiny disk objects.
type snapshotFiles struct {
	Exec   []File         `json:",omitempty"`
	Native []NativeFile   `json:",omitempty"`
	Review []ReviewFile   `json:",omitempty"`
	Texts  map[int]string `json:",omitempty"`
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

func SnapshotName(data []byte) string {
	return fmt.Sprintf("snapshot-%x.json.gz", sha256.Sum256(data))
}

func ValidSnapshotName(name string) bool {
	hash, ok := strings.CutPrefix(name, "snapshot-")
	if !ok {
		return false
	}
	hash, ok = strings.CutSuffix(hash, ".json.gz")
	return ok && len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == ""
}

func SnapshotData(directory, name string, limit int) ([]byte, bool, error) {
	if !ValidSnapshotName(name) {
		return nil, false, errors.New("invalid snapshot reference")
	}
	path := filepath.Join(directory, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, false, errors.New("invalid snapshot file")
	}
	f, err := persistence.OpenRetained(path, limit)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	z, err := gzip.NewReader(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, fmt.Errorf("decode snapshot: %w", err)
	}
	defer z.Close()
	data, err := io.ReadAll(io.LimitReader(z, int64(limit)+1))
	if err != nil {
		return nil, false, fmt.Errorf("read snapshot: %w", err)
	}
	if len(data) > limit || SnapshotName(data) != name {
		return nil, false, errors.New("snapshot size/hash mismatch")
	}
	return data, true, nil
}

func restoreSnapshots(directory string, r *Record, limit int) error {
	refs := r.Snapshots
	if refs == nil {
		return nil
	}
	if r.Version != 2 {
		return errors.New("snapshot references require replay version 2")
	}
	// Bound the combined expansion, not just individual compressed objects.
	remaining := limit
	remainingText := limit
	contents := make(map[string]bool, len(refs.Contents))
	for _, name := range refs.Contents {
		if !ValidSnapshotName(name) {
			return errors.New("invalid snapshot content dependency")
		}
		contents[name] = false
	}
	load := func(name string) (snapshotFiles, error) {
		var files snapshotFiles
		if name == "" {
			return files, nil
		}
		data, exists, err := SnapshotData(directory, name, limit)
		if err != nil {
			return files, fmt.Errorf("snapshot %s: %w", name, err)
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
			data, exists, err := SnapshotData(directory, ref, limit)
			if err != nil {
				return files, fmt.Errorf("snapshot content %s: %w", ref, err)
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
	restoreInventory := func(name string, inventory *Inventory, kind string) error {
		if name == "" {
			return nil
		}
		if inventory == nil || len(inventory.Files) > 0 {
			return fmt.Errorf("conflicting %s inventory snapshot evidence", kind)
		}
		files, err := load(name)
		if err != nil {
			return err
		}
		if len(files.Native) > 0 || len(files.Review) > 0 {
			return fmt.Errorf("invalid %s inventory snapshot kind", kind)
		}
		inventory.Files = files.Exec
		return nil
	}
	var observed, resolved *Inventory
	if h.ExecObservation != nil {
		observed = h.ExecObservation.Inventory
	}
	if h.ResolvedBaseline != nil {
		resolved = h.ResolvedBaseline.Inventory
	}
	if err := restoreInventory(refs.ExecInventoryFiles, observed, "exec"); err != nil {
		return err
	}
	if err := restoreInventory(refs.BaselineInventoryFiles, resolved, "baseline"); err != nil {
		return err
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
	data, err := json.Marshal(&expanded, jsonv1.DefaultOptionsV1(), jsontext.EscapeForHTML(false))
	if err != nil {
		return err
	}
	if len(data) > limit {
		return errors.New("expanded replay record exceeds size limit")
	}
	return nil
}
