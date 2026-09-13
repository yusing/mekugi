package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodeModeHpatchRetainsExactCarrier(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	directory := t.TempDir()
	store, err := openMekugiReplayStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		id, tool, script string
		rejected         bool
	}{
		{"success", mekugiToolName, "in file.txt\ntype \"old\" \"new\"\n", false},
		{"rejected", mekugiToolName, "in file.txt\ntype \"missing\" \"new\"\n", true},
		{"recovered", mekugiRecoveryToolName, `type "missing" "old"`, false},
	} {
		t.Run(step.id, func(t *testing.T) {
			original := map[string]json.RawMessage{
				"type": mustMarshalJSON("custom_tool_call"), "name": mustMarshalJSON(step.tool),
				"call_id": mustMarshalJSON(step.id), "input": mustMarshalJSON(step.script),
			}
			item, ok := decodeResponsesItem(mustMarshalJSON(original))
			if !ok {
				t.Fatal("decode original item")
			}
			if _, err := transform.transformOutputItem(&item); err != nil {
				t.Fatal(err)
			}
			if err := transform.commitLocalCall(step.id); err != nil {
				t.Fatal(err)
			}
			reopened, err := openMekugiReplayStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			retained, found, err := reopened.lookup(t.Context(), workspace, step.id)
			if err != nil || !found {
				t.Fatalf("lookup: found=%v err=%v", found, err)
			}
			if (retained.TranslationError != "") != step.rejected {
				t.Fatalf("unexpected translation: %s", retained.TranslationError)
			}
			if retained.CarrierKind != codeModeCarrierCustom || retained.CarrierPayload == "" || retained.CarrierPayload != *item.Input {
				t.Fatalf("carrier not pinned: kind=%q payload=%q delivered=%q", retained.CarrierKind, retained.CarrierPayload, *item.Input)
			}
			// Legacy records must still render the same bytes after restart.
			legacy := retained
			legacy.CarrierKind, legacy.CarrierPayload = "", ""
			if legacy.carrierInput() != retained.CarrierPayload {
				t.Fatal("legacy rendering changed")
			}
			request := &parsedResponsesRequest{fields: map[string]json.RawMessage{"input": mustMarshalJSON([]any{item.cloneFields()})}}
			restarted := &mekugiProxy{replayStore: reopened}
			if _, err := restarted.reconcileVisibleInput(t.Context(), request, workspace, "fresh-route"); err != nil {
				t.Fatal(err)
			}
			if !sameJSONValue(request.fields["input"], mustMarshalJSON([]any{original})) {
				t.Fatalf("replay changed original: %s", request.fields["input"])
			}
		})
	}
}

func TestReplayFailureLeavesParsedRequestUnchanged(t *testing.T) {
	transform, proxy, _, workspace := newMekugiTestTransform(t, newInProcessMekugiTranslator(t.TempDir()))
	store, err := openMekugiReplayStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proxy.replayStore = store
	history, err := transform.translate("call", "new file.txt\ntype \"new\\n\"\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := transform.commitLocalCall("call"); err != nil {
		t.Fatal(err)
	}
	if err := store.putCommentary(t.Context(), workspace, []string{"generated"}); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"carrier", "confirmation"} {
		t.Run(failure, func(t *testing.T) {
			payload := history.carrierInput()
			if failure == "carrier" {
				payload += "tampered"
			}
			request, err := parseResponsesRequest(mustMarshalJSON(map[string]any{
				"input": []any{
					map[string]any{"type": "message", "id": "generated", "role": "assistant", "content": "notice"},
					map[string]any{"type": "custom_tool_call_output", "call_id": "call", "output": history.Report},
					map[string]any{"type": "custom_tool_call", "call_id": "call", "name": history.CarrierName, "input": payload},
				},
			}))
			if err != nil {
				t.Fatal(err)
			}
			request.cachedInput = 3
			before := string(mustMarshalJSON(request.fields))
			if failure == "confirmation" {
				// The immutable record remains readable, but the receipt cannot fit.
				store.maxBytes = 1
			}
			_, err = proxy.reconcileVisibleInput(t.Context(), &request, workspace, "fresh-route")
			if err == nil {
				t.Fatal("accepted failed reconciliation")
			}
			want := "changed translated payload"
			if failure == "confirmation" {
				want = "change index store quota"
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("expected %s failure, got %v", failure, err)
			}
			if request.cachedInput != 3 || string(mustMarshalJSON(request.fields)) != before {
				t.Fatal("failed reconciliation mutated parsed request")
			}
		})
	}
}

func TestTrackedStatusUsesRetainedScriptNotCarrierKind(t *testing.T) {
	for _, test := range []struct {
		script, want string
	}{
		{"new f\ntype \"shell echo not a command\"\n", "prepared (application unconfirmed)"},
		{"shell echo command\n", "execution plan (see segment attempts)"},
		{"resume M00000000000000000000000000000000\n", "execution plan (see segment attempts)"},
	} {
		history := mekugiHistory{ToolName: mekugiToolName, Script: test.script, CarrierKind: codeModeCarrierCustom}
		history = durableHistory(history)
		if got := trackedStatus(history, false); got != test.want {
			t.Errorf("script %q: got %q, want %q", test.script, got, test.want)
		}
	}
}
