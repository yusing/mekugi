package executionobservation

import (
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/yusing/mekugi/backends/codex/binding"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	activity "github.com/yusing/mekugi/modules/activity/presentation"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
	"github.com/yusing/mekugi/modules/sessionhistory/history"
)

// StoreCompact publishes the exact model answer and separate reload selection before the
// native compaction reference can enter host history. It owns no second store.
func (p *Preparer) StoreCompact(ctx context.Context, thread, summary, reload string) (string, error) {
	if p == nil || p.retained.Store == nil || thread == "" || strings.TrimSpace(summary) == "" {
		return "", errors.New("compaction publication requires a retained thread and summary")
	}
	_, err := compact.ReadCommands(reload)
	if err != nil {
		return "", err
	}
	store := p.retained
	namespace, release, err := store.Store.BeginSession(ctx, store.Writes, store.Limits, thread)
	if err != nil {
		return "", err
	}
	defer release()
	var reference string
	err = store.Store.Transaction(ctx, store.Writes, func(tx *history.Transaction) error {
		handles, err := tx.ReserveHandles(namespace, 1)
		if err != nil {
			return err
		}
		reference = compact.Prefix + namespace + ":" + handles[0]
		data, err := json.Marshal(compact.Record{Version: 1, Reference: reference, Summary: summary, Selection: reload})
		if err != nil {
			return err
		}
		name := compact.Name(reference)
		if err := tx.Admit(store.Limits, name, int64(len(data))); err != nil {
			return err
		}
		if err := tx.Write(name, "compact-pending-", data); err != nil {
			return err
		}
		return tx.Retain(thread, store.Limits, name)
	})
	if err != nil {
		return "", err
	}
	return reference, nil
}

// Read under reconcileVisibleInput's snapshot lease. Prospective inheritance
// uses authenticated host ancestry only until its frozen scope is published.
func (p *Preparer) readCompact(ctx context.Context, reference string) (compact.Record, error) {
	identity, _ := ctx.Value(storageSessionKey{}).(storageSessionIdentity)
	namespace := identity.Namespace
	store := p.retained.Store
	if store == nil || identity.Thread == "" {
		return compact.Record{}, errors.New("compaction recovery has no admitted thread")
	}
	_, exists, err := store.ReadScope(identity.Thread)
	if err != nil {
		return compact.Record{}, err
	}
	if !exists && identity.Fork != "" {
		namespace, err = store.NamespaceForThread(identity.Fork)
		if err != nil {
			return compact.Record{}, err
		}
	}
	source, handle, ok := strings.Cut(strings.TrimPrefix(reference, compact.Prefix), ":")
	owner, err := store.HandleOwner(namespace, handle)
	if err != nil || !ok || owner != source {
		return compact.Record{}, errors.New("compaction reference is unavailable in this session")
	}
	data, err := history.ReadRecord(filepath.Join(store.Directory(), compact.Name(reference)))
	if err != nil {
		return compact.Record{}, err
	}
	return compact.Decode(data, reference)
}

// Prewarm projects immutable history without publishing a scope or running readers.
func (p *Preparer) prepareCompactInput(ctx context.Context, request *codexresponses.Request, session, thread string, metadata codexresponses.TurnMetadata, prewarm bool) (jsontext.Value, map[string]bool, error) {
	if p == nil || p.retained.Store == nil || thread == "" || metadata.ActivityIdentityInvalid || metadata.ThreadID != "" && metadata.ThreadID != thread {
		return nil, nil, errors.New("compaction recovery identity or storage is unavailable")
	}
	namespace, err := p.retained.Store.NamespaceForThread(thread)
	if err != nil {
		return nil, nil, err
	}
	ctx = context.WithValue(ctx, storageSessionKey{}, storageSessionIdentity{Thread: thread, Namespace: namespace})
	ctx, err = prepareHandleScope(ctx, p.retained.Store, p.retained.Writes, metadata)
	if err != nil {
		return nil, nil, err
	}
	release, err := p.retained.Store.SnapshotLease(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &items); err != nil {
		return nil, nil, err
	}
	var pending jsontext.Value
	readCalls := make(map[string]bool)
	for _, item := range items {
		kind, reference := responses.StringField(item, "type"), responses.StringField(item, "encrypted_content")
		if (kind != "compaction" && kind != "compaction_summary") || !strings.HasPrefix(reference, compact.Prefix) {
			continue
		}
		record, err := p.readCompact(ctx, reference)
		if err != nil {
			return nil, nil, err
		}
		call, calls, callErr := compactReaderCall(record, items, thread)
		for _, id := range calls {
			readCalls[id] = true
		}
		if !prewarm && metadata.RequestKind == responses.Turn && record.Selection != "" {
			sources, _ := compact.Sources(record.Selection)
			command := compact.CaptureCommand(record.Selection)
			var reloads []activity.ReloadSource
			for _, source := range sources {
				reloads = append(reloads, activity.ReloadSource{Command: command, Path: source.Path, Skill: source.Skill})
			}
			turn := metadata.TurnID
			if callErr == nil && len(call) == 0 {
				turn = ""
			}
			p.activity.Collect(activity.Supplement{Thread: thread, Turn: turn, Source: record.Reference, Kind: "compact_reload", Text: "Compaction reload", ReloadSources: reloads})
		}
		if prewarm {
			for key := range item {
				delete(item, key)
			}
			item["type"], item["role"] = responses.MustMarshalJSON("message"), responses.MustMarshalJSON("assistant")
			item["content"] = responses.MustMarshalJSON([]map[string]string{{"type": "output_text", "text": record.Summary}})
		} else if record.Selection != "" && metadata.RequestKind == responses.Turn && !codexresponses.ExecutionFree(request) {
			if callErr != nil {
				return nil, nil, callErr
			}
			if len(call) > 0 {
				pending = call
			}
		}
	}
	if prewarm {
		request.Fields["input"], err = json.Marshal(items)
		return nil, readCalls, err
	}
	release()
	ctx, release, err = beginRetainedSession(ctx, p.retained.Store, p.retained.Writes, p.retained.Limits, thread, session)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	ctx, err = prepareHandleScope(ctx, p.retained.Store, p.retained.Writes, metadata)
	if err != nil {
		return nil, nil, err
	}
	workspace, valid := usableRoutingDirectory(metadata.Directories)
	if len(metadata.Directories) != 0 && !valid {
		return nil, nil, errors.New("invalid compaction recovery workspace")
	}
	_, err = p.reconcileVisibleInput(ctx, request, workspace, workspace+"\x00"+thread)
	return pending, readCalls, err
}

// Only the provider view changes. Host history and observation retain the exact
// orchestration calls, continuations and results before this projection.
func compactReaderInput(items []map[string]jsontext.Value, calls map[string]bool) (jsontext.Value, error) {
	visible := make([]map[string]jsontext.Value, 0, len(items))
	introduced := false
	for _, item := range items {
		kind := responses.StringField(item, "type")
		if !calls[responses.StringField(item, "call_id")] {
			visible = append(visible, item)
			continue
		}
		if kind == "custom_tool_call" || kind == "function_call" {
			continue
		}
		if kind != "custom_tool_call_output" && kind != "function_call_output" {
			visible = append(visible, item)
			continue
		}
		var content []jsontext.Value
		var text string
		if json.Unmarshal(item["output"], &text) == nil {
			content = []jsontext.Value{responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": text})}
		} else if err := json.Unmarshal(item["output"], &content); err != nil {
			return nil, fmt.Errorf("decode compaction reader output: %w", err)
		}
		for _, part := range content {
			var parts []jsontext.Value
			if !introduced {
				parts = append(parts, responses.MustMarshalJSON(map[string]string{"type": "input_text", "text": "Reloaded context (tool output). These file and skill bodies are already read context. Reuse supplied content; read again only for missing or changed content. Failed reads are not complete context."}))
				introduced = true
			}
			parts = append(parts, part)
			visible = append(visible, map[string]jsontext.Value{"type": responses.MustMarshalJSON("message"), "role": responses.MustMarshalJSON("user"), "content": responses.MustMarshalJSON(parts)})
		}
	}
	return json.Marshal(visible)
}

// The stock interpreter and nested readers keep the actual thread's permissions,
// approvals and continuations. Completed results stay in native history.
func compactReaderCall(record compact.Record, items []map[string]jsontext.Value, thread string) (jsontext.Value, []string, error) {
	prefix := strings.TrimSuffix(compact.Name(record.Reference), ".json")[:40] + "-"
	sources, err := compact.Sources(record.Selection)
	if err != nil || len(sources) == 0 {
		return nil, nil, err
	}
	reader := compact.CaptureCommand(record.Selection)
	threadHash := sha256.Sum256([]byte(thread))
	var calls []string
	ownID := fmt.Sprintf("%s%x", prefix, threadHash[:8])
	id := ownID
	for _, item := range items {
		if responses.StringField(item, "type") == "custom_tool_call" && codexresponses.MatchesCompactReadProgram(responses.StringField(item, "input"), reader) && strings.HasPrefix(responses.StringField(item, "call_id"), prefix) {
			id = responses.StringField(item, "call_id")
			break
		}
	}
	inherited := id != ownID
	call := map[string]any{"type": "custom_tool_call", "id": id, "call_id": id, "name": "exec", "input": codexresponses.CompactReadProgram(reader)}
	seen := false
	for _, item := range items {
		if responses.StringField(item, "call_id") != call["call_id"] {
			continue
		}
		kind := responses.StringField(item, "type")
		if kind == "custom_tool_call" || kind == "function_call" {
			valid := kind == call["type"] && responses.StringField(item, "name") == call["name"]
			if kind == "custom_tool_call" {
				valid = valid && codexresponses.MatchesCompactReadProgram(responses.StringField(item, "input"), reader)
			} else {
				var args map[string]any
				valid = valid && json.Unmarshal([]byte(responses.StringField(item, "arguments")), &args) == nil && args["cell_id"] == call["cell_id"] && args["terminate"] != true
			}
			if seen || !valid {
				return nil, calls, errors.New("compaction reader identity changed")
			}
			seen = true
			calls = append(calls, responses.StringField(item, "call_id"))
		}
		if kind == "custom_tool_call_output" || kind == "function_call_output" {
			if !seen {
				return nil, calls, errors.New("compaction reader result has no matching call")
			}
			result := binding.DecodeExecutionResult("exec", item["output"])
			if result.ResumeHandle == "" {
				return nil, calls, nil
			}
			cell, ok := strings.CutPrefix(result.ResumeHandle, "cell:")
			if !ok {
				return nil, calls, errors.New("invalid compaction continuation")
			}
			waitID := fmt.Sprintf("%s-w%d", id, len(calls))
			args, _ := json.Marshal(map[string]any{"cell_id": cell, "yield_time_ms": 10000})
			call = map[string]any{"type": "function_call", "id": waitID, "call_id": waitID, "name": "wait", "arguments": string(args), "cell_id": cell}
			seen = false
		}
	}
	if seen {
		return nil, calls, errors.New("compaction reader outcome is unavailable; reads were not repeated")
	}
	if inherited {
		return nil, calls, errors.New("compaction read is still pending in its originating thread")
	}
	delete(call, "cell_id")
	return responses.MustMarshalJSON(call), calls, nil
}

// Hydrate only authenticated completed reload results. Original host history and
// its short retained reference remain untouched for observation and replay.
func (p *Preparer) compactSnapshots(ctx context.Context, input jsontext.Value, calls map[string]bool, thread string, metadata codexresponses.TurnMetadata, prewarm bool) (map[string]jsontext.Value, error) {
	outputs := make(map[string]jsontext.Value)
	if len(calls) == 0 {
		return outputs, nil
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, err
	}
	namespace, err := p.retained.Store.NamespaceForThread(thread)
	if err != nil {
		return nil, err
	}
	if _, exists, err := p.retained.Store.ReadScope(thread); err != nil {
		return nil, err
	} else if !exists && metadata.ForkedFromThreadID != "" {
		namespace, err = p.retained.Store.NamespaceForThread(metadata.ForkedFromThreadID)
		if err != nil {
			return nil, err
		}
	}
	release, err := p.retained.Store.SnapshotLease(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	for _, item := range items {
		id := responses.StringField(item, "call_id")
		if !calls[id] || !strings.HasSuffix(responses.StringField(item, "type"), "_output") {
			continue
		}
		result := binding.DecodeExecutionResult("exec", item["output"])
		if result.ResumeHandle != "" {
			outputs[id] = responses.MustMarshalJSON([]any{})
			continue
		}
		var reference string
		for line := range strings.SplitSeq(result.Text, "\n") {
			if ref, ok := strings.CutPrefix(line, compact.SnapshotPrefix); ok {
				if reference != "" {
					return nil, errors.New("multiple compaction snapshot references")
				}
				reference = strings.TrimSpace(ref)
			}
		}
		if reference == "" {
			continue
		} // Denial, cancellation, or failed publication.
		if p.recovered == nil {
			return nil, errors.New("compaction snapshot recovery is unavailable")
		}
		store, err := p.recovered.AttachStore(p.retained.Store, namespace, thread, recovery.DefaultLimits(), nil)
		if err != nil {
			return nil, err
		}
		snapshot, err := store.Snapshot(reference)
		if err != nil {
			return nil, err
		}
		var captured compact.Snapshot
		if err := json.Unmarshal([]byte(snapshot.Stdout), &captured); err != nil {
			return nil, err
		}
		selected, err := compact.Sources(captured.Selection)
		if err != nil {
			return nil, err
		}
		if len(selected) != len(captured.Sources) {
			return nil, errors.New("reload snapshot source count mismatch")
		}
		command := compact.CaptureCommand(captured.Selection)
		var reloads []activity.ReloadSource
		var parts []map[string]string
		for i, source := range captured.Sources {
			reloads = append(reloads, activity.ReloadSource{Command: command, Path: selected[i].Path, Skill: selected[i].Skill, ExitCode: new(source.ExitCode)})
			body := fmt.Sprintf("%s: exit_code=%d\n%s", source.Command, source.ExitCode, source.Stdout)
			if source.Stderr != "" {
				body += "\n" + source.Stderr
			}
			parts = append(parts, map[string]string{"type": "input_text", "text": body})
		}
		outputs[id] = responses.MustMarshalJSON(parts)
		if !prewarm && metadata.RequestKind == responses.Turn {
			p.activity.Collect(activity.Supplement{Thread: thread, Source: "reload-result:" + id + ":" + reference, Kind: "compact_reload", Text: "Compaction reload", ReloadSources: reloads})
		}
	}
	return outputs, nil
}

func hydrateCompactSnapshots(input jsontext.Value, outputs map[string]jsontext.Value) (jsontext.Value, error) {
	if len(outputs) == 0 {
		return input, nil
	}
	var items []map[string]jsontext.Value
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		if output, ok := outputs[responses.StringField(item, "call_id")]; ok && strings.HasSuffix(responses.StringField(item, "type"), "_output") {
			item["output"] = output
		}
	}
	return json.Marshal(items)
}

func (t *mekugiResponseTransform) CompactReload() jsontext.Value { return t.compactReload }
