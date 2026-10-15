package router

import (
	"context"
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/reference"
	"github.com/yusing/mekugi/contracts/scope"
)

type changeReadSnapshot struct {
	OverlapLabels map[string]string        `json:"overlap_labels,omitempty"`
	Selected      map[string]trackedChange `json:"selected,omitempty"`
	Streams       []changeStream           `json:"streams,omitempty"`
	Frozen        bool                     `json:"frozen,omitzero"`
	Mine          bool                     `json:"mine,omitzero"`
	Workspace     string                   `json:"workspace"`
	IDs           []string                 `json:"ids"`
	Paths         []string                 `json:"paths"`
	View          string                   `json:"view"`
	Offset        int                      `json:"offset"`
	Digest        string                   `json:"digest"`
}

// ChangeReferences supplies the retained evidence owner's existing selection
// format until changehistory becomes its own composition entry.
func ChangeReferences() []reference.Resolver {
	return []reference.Resolver{{
		Field: "changes", Canonicalize: canonicalChangeSelection, Resolve: resolveChangeSelection, Dependencies: changeSelectionDependencies,
	}}
}

func canonicalChangeSelection(data []byte, strict bool) ([]byte, error) {
	var selection changeReadSnapshot
	if err := json.Unmarshal(data, &selection, jsonv1.DefaultOptionsV1(), json.RejectUnknownMembers(strict)); err != nil {
		return nil, err
	}
	if err := validateChangeSelection(&selection); err != nil {
		return nil, err
	}
	return json.Marshal(&selection, jsonv1.DefaultOptionsV1())
}
func validateChangeSelection(selection *changeReadSnapshot) error {
	if selection.Offset < 0 || len(selection.Digest) != 64 || (!selection.Frozen && len(selection.IDs) == 0) ||
		(selection.View != "" && selection.View != "summary" && selection.View != "history" && selection.View != "list" && selection.View != "net") {
		return errors.New("invalid change read selection")
	}
	if selection.Frozen {
		ids := make(map[string]bool, len(selection.IDs))
		for _, id := range selection.IDs {
			if _, _, err := parseChangeID(id); err != nil || ids[id] {
				return errors.New("invalid frozen change IDs")
			}
			ids[id] = true
		}
		calls := make(map[string]bool)
		for id, change := range selection.Selected {
			if !ids[id] || change.Correlation == "" || change.RetiredCalls < 0 {
				return errors.New("invalid frozen change selection")
			}
			for _, call := range change.Calls {
				if call.ID == "" || calls[call.ID] {
					return errors.New("invalid frozen change attempt")
				}
				calls[call.ID] = true
			}
		}
	}
	return nil
}
func changeSelectionStore(selected scope.Retained) *mekugiReplayStore {
	return &mekugiReplayStore{directory: selected.Directory, session: storageSessionIdentity{Namespace: selected.Namespace, Thread: selected.Thread}}
}
func resolveChangeSelection(ctx context.Context, view reference.View, data []byte) (command.OmittedOutput, error) {
	var selection changeReadSnapshot
	if err := json.Unmarshal(data, &selection, jsonv1.DefaultOptionsV1()); err != nil {
		return command.OmittedOutput{}, err
	}
	store := changeSelectionStore(view.Scope)
	options := changeReadOptions{workspace: selection.Workspace, ids: selection.IDs, paths: selection.Paths, view: selection.View, mine: selection.Mine, overlapLabels: selection.OverlapLabels}
	var text string
	var err error
	if selection.Frozen {
		text, err = store.renderChanges(ctx, options, changeIndex{Workspace: selection.Workspace, Changes: selection.Selected, Streams: selection.Streams})
	} else {
		text, _, err = store.readChangeViewLocked(ctx, options, view.Retain)
	}
	if err != nil {
		return command.OmittedOutput{}, err
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(text))) != selection.Digest || selection.Offset < 0 || selection.Offset >= len(text) || !utf8.RuneStart(text[selection.Offset]) {
		return command.OmittedOutput{}, errors.New("change snapshot changed; repeat the original mchanges read")
	}
	output := command.OmittedOutput{Stdout: text[selection.Offset:]}
	if selection.Frozen {
		output.StdoutKind = "rows"
	}
	return output, nil
}
func changeSelectionDependencies(selected scope.Retained, data []byte) ([]string, error) {
	var selection changeReadSnapshot
	if err := json.Unmarshal(data, &selection, jsonv1.DefaultOptionsV1()); err != nil {
		return nil, err
	}
	if !selection.Frozen {
		return changeSelectionStore(selected).changeDependencyNames(selection.Workspace, selection.IDs)
	}
	var names []string
	for _, id := range selection.IDs {
		for _, call := range selection.Selected[id].Calls {
			names = append(names, legacycall.Name(selection.Workspace, call.ID, false))
		}
	}
	return names, nil
}
