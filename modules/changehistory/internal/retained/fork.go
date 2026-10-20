package retained

import (
	json "encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/yusing/mekugi/libraries/legacycall"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

type forkPlan struct {
	selected changes.Scope
	indexes  []changeIndex
	names    []string
}

func (p *forkPlan) Dependencies() []string { return slices.Clone(p.names) }
func (p *forkPlan) Publish(tx *history.Transaction) error {
	store := selectedStore(p.selected)
	store.transaction = tx
	for _, index := range p.indexes {
		if err := store.writeChangeIndex(index); err != nil {
			return err
		}
	}
	return nil
}
func (*service) PrepareFork(selected changes.Scope, source, dest, fork string) (changes.ForkPlan, error) {
	plan := &forkPlan{selected: selected}
	entries, err := os.ReadDir(selected.Store.Directory())
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !hashedRecordName(entry.Name(), changeIndexPrefix) {
			continue
		}
		data, err := history.ReadRecord(filepath.Join(selected.Store.Directory(), entry.Name()))
		if err != nil {
			return nil, err
		}
		var index changeIndex
		if err := json.Unmarshal(data, &index); err != nil {
			return nil, err
		}
		if index.Namespace != source {
			continue
		}
		if index.Version != 1 || changeIndexName(index.Workspace, source) != entry.Name() || index.Changes == nil {
			return nil, errors.New("invalid cloned change index identity")
		}
		if err := validateChangeIndex(index); err != nil {
			return nil, err
		}
		for _, change := range index.Changes {
			for _, id := range change.Dependencies {
				plan.names = append(plan.names, legacycall.Name(index.Workspace, id, false))
			}
			for _, call := range change.Calls {
				plan.names = append(plan.names, legacycall.Name(index.Workspace, call.ID, false))
			}
		}
		index.Namespace = dest
		for i := range index.Streams {
			if index.Streams[i].Thread != fork {
				continue
			}
			for id, change := range index.Changes {
				stream, _, _ := changes.ParseID(id)
				if stream != index.streamName(i) {
					continue
				}
				for j := range change.Calls {
					if change.Calls[j].Thread == "" {
						change.Calls[j].Thread = fork
					}
				}
				index.Changes[id] = change
			}
			index.Streams[i].Thread = dest
		}
		plan.indexes = append(plan.indexes, index)
	}
	return plan, nil
}
