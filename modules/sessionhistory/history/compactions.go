package history

import (
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yusing/mekugi/contracts/observation"
	"github.com/yusing/mekugi/contracts/recordkind"
)

// CompactionObservation retains measurements unavailable in native item history.
// Item identity joins them to the original completed boundary, never a nearby one.
type CompactionObservation struct {
	Thread, Item string
	Context      *observation.Context
	ElapsedNS    *int64
}

func CompactionName(thread, item string) string {
	return fmt.Sprintf("compaction-%x.json", sha256.Sum256([]byte(thread+"\x00"+item)))
}

func (s *Store) ReadCompaction(thread, item string) (*CompactionObservation, int, error) {
	if s == nil || s.directory == "" {
		return nil, 0, nil
	}
	data, err := ReadRecord(filepath.Join(s.directory, CompactionName(thread, item)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, len(data), err
	}
	var r CompactionObservation
	if err := json.Unmarshal(data, &r, json.RejectUnknownMembers(true)); err != nil {
		return nil, len(data), err
	}
	if r.Thread != thread || r.Item != item || item == "" || r.ElapsedNS != nil && *r.ElapsedNS < 0 {
		return nil, len(data), errors.New("compaction observation identity or timing mismatch")
	}
	return &r, len(data), nil
}

var CompactionRecord = recordkind.Spec{ID: "compaction", Match: recordkind.HashedName("compaction-", ".json")}
