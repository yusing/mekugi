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
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide, codeModeJournalGuidance); err != nil {
			t.Fatal(err)
		}
		if !sameJSONValue(before, mustMarshalJSON(fields)) {
			t.Fatal("refreshing stock guidance changed an already-projected request")
		}
	}

	t.Run("exec exec_command", func(t *testing.T) {
		const stock = testCodeModeDescription
		fields := map[string]jsonv1.RawMessage{"input": mustMarshalJSON([]any{testCodeModeAdditionalTools(stock)})}
		if _, err := prepareStockExecution(fields, decodeResponsesToolCatalog(fields), guide, codeModeJournalGuidance); err != nil {
			t.Fatal(err)
		}
		catalog := decodeResponsesToolCatalog(fields)
		got := catalog.additional[0].tools.tools[0].nested.tools[0].Description
		if !strings.Contains(got, stock) {
			t.Fatal("exec stock description was not preserved")
		}
		for _, owner := range []string{guide, codeModeJournalGuidance} {
			if strings.Count(got, owner) != 1 {
				t.Error("exec description must include each guidance owner exactly once")
			}
		}
		checkStableRefresh(t, fields)
	})
}

func TestJournalRulesHaveOneOwnerInPreparedRequests(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	_, _, request, _ := newMekugiTestTransformWithProxy(t, proxy)
	// Count across the actual combined request, not each surface in isolation.
	combined := string(mustMarshalJSON(request.fields))
	owner := string(mustMarshalJSON(codeModeJournalGuidance))
	if count := strings.Count(combined, owner[1:len(owner)-1]); count != 1 {
		t.Errorf("journal guidance appears %d times; want one owner", count)
	}
	for _, rule := range []string{
		"one concrete result and a completion check",
		"sized for about 5-10 minutes of work",
		"pending sibling slices under the same parent",
		"Record results as they become known",
		"finish the turn before starting another slice",
		"reset context and continue with the next pending slice",
	} {
		if !strings.Contains(combined, rule) {
			t.Errorf("prepared request lacks slice guidance: %s", rule)
		}
	}
}

func TestJournalGuidanceUsesRequestRole(t *testing.T) {
	for _, role := range []struct{ name, kind string }{{"/root", ""}, {"/root/child", "thread_spawn"}, {"/root/child/nested", "thread_spawn"}, {"", "review"}} {
		t.Run(role.name+role.kind, func(t *testing.T) {
			proxy := newManagedMekugiProxy(t)
			attachTestReplayStore(t, proxy)
			metadata := codexTurnMetadata{RequestKind: "turn", AgentName: role.name, SubagentKind: role.kind, Directories: map[string]jsonv1.RawMessage{t.TempDir(): nil}}
			want, stale := codeModeJournalGuidance, codeModeSubagentJournalGuidance
			if role.kind != "" {
				want, stale = stale, want
			}
			request, err := parseResponsesRequest(mustMarshalJSON(map[string]any{"model": "gpt-test", "input": []any{testCodeModeAdditionalTools(testCodeModeDescription + "\n" + stale)}}))
			if err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"prewarm", "fresh", "resumed"} {
				if phase == "resumed" {
					store := proxy.replayStore
					proxy = newManagedMekugiProxy(t)
					proxy.replayStore = store
				}
				transform, err := proxy.prepareModelRequest(t.Context(), &request, "session", "thread", metadata, true, phase == "prewarm")
				if err != nil {
					t.Fatalf("%s: %v", phase, err)
				}
				if transform != nil {
					transform.Close()
				}
				got := decodeResponsesToolCatalog(request.fields).additional[0].tools.tools[0].nested.tools[0].Description
				if strings.Count(got, want) != 1 || strings.Contains(got, stale) || !strings.Contains(got, testCodeModeDescription) {
					t.Fatalf("%s: journal role or stock contract mismatch", phase)
				}
			}
		})
	}
	if len(codeModeSubagentJournalGuidance) >= len(codeModeJournalGuidance) || strings.Contains(codeModeSubagentJournalGuidance, "5-10 minutes") {
		t.Fatal("subagent guide retains Main's planning policy")
	}
}
