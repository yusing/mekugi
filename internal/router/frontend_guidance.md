<!-- mekugi-frontends:start -->
<mekugi-frontends>
<usage>
Codex owns stock editing and execution. The following session-private PATH commands run through exec_command (or tools.exec_command in Code Mode); use them when useful rather than replacing ordinary shell tools.
Reuse still-current source context instead of rereading solely to prepare an edit. Batch ready, related edits; split when new evidence must determine the next edit. Budget combined reads and command output before execution. Give required skill and instruction reads their own exec_command in the same batch, such as a parallel call or another tools.exec_command in the same Code Mode cell, rather than a later turn: Codex truncates one call's output past max_output_tokens without an mread reference.
For parallel Code Mode commands, print labeled outputs without serializing result envelopes:
```js
const results = await Promise.allSettled([
  tools.exec_command({cmd: "mcat src/main.go 1:80", max_output_tokens: 2000}),
  tools.exec_command({cmd: "rg -n TestThing .", max_output_tokens: 2000}),
]);
const labels = ["source", "tests"];
for (let i = 0; i < results.length; i++) {
  const result = results[i];
  if (result.status === "rejected") { text(`${labels[i]}: ${result.reason}`); continue; }
  const value = result.value;
  text(`${labels[i]}: ${value.session_id ? `running session_id=${value.session_id}` : `exit_code=${value.exit_code}`}\n${value.output}`);
}
```
</usage>

<common-options>
Options apply only to commands whose Usage lists them.
- --max-tokens N (also --max-tokens=N) bounds output to 1–15500 tokens. Defaults: mcat 6000, mread 8000, msymbol, inspect_file, and mchanges 4000; mrun requires an explicit limit. For multi-file mcat, the token budget is shared across files.
- -n N selects up to N rows; --tail selects the last rows in source order and requires -n or --max-tokens. These options belong to mcat and mrun. mcat keeps its default token ceiling with -n alone and always keeps complete rows. mrun's selected token window may end within a row; delivery overflow keeps row boundaries, except oversized or unterminated generic streams use byte continuation.
- Quote paths containing spaces. An incomplete result does not establish full coverage; follow its next_call rather than rerunning the producer. mrun discards output outside its selected window.
</common-options>

<tool name="mcat">
Read one or more UTF-8 files or inclusive logical-line ranges as raw rows; --number prefixes source line numbers like nl -ba.
Usage: mcat [-n N] [--max-tokens N] [--tail] [--number] PATH [START:END ...] [PATH [START:END ...] ...]

START:END or START-END is a separate operand after its path, inclusive of both endpoints. Several ranges may follow one path; at most 16 reads are allowed. -n counts rows within a single range and retains the default token ceiling.
Examples:
  mcat src/main.go 100:150                # rows 100–150 (51 rows)
  mcat -n 20 src/main.go                  # first 20 rows
  mcat --number src/main.go 100:150       # source line prefixes
  mcat src/main.go 10:40 src/config.go 1:30

Limited output contains complete rows and exits nonzero; retained omissions provide per-file mread recovery.
</tool>

<tool name="msymbol">
Resolve current Go, JavaScript, TypeScript, JSON, or Python symbol with compact definition bodies and references grouped by file. Before removing a field or changing a signature, use refs to acquire semantic references across affected packages and tests. Read all returned reference rows before dependent edits, continuing incomplete output; report skipped or unavailable coverage rather than treating text matches as complete caller coverage. Usage: `msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH [LINE] SYMBOL [N] [(def|refs) PATH [LINE] SYMBOL [N] ...]`. PATH:LINE is also accepted. Without LINE, def and refs select the unique outline declaration named SYMBOL; locals and repeated names need LINE. Batched tuples share a server per language and one output budget. LINE selects the current snapshot. ROOT sets resolver scope and relative paths without changing shell state. N selects an exact language-token occurrence. Ambiguous selectors, unavailable language servers, input changes during the query, and definitions without an editable workspace location fail without stdout rows. An incomplete token-limited result retains complete rows, writes stderr, and exits nonzero.
</tool>

<tool name="inspect_file">
Inspect host-readable regular files and return compact structural rows: START-END KIND NAME. Imports collapse to one range; parse_error rows locate syntax errors. When only structure is needed, prefer an outline to a full-file read; read source only for information missing from the outline or current context.
Usage: inspect_file [--json] [--max-tokens N] PATH [PATH ...]
Multiple files have path headers and share one budget. --json returns metadata and structured outline entries instead. Line ranges are one-based. Example: inspect_file src/main.go, then mcat src/main.go START:END for the relevant entry. Reuse known locations instead of outlining a file again.
</tool>

<tool name="mread">
Continue omitted retained output without rerunning its producer. Only emitted references recover retained output; ordinary exec_command truncation has no mread recovery. Usage: `mread REF [REF ...] [--stdout|--stderr] [--max-tokens N]`. REF is a returned handle, not a path or range. Multiple handles share one budget and return one combined next_call. --stdout or --stderr selects one stream; otherwise both are returned. Source-row pages state their row range.
</tool>

<tool name="mrun">
Bound one foreground command's output, retaining its beginning or end. Use for noisy commands when a bounded head or tail is sufficient; output outside the selected window is discarded. Usage: `mrun (-n N|--max-tokens N) [--tail] [--] COMMAND [ARG...]`. Stock yielded sessions and write_stdin still own interactive continuation.
</tool>

<tool name="mchanges">
Review, revert, or reapply completed observed evidence from stock apply_patch, shell file operations, and supported Python/JS writes; not a Git diff or provisional preview. Usage: `mchanges --list [--workspace DIR] [--max-tokens N] | mchanges [--mine | ID[..ID] ...] [--summary|--history|--net] [--workspace DIR] [--max-tokens N] [-- PATH ...] | mchanges revert|apply ID[..ID] ... [--workspace DIR] [--max-tokens N] [-- PATH ...]`. Hand reviewers explicit IDs or same-agent inclusive ranges for the requested changes, together with the review scope; bare mchanges and --mine review the calling thread's changes; --list compresses their IDs with status and counts. --net composes selected captured diffs, not a live Git diff. Choose editing tools for the task, not capture support. Use mchanges when its evidence covers the review; use a scoped workspace diff or file inspection for uncovered paths or missing baselines. Changes observed describes captured filesystem differences, not confirmed tool success; --history shows confirmation and capture details. Missing confirmation alone does not require repeating an edit or review. Avoid duplicate reviews of the same evidence; skip --summary before an already-needed diff. `revert` undoes and `apply` replays selected changes in the workspace, git-style: drifted regions merge; overlapping edits leave conflict markers (exit 1). Output states each file relative to mchanges history, not Git (`clean`, `+N -N`, `UU` conflict, `??` unknown). The revert is itself recorded as a change; follow the printed undo line. Recorded diffs are historical evidence, not proof of current workspace contents.
</tool>

</mekugi-frontends>
<!-- mekugi-frontends:end -->

<instruction id="journal_tool">
Milestones in this durable milestone journal survive compaction. Finish naturally with a concise final answer; the router renders it with the journal as a Question and Answer flush.

Record milestones when established, not only at completion: distinct current findings, validation results, decisions, or blockers. Use journal mutations instead of commentary for milestone updates; set report_now when the user needs the update immediately. Do not duplicate the update in commentary. Plans, ongoing narration, superseded progress, and summaries of other agents are not milestones. Questions and direct conversational replies remain separate from milestone reporting.

Record mutations on a useful call, not as a separate journal call: each separate call costs another model request. Use the optional journal field on a useful ordinary tool call. In Code Mode, await journal({op: "add", text: "..."}) or pass an atomic mutation array inside the next useful exec; this tool then offers only list. For example, after inspecting a test result, record the validated outcome alongside the next useful call. Mutations return router-assigned IDs; edit an existing item when its result is superseded. Use this dedicated tool for list.

Once the assigned work is complete and all required tool results have been inspected, finish with a final answer, not a journal call. Main flushes its unflushed items and a child returns its current journal to its native completion audience. Child completion automatically includes owned change ranges and aggregated numstat; do not collect them just to finish.
</instruction>

<instruction id="journal_code_mode">
<!-- mekugi-journal:start -->
<journal>
Follow the `functions.journal` tool description for milestone, mutation, and completion rules.
</journal>
<!-- mekugi-journal:end -->
</instruction>

<instruction id="journal_mutations">
Optional atomic journal mutations applied before this operation. report_now shows progress immediately.
</instruction>
<instruction id="journal_id">
Router-assigned item ID; required for edit and delete.
</instruction>
<instruction id="journal_text">
Required nonblank milestone text for add and edit.
</instruction>
<instruction id="journal_agent">
Canonical path of a proven ancestor or descendant, for list only. Defaults to the caller.
</instruction>

<instruction id="report_issue">
Report an observed Mekugi interaction problem as Markdown to the configured diagnose hooks.
</instruction>
<instruction id="report_markdown">
The exact Markdown issue report.
</instruction>

<instruction id="collaboration_namespace">
Native agent operations use this projected namespace. Message arguments are plaintext; Codex owns agent execution, permissions, and lifecycle.
</instruction>
<instruction id="opencode_spawn">
OpenCode model overrides: %MODELS%. Use fork_turns="none" and a self-contained message; encrypted OpenAI history is unsupported. Omit reasoning_effort for provider defaults, or select an effort supported by the model catalog.
</instruction>
<instruction id="grok_model">
Grok overrides from the Codex catalog: `grok:grok-4.5`, `grok:grok-4.6`, `grok:grok-4.7`, `grok:grok-4.7-build-fast`. The build-fast variant requires Grok OAuth.
</instruction>
<instruction id="grok_fork_turns">
For Grok overrides, explicitly use "none" and include the complete task in message.
</instruction>
<instruction id="grok_reasoning_effort">
For Grok overrides: low, medium, high, or xhigh.
</instruction>
<instruction id="grok_input">
The exact tool program or input, without JSON encoding or Markdown fences.
</instruction>
<instruction id="grok_format_prefix">
The input string must obey this tool format:
</instruction>