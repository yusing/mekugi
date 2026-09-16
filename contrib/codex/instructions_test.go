package codexinstructions

import (
	"strings"
	"testing"
)

func TestMekugiToolDescriptionStaysNonInstructional(t *testing.T) {
	const want = "HPATCH/2 edits and mixed edit/command execution (Code Mode required for mixed scripts). Edit validation is atomic; failed host application may have partial effects."
	if MekugiToolDescription != want {
		t.Fatalf("MekugiToolDescription = %q, want %q", MekugiToolDescription, want)
	}
}

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

func TestInstructionsTeachMixedScriptBoundaries(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			for _, required := range []string{
				"With Code Mode available, `shell COMMAND`", "`<<` is forbidden even inside quotes",
				"exact `shell <<SHELL`", "Missing closure rejects before effects",
				"Completed effects are not rolled back", "A preflight rejection applies nothing and has no continuation handle",
				"`resume HANDLE`", "`resume HANDLE retry`", "`resume HANDLE repair`", "`resume HANDLE accept`",
				"Successful recovery automatically runs the retained suffix", "one hour after creation without renewal",
				"missing confirmation does not mean rollback", "Do not replay a mixed script",
				"each edit segment with `in` or `new`", "Native-only clients use separate hpatch and shell calls",
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
			got := InstructionsForModel(model, compact)
			for _, required := range []string{
				"`<<PATCH-` removes exactly the final body terminator",
				"Literal targets own only their matched bytes",
				"count separators already at the destination",
				"deleting text alone leaves the line terminator",
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
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			for _, required := range []string{
				"Record meaningful milestones with a supported call's `journal` array", "Each mutation array is atomic",
				"The final flush is your final report", "concise, current, evidence-backed findings, results",
				"One item per distinct point; no plans, narration, or superseded progress",
				"Descendant journals are delivered automatically", "Do not repeat or summarize other agents' journals",
				"native child assignment", "set `answer: true`", "Mekugi attaches the source question; do not repeat it",
				"Do not use `update_plan`, Tasks lists, or standalone `phase: \"commentary\"` messages",
				"final-channel answer", "`{\"op\":\"finish\"}`", "This ends the turn without another model request",
				"the only call after required tool results arrive", "Do not use a wait tool to finish",
				"Complete through the finishing operation", "`journal edit ID TEXT`", "`journal finish [JSON_ARRAY]`",
				"successful terminal host result", "Failed or cancelled execution and newer user input do not finish the turn",
				"`journal add 'Tests passed' --report-now`",
				"`await journal({op: \"add\", text: \"Tests passed\", report_now: true})`", "`hhelp journal`",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("model %q compact %v omits %q", model, compact, required)
				}
			}
		}
	}
}

func TestNonAstraExecutionGuidanceKeepsAstraFocused(t *testing.T) {
	for _, compact := range []bool{false, true} {
		astra := InstructionsForModel("gpt-6-astra", compact)
		for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", ""} {
			guidance := InstructionsForModel(model, compact)
			for _, detailed := range []string{
				"For a Python-only call",
				"#!python3\nprint(\"ready\")",
				"Do not wrap that body in `python3 - <<'PY'`",
				"A compound Bash/POSIX script may still\nfeed a Python command through a heredoc",
				"Group ready commands in one multiline script",
				"when they share an interpreter and options:",
				"Known dependencies belong in that script's execution order or conditions, not separate model calls",
				"generation followed by tests can share a script that stops if generation fails",
				"Use a later call only when inspecting earlier output is necessary to choose the next command",
				"Interactive programs need separate calls",
				"wait \"$first_check_pid\" || checks_status=$?",
				"wait \"$second_check_pid\" || checks_status=$?",
				"A stale-target correction must preserve the full intended span",
			} {
				if !strings.Contains(guidance, detailed) || strings.Contains(astra, detailed) {
					t.Errorf("model %q compact %v: detailed non-Astra guidance is missing or leaked into Astra: %q", model, compact, detailed)
				}
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
			got := InstructionsForModel(model, compact)
			for _, required := range []string{
				"Batch ready work. Keep dependent operations sequential.",
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

func TestInstructionsOwnCompleteShellWorkflow(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			for _, required := range []string{
				"Tool defaults do not override the interface under test.", "Submit free-form programs directly to `functions.shell`",
				"Bash: commands without a shebang", "For a single-interpreter program",
				"submit the body directly, not inside a quoted interpreter command or shell heredoc wrapper",
				"Shell heredocs may still supply command data within compound Bash/POSIX programs",
				"There is no closing submission delimiter", "Omit `workdir` for the current workspace",
				"not `/usr/bin/env`", "`#!params=<JSON object>`", "A running outer Code Mode cell owns continuation",
				"use native session facilities for interactive input or termination", "`hhelp shell`",
			} {
				if !strings.Contains(got, required) {
					t.Errorf("model %q compact %v omits %q", model, compact, required)
				}
			}
			if strings.Contains(got, "#!batch=") || strings.Contains(got, "#!cmd=") {
				t.Fatal("advanced shell syntax must be deferred, not persistently injected")
			}
		}
	}
	shell, ok := Help("shell")
	if !ok {
		t.Fatal("shell help is unavailable")
	}
	for _, required := range []string{
		"Explicit batches require Code Mode and run sequentially", "#!batch=NEXT_PROGRAM",
		"optional interpreter selector, optional directive lines, then program source",
		"accepts exactly one `{.}` placeholder", "`hcat @shell/<reference>`",
		"only `#!script=@shell/<reference>`", "never mix retained scripts and workspace files",
		"`#!batch-stop=SEPARATOR`", "Omitted params inherit the previous complete object",
		"shell variables and `cd` changes do not carry over", "expire at the reported deadline or earlier",
		"Reads and edits do not renew them", "Save durable source in workspace files",
	} {
		if !strings.Contains(instructionWords(shell), required) {
			t.Errorf("shell help omits %q", required)
		}
	}
}

func TestInstructionsAcquireAndReuseVerifiedTargets(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		got := instructionWords(InstructionsForModel(model, true))
		for _, required := range []string{
			"`hsymbol refs PATH LINE SYMBOL [N]`", "`hsymbol def PATH LINE SYMBOL [N]`",
			"`LINE:HASH` instead of `LINE` enforces prior evidence", "Reuse current target evidence",
			"Existing-file edits require a target", "Targetless `type VALUE` is valid only immediately after",
			"unchanged saved rows remain valid even when edits shifted their line numbers",
			`type "return oldResult, nil" "return newResult, nil"`, "encode embedded LF", "u000A",
			"`line` and `line_end` are inclusive `LINE:HASH` endpoints", "`hhelp read`",
		} {
			if !strings.Contains(got, required) {
				t.Errorf("model %q omits %q", model, required)
			}
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
	const want = "\nRepair only the stale targets in the retained rejected script. Each line is a current `HANDLE` command handle followed directly by one different ordinary HPATCH/2 target. Submit every listed correction in one atomic payload. Other commands and fields are preserved. A re-rejection changes no workspace file and makes every earlier handle stale. For other corrections, use ordinary type/add mutations through functions.hpatch_recover against retained-script text.\n\n" + references
	if got := RecoveryGuidance(references); got != want {
		t.Fatalf("RecoveryGuidance() = %q, want %q", got, want)
	}
}

func TestRecoveryGuidanceWithoutReferences(t *testing.T) {
	const want = "\nRepair only the stale targets in the retained rejected script. Each line is a current `HANDLE` command handle followed directly by one different ordinary HPATCH/2 target. Submit every listed correction in one atomic payload. Other commands and fields are preserved. A re-rejection changes no workspace file and makes every earlier handle stale. For other corrections, use ordinary type/add mutations through functions.hpatch_recover against retained-script text.\n\n"
	if got := RecoveryGuidance(""); got != want {
		t.Fatalf("RecoveryGuidance() = %q, want %q", got, want)
	}
}

func TestInstructionsConsolidateDeliveredContracts(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := instructionWords(InstructionsForModel(model, compact))
			for _, required := range []string{
				"Shell workers budget combined display output automatically", "For omitted output, run the exact `next_call: hread REF`",
				"Read returned reference rows and resolve incomplete results", "Report skipped or unavailable references",
				"## Journal", "### Rejected-script recovery", "Nonempty line and range `type` replacements preserve",
				"`advisory`", "`<<TEXT` (keep final terminator)", "`continuation` notice's `next_call`",
				"Retained scripts are thread-private", "`inspect_file PATH`", "A plain line queries the current snapshot",
				"not workspace files", "reevaluate the complete script atomically",
				"Invalid corrections leave the workspace and retained baseline unchanged",
			} {
				if strings.Count(got, required) != 1 {
					t.Errorf("model %q compact %v: expected one %q", model, compact, required)
				}
			}
			for _, obsolete := range []string{
				"one complete ordinary script for non-target", "after obtaining a verified selector row",
				"selector lines are reserved even inside", "Read again only when", "one atomic hpatch script",
			} {
				if strings.Contains(got, obsolete) {
					t.Errorf("model %q retains %q", model, obsolete)
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
			for _, required := range []string{
				"edits with `hchanges amber1..amber3`", "recovery keeps that ID", "without repeating IDs or filters",
				"rather than Git diff", "do not routinely pair", "`hhelp changes`",
			} {
				if strings.Count(got, required) != 1 {
					t.Errorf("model %q compact %v: expected one %q", model, compact, required)
				}
			}
			if strings.Contains(got, "hchanges read") || strings.Contains(got, "`--path PATH`") {
				t.Fatal("obsolete change syntax")
			}
		}
	}
}

// Prose wrapping is not an interface contract; syntax-specific tests retain exact comparisons.
func instructionWords(text string) string { return strings.Join(strings.Fields(text), " ") }
