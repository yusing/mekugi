package executionobservation

import (
	"bytes"
	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/command"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/outputdeduplication"
	"github.com/yusing/mekugi/modules/outputrecovery/recovery"
)

func TestCompactReloadProviderInputHidesOrchestration(t *testing.T) {
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	workspace := t.TempDir()
	const selection = `{"sources":[{"path":"AGENTS.md"},{"path":"selected.txt"},{"path":"nested/AGENTS.md"}]}`
	ref, err := p.StoreCompact(t.Context(), "root", "handover", selection)
	if err != nil {
		t.Fatal(err)
	}
	record := compact.Record{Reference: ref, Selection: selection}
	items := []map[string]jsontext.Value{{"type": jsontext.Value(`"compaction"`), "encrypted_content": mustTestJSON(t, ref)}}
	completed := []map[string]string{{"type": "input_text", "text": "cat selected.txt: exit_code=0\nfresh source; EACCES denied.txt"}}
	result := compactTestSnapshot(t, p, compact.Snapshot{Selection: selection, Sources: []compact.CapturedSource{
		{Command: "cat AGENTS.md", Stdout: "host instructions must not be reloaded"},
		{Command: "cat selected.txt", Stdout: "fresh source; EACCES denied.txt"},
		{Command: "cat nested/AGENTS.md", Stdout: "nested host instructions must not be reloaded"},
	}})
	raw, _, err := compactReaderCall(record, items, "root")
	if err != nil {
		t.Fatal(err)
	}
	var call map[string]jsontext.Value
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	var generatedID string
	_ = json.Unmarshal(call["call_id"], &generatedID)
	items = append(items, call, map[string]jsontext.Value{"type": mustTestJSON(t, "custom_tool_call_output"), "call_id": call["call_id"], "output": mustTestJSON(t, result)})
	// Identical program text without orchestration provenance remains ordinary
	// tool history, including its paired result.
	ordinary := map[string]jsontext.Value{"type": jsontext.Value(`"custom_tool_call"`), "name": jsontext.Value(`"exec"`), "call_id": jsontext.Value(`"ordinary"`), "input": items[1]["input"]}
	items = append(items, ordinary, map[string]jsontext.Value{"type": jsontext.Value(`"custom_tool_call_output"`), "call_id": ordinary["call_id"], "output": jsontext.Value(`"ordinary output"`)})
	native := mustTestJSON(t, items)
	if _, _, err := compactReaderCall(record, items[:2], "fork"); err == nil {
		t.Fatal("inherited live reader was resumed")
	}
	for _, mode := range []string{"turn", "prewarm", "execution-free", "fork", "fork-prewarm"} {
		t.Run(mode, func(t *testing.T) {
			fields := map[string]any{"model": "gpt-test", "instructions": testBaseInstructions, "tools": testExecResponsesTools(), "input": items}
			if mode == "execution-free" {
				fields["generate"] = false
			}
			request, err := codexresponses.ParseRequest(mustTestJSON(t, fields))
			if err != nil {
				t.Fatal(err)
			}
			metadata := codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{workspace: jsonv1.RawMessage(`{}`)}}
			if strings.HasPrefix(mode, "fork") {
				metadata.ThreadID, metadata.ForkedFromThreadID = "fork", "root"
			}
			transform, err := p.prepareModelRequest(t.Context(), &request, "routing", metadata.ThreadID, metadata, true, strings.HasSuffix(mode, "prewarm"))
			if err != nil {
				t.Fatal(err)
			}
			if transform != nil {
				defer transform.Close()
				if len(transform.compactReload) != 0 {
					t.Fatal("completed reload requested another read")
				}
			}
			var visible []map[string]jsontext.Value
			if err := json.Unmarshal(request.Fields["input"], &visible); err != nil {
				t.Fatal(err)
			}
			if len(visible) != 4 || !bytes.Equal(request.OriginalField("input"), native) {
				t.Fatalf("changed native history or lost provider context: %s", request.Fields["input"])
			}
			for _, item := range visible {
				var id string
				_ = json.Unmarshal(item["call_id"], &id)
				if id == generatedID {
					t.Fatalf("synthetic payload leaked: %s", request.Fields["input"])
				}
			}
			var role string
			var parts []map[string]string
			_ = json.Unmarshal(visible[1]["role"], &role)
			_ = json.Unmarshal(visible[1]["content"], &parts)
			if role != "user" || len(parts) == 0 || !strings.Contains(parts[0]["text"], "already read context") || !reflect.DeepEqual(parts[1:], completed) {
				t.Fatalf("lost result content or provenance: %s", request.Fields["input"])
			}
			if !bytes.Equal(visible[2]["input"], ordinary["input"]) || !bytes.Equal(visible[3]["output"], jsontext.Value(`"ordinary output"`)) {
				t.Fatal("ordinary tool history changed")
			}
		})
	}
}

func TestCompactReloadDeduplicatesOrdinaryReread(t *testing.T) {
	p := newManagedMekugiProxy(t)
	attachTestReplayStore(t, p)
	service, err := outputdeduplication.Module.New(outputdeduplication.Deps{}, module.Slots{})
	if err != nil {
		t.Fatal(err)
	}
	p.inputProjections = []binding.InputProjection{outputdeduplication.Module.Bindings.InputProjection(service)}
	selection := `{"sources":[{"path":"selected.txt"}]}`
	ref, err := p.StoreCompact(t.Context(), "root", "handover", selection)
	if err != nil {
		t.Fatal(err)
	}
	record := compact.Record{Version: 1, Reference: ref, Selection: selection}
	call, _, err := compactReaderCall(record, nil, "root")
	if err != nil {
		t.Fatal(err)
	}
	var reader map[string]jsontext.Value
	if err := json.Unmarshal(call, &reader); err != nil {
		t.Fatal(err)
	}
	body := strings.Repeat("Source rows shared with an ordinary unnumbered read.\n", 12)
	result := compactTestSnapshot(t, p, compact.Snapshot{Selection: selection, Sources: []compact.CapturedSource{{Command: "cat selected.txt", Stdout: body}}})
	input := []any{
		map[string]any{"type": "compaction", "encrypted_content": ref}, reader,
		map[string]any{"type": "custom_tool_call_output", "call_id": reader["call_id"], "output": result},
		map[string]any{"type": "custom_tool_call", "call_id": "reread", "name": "exec", "input": `text((await tools.exec_command({cmd:"cat selected.txt"})).output)`},
		map[string]any{"type": "custom_tool_call_output", "call_id": "reread", "output": body},
	}
	request, err := codexresponses.ParseRequest(mustTestJSON(t, map[string]any{"model": "gpt-test", "instructions": testBaseInstructions, "tools": testExecResponsesTools(), "input": input}))
	if err != nil {
		t.Fatal(err)
	}
	original := bytes.Clone(request.OriginalField("input"))
	metadata := codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn", Directories: map[string]jsonv1.RawMessage{t.TempDir(): jsonv1.RawMessage(`{}`)}}
	transform, err := p.prepareRequest(t.Context(), &request, "routing", "root", metadata, true)
	if err != nil {
		t.Fatal(err)
	}
	defer transform.Close()
	var visible []map[string]jsontext.Value
	if err := json.Unmarshal(request.Fields["input"], &visible); err != nil {
		t.Fatal(err)
	}
	var repeated string
	if err := json.Unmarshal(visible[len(visible)-1]["output"], &repeated); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(repeated, "[same as O") || strings.Contains(repeated, body) || !bytes.Contains(visible[1]["content"], []byte("[O")) {
		t.Fatalf("reload source was not reusable by deduplication: %s", request.Fields["input"])
	}
	if len(transform.compactReload) != 0 || !bytes.Equal(original, request.OriginalField("input")) {
		t.Fatal("replayed completed read or changed native evidence")
	}
}

func TestCompactReloadIntroducesMultipleOutputsOnce(t *testing.T) {
	var items []map[string]jsontext.Value
	calls := map[string]bool{"first": true, "second": true}
	for _, id := range []string{"first", "second"} {
		items = append(items, map[string]jsontext.Value{
			"type":    jsontext.Value(`"custom_tool_call_output"`),
			"call_id": mustTestJSON(t, id), "output": mustTestJSON(t, id+" source body"),
		})
	}
	input, err := compactReaderInput(items, calls)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(input, []byte("already read context")) != 1 {
		t.Fatalf("reload introduction repeated: %s", input)
	}
	var visible []struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(input, &visible); err != nil {
		t.Fatal(err)
	}
	if len(visible) != 2 || len(visible[0].Content) != 2 || visible[0].Content[1].Text != "first source body" || len(visible[1].Content) != 1 || visible[1].Content[0].Text != "second source body" {
		t.Fatalf("reload bodies changed: %s", input)
	}
}

func compactTestSnapshot(t *testing.T, p *Preparer, snapshot compact.Snapshot) string {
	t.Helper()
	ns, err := p.retained.Store.NamespaceForThread("root")
	if err != nil {
		t.Fatal(err)
	}
	store, err := p.recovered.AttachStore(p.retained.Store, ns, "root", recovery.DefaultLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Publish(t.Context(), command.OmittedOutput{Stdout: string(data)}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return "Script completed\nWall time 0.1 seconds\nOutput:\n" + compact.SnapshotPrefix + ref + "\n"
}

func TestCompactReloadWaitsWithoutPartialProviderContext(t *testing.T) {
	record := compact.Record{Reference: "mekugi:compact:thread:amber", Selection: `{"sources":[{"path":"selected.txt"}]}`}
	var items []map[string]jsontext.Value
	for _, output := range []string{
		"Script running with cell ID slow\nWall time 10 seconds\nOutput:\n",
		"Script running with cell ID slow\nWall time 10 seconds\nOutput:\n",
		"Script completed\nWall time 1 seconds\nOutput:\n" + compact.SnapshotPrefix + "amber\n",
	} {
		raw, _, err := compactReaderCall(record, items, "root")
		if err != nil {
			t.Fatal(err)
		}
		var call map[string]jsontext.Value
		if err := json.Unmarshal(raw, &call); err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			if string(call["name"]) != `"wait"` {
				t.Fatalf("expected internal wait, got %s", raw)
			}
			if _, _, err := compactReaderCall(record, items, "fork"); err == nil {
				t.Fatal("fork resumed live parent cell")
			}
		}
		var kind string
		_ = json.Unmarshal(call["type"], &kind)
		items = append(items, call, map[string]jsontext.Value{"type": mustTestJSON(t, kind+"_output"), "call_id": call["call_id"], "output": mustTestJSON(t, output)})
	}
	pending, calls, err := compactReaderCall(record, items, "root")
	if err != nil || len(pending) != 0 || len(calls) != 3 {
		t.Fatalf("unfinished acquisition: %s %v %v", pending, calls, err)
	}
}
