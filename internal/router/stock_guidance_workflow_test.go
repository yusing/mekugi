package router

import (
	jsonv1 "encoding/json"
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

	t.Run("exec exec_command", func(t *testing.T) {
		const stock = testCodeModeDescription
		fields := map[string]jsonv1.RawMessage{"input": mustMarshalJSON([]any{testCodeModeAdditionalTools(stock)})}
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err != nil {
			t.Fatal(err)
		}
		catalog := decodeResponsesToolCatalog(fields)
		got := catalog.additional[0].tools.tools[0].nested.tools[0].Description
		if !strings.Contains(got, stock) {
			t.Fatal("exec stock description was not preserved")
		}
		if !strings.Contains(got, "On macOS, pass `shell:\"bash\"` to `tools.exec_command` unless the task explicitly requires another shell") {
			t.Fatal("exec description lost macOS Bash selection guidance")
		}
		if strings.Count(got, guide) != 1 {
			t.Error("exec description must include frontend guidance exactly once")
		}
		checkStableRefresh(t, fields)
	})
}
