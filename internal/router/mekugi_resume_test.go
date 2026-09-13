package router

import (
	"encoding/json"
	"github.com/yusing/mekugi"
	"strconv"
	"testing"
)

func TestReplayVisibleViewSurvivesRestartAndFork(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	histories := map[string]mekugiHistory{}
	for _, id := range []string{"a", "b"} {
		histories[id] = mekugiHistory{ToolName: mekugiToolName, Root: workspace, Script: "original-" + id, CarrierName: "exec", CarrierKind: codeModeCarrierCustom, CarrierPayload: "translated-" + id, UpstreamItem: map[string]json.RawMessage{"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(mekugiToolName), "call_id": mustMarshalJSON(id), "input": mustMarshalJSON("original-" + id)}}
	}
	if err := store.put(t.Context(), workspace, histories); err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{replayStore: store}
	request := func(ids ...string) *parsedResponsesRequest {
		items := []map[string]json.RawMessage{}
		for _, id := range ids {
			items = append(items, map[string]json.RawMessage{"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON("exec"), "call_id": mustMarshalJSON(id), "input": mustMarshalJSON("translated-" + id)})
		}
		return &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON(items)}}
	}
	parent, err := proxy.reconcileVisibleInput(t.Context(), request("a", "b"), workspace, "parent")
	if err != nil {
		t.Fatal(err)
	}
	forkRequest := request("a")
	fork, err := proxy.reconcileVisibleInput(t.Context(), forkRequest, workspace, "different-route")
	if err != nil {
		t.Fatal(err)
	}
	if len(parent) != 2 || len(fork) != 1 || fork["a"].sequence != 1 {
		t.Fatalf("views parent=%v fork=%v", parent, fork)
	}
	if _, ok := fork["b"]; ok {
		t.Fatal("fork inherited invisible call")
	}
	other, err := proxy.reconcileVisibleInput(t.Context(), request("a"), t.TempDir(), "parent")
	if err != nil || len(other) != 0 {
		t.Fatalf("workspace isolation: %v %v", other, err)
	}
	bad := request("a", "b")
	var items []map[string]json.RawMessage
	json.Unmarshal(bad.fields["input"], &items)
	items[1]["input"] = mustMarshalJSON("tampered")
	bad.fields["input"] = mustMarshalJSON(items)
	before := string(bad.fields["input"])
	if _, err := proxy.reconcileVisibleInput(t.Context(), bad, workspace, "parent"); err == nil {
		t.Fatal("accepted changed carrier")
	}
	if string(bad.fields["input"]) != before {
		t.Fatal("failed reconciliation changed input")
	}
	if len(parent) != 2 {
		t.Fatal("failed reconciliation changed another view")
	}
}

func TestReplayRecoveryAndAliasesAreRequestLocal(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	alias := mekugi.TargetAlias{Path: "file", Before: "1:1111", After: "1:2222"}
	histories := map[string]mekugiHistory{
		"old": {ToolName: mekugiToolName, Root: workspace, Script: "old", CarrierName: "exec", CarrierPayload: "old carrier", CarrierKind: codeModeCarrierCustom, TranslationError: "rejected", EvaluatorRejected: true, CorrelationID: "attempt", Attempt: 1},
		"new": {ToolName: mekugiToolName, Root: workspace, Script: "new", CarrierName: "exec", CarrierPayload: "new carrier", CarrierKind: codeModeCarrierCustom, Report: "applied", Aliases: []mekugi.TargetAlias{alias}},
	}
	if err := store.put(t.Context(), workspace, histories); err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{replayStore: store}
	view := func(items ...any) *mekugiResponseTransform {
		t.Helper()
		request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON(items)}}
		visible, err := proxy.reconcileVisibleInput(t.Context(), request, workspace, "reused-route")
		if err != nil {
			t.Fatal(err)
		}
		return &mekugiResponseTransform{proxy: proxy, directory: workspace, visible: visible}
	}
	output := func(id, text string) any {
		return map[string]any{"type": "custom_tool_call_output", "call_id": id, "output": text}
	}
	parent := view(output("old", "failed"), output("new", "applied"))
	fork := view(output("old", "failed"))
	failed := view(output("new", "failed"))
	empty := view(map[string]any{"type": "message", "role": "user", "content": "edited"})
	if _, err := parent.recoveryHistory(); err == nil {
		t.Fatal("parent recovered older failure after success")
	}
	if recovered, err := fork.recoveryHistory(); err != nil || recovered.Script != "old" {
		t.Fatalf("fork recovery: %+v %v", recovered, err)
	}
	if _, err := empty.recoveryHistory(); err == nil {
		t.Fatal("empty view recovered invisible call")
	}
	if len(parent.targetAliases()) != 1 || len(failed.targetAliases()) != 0 || len(fork.targetAliases()) != 0 {
		t.Fatal("confirmation leaked between views")
	}
	if err := store.putCommentary(t.Context(), workspace, []string{"msg_mekugi_commentary_known"}); err != nil {
		t.Fatal(err)
	}
	request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON([]any{assistantCommentaryMessage("msg_mekugi_commentary_known", "known"), assistantCommentaryMessage("msg_mekugi_commentary_unknown", "unknown")})}}
	if _, err := proxy.reconcileVisibleInput(t.Context(), request, workspace, "fork"); err != nil {
		t.Fatal(err)
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || jsonString(messages[0], "id") != "msg_mekugi_commentary_unknown" {
		t.Fatalf("commentary provenance = %s", request.fields["input"])
	}
}

func TestReplayNoWorkspaceForkAndLateValidation(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	history := mekugiHistory{ToolName: mekugiToolName, CarrierName: "exec", CarrierKind: codeModeCarrierCustom, CarrierPayload: "carrier", Report: "applied"}
	if err := store.put(t.Context(), "", map[string]mekugiHistory{"call": history}); err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{replayStore: store}
	makeRequest := func(payload string) *parsedResponsesRequest {
		return &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON([]any{
			map[string]any{"type": "custom_tool_call_output", "call_id": "call", "output": "applied"},
			map[string]any{"type": "custom_tool_call", "call_id": "call", "name": "exec", "input": payload},
		})}}
	}
	parent, err := proxy.reconcileVisibleInput(t.Context(), makeRequest("carrier"), "", "parent")
	if err != nil {
		t.Fatal(err)
	}
	fork, err := proxy.reconcileVisibleInput(t.Context(), makeRequest("carrier"), "", "fork")
	if err != nil {
		t.Fatal(err)
	}
	if !parent["call"].confirmed || !fork["call"].confirmed {
		t.Fatal("no-workspace inherited calls missing")
	}
	bad := makeRequest("tampered")
	before := string(bad.fields["input"])
	if _, err := proxy.reconcileVisibleInput(t.Context(), bad, "", "reused-route"); err == nil {
		t.Fatal("accepted late altered carrier")
	}
	if string(bad.fields["input"]) != before {
		t.Fatal("late validation changed original input")
	}
	retained, _, err := store.lookup(t.Context(), "", "call")
	if err != nil {
		t.Fatal(err)
	}
	if retained.confirmed {
		t.Fatal("failed validation persisted output confirmation")
	}
}

func TestReplayCompletedSSEIsDurableBeforeTerminal(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(strconv.FormatBool(native), func(t *testing.T) {
			calls := 0
			transform, proxy, _, _ := newMekugiTestTransform(t, testTranslator(t, &calls))
			if native {
				transform, _ = newNativeMekugiTestTransformWithProxy(t, proxy)
			}
			directory := t.TempDir()
			store, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			proxy.replayStore = store
			added := testMekugiItem()
			added["status"] = "in_progress"
			added["input"] = ""
			added["extra"] = map[string]any{"kept": true}
			if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})); err != nil {
				t.Fatal(err)
			}
			if _, found, err := store.lookup(t.Context(), transform.directory, "call-H"); err != nil || found {
				t.Fatalf("unfinished retained: %v %v", found, err)
			}
			emitted, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "item-H", "input": testMekugiScript}))
			if err != nil {
				t.Fatal(err)
			}
			if len(emitted) != 2 {
				t.Fatalf("completed events: %s", emitted)
			}
			var envelope struct {
				Item map[string]json.RawMessage `json:"item"`
			}
			if err := json.Unmarshal(emitted[0], &envelope); err != nil {
				t.Fatal(err)
			}
			var done map[string]json.RawMessage
			if err := json.Unmarshal(emitted[1], &done); err != nil {
				t.Fatal(err)
			}
			kind := codeModeCarrierCustom
			if native {
				kind = codeModeCarrierFunction
			}
			payloadField := carrierPayloadField(kind)
			envelope.Item[payloadField] = done[payloadField]
			reopened, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			restarted := &mekugiProxy{replayStore: reopened}
			request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON([]any{envelope.Item})}}
			if _, err := restarted.reconcileVisibleInput(t.Context(), request, transform.directory, "forked-route"); err != nil {
				t.Fatal(err)
			}
			var replayed []map[string]json.RawMessage
			if err := json.Unmarshal(request.fields["input"], &replayed); err != nil {
				t.Fatal(err)
			}
			item := replayed[0]
			if jsonString(item, "type") != "custom_tool_call" || jsonString(item, "name") != mekugiToolName || jsonString(item, "id") != "item-H" || jsonString(item, "input") != testMekugiScript || string(item["extra"]) != "{\"kept\":true}" || len(item["arguments"]) != 0 || calls != 1 {
				t.Fatalf("restart replay lost original: %s, executions %d", request.fields["input"], calls)
			}
		})
	}
}

func TestReplayStoreFailureDoesNotExposeCompletedCarrier(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, testTranslator(t, new(int)))
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	if err := store.put(t.Context(), workspace, map[string]mekugiHistory{"call-H": {Script: "conflict"}}); err != nil {
		t.Fatal(err)
	}
	added := testMekugiItem()
	added["status"] = "in_progress"
	added["input"] = ""
	if _, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.output_item.added", "item": added})); err != nil {
		t.Fatal(err)
	}
	visible, err := transform.TransformSSE(mustTestJSON(t, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": "item-H", "input": testMekugiScript}))
	if err == nil || len(visible) != 0 {
		t.Fatalf("failed durability exposed carrier: %s %v", visible, err)
	}
}

func TestReplayConcurrentViewsWithReusedRoutingKey(t *testing.T) {
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	histories := map[string]mekugiHistory{}
	for _, id := range []string{"parent", "fork"} {
		histories[id] = mekugiHistory{ToolName: mekugiToolName, Script: id, CarrierName: "exec", CarrierKind: codeModeCarrierCustom, CarrierPayload: id, TranslationError: "rejected", EvaluatorRejected: true}
	}
	if err := store.put(t.Context(), workspace, histories); err != nil {
		t.Fatal(err)
	}
	proxy := &mekugiProxy{replayStore: store}
	for index := range 16 {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			id := []string{"parent", "fork"}[index%2]
			request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON([]any{map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": id, "input": id}})}}
			visible, err := proxy.reconcileVisibleInput(t.Context(), request, workspace, "same-cache-key")
			if err != nil {
				t.Fatal(err)
			}
			transform := &mekugiResponseTransform{visible: visible}
			recovered, err := transform.recoveryHistory()
			if err != nil || recovered.Script != id || len(visible) != 1 {
				t.Fatalf("cross-contaminated view: %+v %v", recovered, err)
			}
		})
	}
}

func TestReplayJSONExcludesUnfinishedCalls(t *testing.T) {
	calls := 0
	transform, proxy, _, workspace := newMekugiTestTransform(t, testTranslator(t, &calls))
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	unfinished := testMekugiItem()
	unfinished["status"] = "in_progress"
	if _, err := transform.TransformJSON(mustTestJSON(t, map[string]any{"status": "in_progress", "output": []any{unfinished}})); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.lookup(t.Context(), workspace, "call-H"); err != nil || found || calls != 0 {
		t.Fatalf("unfinished call evaluated/retained: %d %v %v", calls, found, err)
	}
}

func TestReplayReceivedReplyDoesNotRepeatAfterReconciliation(t *testing.T) {
	workspace := t.TempDir()
	storeDirectory := t.TempDir()
	store, err := openMekugiReplayStore(storeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newManagedMekugiProxy(t, testTranslator(t, new(int)))
	proxy.replayStore = store
	envelope := map[string]any{"type": "agent_message", "id": "received-reply", "author": "/root/worker", "recipient": "/root", "content": []any{map[string]any{"type": "input_text", "text": "Message Type: MESSAGE\nTask name: /root\nSender: /root/worker\nPayload:\nresult"}}}
	prepare := func(proxy *mekugiProxy, input []any, thread string) (*mekugiResponseTransform, *parsedResponsesRequest) {
		t.Helper()
		request, err := parseResponsesRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "input": input, "tools": testNativeResponsesTools(), "tool_choice": "auto"}))
		if err != nil {
			t.Fatal(err)
		}
		transform, err := proxy.prepareRequest(t.Context(), &request, "reused-route", thread, codexTurnMetadata{RequestKind: "turn", Directories: map[string]json.RawMessage{workspace: nil}}, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(transform.Close)
		return transform, &request
	}
	first, _ := prepare(proxy, []any{envelope}, "original-thread")
	response, err := first.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{}}))
	if err != nil {
		t.Fatal(err)
	}
	var original struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if err := json.Unmarshal(response, &original); err != nil {
		t.Fatal(err)
	}
	if len(original.Output) != 1 {
		t.Fatalf("initial reply projection: %s", response)
	}
	generated := original.Output[0]
	for _, restart := range []bool{false, true} {
		if restart {
			reopened, err := openMekugiReplayStore(storeDirectory)
			if err != nil {
				t.Fatal(err)
			}
			proxy = newManagedMekugiProxy(t, testTranslator(t, new(int)))
			proxy.replayStore = reopened
		}
		next, request := prepare(proxy, []any{envelope, generated}, "continued-thread")
		if len(next.subagentDeferred) != 0 || len(next.subagentResponses) != 0 {
			t.Fatalf("restart=%v repeated projection", restart)
		}
		var forwarded []map[string]json.RawMessage
		if err := json.Unmarshal(request.fields["input"], &forwarded); err != nil {
			t.Fatal(err)
		}
		if len(forwarded) != 1 || jsonString(forwarded[0], "id") != "received-reply" {
			t.Fatalf("restart=%v replay input: %s", restart, request.fields["input"])
		}
		output, err := next.TransformJSON(mustTestJSON(t, map[string]any{"status": "completed", "output": []any{}}))
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Output []map[string]json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(output, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Output) != 0 {
			t.Fatalf("restart=%v repeated response commentary: %s", restart, output)
		}
	}
}
