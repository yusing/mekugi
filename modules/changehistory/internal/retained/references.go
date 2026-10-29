package retained

// Source: internal/router/change_references.go@[1a1981ba] retained change behavior.

import (
	"context"
	"crypto/sha256"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/yusing/mekugi/modules/changehistory/changes"

	"github.com/yusing/mekugi/libraries/legacycall"

	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/reference"
	"github.com/yusing/mekugi/contracts/scope"
)

type changeReadSnapshot struct {
	ResolvedPaths []string          `json:"resolved_paths,omitempty"`
	Workspaces    map[string]string `json:"workspaces,omitempty"`

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

// ChangeReferences resolves the historical retained selection format.
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
			if _, _, err := changes.ParseID(id); err != nil || ids[id] {
				return errors.New("invalid frozen change IDs")
			}
			ids[id] = true
		}
		calls := make(map[string]bool)
		index := changeIndex{Workspace: selection.Workspace, workspaces: selection.Workspaces}
		for id, change := range selection.Selected {
			if !ids[id] || change.Correlation == "" || change.RetiredCalls < 0 {
				return errors.New("invalid frozen change selection")
			}
			for _, call := range change.Calls {
				name := legacycall.Name(index.workspaceFor(id), call.ID, false)
				if call.ID == "" || calls[name] {
					return errors.New("invalid frozen change attempt")
				}
				calls[name] = true
			}
		}
	}
	return nil
}
func changeSelectionStore(selected scope.Retained) *store {
	return &store{directory: selected.Directory, session: scope.Retained{Namespace: selected.Namespace, Thread: selected.Thread}}
}
func resolveChangeSelection(ctx context.Context, view reference.View, data []byte) (command.OmittedOutput, error) {
	var selection changeReadSnapshot
	if err := json.Unmarshal(data, &selection, jsonv1.DefaultOptionsV1()); err != nil {
		return command.OmittedOutput{}, err
	}
	store := changeSelectionStore(view.Scope)
	options := changeReadOptions{workspace: selection.Workspace, ids: selection.IDs, paths: selection.Paths, resolvedPaths: selection.ResolvedPaths, view: selection.View, mine: selection.Mine, overlapLabels: selection.OverlapLabels}
	var text string
	var err error
	if selection.Frozen {
		text, err = store.renderChanges(ctx, options, changeIndex{Workspace: selection.Workspace, workspaces: selection.Workspaces, Changes: selection.Selected, Streams: selection.Streams})
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
		return changeSelectionStore(selected).dependencies(selection.Workspace, selection.IDs, false)
	}
	var names []string
	for _, id := range selection.IDs {
		for _, call := range selection.Selected[id].Calls {
			index := changeIndex{Workspace: selection.Workspace, workspaces: selection.Workspaces}
			names = append(names, legacycall.Name(index.workspaceFor(id), call.ID, false))
		}
	}
	return names, nil
}
