package router

import (
	"context"
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"errors"
	"maps"
	"path/filepath"
	"strings"
)

// Provider ciphertext is opaque. Retired local context references require an
// unavailable local interpreter and must not be forwarded as provider state.
func validateProviderContext(input jsonv1.RawMessage) error {
	var items []map[string]jsonv1.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return nil
	}
	for _, item := range items {
		kind := jsonString(item, "type")
		if (kind == "compaction" || kind == "compaction_summary") && strings.HasPrefix(jsonString(item, "encrypted_content"), "mekugi:") {
			return incompatibleRequest("unsupported_context_reference", "This session contains an unsupported local context reference. Start a new session.")
		}
	}
	return nil
}

// Compaction can carry the first completed host result for an observed edit.
// Retain that evidence without projecting changes into the provider request.
func (p *mekugiProxy) observeCompactionInput(ctx context.Context, request *parsedResponsesRequest, session, thread string, metadata codexTurnMetadata) error {
	if p == nil || p.replayStore == nil || thread == "" {
		return nil
	}
	ctx, release, err := p.replayStore.beginSession(ctx, thread, session)
	if err != nil {
		return err
	}
	defer release()
	workspace, valid := usableRoutingDirectory(metadata.Directories)
	if len(metadata.Directories) != 0 && !valid {
		return errors.New("compaction observation requires one valid workspace")
	}
	if workspace == "" {
		err = p.replayStore.locked(ctx, func() error {
			workspace, err = p.replayStore.retainedCallWorkspace(thread)
			return err
		})
		if err != nil {
			return err
		}
	}
	ctx, err = p.replayStore.prepareHandleScope(ctx, metadata)
	if err != nil {
		return err
	}
	observed := *request
	observed.fields = maps.Clone(request.fields)
	_, err = p.reconcileVisibleInput(ctx, &observed, workspace, workspace+"\x00"+thread)
	return err
}

// The selected thread's retained call catalog supplies observation scope only.
// It cannot supply an execution directory or authorize an unrelated thread.
func (s *mekugiReplayStore) retainedCallWorkspace(thread string) (string, error) {
	session, err := s.readRetainedSession(storageSessionName(thread))
	if err != nil {
		return "", err
	}
	workspace := ""
	for name := range session.Files {
		if !strings.HasPrefix(name, "call-") {
			continue
		}
		data, err := readManagedOutputFile(filepath.Join(s.directory, name))
		if err != nil {
			return "", err
		}
		var record replayRecord
		// Persisted command hashes use the version-1 numeric byte-array format.
		if json.Unmarshal(data, &record, jsonv1.FormatByteArrayAsArray(true)) != nil ||
			(record.Version != 1 && record.Version != 2) || record.Commentary || replayRecordName(record.Workspace, record.CallID, false) != name {
			return "", errors.New("invalid retained call workspace evidence")
		}
		if record.History.ExecutingThread != thread {
			continue
		}
		candidate := record.Workspace
		if !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate {
			return "", errors.New("invalid retained call workspace")
		}
		if workspace != "" && workspace != candidate {
			return "", errors.New("retained call workspace is ambiguous")
		}
		workspace = candidate
	}
	if workspace == "" {
		return "", errors.New("no retained call workspace")
	}
	return workspace, nil
}
