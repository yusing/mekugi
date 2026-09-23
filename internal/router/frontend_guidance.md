<!-- mekugi-frontends:start -->
<mekugi-frontends>
<usage>
Codex owns stock editing and execution. The following session-private PATH commands run through exec_command (or tools.exec_command in Code Mode); use them when useful rather than replacing ordinary shell tools.
Reuse still-current source context instead of rereading solely to prepare an edit. Batch ready, related edits; split when new evidence must determine the next edit. Budget combined reads and command output before execution.
</usage>

<tool name="mcat">
Read one or more UTF-8 files or inclusive logical-line ranges as raw rows without line or hash prefixes. Usage: `mcat [-n N] [--max-tokens N] [--tail] PATH [START:END] [PATH [START:END] ...]`. --max-tokens N sets a strict total ceiling (1–15500; default 4000). -n N selects complete lines without tokenization unless --max-tokens is also supplied. --tail requires -n or --max-tokens and selects final rows in source order. Multiple files support --max-tokens and provide per-file mread recovery. An incomplete token-limited result retains complete rows, writes stderr, and exits nonzero.
</tool>

<tool name="msymbol">
Resolve one current Go, JavaScript, TypeScript, JSON, or Python symbol and emit complete rows as `"PATH":LINE TEXT`. Before removing a field or changing a signature, use refs to acquire semantic references across affected packages and tests. Read all returned reference rows before dependent edits, continuing incomplete output; report skipped or unavailable coverage rather than treating text matches as complete caller coverage. Usage: `msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH LINE SYMBOL [N]`. LINE selects the current snapshot. ROOT sets resolver scope and relative paths without changing shell state. N selects an exact language-token occurrence. --max-tokens follows the shared 4000-token default and strict 1–15500 ceiling. Ambiguous selectors, unavailable language servers, input changes during the query, and definitions without an editable workspace location fail without stdout rows. An incomplete token-limited result retains complete rows, writes stderr, and exits nonzero.
</tool>

<tool name="inspect_file">
Inspect one host-readable regular file and return bounded JSON metadata and a structural outline. When only structure is needed, prefer an outline to a full-file read; read source only for information missing from the outline or current context. --max-tokens N sets the shared strict 1–15500 ceiling (default 4000). Recover omitted entries with mread. Outline line and line_end are one-based source line numbers.

Result shape schema:
{
  "success": {
    "ok": true,
    "data": {
      "path": "string",
      "kind": "code | markdown | json | none",
      "language": "go | javascript | typescript | python | null",
      "size_bytes": "integer",
      "line_count": "integer | null",
      "parse_complete": "boolean",
      "outline": "outline_entry[]"
    },
    "truncated": "boolean",
    "truncation": "null | {reason: output_bytes | output_tokens, after_entries: integer}"
  },
  "failure": {
    "ok": false,
    "path": "string | null",
    "error": {
      "code": "usage | not_found | not_regular | not_utf8 | read | parse | output_limit",
      "message": "string"
    }
  },
  "outline_entry": [
    {
      "kind": "import | constant | variable | type | class | function",
      "name": "string",
      "line": "integer",
      "line_end": "integer"
    },
    {
      "kind": "method",
      "name": "string",
      "receiver": "string",
      "line": "integer",
      "line_end": "integer"
    },
    {
      "kind": "heading",
      "name": "string",
      "level": "1 | 2 | 3 | 4 | 5 | 6",
      "line": "integer",
      "line_end": "integer"
    },
    {
      "kind": "frontmatter",
      "name": "string",
      "line": "integer",
      "line_end": "integer"
    },
    {
      "kind": "json",
      "pointer": "RFC 6901 string",
      "value_type": "object | array | string | number | boolean | null",
      "line": "integer",
      "line_end": "integer"
    }
  ]
}
</tool>

<tool name="mread">
Continue omitted retained output without rerunning its producer. Usage: `mread REF [--stdout|--stderr] [--max-tokens N]`. Follow an incomplete result's exact next_call; incomplete output does not establish coverage.
</tool>

<tool name="mrun">
Bound one foreground command's output, retaining its beginning or end. Use for noisy commands when a bounded head or tail is sufficient; output outside the selected window is discarded. Only an emitted mread reference recovers retained delivery overflow; ordinary exec_command truncation has no mread recovery. Usage: `mrun (-n N|--max-tokens N) [--tail] -- COMMAND [ARG...]`. Stock yielded sessions and write_stdin still own interactive continuation.
</tool>

<tool name="mchanges">
Review completed observed stock apply_patch evidence, not a Git diff or provisional preview. Usage: `mchanges --list` or `mchanges ID[..ID] ... [--summary|--history] [-- PATH ...]`. Hand reviewers explicit IDs or same-agent inclusive ranges for the requested changes, together with the review scope; --list only lists the calling thread's changes. Prefer mchanges for captured edits. Use Git for shell-generated or unrelated changes. Do not routinely pair Git diff with mchanges for the same edits; skip --summary before an already-needed diff. Omitted review output supplies mread continuation; recorded diffs are historical evidence, not proof of current workspace contents.
</tool>

</mekugi-frontends>
<!-- mekugi-frontends:end -->

<instruction id="journal_tool">
Manage the calling thread's durable milestone journal. Record distinct current results, validation, decisions, or blockers, not plans, narration, superseded progress, or summaries of other agents. Mutations return router-assigned IDs; report_now requests immediate user-visible delivery. Prefer the optional journal field on a useful ordinary tool call. In Code Mode, await journal({op: "add", text: "..."}) or pass an atomic mutation array. Use this dedicated tool for list. Once the assigned work is complete and all required tool results have been inspected, request finish, optionally with final mutations in journal. Finish can complete only with no host-dispatched calls in the same response. Successful finish ends the turn without another model request or a separate final answer; main flushes its unflushed items and a child returns its current journal to its native completion audience. Child completion automatically includes owned change ranges and aggregated numstat; do not collect them just to finish.
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
<instruction id="journal_answer_mutation">
Mark text as an answer to the latest user message or native assignment to this child; edit preserves the association when omitted and clears it when false.
</instruction>
<instruction id="journal_id">
Router-assigned item ID; required for edit and delete.
</instruction>
<instruction id="journal_text">
Required nonblank milestone text for add and edit.
</instruction>
<instruction id="journal_answer">
For add/edit, associate text with the latest user message or native assignment to this child. Omit on edit to preserve; false clears it.
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