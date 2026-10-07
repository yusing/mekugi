package router

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
)

// Official native forks do not copy child transcripts. Retain only the SDK's
// selected transcript identities, never messages or executable child bindings.
type nativeHistoryChild struct {
	SessionID  string   `json:"sessionID"`
	AgentID    string   `json:"agentID"`
	MessageIDs []string `json:"messageIDs"`
}

type nativeHistorySelection struct {
	Source   ObservationBinding
	Children []nativeHistoryChild
}

func validNativeHistoryBinding(b ObservationBinding) bool {
	return b.Runtime == "claude" && b.Session != "" && len(b.Session) <= 1024 && filepath.IsAbs(b.Workspace) && b.Agent == "" && b.Branch == ""
}

func validNativeHistorySelection(target ObservationBinding, selection *nativeHistorySelection) bool {
	if selection == nil || !validNativeHistoryBinding(target) || !validNativeHistoryBinding(selection.Source) || target.Session == selection.Source.Session || target.Workspace != selection.Source.Workspace || len(selection.Children) > 128 {
		return false
	}
	count := 0
	for _, child := range selection.Children {
		if child.SessionID == "" || child.SessionID == target.Session || len(child.SessionID) > 1024 || child.AgentID == "" || len(child.AgentID) > 1024 {
			return false
		}
		count += len(child.MessageIDs)
		for _, id := range child.MessageIDs {
			if id == "" || len(id) > 128 {
				return false
			}
		}
	}
	return count <= 2000
}

func (o *nativeObservationOwner) retainForkHistory(ctx context.Context, target, source ObservationBinding, children []nativeHistoryChild) error {
	selection := &nativeHistorySelection{Source: source, Children: children}
	if !validNativeHistorySelection(target, selection) {
		return errors.New("invalid native fork history selection")
	}
	o.mu.Lock()
	bound, ok := o.bindings[target]
	o.mu.Unlock()
	if !ok {
		return errors.New("native fork session is not bound")
	}
	ctx = context.WithValue(ctx, storageSessionKey{}, bound.Value(storageSessionKey{}))
	key := observationThread(target) + "/history-selection"
	prior, found, err := o.store.lookup(ctx, target.Workspace, key)
	if err != nil {
		return err
	}
	if found {
		if !reflect.DeepEqual(prior.NativeHistory, selection) {
			return errors.New("native fork history selection changed")
		}
		return nil
	}
	return o.store.put(ctx, target.Workspace, map[string]mekugiHistory{key: {
		Root: target.Workspace, ExecutingThread: observationThread(target), NativeHistory: selection,
	}})
}

// This read is used during SDK-verified resume preflight, before changing the
// live observation binding. It does not grant or create child caller authority.
func (o *nativeObservationOwner) readForkHistory(ctx context.Context, target ObservationBinding) ([]nativeHistoryChild, error) {
	if !validNativeHistoryBinding(target) || target.Runtime != o.runtime {
		return nil, errors.New("invalid native history selection scope")
	}
	history, found, err := o.store.lookup(ctx, target.Workspace, observationThread(target)+"/history-selection")
	if err != nil || !found || history.NativeHistory == nil {
		return nil, err
	}
	if history.Root != target.Workspace || history.ExecutingThread != observationThread(target) || !validNativeHistorySelection(target, history.NativeHistory) {
		return nil, errors.New("native history selection scope changed")
	}
	return history.NativeHistory.Children, nil
}
