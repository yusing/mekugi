package retained

import (
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/contracts/recordkind"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func RecordKinds() []recordkind.Spec {
	return []recordkind.Spec{{ID: "changes", Match: matchChangeIndex, Keep: true, MaxRecordBytes: history.MaxRecordBytes, Retirement: func(ctx context.Context, record recordkind.Record) (recordkind.RetirementPlanner, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var index changeIndex
		if err := json.Unmarshal(record.Data, &index, jsonv1.DefaultOptionsV1()); err != nil {
			return nil, err
		}
		if changeIndexName(index.Workspace, index.Namespace) != record.Name {
			return nil, errors.New("invalid background change index identity")
		}
		if err := validateChangeIndex(index); err != nil {
			return nil, err
		}
		return changeRetirementPlanner(index), nil
	}}}
}

var matchChangeIndex = recordkind.HashedName(changeIndexPrefix, ".json")
