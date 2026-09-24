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
	frontendRequirements := map[string][]string{
		"shared usage": {
			"Reuse still-current source context",
			"Batch ready, related edits",
		},
		"mchanges": {
			"same-agent inclusive ranges",
			"calling thread",
			"Do not routinely pair Git diff with mchanges",
			"skip --summary before an already-needed diff",
		},
		"msymbol": {
			"Before removing a field or changing a signature",
			"semantic references",
			"affected packages and tests",
			"unavailable coverage",
		},
		"inspect_file": {
			"only structure is needed",
			"outline to a full-file read",
		},
		"mrun": {
			"Use for noisy commands",
			"outside the selected window is discarded",
			"Only an emitted mread reference recovers retained delivery overflow",
			"ordinary exec_command truncation has no mread recovery",
		},
		"mcat": {
			"START:END or START-END is a separate operand after its path, inclusive of both endpoints.",
			"mcat src/main.go 100:150",
			"mcat -n 20 src/main.go",
			"first 20 rows",
		},
	}
	checkFrontendRequirements := func(t *testing.T, description string) {
		t.Helper()
		for owner, requirements := range frontendRequirements {
			for _, required := range requirements {
				if !strings.Contains(description, required) {
					t.Errorf("%s projection is missing %q", owner, required)
				}
			}
		}
		for _, required := range []string{
			"<common-options>",
			"--max-tokens N bounds output to 1–15500 tokens.",
			"-n N selects up to N rows;",
			"mcat keeps its default token ceiling with -n alone and always keeps complete rows; mrun token limits may cut within a row.",
		} {
			if count := strings.Count(description, required); count != 1 {
				t.Errorf("shared option guidance %q appears %d times; want one owner", required, count)
			}
		}
	}
	checkStableRefresh := func(t *testing.T, fields map[string]jsonv1.RawMessage) {
		t.Helper()
		if _, ok := fields["previous_response_id"]; ok {
			t.Fatal("guidance fixture unexpectedly includes a previous-response handle")
		}
		if raw, ok := fields["input"]; ok {
			var input []map[string]jsonv1.RawMessage
			if err := jsonv1.Unmarshal(raw, &input); err != nil {
				t.Fatal(err)
			}
			for _, item := range input {
				if jsonString(item, "type") != "additional_tools" {
					t.Fatalf("guidance fixture unexpectedly includes conversation history: %s", raw)
				}
			}
		}
		before := mustMarshalJSON(fields)
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide); err != nil {
			t.Fatal(err)
		}
		if !sameJSONValue(before, mustMarshalJSON(fields)) {
			t.Fatal("refreshing stock guidance changed an already-projected request")
		}
		checkFrontendRequirements(t, string(mustMarshalJSON(fields)))
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
		if len(after) != 2 || jsonString(after[0], "description") != "run a command\n\n"+guide {
			t.Fatalf("exec_command did not receive the registry guidance additively: %s", fields["tools"])
		}
		if jsonString(before[1], "description") != jsonString(after[1], "description") {
			t.Fatalf("non-owner apply_patch description changed: before=%s after=%s", before[1]["description"], after[1]["description"])
		}
		checkFrontendRequirements(t, jsonString(after[0], "description"))
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
		checkFrontendRequirements(t, got)
		if !strings.Contains(got, "Follow the `functions.journal` tool description") {
			t.Error("Code Mode guidance does not point to the journal owner")
		}
		if strings.Contains(codeModeJournalGuidance, "After every required tool result, finish") {
			t.Error("Code Mode journal guidance still directs finishing after every individual tool result")
		}
		if strings.Contains(got, "finish alone") || strings.Contains(got, "finish as the only call") {
			t.Error("Code Mode guidance unnecessarily forbids accompanying router-owned mutations")
		}
		checkStableRefresh(t, fields)
	})

	for _, required := range []string{"owned change ranges", "aggregated numstat"} {
		if !strings.Contains(journalToolDescription, required) {
			t.Errorf("child completion guidance is missing %q", required)
		}
	}
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
			for _, rule := range []string{
				"Record milestones when established, not only at completion",
				"Use journal mutations instead of commentary for milestone updates",
				"Prefer the optional journal field",
				"Once the assigned work is complete",
				"no host-dispatched calls in the same response",
				"Successful finish ends the turn",
			} {
				if count := strings.Count(combined, rule); count != 1 {
					t.Errorf("journal rule %q appears %d times; want one owner", rule, count)
				}
			}
		})
	}
}
