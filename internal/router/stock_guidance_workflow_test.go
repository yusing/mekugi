package router

import (
	jsonv1 "encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusing/mekugi/internal/appserver"
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
		if !strings.Contains(got, "On macOS, pass `shell:\"bash\"` to `tools.exec_command` unless the task explicitly requires another shell") {
			t.Fatal("exec description lost macOS Bash selection guidance")
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
	for _, rule := range []string{"reset:\"slice\"", "pending sibling", "child tasks under it", "questions and requested answers stay conversational"} {
		if !strings.Contains(codeModeJournalGuidance, rule) {
			t.Errorf("journal usage guidance is missing: %s", rule)
		}
	}
	if !strings.Contains(codeModeJournalGuidance, "Use ASD-STE100") || !strings.Contains(proxy.registry.frontendGuidance, "Promise.allSettled([") {
		t.Fatal("projected guidance lost journal format or batching guidance")
	}
	for _, contract := range []string{"tools.mcp__mekugi__journal_read", "tools.mcp__mekugi__journal_mutate", "structuredContent.paths", "structuredContent.nodes", "Check `isError`",
		// Codex defers the MCP tools, so guidance carries their schema declarations.
		"type JournalMutation =", `| { before?: string; body?: string; kind?: "note" | "context"; op: "add"; title: string; under?: string; }`, "tasks?: Array<string | JournalTask>", "// Node path to read"} {
		if !strings.Contains(codeModeJournalGuidance, contract) || !strings.Contains(codeModeSubagentJournalGuidance, contract) {
			t.Errorf("shared MCP guidance missing: %s", contract)
		}
	}
	for _, retired := range []string{"await journal(", "declare function journal", "<journal-input-types", "exec-local journal helper", "Native `exec_command` or `write_stdin` journal arrays"} {
		if strings.Contains(combined, retired) || strings.Contains(codeModeSubagentJournalGuidance, retired) {
			t.Errorf("retired journal interface remains: %s", retired)
		}
	}

	for _, policy := range []string{"update affected documents before implementation", "review when warranted"} {
		if strings.Contains(combined+proxy.registry.frontendGuidance, policy) {
			t.Errorf("projected guidance retains copied workflow policy: %s", policy)
		}
	}
}

func TestJournalContextSliceReminderUsesHostUsage(t *testing.T) {
	proxy := newManagedMekugiProxy(t)
	u := newAppServerSessionTestUI(t, t.TempDir())
	u.proxy = proxy
	check := func(want bool) {
		t.Helper()
		_, _, request, _ := newMekugiTestTransformWithProxy(t, proxy)
		got := decodeResponsesToolCatalog(request.fields).additional[0].tools.tools[0].nested.tools[0].Description
		count := strings.Count(got, embeddedInstruction("journal_context_reminder"))
		if (count == 1) != want || count > 1 {
			t.Fatalf("reminder count=%d, want=%v", count, want)
		}
	}
	usage := func(thread string, used, window uint64) {
		appServerTestNotify(t, u, "thread/tokenUsage/updated", map[string]any{
			"threadId": thread, "tokenUsage": map[string]any{
				"last": map[string]any{"totalTokens": used}, "modelContextWindow": window,
			},
		})
	}
	usage("thread-1", 699, 1000)
	check(false)
	usage("thread-1", 700, 1000)
	check(true)
	appServerTestNotify(t, u, "item/completed", map[string]any{
		"threadId": "thread-1", "turnId": "reset", "item": appServerItem{ID: "reset", Type: "contextCompaction"},
	})
	check(false)
	usage("child", 900, 1000)
	check(false)
	usage("thread-1", 900, 0)
	check(false)
	// A fresh frontend restores the hint from host context facts, not live ancestry.
	u.proxy = newManagedMekugiProxy(t)
	proxy = u.proxy
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(rollout, []byte("{\"type\":\"session_meta\",\"payload\":{\"id\":\"thread-1\"}}\n{\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"total_tokens\":700},\"model_context_window\":1000}}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	u.restoreContextUsage(&activityPaneAgent{}, appServerThreadInfo{ID: "thread-1", Path: rollout})
	check(true)
	// Headless receives the same host usage, without starting another turn.
	h := &headlessAppServer{ctx: t.Context(), proxy: proxy, thread: "thread-1", reset: &journalResetDriver{workspace: u.session.cwd}}
	params := mustMarshalJSON(map[string]any{"threadId": "thread-1", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 100}, "modelContextWindow": 1000}})
	if err := h.message(appserver.Message{Method: "thread/tokenUsage/updated", Params: []byte(params)}); err != nil {
		t.Fatal(err)
	}
	check(false)
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
				if strings.Count(got, `Give each item a short, clear title or first line with no trailing punctuation. Prefer an action-subject clause with the action in bold:`) != 1 || !strings.Contains(got, `"**Completed** the task"`) {
					t.Fatalf("%s: journal item formatting guidance is missing or duplicated", phase)
				}
			}
		})
	}
	if len(codeModeSubagentJournalGuidance) >= len(codeModeJournalGuidance) || strings.Contains(codeModeSubagentJournalGuidance, "5-10 minutes") {
		t.Fatal("subagent guide retains Main's planning policy")
	}
}
