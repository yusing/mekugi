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
			got := InstructionsForModel(model, compact)
			for _, required := range []string{
				"`shell go test ./...`",
				"`<<` is not allowed",
				"never falls back to single-line execution",
				"exact `shell <<SHELL` header",
				"Completed\nedits and shell effects are not rolled back",
				"`resume HANDLE`",
				"`resume HANDLE retry`",
				"Successful recovery automatically runs the retained suffix in the same carrier",
				"`resume HANDLE repair`",
				"`resume HANDLE accept`",
				"one hour from creation",
				"partial or unknown effects",
				"Do not replay a mixed script",
				"each edit segment with `in` or `new`",
				"Native-only clients use separate hpatch and shell calls",
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
			for _, required := range []string{
				"Record meaningful milestones on a supported tool call",
				"`functions.journal`",
				"Each mutation array is atomic",
				"Use one item per checkpoint or milestone",
				"findings, results, validation, or blockers, not plans",
				"Edit or delete superseded entries",
				"The final flush is your final report",
				"with claims supported by the work completed",
				"set `answer: true`",
				"do not repeat it in tool arguments",
				"omit `answer` to preserve the attached question",
				"Do not use `update_plan`, Tasks lists, or standalone `phase: \"commentary\"` messages",
				"do not write a final-channel answer",
				"`{\"op\":\"finish\"}`",
				"This ends the turn without another model request",
				"Do not use a wait tool to finish",
				"Complete through that call, without a separate final-channel message",
				"`journal add 'Tests passed' --report-now`",
				"`await journal({op: \"add\", text: \"Tests passed\", report_now: true})`",
			} {
				if !strings.Contains(test.instructions, required) {
					t.Errorf("instructions omit journal rule %q", required)
				}
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

func TestInstructionsDoNotPreferSequentialBatchesForIndependentCommands(t *testing.T) {
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
		for _, compact := range []bool{false, true} {
			got := InstructionsForModel(model, compact)
			if strings.Contains(got, "prefer one batch for ready, independent") {
				t.Fatalf("model %q compact %v steers independent commands into separate sequential executions", model, compact)
			}
		}
	}
}

func TestInstructionsOwnCompleteShellWorkflow(t *testing.T) {
	for _, required := range []string{
		"Use separate shell calls for interactive programs",
		"Explicit batches require Code Mode and run sequentially",
		"#!batch=NEXT_PROGRAM\n#!params={\"yield_time_ms\":1000}\necho hello\nNEXT_PROGRAM\n#!python3\nprint(\"hello\")",
		"Tool defaults do not override the interface under test.",
		"Tool coordination below covers native-interface tasks.",
		"Submit free-form programs to `functions.shell`",
		"Bash: write commands directly, without a shebang.",
		"a shell heredoc such as `python3 - <<'PY'`",
		"There is no closing delimiter.",
		"optional interpreter selector, optional directive lines, then program source",
		"Omit `workdir` to use the current workspace.",
		"rather than `/usr/bin/env`",
		"accepts exactly one `{.}` placeholder",
		"`#!params=<JSON object>`",
		"`hcat @shell/<reference>`",
		"only `#!script=@shell/<reference>`",
		"never mix retained scripts and workspace files",
		"use native session facilities for interactive input or termination",
		"`#!batch-stop=SEPARATOR`",
		"Omitted params inherit the previous complete object",
		"shell variables and `cd` changes do not carry over",
		"expire at the reported deadline or earlier",
		"Reads and edits do not renew them",
		"Save durable source in workspace files",
	} {
		for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
			for _, compact := range []bool{false, true} {
				if !strings.Contains(InstructionsForModel(model, compact), required) {
					t.Errorf("model %q compact %v: instructions omit shell workflow %q", model, compact, required)
				}
			}
		}

	}
}

func TestInstructionsAcquireAndReuseVerifiedTargets(t *testing.T) {
	for _, required := range []string{
		"Acquire target-bearing context for existing-file edits.",
		"use `-F` with repeated `-e` literals",
		"Copy inspect_file `LINE:HASH` spans",
		"`hsymbol refs PATH LINE SYMBOL [N]`",
		"`hsymbol def PATH LINE SYMBOL [N]`",
		"`LINE:HASH` instead of `LINE` to enforce a prior read",
		"A leading `--workspace ROOT` chooses resolver scope",
		"Read again only when those forms no longer identify the intended current span",
		"Existing-file edits require a target.",
		"Targetless `type VALUE` is valid only immediately after",
		"unchanged saved rows remain valid even when edits shifted their line numbers",
		"Copy complete `LINE:HASH` endpoints from the intended span",
		`type "return oldResult, nil" "return newResult, nil"`,
		`C3:bcde0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab "return oldResult, nil"`,
		"exact known target text spans logical lines or includes a trailing LF",
	} {
		if !strings.Contains(InstructionsForModel("", true), required) {
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
	const want = "\nRepair only the stale targets in the retained rejected script. Each line is a current `C...` command handle followed directly by one different ordinary HPATCH/2 target. Submit every listed correction in one atomic payload. Other commands and fields are preserved. A re-rejection changes no workspace file and makes every earlier handle stale. For other corrections, use ordinary type/add mutations through functions.hpatch_recover against retained-script text.\n\n" + references
	if got := RecoveryGuidance(references); got != want {
		t.Fatalf("RecoveryGuidance() = %q, want %q", got, want)
	}
}

func TestRecoveryGuidanceWithoutReferences(t *testing.T) {
	const want = "\nRepair only the stale targets in the retained rejected script. Each line is a current `C...` command handle followed directly by one different ordinary HPATCH/2 target. Submit every listed correction in one atomic payload. Other commands and fields are preserved. A re-rejection changes no workspace file and makes every earlier handle stale. For other corrections, use ordinary type/add mutations through functions.hpatch_recover against retained-script text.\n\n"
	if got := RecoveryGuidance(""); got != want {
		t.Fatalf("RecoveryGuidance() = %q, want %q", got, want)
	}
}

func TestInstructionsConsolidateDeliveredContracts(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-5.6-sol"} {
		for _, compact := range []bool{false, true} {
			got := InstructionsForModel(model, compact)
			for _, required := range []string{
				"## Journal\n",
				"### Rejected-script recovery\n",
				"Nonempty line and range `type` replacements preserve",
				"`advisory`",
				"`<<TEXT` (keep final terminator)",
				"`continuation` notice's `next_call`",
				"Retained scripts are thread-private",
				"`--preview-bytes N`",
				"`inspect_file PATH` for bounded metadata",
				"plain lines query the\ncurrent snapshot",
				"For values, framing, paths, conflicting commands, or mixed corrections",
				"not workspace files",
				"keep the two payload forms separate",
				"reevaluate the complete script atomically",
				"use its script rows and refreshed command handles",
				"Invalid corrections leave the workspace and retained baseline unchanged",
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
			got := InstructionsForModel(model, compact)
			for _, required := range []string{"hchanges read hp_a1..hp_a3", "recovery keeps that ID", "--history", "--cursor HASH:BYTE", "rather than Git diff", "not before an already-needed", "do not routinely pair", "Flags may appear before or after IDs"} {
				if strings.Count(got, required) != 1 {
					t.Errorf("model %q compact %v: expected one %q", model, compact, required)
				}
			}
		}
	}
}
