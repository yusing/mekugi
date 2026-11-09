package history

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/libraries/legacycall"
)

// HostRecord adds owner-supplied dependencies without changing the frozen
// historical codec. The host owner interprets no feature attachment payload.
type HostRecord struct {
	legacycall.Record
	Attachments []string `json:",omitempty"`
}

// ReadHostRecord reads host facts and attachment identities only. Feature owners
// open their selected attachments separately, without expanding unrelated text.
func ReadHostRecord(directory, workspace, callID string, commentary bool) (HostRecord, bool, error) {
	var result HostRecord
	name := legacycall.Name(workspace, callID, commentary)
	data, err := ReadRecord(filepath.Join(directory, name))
	if errors.Is(err, os.ErrNotExist) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	var header struct{ Version int }
	if err := json.Unmarshal(data, &header); err != nil {
		return result, false, err
	}
	if header.Version != 3 {
		record, found, err := legacycall.Read(directory, workspace, callID, commentary, MaxRecordBytes)
		return HostRecord{Record: record}, found, err
	}
	if err := json.Unmarshal(data, &result, jsonv1.DefaultOptionsV1(), json.RejectUnknownMembers(true)); err != nil {
		return result, false, err
	}
	if result.Workspace != workspace || result.CallID != callID || result.Commentary != commentary || result.Snapshots != nil {
		return result, false, errors.New("host attachment record identity mismatch")
	}
	if _, err := HostDependencies(name, data); err != nil {
		return result, false, err
	}
	return result, true, nil
}

func (s *Store) ReadAttachedHostCall(workspace, callID string, commentary bool) (HostRecord, bool, error) {
	return ReadHostRecord(s.directory, workspace, callID, commentary)
}

// HostDependencies preserves historical dependency interpretation at its codec.
// Version 3 only names feature roots; their registered owners supply the graph.
func HostDependencies(name string, data []byte) ([]string, error) {
	var value struct {
		Version           int
		Workspace, CallID string
		Commentary        bool
		Attachments       []string
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	if value.Version != 3 {
		return legacycall.Dependencies(name, data)
	}
	if legacycall.Name(value.Workspace, value.CallID, value.Commentary) != name {
		return nil, errors.New("invalid host attachment identity")
	}
	seen := make(map[string]bool)
	for _, ref := range value.Attachments {
		if ref == "" || ref == "." || ref == ".." || filepath.Base(ref) != ref || ref == name || seen[ref] {
			return nil, errors.New("invalid host attachment dependency")
		}
		seen[ref] = true
	}
	return value.Attachments, nil
}
