package executionobservation

import (
	"context"
	json "encoding/json/v2"
	"errors"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"

	"github.com/yusing/mekugi/libraries/legacycall"
	changecapture "github.com/yusing/mekugi/modules/changehistory/capture"
	"github.com/yusing/mekugi/modules/changehistory/changes"
	"github.com/yusing/mekugi/modules/changehistory/evidence"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

func captureSource(selected changes.Scope) *evidence.Source {
	if selected.Store == nil {
		return nil
	}
	return &evidence.Source{Store: selected.Store, Thread: selected.Thread, Limits: selected.Limits, Writes: selected.Writes}
}

// Historical inventory diagnostics remain facts when a new interval is cut
// from an old observation. They do not become live capture state again.
type observationMetadata struct {
	changecapture.Observation
	Inventory *legacycall.Inventory `json:",omitempty"`
}

type baselineMetadata struct {
	resolvedStockBaseline
	Inventory *legacycall.Inventory `json:",omitempty"`
}

func decodeObservation(selected changes.Scope, record history.HostRecord) (mekugiHistory, error) {
	value := legacyHistory(record.History)
	root, err := evidence.Root(record)
	if err != nil || root == "" {
		return value, err
	}
	snapshot, err := evidence.Read(captureSource(selected), root)
	if err != nil {
		return value, err
	}
	value.sourceAnchor = snapshot.SourceAnchor
	if len(snapshot.Observation) > 0 {
		var metadata observationMetadata
		if err := json.Unmarshal(snapshot.Observation, &metadata, json.RejectUnknownMembers(true)); err != nil {
			return value, err
		}
		value.ExecObservation = &metadata.Observation
		if metadata.Inventory != nil {
			if value.legacy == nil {
				value.legacy = new(legacycall.History)
			}
			value.legacy.ExecObservation = &legacycall.Observation{Inventory: metadata.Inventory}
		}
		if len(value.ExecObservation.Files) != 0 {
			return value, errors.New("inline files in capture metadata")
		}
		value.ExecObservation.Files = snapshot.Files
	} else if len(snapshot.Files) > 0 {
		return value, errors.New("endpoint evidence without observation")
	}
	if len(snapshot.Baseline) > 0 {
		var metadata baselineMetadata
		if err := json.Unmarshal(snapshot.Baseline, &metadata, json.RejectUnknownMembers(true)); err != nil {
			return value, err
		}
		value.ResolvedBaseline = &metadata.resolvedStockBaseline
		if metadata.Inventory != nil {
			if value.legacy == nil {
				value.legacy = new(legacycall.History)
			}
			value.legacy.ResolvedBaseline = &legacycall.Baseline{Inventory: metadata.Inventory}
		}
		if len(value.ResolvedBaseline.Files) != 0 {
			return value, errors.New("inline files in baseline metadata")
		}
		value.ResolvedBaseline.Files = snapshot.BaselineFiles
	} else if len(snapshot.BaselineFiles) > 0 {
		return value, errors.New("endpoint evidence without baseline")
	}
	for _, patch := range snapshot.Patches {
		value.NativePatches = append(value.NativePatches, changecapture.PatchObservation{Input: patch.Input, Files: patch.Files})
	}
	value.reviewFiles = snapshot.Reviews
	return value, nil
}

func lookupObservation(ctx context.Context, selected changes.Scope, workspace, call string) (mekugiHistory, bool, error) {
	var value mekugiHistory
	var found bool
	if selected.Store == nil {
		return value, false, nil
	}
	err := selected.Store.Transaction(ctx, nil, func(*history.Transaction) error {
		record, exists, err := selected.Store.ReadAttachedHostCall(workspace, call, false)
		if err != nil || !exists {
			return err
		}
		value, err = decodeObservation(selected, record)
		found = err == nil
		return err
	})
	return value, found, err
}

func publishCapturedObservation(tx *history.Transaction, selected changes.Scope, workspace, call string, value mekugiHistory) error {
	previous, exists, err := selected.Store.ReadAttachedHostCall(workspace, call, false)
	if err != nil {
		return err
	}
	if exists && previous.Version != 3 || value.ExecObservation == nil && value.ResolvedBaseline == nil && len(value.NativePatches) == 0 && len(value.reviews()) == 0 {
		return publishObservation(tx, selected, legacycall.Record{Version: 1, Workspace: workspace, CallID: call, History: retainedHistory(value)})
	}
	snapshot := evidence.Snapshot{Reviews: value.reviews(), SourceAnchor: value.sourceAnchor}
	if value.ExecObservation != nil {
		metadata := observationMetadata{Observation: *value.ExecObservation}
		if value.legacy != nil && value.legacy.ExecObservation != nil {
			metadata.Inventory = value.legacy.ExecObservation.Inventory
		}
		metadata.Files = nil
		snapshot.Observation, err = json.Marshal(&metadata, json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true), json.Deterministic(true))
		if err != nil {
			return err
		}
		snapshot.Files = value.ExecObservation.Files
	}
	if value.ResolvedBaseline != nil {
		metadata := baselineMetadata{resolvedStockBaseline: *value.ResolvedBaseline}
		if value.legacy != nil && value.legacy.ResolvedBaseline != nil {
			metadata.Inventory = value.legacy.ResolvedBaseline.Inventory
		}
		metadata.Files = nil
		snapshot.Baseline, err = json.Marshal(&metadata, json.FormatNilSliceAsNull(true), json.FormatNilMapAsNull(true), json.Deterministic(true))
		if err != nil {
			return err
		}
		snapshot.BaselineFiles = value.ResolvedBaseline.Files
	}
	for _, patch := range value.NativePatches {
		snapshot.Patches = append(snapshot.Patches, evidence.Patch{Input: patch.Input, Files: patch.Files})
	}
	root, err := evidence.Write(tx, captureSource(selected), snapshot)
	if err != nil {
		return requestexecution.StoragePublicationError(err)
	}
	ordered := value.ChangeID != "" && len(snapshot.Reviews) > 0 || value.ExecOutcome != nil && value.ExecOutcome.Status == changecapture.StatusFailed && value.ExecOutcome.SharedWith == ""
	value.ExecObservation, value.ResolvedBaseline, value.NativePatches, value.ReviewFiles, value.reviewFiles = nil, nil, nil, nil, nil
	record := history.HostRecord{Record: legacycall.Record{Version: 3, Workspace: workspace, CallID: call, History: retainedHistory(value)}, Attachments: []string{root}}
	return requestexecution.StoragePublicationError(tx.PublishAttachedHostCall(selected.Thread, selected.Limits, record, ordered))
}
