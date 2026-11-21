package executionobservation

import (
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"path/filepath"

	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// Historical fixtures exercise compatibility and retention without exposing a
// writable index or selection codec from the feature's public API.
const changeIndexPrefix = "changes-v3-"

func changeIndexName(workspace, namespace string) string {
	return fmt.Sprintf("%s%x.json", changeIndexPrefix, sha256.Sum256([]byte(namespace+"\x00"+workspace)))
}

type historicalChangeIndex struct {
	Sequence             uint64 `json:",omitzero"`
	Version              int
	Workspace, Namespace string
	Streams              []historicalChangeStream
	Changes              map[string]historicalTrackedChange
}

type historicalChangeStream struct {
	Name, Thread  string
	Next, Retired int
}

type historicalTrackedChange struct {
	Correlation string
	Calls       []struct {
		Sequence   uint64
		Thread, ID string
		Managed    bool
	}
	Dependencies []string
	RetiredCalls int
}

func readChangeIndexFixture(s *mekugiReplayStore, workspace string) (historicalChangeIndex, error) {
	var index historicalChangeIndex
	data, err := history.ReadRecord(filepath.Join(s.directory, changeIndexName(workspace, s.session.Namespace)))
	if err == nil {
		err = json.Unmarshal(data, &index)
	}
	return index, err
}

func writeChangeIndexFixture(s *mekugiReplayStore, index historicalChangeIndex) error {
	data, err := json.Marshal(&index, jsonv1.DefaultOptionsV1())
	if err != nil {
		return err
	}
	return s.transaction.Write(changeIndexName(index.Workspace, index.Namespace), "changes-fixture-", data)
}
