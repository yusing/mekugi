package launch

import (
	"github.com/yusing/mekugi/backends/codex/binding"
	codexresponses "github.com/yusing/mekugi/backends/codex/responses"
	"github.com/yusing/mekugi/libraries/modelprotocol/responses"

	jsonv1 "encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestProjectedStockGuidanceRetainsAgentWorkflows(t *testing.T) {
	registry := newManagedMekugiProxy(t).registry
	guide := registry.FrontendGuidance
	checkStableRefresh := func(t *testing.T, fields map[string]jsonv1.RawMessage) {
		t.Helper()
		before := responses.MustMarshalJSON(fields)
		if _, err := binding.PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, testGuidance()); err != nil {
			t.Fatal(err)
		}
		if !responses.SameJSONValue(before, responses.MustMarshalJSON(fields)) {
			t.Fatal("refreshing stock guidance changed an already-projected request")
		}
	}

	t.Run("exec exec_command", func(t *testing.T) {
		const stock = testCodeModeDescription
		fields := map[string]jsonv1.RawMessage{"input": responses.MustMarshalJSON([]any{testCodeModeAdditionalTools(stock)})}
		if _, err := binding.PrepareStockExecution(fields, codexresponses.DecodeToolCatalog(fields), guide, testGuidance()); err != nil {
			t.Fatal(err)
		}
		catalog := codexresponses.DecodeToolCatalog(fields)
		got := catalog.Additional[0].Tools.Tools[0].Nested.Tools[0].Description()
		if !strings.Contains(got, stock) {
			t.Fatal("exec stock description was not preserved")
		}
		if strings.Contains(got, "Pass `shell:\"bash\"` to `tools.exec_command` unless the task explicitly requires another shell") {
			t.Fatal("exec description contains workflow policy")
		}
		if strings.Count(got, guide.Descriptions) != 1 {
			t.Error("exec description must include frontend guidance exactly once")
		}
		instructions := responses.StringField(fields, "instructions")
		if instructions != guide.Workflow || strings.Contains(instructions, "Pass `shell:") != (runtime.GOOS == "darwin") {
			t.Fatal("base instructions lost workflow")
		}
		for _, removed := range []string{"Codex owns stock editing", "On macOS", "Give required skill and instruction reads their own exec_command"} {
			if strings.Contains(instructions, removed) {
				t.Fatalf("workflow retains removed guidance %q", removed)
			}
		}
		checkStableRefresh(t, fields)
	})
}
