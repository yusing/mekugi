package router

import (
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"
	"strings"
	"testing"
)

func TestProjectedStockGuidanceRetainsAgentWorkflows(t *testing.T) {
	registry := newManagedMekugiProxy(t).registry
	guide := registry.FrontendGuidance
	checkStableRefresh := func(t *testing.T, fields map[string]jsonv1.RawMessage) {
		t.Helper()
		before := responses.MustMarshalJSON(fields)
		if _, err := prepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, testGuidance()); err != nil {
			t.Fatal(err)
		}
		if !responses.SameJSONValue(before, responses.MustMarshalJSON(fields)) {
			t.Fatal("refreshing stock guidance changed an already-projected request")
		}
	}

	t.Run("exec exec_command", func(t *testing.T) {
		const stock = testCodeModeDescription
		fields := map[string]jsonv1.RawMessage{"input": responses.MustMarshalJSON([]any{testCodeModeAdditionalTools(stock)})}
		if _, err := prepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, testGuidance()); err != nil {
			t.Fatal(err)
		}
		catalog := codexresponses.DecodeToolCatalog(fields)
		got := catalog.Additional[0].Tools.Tools[0].Nested.Tools[0].Description()
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
