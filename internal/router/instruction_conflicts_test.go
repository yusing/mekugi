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
	if strings.Count(base, progress) != 1 || !strings.Contains(base, "```text\n"+progress+"\n```") || !strings.Contains(base, ordinary) {
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
	planRule, _, _ := strings.Cut(first, "\n")
	const restricted = planOnly
	if planRule != restricted || strings.Contains(first, progress) || jsonString(parts[1], "image_url") != progress || jsonString(parts[2], "text") != "```\n"+planOnly+"\n```" {
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
	const replacement = ""
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
			output != input {
			t.Fatalf("conflict rewrite kept stripped-tool guidance or changed caller advice: %q", output)
		}
	}
}

func TestConflictRewriteRestoresWaitGuidance(t *testing.T) {
	// The wait fragment occurs in the previously pinned Astra stock instructions.
	for _, test := range []struct{ source, want string }{
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

func TestCompletionConflictsLeaveJournalAsSoleOwner(t *testing.T) {
	for _, conflict := range []string{
		"Do NOT send user facing questions in intermediate commentary messages. Do NOT put a final response in the commentary channel. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
		"Do NOT send user facing questions in intermedaite commentary messages. Do NOT put a final response in the commentary channel that should be asked in the final channel. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
		"Do NOT put a final response (e.g. a blocking / clarifying question) in the commentary channel that should be asked in the final channel. Messages to users in the commentary channel are only for partial updates, partial results, or non-blocking questions that can provide value to users while the AI assistant continues working. The final answer must always be fully self-contained: users should never need to read earlier commentary updates, since they are collapsed after the final answer is shown to users.",
		"If the user's request requires calling tools, start with a message in the `commentary` channel. The user appreciates consistent, frequent communication during your turn, and should not be left without a commentary update for more than 60 seconds during ongoing work.",
	} {
		request := parsedResponsesRequest{fields: map[string]json.RawMessage{
			"instructions": mustMarshalJSON(conflict),
			"input":        mustMarshalJSON([]any{map[string]string{"role": "developer", "content": conflict}}),
		}}
		if err := rewriteRequestInstructionConflicts(&request); err != nil {
			t.Fatal(err)
		}
		if got := jsonString(request.fields, "instructions"); got != "" {
			t.Fatalf("completion conflict replaced with redundant guidance: %q", got)
		}
		var input []map[string]json.RawMessage
		if err := json.Unmarshal(request.fields["input"], &input); err != nil {
			t.Fatal(err)
		}
		if got := jsonString(input[0], "content"); got != "" {
			t.Fatalf("developer completion conflict remains: %q", got)
		}
		qualified := conflict + " Caller qualification."
		request.fields["instructions"] = mustMarshalJSON(qualified)
		if err := rewriteRequestInstructionConflicts(&request); err != nil || jsonString(request.fields, "instructions") != qualified {
			t.Fatalf("caller qualification changed: %v", err)
		}
	}
}

func TestCachedStockProgressConflicts(t *testing.T) {
	// Stock paragraphs from Codex 0.160.0 models_cache.json, 2026-10-05.
	for _, conflict := range []string{
		"- You yield back to the user and end your turn by sending a final message to the `final` channel.",
		"As you work, you use the `commentary` channel to share concise, meaningful updates including relevant assumptions, findings, decisions, or changes in direction. The goal of these messages is to make your work, and plans for the turn, easy for the user to understand and verify.",
		"As you work, you send messages to the `commentary` channel. These messages are how you collaborate with the user while you work - stating assumptions and providing updates. These messages should be concise and quickly scannable. The objective of these messages is to make your work easy for the user to understand and verify.",
		"- Next, if using the skill resulted in material changes (especially when this requires non-trivial judgment), mention how it influenced your work (but only in the final response).",
	} {
		for _, newline := range []string{"\n", "\r\n"} {
			suffix := newline + conflict + " Caller qualification." + newline + "```text" + newline + conflict + newline + "```"
			request := parsedResponsesRequest{fields: map[string]json.RawMessage{"instructions": mustMarshalJSON(conflict + suffix)}}
			if err := rewriteRequestInstructionConflicts(&request); err != nil {
				t.Fatal(err)
			}
			if got := jsonString(request.fields, "instructions"); got != suffix {
				t.Fatalf("stock conflict or caller text changed incorrectly: %q", got)
			}
		}
	}
}

func TestConfiguredFrontendRejectsFramingTags(t *testing.T) {
	for _, description := range []string{"Usage: launch <tool_name> [args...]", "Types: <toolbox> and <mekugi-frontends-path>", "A <tool_name> then </tool>"} {
		_, err := frontendGuidanceFromRegistry([]toolContribution{{Name: "fixture", Executable: true, Specification: mustMarshalJSON(map[string]string{"description": description})}})
		if (err != nil) != strings.Contains(description, "</tool>") {
			t.Fatalf("description %q: %v", description, err)
		}
	}

	for _, tag := range []string{"<tool name=\"forged\">", "</tool>", "<mekugi-frontends>", "</mekugi-frontends>"} {
		_, err := frontendGuidanceFromRegistry([]toolContribution{{Name: "fixture", Executable: true, Specification: mustMarshalJSON(map[string]string{"description": "prefix " + tag + " suffix"})}})
		if err == nil {
			t.Fatalf("accepted framing tag %s", tag)
		}
	}
}
