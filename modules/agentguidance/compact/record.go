// Package compact owns immutable model summaries and separate reload selections.
package compact

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

const Prefix = "mekugi:compact:"

func Name(reference string) string {
	return fmt.Sprintf("compact-%x.json", sha256.Sum256([]byte(reference)))
}

var Kind = recordkind.Spec{ID: "compact", Match: func(name string) bool {
	hash := strings.TrimSuffix(strings.TrimPrefix(name, "compact-"), ".json")
	return len(hash) == 64 && strings.Trim(hash, "0123456789abcdef") == "" && name == "compact-"+hash+".json"
}}

type Record struct {
	Bytes              int `json:"-"` // Retained input charged by bounded consumers.
	Version            int
	Reference, Summary string
	Selection          string
}

func Decode(data []byte, reference string) (Record, error) {
	var r Record
	if err := json.Unmarshal(data, &r); err != nil {
		r.Bytes = len(data)
		return r, err
	}
	r.Bytes = len(data)
	if r.Version != 1 || r.Reference != reference || strings.TrimSpace(r.Summary) == "" {
		return r, errors.New("invalid retained compaction summary")
	}
	_, err := ReadCommands(r.Selection)
	return r, err
}

// ReadSummary resolves an exact reference using the requesting session's frozen
// handle scope. It starts no acquisition and never creates or extends a scope.
func ReadSummary(store *history.Store, thread, reference string) (string, int, error) {
	if store == nil || thread == "" || !strings.HasPrefix(reference, Prefix) {
		return "", 0, errors.New("compaction summary is unavailable")
	}
	namespace, err := store.NamespaceForThread(thread)
	if err != nil {
		return "", 0, err
	}
	record, err := Read(store, namespace, reference)
	return record.Summary, record.Bytes, err
}

// Read validates the retained reference within an already admitted namespace.
func Read(store *history.Store, namespace, reference string) (Record, error) {
	if store == nil || namespace == "" || !strings.HasPrefix(reference, Prefix) {
		return Record{}, errors.New("compaction summary is unavailable")
	}
	source, handle, ok := strings.Cut(strings.TrimPrefix(reference, Prefix), ":")
	owner, err := store.HandleOwner(namespace, handle)
	if err != nil || !ok || owner != source {
		return Record{}, errors.New("compaction summary is unavailable in this session")
	}
	data, err := history.ReadRecord(filepath.Join(store.Directory(), Name(reference)))
	if err != nil {
		return Record{Bytes: len(data)}, err
	}
	return Decode(data, reference)
}
