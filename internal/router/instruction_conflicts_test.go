package router

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestConflictRewriteOnlyInstructionCarriers(t *testing.T) {
	const progress = "As you work, you send messages to the `commentary` channel."
	const planOnly = "Use the `request_user_input` tool only when it is listed in the available tools for this turn."
	const ordinary = "- To reduce round trips, batch independent searches, reads, and other tool calls in one functions.exec using await Promise.allSettled([...]); keep each batch bounded to decision-relevant output by selecting needed ranges or fields first, and inspect every returned result. If output truncates, retrieve only the missing evidence rather than repeating an unchanged whole scan. Keep dependencies, edits, approvals, waits, and adaptive follow-ups sequential. Avoid unnecessary output."
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustMarshalJSON("Lead\n" + progress + "\n```text\n" + progress + "\n```\n" + ordinary),
		"tools": mustMarshalJSON([]any{map[string]string{
			"type": "function", "name": "request_user_input", "description": "This tool is only available in Plan mode.",
		}}),
		"input": mustMarshalJSON([]any{
			map[string]any{"type": "message", "role": "developer", "content": []any{
				map[string]string{"type": "input_text", "text": planOnly + "\n" + progress},
				map[string]string{"type": "input_image", "image_url": progress},
				map[string]string{"type": "input_text", "text": "```\n" + planOnly + "\n```"},
			}},
			map[string]string{"type": "message", "role": "user", "content": progress},
			map[string]string{"type": "message", "role": "system", "content": progress},
			map[string]string{"type": "function_call_output", "output": progress},
		}),
	}}
	if err := rewriteRequestInstructionConflicts(&request); err != nil {
		t.Fatal(err)
	}
	base := jsonString(request.fields, "instructions")
	if !strings.Contains(base, "batch journal mutations") || !strings.Contains(base, "```text\n"+progress+"\n```") || !strings.Contains(base, ordinary) {
		t.Fatalf("top-level rewrite changed unrelated or fenced text: %q", base)
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(request.fields["input"], &items); err != nil {
		t.Fatal(err)
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(items[0]["content"], &parts); err != nil {
		t.Fatal(err)
	}
	first := jsonString(parts[0], "text")
	if !strings.Contains(first, "Do not call the `request_user_input` tool in Default mode") || !strings.Contains(first, "batch journal mutations") || jsonString(parts[1], "image_url") != progress || jsonString(parts[2], "text") != "```\n"+planOnly+"\n```" {
		t.Fatalf("developer multipart rewrite = %s", items[0]["content"])
	}
	for _, item := range items[1:] {
		if !strings.Contains(string(mustMarshalJSON(item)), progress) {
			t.Fatalf("non-developer content changed: %s", mustMarshalJSON(item))
		}
	}
	before := mustMarshalJSON(request.fields)
	if err := rewriteRequestInstructionConflicts(&request); err != nil || !sameJSONValue(before, mustMarshalJSON(request.fields)) {
		t.Fatalf("conflict rewrite not idempotent: %v", err)
	}
}

func TestSolLunaInstructionConflictRewrite(t *testing.T) {
	// Shared stock wording from the 2026-09-23 models cache, including its typo.
	const conflict = "Do NOT send user facing questions in intermedaite commentary messages. Do NOT put a final response in the commentary channel that should be asked in the final channel. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users."
	const replacement = "Use the user-input tools for questions when available. Record the terminal result in the journal; do not emit provider final-answer text."
	const policy = "- Do not add or run tests unless the user asks you to test or verify implementation."
	for _, newline := range []string{"\n", "\r\n"} {
		suffix := newline + "```text" + newline + conflict + newline + "```" + newline + policy
		source := conflict + suffix
		request := parsedResponsesRequest{fields: map[string]json.RawMessage{
			"instructions": mustMarshalJSON(source),
			"input": mustMarshalJSON([]any{
				map[string]any{"role": "developer", "content": []any{
					map[string]string{"type": "input_text", "text": source},
				}},
				map[string]string{"role": "user", "content": source},
				map[string]string{"role": "system", "content": source},
			}),
		}}
		if err := rewriteRequestInstructionConflicts(&request); err != nil {
			t.Fatal(err)
		}
		if got := jsonString(request.fields, "instructions"); got != replacement+suffix {
			t.Fatalf("top-level rewrite = %q", got)
		}
		wantInput := mustMarshalJSON([]any{
			map[string]any{"role": "developer", "content": []any{
				map[string]string{"type": "input_text", "text": replacement + suffix},
			}},
			map[string]string{"role": "user", "content": source},
			map[string]string{"role": "system", "content": source},
		})
		if !sameJSONValue(request.fields["input"], wantInput) {
			t.Fatalf("input rewrite = %s", request.fields["input"])
		}
		before := mustMarshalJSON(request.fields)
		if err := rewriteRequestInstructionConflicts(&request); err != nil || !sameJSONValue(before, mustMarshalJSON(request.fields)) {
			t.Fatalf("conflict rewrite not idempotent: %v", err)
		}
	}
}

func TestPlanOnlyConflictRewriteFindsNestedAdditionalTool(t *testing.T) {
	const phrase = "Use the `request_user_input` tool only when it is listed in the available tools for this turn."
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustMarshalJSON(phrase),
		"input": mustMarshalJSON([]any{
			map[string]any{"type": "additional_tools", "tools": []any{
				map[string]any{"type": "namespace", "name": "functions", "tools": []any{
					map[string]string{"type": "function", "name": "request_user_input", "description": "This tool is only available in Plan mode."},
				}},
			}},
			map[string]string{"type": "message", "role": "developer", "content": phrase},
		}),
	}}
	if err := rewriteRequestInstructionConflicts(&request); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonString(request.fields, "instructions"), "Do not call") ||
		!strings.Contains(string(request.fields["input"]), "Do not call") {
		t.Fatalf("additional Plan-only tool was not honored: %s", mustMarshalJSON(request.fields))
	}
}

func TestPlanOnlyConflictIgnoresCustomNamespace(t *testing.T) {
	const phrase = "Use the `request_user_input` tool only when it is listed in the available tools for this turn."
	request := parsedResponsesRequest{fields: map[string]json.RawMessage{
		"instructions": mustMarshalJSON(phrase),
		"input": mustMarshalJSON([]any{map[string]any{"type": "additional_tools", "tools": []any{
			map[string]any{"type": "namespace", "name": "custom", "tools": []any{
				map[string]string{"type": "function", "name": "request_user_input", "description": "This tool is only available in Plan mode."},
			}},
		}}}),
	}}
	if err := rewriteRequestInstructionConflicts(&request); err != nil {
		t.Fatal(err)
	}
	if got := jsonString(request.fields, "instructions"); got != phrase {
		t.Fatalf("custom namespace affected stock guidance: %q", got)
	}
}

func TestConflictRewriteLeavesOrdinaryStockAdvice(t *testing.T) {
	const ordinary = "- When possible, prefer parallelization over sequential tool calls, as this will help with round-trip latency and let you get work done faster."
	const generic = "- Do not make single-step plans."
	const customized = "- Use the plan tool to explain the work only when explicitly requested."
	for _, newline := range []string{"\n", "\r\n"} {
		input := ordinary + newline + generic + newline + customized + newline +
			"When using the planning tool:" + newline +
			"- Skip using the planning tool for straightforward tasks (roughly the easiest 25%)." + newline +
			generic + newline +
			"- When you made a plan, update it after having performed one of the sub-tasks that you shared on the plan." + newline +
			"- Use the plan tool to explain the work" + newline
		request := parsedResponsesRequest{fields: map[string]json.RawMessage{"instructions": mustMarshalJSON(input)}}
		if err := rewriteRequestInstructionConflicts(&request); err != nil {
			t.Fatal(err)
		}
		output := jsonString(request.fields, "instructions")
		if !strings.Contains(output, ordinary) || !strings.Contains(output, customized) ||
			strings.Count(output, generic) != 2 || strings.Contains(output, "When using the planning tool") ||
			strings.Contains(output, "- Use the plan tool to explain the work"+newline) {
			t.Fatalf("conflict rewrite kept stripped-tool guidance or changed caller advice: %q", output)
		}
	}
}

func TestConflictRewriteRestoresDecisionAndWaitGuidance(t *testing.T) {
	// Planning fragments still occur in Codex's collaboration-mode Plan template.
	// The wait fragment occurs in the previously pinned Astra stock instructions.
	for _, test := range []struct{ source, want string }{
		{"* Keep asking until you can clearly state: goal + success criteria, audience, in/out of scope, constraints, current state, and the key preferences/tradeoffs.", "* Resolve enough intent to clearly state: goal + success criteria, audience, in/out of scope, constraints, current state, and the key preferences/tradeoffs."},
		{"* Once intent is stable, keep asking until the spec is decision complete: approach, interfaces (APIs/schemas/I/O), data flow, edge cases/failure modes, testing + acceptance criteria, rollout/monitoring, and any migrations/compat constraints.", "* Once intent is stable, resolve the spec until it is decision complete: approach, interfaces (APIs/schemas/I/O), data flow, edge cases/failure modes, testing + acceptance criteria, rollout/monitoring, and any migrations/compat constraints."},
		{"You SHOULD ask many questions, but each question must:", "Ask only the questions needed to make the plan decision complete. Each question must:"},
		{"- Avoid performing blocking sleep or wait calls longer than 60 seconds, as they may prevent you from communicating with the user for their duration.", "- Use completion notifications or interruptible waits; do not shorten waits solely to record progress."},
	} {
		t.Run(test.source, func(t *testing.T) {
			const policy = "Ask for approval before publishing."
			for _, newline := range []string{"\n", "\r\n"} {
				suffix := newline + policy + newline + test.source + " Caller qualification." + newline + "```text" + newline + test.source + newline + "```"
				source := test.source + suffix
				request := parsedResponsesRequest{fields: map[string]json.RawMessage{
					"instructions": mustMarshalJSON(source),
					"input": mustMarshalJSON([]any{
						map[string]any{"role": "developer", "content": []any{map[string]string{"type": "input_text", "text": source}}},
						map[string]string{"role": "user", "content": source},
						map[string]string{"role": "system", "content": source},
					}),
				}}
				if err := rewriteRequestInstructionConflicts(&request); err != nil {
					t.Fatal(err)
				}
				if got := jsonString(request.fields, "instructions"); got != test.want+suffix {
					t.Errorf("top-level conflict not resolved: %q", got)
				}
				wantInput := mustMarshalJSON([]any{
					map[string]any{"role": "developer", "content": []any{map[string]string{"type": "input_text", "text": test.want + suffix}}},
					map[string]string{"role": "user", "content": source},
					map[string]string{"role": "system", "content": source},
				})
				if !sameJSONValue(request.fields["input"], wantInput) {
					t.Error("developer conflict unresolved or unrelated carriers changed")
				}
				before := mustMarshalJSON(request.fields)
				if err := rewriteRequestInstructionConflicts(&request); err != nil || !sameJSONValue(before, mustMarshalJSON(request.fields)) {
					t.Fatalf("conflict rewrite not idempotent: %v", err)
				}
			}
		})
	}
}

func TestFrontendGuidanceUsesAuthenticatedDescriptions(t *testing.T) {
	registry := newManagedMekugiProxy(t).registry
	guide := registry.frontendGuidance
	generated, err := os.ReadFile(filepath.Join(registry.SnapshotDir, "frontend_guidance.md"))
	if err != nil || string(generated) != guide || strings.Contains(guide, "{{") || strings.Contains(guide, "<instruction id=") {
		t.Fatalf("standalone generated frontend guidance differs from projection: %v", err)
	}
	configured := newToolPluginTestProxy(t).registry
	generated, err = os.ReadFile(filepath.Join(configured.SnapshotDir, "frontend_guidance.md"))
	pluginFrame := "<tool name=\"plugin_tool\">\nfixture plugin tool\n</tool>"
	if err != nil || string(generated) != configured.frontendGuidance || !strings.Contains(string(generated), pluginFrame) ||
		strings.Index(string(generated), pluginFrame) > strings.Index(string(generated), "</mekugi-frontends>") {
		t.Fatalf("configured frontend missing from generated Markdown: %v", err)
	}
	byName := make(map[string]toolContribution, len(registry.ordered))
	for _, contribution := range registry.ordered {
		byName[contribution.Name] = contribution
	}
	for _, name := range []string{"mcat", "inspect_file", "msymbol", "mrun", "mchanges", "mread"} {
		var specification struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(byName[name].Specification, &specification); err != nil {
			t.Fatal(err)
		}
		if specification.Description == "" || strings.Count(guide, specification.Description) != 1 {
			t.Fatalf("%s description is not projected exactly once", name)
		}
	}
	for _, removed := range []string{"hcat", "hread", "hpatch", "hchanges", "functions.shell"} {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(removed) + `\b`).MatchString(guide) {
			t.Fatalf("retired instruction %q in frontend guide", removed)
		}
	}
	stale := append([]toolContribution(nil), registry.ordered...)
	for index := range stale {
		if stale[index].Name == "mcat" {
			stale[index].Specification = mustMarshalJSON(map[string]string{"type": "custom", "name": "mcat", "description": "stale description"})
			break
		}
	}
	if _, err := frontendGuidanceFromRegistry(stale); err == nil {
		t.Fatal("accepted stale generated frontend guidance")
	}
	for _, initial := range []string{"stock tool description", "stock tool description\n" + frontendGuidanceStart + "stale" + frontendGuidanceEnd} {
		first, err := injectFrontendGuidance(initial, guide)
		if err != nil {
			t.Fatal(err)
		}
		second, err := injectFrontendGuidance(first, guide)
		if err != nil || first != second || strings.Count(second, frontendGuidanceStart) != 1 {
			t.Fatalf("frontend guidance not stable: %v", err)
		}
	}
	for _, malformed := range []string{frontendGuidanceStart, frontendGuidanceEnd, frontendGuidanceEnd + frontendGuidanceStart, frontendGuidanceStart + frontendGuidanceStart + frontendGuidanceEnd} {
		if _, err := injectFrontendGuidance(malformed, guide); err == nil {
			t.Fatalf("accepted malformed frontend markers: %q", malformed)
		}
	}
}
