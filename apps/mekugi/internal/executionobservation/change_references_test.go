package executionobservation

import (
	"context"
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"fmt"
	"path/filepath"

	"github.com/yusing/mekugi/modules/sessionhistory/history"

	"github.com/yusing/mekugi/modules/changehistory/changes"
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

func putHistoricalChangeRead(s *mekugiReplayStore, ctx context.Context, query changes.Query, text string, offset int) (string, error) {
	index, err := s.changes.Inspect(s.scoped(ctx).changeScope(), query.Workspace)
	if err != nil {
		return "", err
	}
	selected := make(map[string]changes.Change, len(query.IDs))
	for _, id := range query.IDs {
		selected[id] = index.Changes[id]
	}
	data, err := json.Marshal(map[string]any{
		"workspace": query.Workspace, "ids": query.IDs, "paths": query.Paths, "view": query.View,
		"mine": query.Mine, "selected": selected, "streams": index.Streams, "frozen": true,
		"offset": offset, "digest": fmt.Sprintf("%x", sha256.Sum256([]byte(text))),
	}, jsonv1.DefaultOptionsV1())
	if err != nil {
		return "", err
	}
	session, err := s.recoverySession(ctx)
	if err != nil {
		return "", err
	}
	handles, err := session.Reserve(ctx, 1)
	if err != nil {
		return "", err
	}
	return session.PublishSelectionAt(ctx, handles[0], data)
}
