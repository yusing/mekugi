package codexinstructions

import (
	"strings"
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestInstructionsSelectModelWorkflowIndependentlyOfTransport(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6-astra-2026-09-01", "gpt-5.6-sol", "gpt-6-astral", "other-astra", ""} {
		for _, compact := range []bool{false, true} {
			got := InstructionsForModel(model, compact)
			astra := model == "gpt-6-astra" || model == "gpt-6-astra-2026-09-01"
			workflow, excluded := defaultWorkflow, astraWorkflow
			if astra {
				workflow, excluded = astraWorkflow, defaultWorkflow
			}
			if !strings.Contains(got, strings.TrimSuffix(workflow, "\n")) || strings.Contains(got, strings.TrimSuffix(excluded, "\n")) {
				t.Fatalf("model %q compact %v: wrong workflow", model, compact)
			}
			if strings.Contains(got, "## CTP/2 transport") != compact {
				t.Fatalf("model %q compact %v: wrong transport guidance", model, compact)
			}
			for _, heading := range []string{"## File editing\n", "## Journal\n", "## Shell execution\n", "## Edit planning\n", "## Target reuse\n", "## Target acquisition\n"} {
				if strings.Count(got, heading) != 1 {
					t.Fatalf("model %q: workflow section %q must occur once", model, heading)
				}
			}
			for _, marker := range []string{"<!-- mekugi-model-instructions:start -->", "<!-- mekugi-model-instructions:end -->"} {
				if strings.Count(got, marker) != 1 {
					t.Fatalf("model %q: marker %q must occur once", model, marker)
				}
			}
			// Only the workflow varies; syntax and private-tool contracts remain shared.
			baseline := InstructionsForModel("", compact)
			if strings.Replace(got, strings.TrimSuffix(workflow, "\n"), "", 1) != strings.Replace(baseline, strings.TrimSuffix(defaultWorkflow, "\n"), "", 1) {
				t.Fatalf("model %q compact %v: shared guidance changed", model, compact)
			}
		}
	}
}

func TestInstructionsTeachStandaloneShellEdits(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			for _, required := range []string{
				"`hpatch [SCRIPT]` through `functions.shell`",
				"standalone shell command",
				"normal shell semantics",
				"`hpatch --recover HANDLE [SCRIPT]`",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("model %q compact %v omits %q", model, compact, required)
				}
			}
		}
	}
}

func TestInstructionsTeachExplicitNewlineOwnership(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			for _, required := range []string{
				"Values: JSON-compatible strings or heredoc.", "Literal targets own only matched bytes",
				"count separators already at the destination", "deleting text alone leaves its line terminator",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("model %q compact %v omits %q", model, compact, required)
				}
			}
		}
	}
}

func TestInstructionsOwnCTP2Representation(t *testing.T) {
	for _, required := range []string{
		"## CTP/2 transport",
		"CTP/2 is an inline representation used in some model-visible strings",
		"CTP itself requires no inspection or tool call",
		"including all CTP/1 text",
		"A content-local dictionary and its reference body occupy one string",
		"Each `ID=VALUE` line defines",
		"Expand `@{ID}`",
		"`@@{ID}` is literal",
		"`@{ID}`, and every other `@` is literal",
		"The dictionary is local to that one string",
		"A visible-line representation may reuse exact lines",
		"`=SUFFIX,START,COUNT`",
		"`+JSON_STRING`",
		"compaction removes sources that are no longer visible",
		"`!ctp2 L` plus a line feed starts literal text",
		"Newly emitted tool names, tool inputs, and function arguments are literal",
		"Every decoded byte is final text",
	} {
		if !strings.Contains(InstructionsForModel("", true), required) {
			t.Errorf("default instructions omit CTP representation rule %q", required)
		}
	}
}

func TestNativeInstructionsOmitOnlyCTPRepresentation(t *testing.T) {
	native := InstructionsForModel("", false)
	if strings.Contains(native, "## CTP/2 transport") || strings.Contains(native, "!ctp2") || strings.Contains(native, "!V=") {
		t.Fatal("native instructions contain CTP guidance")
	}
	for _, required := range []string{
		"<!-- mekugi-model-instructions:start -->",
		"## File editing",
		"## Shell execution",
		"<!-- mekugi-model-instructions:end -->",
	} {
		if !strings.Contains(native, required) {
			t.Errorf("native instructions omit %q", required)
		}
	}
}

func TestInstructionsExposeJournalAuthoring(t *testing.T) {
	for _, test := range []struct {
		name         string
		instructions string
	}{
		{name: "CTP", instructions: InstructionsForModel("", true)},
		{name: "native", instructions: InstructionsForModel("", false)},
		{name: "astra CTP", instructions: InstructionsForModel("gpt-6-astra", true)},
		{name: "astra native", instructions: InstructionsForModel("gpt-6-astra", false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			guidance := instructionWords(test.instructions)
			for _, required := range []string{
				"Record meaningful milestones in a supported useful call's `journal` array", "`functions.journal`",
				"Each mutation array is atomic", "The final flush is your final report",
				"concise, current, evidence-backed findings, results", "one item per distinct point; no plans, narration, or superseded progress",
				"Descendant journals are delivered automatically", "do not repeat or summarize other agents' journals",
				"plaintext native assignment", "Set `answer: true`", "Mekugi attaches the source",
				"on edit to preserve", "Do not use `update_plan`, Tasks lists", "final-channel answers",
				"`{\"op\":\"finish\"}`", "only call after required results", "wait tools to finish",
				"Complete this operation for subagent assignments", "journal list [AGENT]", "journal edit ID TEXT",
				"journal delete ID", "journal batch JSON_ARRAY", "journal finish [JSON_ARRAY]",
				"Add writes its assigned item ID",
				"successful final shell invocation", "Required operands remain exact argv values",
				"`journal add 'Tests passed' --report-now`", "`await journal({op: \"add\", text: \"Tests passed\", report_now: true})`",
			} {
				if !strings.Contains(guidance, required) {
					t.Errorf("instructions omit journal rule %q", required)
				}
			}
			if strings.Contains(guidance, "--json") {
				t.Error("instructions expose removed journal --json option")
			}
		})
	}
}

func TestNonAstraExecutionGuidanceKeepsAstraFocused(t *testing.T) {
	for _, compact := range []bool{false, true} {
		sol := InstructionsForModel("gpt-5.6-sol", compact)
		astra := InstructionsForModel("gpt-6-astra", compact)
		for _, detailed := range []string{
			"when they share an interpreter and options:",
			"wait \"$first_check_pid\" || checks_status=$?",
			"wait \"$second_check_pid\" || checks_status=$?",
			"A stale-target correction must preserve the full intended span",
		} {
			if !strings.Contains(sol, detailed) || strings.Contains(astra, detailed) {
				t.Errorf("compact %v: detailed non-Astra guidance is missing or leaked into Astra: %q", compact, detailed)
			}
		}
		if !strings.Contains(astra, "Group ready reads and searches in one multiline script") {
			t.Errorf("compact %v: Astra omits concise execution guidance", compact)
		}
	}
}

func TestInstructionsBatchReadyWorkWithoutHpatchIsolation(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			for _, required := range []string{
				"Batch ready work; keep dependent operations sequential.",
				"or a validation result must determine the next edit",
				"Budget combined reads and searches before execution",
			} {
				if !strings.Contains(got, required) {
					t.Fatalf("model %q compact %v omits batching guidance %q", model, compact, required)
				}
			}
			for _, obsolete := range []string{"Run hpatch alone", "run hpatch alone", "Do not call hpatch in parallel", "Do not call this tool in parallel", "Keep dependent work and mutations sequential", "Do not overlap dependent commands, edits"} {
				if strings.Contains(got, obsolete) {
					t.Fatalf("model %q compact %v retains hpatch isolation: %q", model, compact, obsolete)
				}
			}
			if strings.Contains(got, "prefer one batch for ready, independent") {
				t.Fatalf("model %q compact %v steers independent commands into separate sequential executions", model, compact)
			}
		}
	}
}

func TestInstructionsShellInterpreterBatches(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
		for _, compact := range []bool{false, true} {
			got := InstructionsForModel(model, compact)
			for _, required := range []string{
				"echo hello\n#!python3\nprint(\"hello\")",
				"Each new column-zero `#!interpreter` line starts a program",
				"Prefer these batches over separate shell calls for noninteractive programs.",
				"Batches continue after nonzero",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("model %q compact %v missing %q", model, compact, required)
				}
			}
			if strings.Contains(got, "#!batch") {
				t.Errorf("model %q compact %v retains old batch syntax", model, compact)
			}
		}
	}
}

func TestInstructionsOwnCompleteShellWorkflow(t *testing.T) {
	for _, required := range []string{
		"Use separate shell calls for interactive programs",
		"Explicit Code Mode batches run programs sequentially",
		"#!params={\"yield_time_ms\":1000}\necho hello\n#!python3\nprint(\"hello\")",
		"Tool defaults do not override the interface under test.",
		"Tool coordination below covers native-interface tasks.",
		"Submit free-form programs to `functions.shell`",
		"Bash: write commands directly, without a shebang.",
		"shell heredoc such as `python3 - <<'PY'`",
		"There is no closing delimiter.",
		"optional interpreter selector, optional directive lines, then program source",
		"Omit `workdir` to use the current workspace",
		"not `/usr/bin/env`",
		"Write compound Bash/POSIX programs directly, using ordinary shell pipelines and redirections.",
		"`#!params=<JSON object>`",
		"use native session facilities for interactive input or termination",
		"Each new column-zero `#!interpreter` line starts a program",
		"Omitted params inherit the previous complete object",
		"Variables and `cd` do not carry over",
		"Use ordinary script files for source that needs repeated editing or execution",
	} {
		for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
			for _, compact := range []bool{false, true} {
				if !strings.Contains(instructionWords(InstructionsForModel(model, compact)), instructionWords(required)) {
					t.Errorf("model %q compact %v: instructions omit shell workflow %q", model, compact, required)
				}
			}
		}

	}
}

func TestInstructionsDoNotAdvertiseRetainedShellSource(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
		for _, compact := range []bool{false, true} {
			guidance := InstructionsForModel(model, compact)
			for _, obsolete := range []string{"@shell/", "#!script=", "#!cmd", "runner placeholder", "script_ref", "Retained scripts are thread-private"} {
				if strings.Contains(guidance, obsolete) {
					t.Errorf("model %q compact %v advertises removed source retention: %q", model, compact, obsolete)
				}
			}
		}
	}
}

func TestInstructionsAcquireAndReuseVerifiedTargets(t *testing.T) {
	guidance := instructionWords(InstructionsForModel("", true))
	for _, required := range []string{
		"Acquire target-bearing context for existing-file edits", "use `-F` with repeated `-e` literals",
		"Copy inspect_file `LINE:HASH` spans", "`hsymbol refs PATH LINE SYMBOL [N]`",
		"`hsymbol def PATH LINE SYMBOL [N]`", "`LINE:HASH` instead of `LINE` to enforce a prior read",
		"`--workspace ROOT` selects resolver scope", "Read again only when those forms no longer identify the intended current span",
		"Existing-file edits require a target", "Targetless `type VALUE` is allowed only immediately after `new`",
		"Unchanged saved rows remain valid after line shifts", "Copy complete `LINE:HASH` endpoints from the intended span",
		`maple "return oldResult, nil"`, "Encode an embedded LF as `\\n` or `\\u000A`",
	} {
		if !strings.Contains(guidance, required) {
			t.Errorf("default instructions omit target acquisition rule %q", required)
		}
	}
}

func TestInstructionsStayWithinMekugiAndPrivateTools(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
		for _, excluded := range []string{
			"behavioral validation",
			"behavior-defining helper or callee semantics",
			"trace one concrete boundary",
			"imports inside the existing import declaration",
			"approval checkpoint",
			"required checks pass",
			"Keep progress updates brief",
			"complete task's correctness",
			"`git diff` on a changed file",
			"execution history",
		} {
			if strings.Contains(InstructionsForModel(model, true), excluded) {
				t.Errorf("model %q contains out-of-scope guidance %q", model, excluded)
			}
		}
	}
}

func TestRecoveryGuidanceRendersDynamicReferences(t *testing.T) {
	const references = "Rejected target commands:\n"
	const want = "\nRepair only the stale targets in the retained rejected script. Each line is a current `HANDLE` command handle followed directly by one different ordinary HPATCH/2 target. Submit every listed correction in one atomic payload. Other commands and fields are preserved. A re-rejection changes no workspace file and makes every earlier handle stale. For other corrections, use ordinary type/add mutations through hpatch --recover HANDLE against retained-script text.\n\n" + references
	if got := RecoveryGuidance(references); got != want {
		t.Fatalf("RecoveryGuidance() = %q, want %q", got, want)
	}
}

func TestRecoveryGuidanceWithoutReferences(t *testing.T) {
	const want = "\nRepair only the stale targets in the retained rejected script. Each line is a current `HANDLE` command handle followed directly by one different ordinary HPATCH/2 target. Submit every listed correction in one atomic payload. Other commands and fields are preserved. A re-rejection changes no workspace file and makes every earlier handle stale. For other corrections, use ordinary type/add mutations through hpatch --recover HANDLE against retained-script text.\n\n"
	if got := RecoveryGuidance(""); got != want {
		t.Fatalf("RecoveryGuidance() = %q, want %q", got, want)
	}
}

func TestInstructionsConsolidateDeliveredContracts(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			for _, required := range []string{
				"Shell workers budget combined display output automatically",
				"For omitted output, run the exact `next_call: hread REF`",
				"read all returned reference rows before batching dependent edits",
				"Report skipped or unavailable references",
				"## Journal",
				"### Rejected-script recovery",
				"Nonempty line and range `type` replacements preserve",
				"`advisory`",
				"Values: JSON-compatible strings or heredoc.",
				"`continuation` notice's `next_call`",
				"`--preview-bytes N`",
				"`inspect_file PATH` for bounded metadata",
				"Plain lines query the current snapshot",
				"For a parsed command's target or value",
				"not workspace files",
				"Keep the two payload forms separate",
				"reevaluate the complete script atomically",
				"use its script rows and refreshed command handles",
				"Invalid corrections change neither workspace nor retained baseline",
			} {
				if strings.Count(got, required) != 1 {
					t.Errorf("model %q compact %v: contract %q must occur once", model, compact, required)
				}
			}
			for _, obsolete := range []string{
				"one complete ordinary script for non-target",
				"after obtaining a verified selector row",
				"selector lines are reserved even inside",
			} {
				if strings.Contains(got, obsolete) {
					t.Errorf("model %q compact %v: superseded guidance %q", model, compact, obsolete)
				}
			}
		}
	}
}

func TestInstructionsTeachBoundedCommandOutputAndTailReads(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := InstructionsForModel(model, compact)
			for _, required := range []string{
				"hrun [-n N] [--max-tokens N] [--tail] -- COMMAND [ARG...]",
				"preserves the command's exit status",
				"`hcat [-n N] [--tail]`",
				"first/last complete rows",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("model=%q compact=%t missing %q", model, compact, required)
				}
			}
		}
	}
}

func TestInstructionsTeachCompactChangeHandoffs(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol", "grok:grok-4.6"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			if strings.Contains(got, "hchanges read") || strings.Contains(got, "`--path PATH`") {
				t.Errorf("model %q compact %v: obsolete change command syntax", model, compact)
			}
			for _, required := range []string{"edits with `hchanges amber1..amber3`", "recovery keeps that ID", "--history", "without repeating IDs or filters", "rather than Git diff", "skip it before an already-needed", "do not routinely pair"} {
				if strings.Count(got, required) != 1 {
					t.Errorf("model %q compact %v: expected one %q", model, compact, required)
				}
			}
		}
	}
}

func TestInstructionsStayWithinTokenBudget(t *testing.T) {
	codec, err := tokenizer.ForModel(tokenizer.GPT5)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		model   string
		compact bool
		max     int
	}{
		{model: "gpt-6-astra", max: 3900},
		{model: "gpt-5.6-sol", max: 4050},
		{model: "gpt-6-astra", compact: true, max: 4400},
		{model: "gpt-5.6-sol", compact: true, max: 4550},
	} {
		got, err := codec.Count(InstructionsForModel(test.model, test.compact))
		if err != nil {
			t.Fatal(err)
		}
		if got > test.max {
			t.Errorf("model %q compact %v: %d tokens exceeds budget %d", test.model, test.compact, got, test.max)
		}
	}
}

func instructionWords(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func TestInstructionsTeachMultiFileHcat(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := InstructionsForModel(model, compact)
			for _, required := range []string{
				"`hcat [--max-tokens N] PATH [START:END] [PATH [START:END] ...]`",
				"ranges apply to the preceding path",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("model=%q compact=%t missing %q", model, compact, required)
				}
			}
			if strings.Contains(got, "hcat --batch") {
				t.Errorf("model=%q compact=%t teaches retired batch syntax", model, compact)
			}
		}
	}
}
