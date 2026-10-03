package router

import (
	jsonv1 "encoding/json"
	json "encoding/json/v2"
	"strings"
	"testing"
)

func TestProjectedStockGuidanceRetainsAgentWorkflows(t *testing.T) {
	registry := newManagedMekugiProxy(t).registry
	guide := registry.frontendGuidance
	checkStableRefresh := func(t *testing.T, fields map[string]jsonv1.RawMessage) {
		t.Helper()
		before := mustMarshalJSON(fields)
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err != nil {
			t.Fatal(err)
		}
		if !sameJSONValue(before, mustMarshalJSON(fields)) {
			t.Fatal("refreshing stock guidance changed an already-projected request")
		}
	}

	t.Run("native exec_command", func(t *testing.T) {
		fields := map[string]jsonv1.RawMessage{"tools": mustMarshalJSON(testNativeResponsesTools())}
		var before []map[string]jsonv1.RawMessage
		if err := json.Unmarshal(fields["tools"], &before); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err != nil {
			t.Fatal(err)
		}
		var after []map[string]jsonv1.RawMessage
		if err := json.Unmarshal(fields["tools"], &after); err != nil {
			t.Fatal(err)
		}
		if len(after) != 2 || jsonString(after[0], "description") != "run a command\n\n"+guide+"\n\n"+codeModeJournalGuidance {
			t.Fatalf("exec_command did not receive the registry guidance additively: %s", fields["tools"])
		}
		if jsonString(before[1], "description") != jsonString(after[1], "description") {
			t.Fatalf("non-owner apply_patch description changed: before=%s after=%s", before[1]["description"], after[1]["description"])
		}
		checkStableRefresh(t, fields)
	})

	t.Run("Code Mode exec_command", func(t *testing.T) {
		const stock = testCodeModeDescription
		fields := map[string]jsonv1.RawMessage{"input": mustMarshalJSON([]any{testCodeModeAdditionalTools(stock)})}
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err != nil {
			t.Fatal(err)
		}
		catalog := decodeResponsesToolCatalog(fields)
		got := catalog.additional[0].tools.tools[0].nested.tools[0].Description
		if !strings.Contains(got, stock) {
			t.Fatal("Code Mode stock description was not preserved")
		}
		for _, owner := range []string{guide, codeModeJournalGuidance} {
			if strings.Count(got, owner) != 1 {
				t.Error("Code Mode description must include each guidance owner exactly once")
			}
		}
		checkStableRefresh(t, fields)
	})
}

func TestJournalRulesHaveOneOwnerInPreparedRequests(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "Code Mode", true: "native"}[native], func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			var request *parsedResponsesRequest
			if native {
				_, request = newNativeMekugiTestTransformWithProxy(t, proxy)
			} else {
				_, _, request, _ = newMekugiTestTransformWithProxy(t, proxy)
			}
			// Count across the actual combined request, not each surface in isolation.
			combined := string(mustMarshalJSON(request.fields))
			owner := string(mustMarshalJSON(codeModeJournalGuidance))
			if count := strings.Count(combined, owner[1:len(owner)-1]); count != 1 {
				t.Errorf("journal guidance appears %d times; want one owner", count)
			}
		})
	}
}
