package legacycall

import (
	"bytes"
	"crypto/sha256"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
)

// Source: internal/router/mekugi_store.go:123:201@[ff834f05] record identity and read
func Name(workspace, callID string, commentary bool) string {
	prefix := "call-"
	if commentary {
		prefix = "commentary-"
	}
	return prefix + fmt.Sprintf("%x.json", sha256.Sum256(fmt.Appendf(nil, "%t\x00%s\x00%s", commentary, workspace, callID)))
}
func Read(directory, workspace, callID string, commentary bool, limit int) (Record, bool, error) {
	name := filepath.Join(directory, Name(workspace, callID, commentary))
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return Record{}, false, errors.New("invalid replay record file")
	}
	f, err := os.Open(name)
	if err != nil {
		return Record{}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return Record{}, false, err
	}
	if len(data) > limit {
		return Record{}, false, errors.New("replay record exceeds size limit")
	}
	var r Record
	// Older records contain a tool-success flag in History. Accept it only
	// at the wire boundary; it is not part of the saved filesystem changes.
	wire := struct {
		*Record
		History struct {
			*History
			LegacyToolSuccess bool `json:"Applied"`
			ExecObservation   *struct {
				Observation
				LegacySweep bool `json:"Sweep"`
			}
		}
	}{Record: &r}
	wire.History.History = &r.History
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	if err := json.UnmarshalDecode(dec, &wire, jsonv1.DefaultOptionsV1(), json.RejectUnknownMembers(true)); err != nil {
		return r, false, fmt.Errorf("decode replay record: %w", err)
	}
	if wire.History.ExecObservation != nil {
		r.History.ExecObservation = &wire.History.ExecObservation.Observation
	}
	if err := json.UnmarshalDecode(dec, new(any), jsonv1.DefaultOptionsV1()); !errors.Is(err, io.EOF) {
		return r, false, errors.New("trailing replay record data")
	}
	if (r.Version != 1 && r.Version != 2) || r.Workspace != workspace || r.CallID != callID || r.Commentary != commentary {
		return r, false, errors.New("replay record identity/version mismatch")
	}
	if err := restoreSnapshots(directory, &r, limit); err != nil {
		return r, false, err
	}
	// Consumers and immutable comparisons use the expanded version-1 facts.
	r.Version, r.Snapshots = 1, nil
	return r, true, nil
}

// Read only envelope references for ownership/cleanup, never decompress all
// baselines just to enumerate dependencies. Identity is checked before use.
func Dependencies(name string, data []byte) ([]string, error) {
	// Do not decode History here. Its older wire types (for example fixed byte
	// arrays in command-segment evidence) retain their version-1 JSON semantics.
	var r struct {
		Version    int
		Workspace  string
		CallID     string
		Commentary bool
		Snapshots  *Snapshots
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	if (r.Version != 1 && r.Version != 2) || Name(r.Workspace, r.CallID, r.Commentary) != name || r.Version == 1 && r.Snapshots != nil {
		return nil, errors.New("invalid snapshot dependency envelope")
	}
	names := r.Snapshots.Names()
	for _, ref := range names {
		if !ValidSnapshotName(ref) {
			return nil, errors.New("invalid snapshot dependency identity")
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}
