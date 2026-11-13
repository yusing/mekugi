package executionobservation

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusing/mekugi/apps/mekugi/internal/requestexecution"
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/contracts/module"
	"github.com/yusing/mekugi/libraries/modelprotocol/attachments"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"
	"github.com/yusing/mekugi/modules/agentguidance/compact"
	"github.com/yusing/mekugi/modules/conversation/composer"
	"github.com/yusing/mekugi/modules/outputdeduplication"
	transportcapture "github.com/yusing/mekugi/modules/transportdiagnostics/capture"
	"github.com/yusing/mekugi/testsupport/moduletest"
)

func TestSuppliedSourcesThroughNativePreparedRequest(t *testing.T) {
	body := strings.Repeat("SUPPLIED_NATIVE_BODY useful source instructions and retained evidence\n", 16)
	supplied := (compact.Snapshot{Sources: []compact.CapturedSource{{Command: "mcat supplied.txt", Stdout: body, Stderr: "reader diagnostic"}}}).AttachmentText()
	var suppliedBody string
	attachments.ProjectSources(supplied, func(source string) string { suppliedBody = source; return source })
	if suppliedBody != body {
		t.Fatalf("native acquisition lost typed source boundary: %s", supplied)
	}
	for _, route := range []string{"agent-message", "tool-output", "side-question"} {
		t.Run(route, func(t *testing.T) {
			message := "Original instructions remain intact.\nReferenced sources captured when sent:\n" + supplied
			var items []map[string]any
			switch route {
			case "side-question":
				items = []map[string]any{{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": message}}}}
			case "agent-message":
				items = []map[string]any{{"type": "agent_message", "id": "amsg_native", "author": "/root", "recipient": "/root/worker", "content": []map[string]string{{"type": "input_text", "text": message}}}}
			case "tool-output":
				items = []map[string]any{{"type": "function_call_output", "call_id": "native-coordination", "output": message}}
			}
			// The user attachment producer supplies the same bytes independently.
			draft := composer.Draft{Skills: []composer.Token{{Name: "guide", Path: "/never-reopen/guide"}}}
			draft.SnapshotSkills(func(_, _ string) ([]byte, error) { return []byte(body), nil })
			items = append(items, map[string]any{"role": "user", "content": []map[string]string{{"type": "input_text", "text": draft.Attachments[0]}}})
			original := responses.MustMarshalJSON(items)
			for _, enabled := range []bool{true, false} {
				p := newManagedMekugiProxy(t)
				p.InputProjections = nil
				if enabled {
					service, err := outputdeduplication.Module.New(outputdeduplication.Deps{}, module.Slots{})
					if err != nil {
						t.Fatal(err)
					}
					p.InputProjections = []binding.InputProjection{outputdeduplication.Module.Bindings.InputProjection(service)}
				}
				recorder, err := new(transportcapture.Service).Open(transportcapture.Config{Mode: "mekugi"})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = recorder.Close() })
				handler := recorder.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					tools := testExecResponsesTools()
					if route == "side-question" {
						tools = nil
					}
					request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "instructions": testBaseInstructions, "tools": tools, "input": items}))
					if err != nil {
						t.Fatal(err)
					}
					prepared, err := p.Prepare(r.Context(), &request, requestexecution.Preparation{Session: "runtime", Thread: "root", MetadataValid: true, Metadata: codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn"}})
					if err != nil {
						t.Fatal(err)
					}
					if prepared != nil {
						defer prepared.Close()
					}
					requestexecution.ProjectFileAttachments(&request)
					want := 32
					if enabled {
						want = 16
					}
					if bytes.Count(request.Fields["input"], []byte("SUPPLIED_NATIVE_BODY")) != want || bytes.Contains(request.Fields["input"], []byte("See O1")) != enabled {
						t.Fatalf("delivery source was not projected, enabled=%v: %s", enabled, request.Fields["input"])
					}
					if !bytes.Contains(request.Fields["input"], []byte("Original instructions remain intact.")) || !bytes.Contains(request.Fields["input"], []byte("reader diagnostic")) || !bytes.Equal(request.OriginalField("input"), original) {
						t.Fatal("projection changed instructions, diagnostics or retained input")
					}
					w.WriteHeader(http.StatusOK)
				}))
				handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`)))
				metrics := recorder.Snapshot().OutputDeduplication
				if enabled && (metrics.Occurrences != 1 || metrics.SavedTokens <= 0 || metrics.Unmeasured != 0) || !enabled && metrics.Projections != 0 {
					t.Fatalf("attachment receipt did not reach capture, enabled=%v: %+v", enabled, metrics)
				}
			}
		})
	}
}

func TestSuppliedSourcesShareHydratedReloadAndCompletion(t *testing.T) {
	for _, completion := range []bool{false, true} {
		p := newManagedMekugiProxy(t)
		attachTestReplayStore(t, p)
		service, err := outputdeduplication.Module.New(outputdeduplication.Deps{}, module.Slots{})
		if err != nil {
			t.Fatal(err)
		}
		p.InputProjections = []binding.InputProjection{outputdeduplication.Module.Bindings.InputProjection(service)}
		selection := `{"sources":["@selected.txt"]}`
		source := compact.CapturedSource{Command: "cat selected.txt", Stdout: "short supplied source"}
		if completion {
			selection = `{"sources":["mchanges:amber1"]}`
			source.Command = "mchanges amber1"
		}
		ref, err := moduletest.StoreCompact(t.Context(), p.Retained, "root", "handover", selection)
		if err != nil {
			t.Fatal(err)
		}
		reader, _, err := compactReaderCall(compact.Record{Version: 1, Reference: ref, Selection: selection}, nil, "root")
		if err != nil {
			t.Fatal(err)
		}
		var call map[string]jsontext.Value
		if err := json.Unmarshal(reader, &call); err != nil {
			t.Fatal(err)
		}
		snapshot := compact.Snapshot{Selection: selection, Sources: []compact.CapturedSource{source}}
		if completion {
			snapshot.Sources, snapshot.Completion = nil, &source
		}
		items := []any{
			map[string]any{"type": "compaction", "encrypted_content": ref},
			map[string]any{"role": "user", "content": []map[string]string{{"type": "input_text", "text": (compact.Snapshot{Sources: []compact.CapturedSource{source}}).AttachmentText()}}},
			call, map[string]any{"type": "custom_tool_call_output", "call_id": call["call_id"], "output": compactTestSnapshot(t, p, snapshot)},
		}
		request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "input": items, "tools": testExecResponsesTools()}))
		if err != nil {
			t.Fatal(err)
		}
		original := bytes.Clone(request.OriginalField("input"))
		prepared, err := p.Prepare(t.Context(), &request, requestexecution.Preparation{Session: "routing", Thread: "root", MetadataValid: true, Metadata: codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn"}})
		if err != nil {
			t.Fatal(err)
		}
		if prepared != nil {
			prepared.Close()
		}
		visible := request.Fields["input"]
		if bytes.Count(visible, []byte(source.Stdout)) != 1 || !bytes.Contains(visible, []byte("See O1")) || !bytes.Contains(visible, []byte(source.Command+": exit_code=0")) || !bytes.Equal(original, request.OriginalField("input")) {
			t.Fatalf("hydrated source did not share request references, completion=%v: %s", completion, visible)
		}
	}
}

func TestNativeReloadDeduplicatesRecoveredShortSkill(t *testing.T) {
	body := "CURRENT_ATTACHED_SKILL"
	p := newManagedMekugiProxy(t)
	service, err := outputdeduplication.Module.New(outputdeduplication.Deps{}, module.Slots{})
	if err != nil {
		t.Fatal(err)
	}
	p.InputProjections = []binding.InputProjection{outputdeduplication.Module.Bindings.InputProjection(service)}
	draft := composer.Draft{Skills: []composer.Token{{Name: "guide", Path: "/workspace/SKILL.md"}}}
	draft.SnapshotSkills(func(_, _ string) ([]byte, error) { return []byte(body), nil })
	header := "\nSource \"@/workspace/SKILL.md\":\n"
	reload := "Reloaded context (tool output). These source bodies are already read context; reread only missing or changed content. Failed reads are not complete context.\n" + attachments.SourceRanges(header+body+"\n", []attachments.SourceRange{{Start: len(header), End: len(header) + len(body)}})
	items := []map[string]any{
		{"type": "function_call_output", "namespace": "context", "name": "reload", "output": reload},
		{"type": "custom_tool_call", "name": "exec", "call_id": "recall", "input": "text(await tools.exec_command({cmd:'mread maple birch'}));"},
		{"type": "custom_tool_call_output", "call_id": "recall", "output": "Script completed\nWall time 0.1 seconds\nOutput:\n\n--- maple ---\n<selected-text label=\"selection\" source=\"screen\">\nselected screen\n</selected-text>\n\n--- birch ---\n" + draft.Attachments[0]},
	}
	request, err := codexresponses.ParseRequest(responses.MustMarshalJSON(map[string]any{"model": "gpt-test", "instructions": testBaseInstructions, "tools": testExecResponsesTools(), "input": items}))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := p.Prepare(t.Context(), &request, requestexecution.Preparation{Session: "runtime", Thread: "root", MetadataValid: true, Metadata: codexresponses.TurnMetadata{ThreadID: "root", RequestKind: "turn"}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared != nil {
		defer prepared.Close()
	}
	if bytes.Count(request.Fields["input"], []byte(body)) != 1 || !bytes.Contains(request.Fields["input"], []byte("See O1")) {
		t.Fatalf("native reload body repeated through retained attachment recall: %s", request.Fields["input"])
	}
}
