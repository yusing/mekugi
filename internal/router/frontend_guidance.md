<!-- mekugi-frontends:start -->
<mekugi-frontends>
<usage>
Codex owns stock editing and execution. The following session-private PATH commands run through tools.exec_command; use them when useful rather than replacing ordinary shell tools.
On macOS, pass `shell:"bash"` to `tools.exec_command` unless the task explicitly requires another shell; Codex may otherwise select Zsh even when the login shell is Fish.
These commands support shell pipes, for example `mchanges amber1..amber5 --net | git apply --cached -p0`.
Keep command output in tool results; redirect to a file only when the task needs that artifact, not for display preference. The user can see exec output; responses add only useful interpretation or requested information beyond that output. Disposable logs pollute change history and diff stats.
Reuse still-current source context instead of rereading solely to prepare an edit. Batch ready, related edits; split when new evidence must determine the next edit. Budget combined reads and command output before execution. Give required skill and instruction reads their own exec_command in the same batch, such as a parallel call or another tools.exec_command in the same exec cell, rather than a later turn: Codex truncates one call's output past max_output_tokens without an mread reference.
For parallel exec commands, print labeled outputs without serializing result envelopes:
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
Resolve current Go, JavaScript, TypeScript, JSON, or Python symbol with compact definition bodies and references grouped by file. Before removing a field or changing a signature, use refs to acquire semantic references across affected packages and tests. Read all returned reference rows before dependent edits, continuing incomplete output; report skipped or unavailable coverage rather than treating text matches as complete caller coverage. Usage: `msymbol [--max-tokens N] [--workspace ROOT] (def|refs) PATH [LINE] SYMBOL [N] [(def|refs) PATH [LINE] SYMBOL [N] ...]`. PATH:LINE is also accepted. Without LINE, def and refs select the unique outline declaration named SYMBOL; locals and repeated names need LINE. Batched tuples share a server per language and one output budget; independent tuples continue after a failure. LINE selects the current snapshot. ROOT sets resolver scope and relative paths without changing shell state. N selects an exact language-token occurrence. Ambiguous selectors, unavailable language servers, input changes during the query, and definitions without an editable workspace location fail only the affected tuple, reporting errors on stderr while successful tuples remain on stdout. An incomplete token-limited result retains complete rows, writes stderr, and exits nonzero.
</tool>

<tool name="inspect_file">
Inspect host-readable regular files and return compact structural rows: START-END KIND NAME. Imports collapse to one range; parse_error rows locate syntax errors. When only structure is needed, prefer an outline to a full-file read; read source only for information missing from the outline or current context.
Usage: inspect_file [--json] [--max-tokens N] PATH [PATH ...]
Multiple files have path headers and share one budget. Failed paths report errors on stderr (JSON envelopes with --json), preserve successful stdout, and exit nonzero. --json returns metadata and structured outline entries instead. Line ranges are one-based. Example: inspect_file src/main.go, then mcat src/main.go START:END for the relevant entry. Reuse known locations instead of outlining a file again.
</tool>

<tool name="mread">
Continue omitted retained output without rerunning its producer. Only emitted references recover retained output; ordinary exec_command truncation has no mread recovery. Usage: `mread REF [REF ...] [--stdout|--stderr] [--max-tokens N]`. REF is a returned handle, not a path or range. Multiple handles share one budget and return one combined next_call. Failed handles report errors on stderr without discarding other handles' stdout; any failure exits nonzero. --stdout or --stderr selects one stream; otherwise both are returned. Source-row pages state their row range.
</tool>

<tool name="mrun">
Bound one foreground command's output, retaining its beginning or end. Use for noisy commands when a bounded head or tail is sufficient; output outside the selected window is discarded. Usage: `mrun (-n N|--max-tokens N) [--tail] [--] COMMAND [ARG...]`. Stock yielded sessions and write_stdin still own interactive continuation.
</tool>

<tool name="mchanges">
Review, revert, or reapply captured filesystem changes from stock apply_patch, shell file operations, and supported Python/JS writes; not a Git diff or provisional preview. Usage: `mchanges --list [ID[..ID] ...] [--workspace DIR] [--max-tokens N] | mchanges [--mine | ID[..ID] ...] [--summary|--net] [--workspace DIR] [--max-tokens N] [-- PATH ...] | mchanges revert|apply ID[..ID] ... [--workspace DIR] [--max-tokens N] [-- PATH ...]`. Reuse the change IDs supplied with completed edit receipts; do not run --list when those IDs are already available. Use `mchanges --list` only to recover missing IDs. For handoffs and review requests, give explicit mchanges IDs or same-agent inclusive ranges and the review scope, not vague ‘diff’, ‘pending changes’, or ‘pending diff’ labels. Bare mchanges, --mine, and --list without IDs are scoped to the calling thread, not whole-workspace coverage; --list compresses the caller's IDs with counts; --summary shows diff-pane file statuses and per-path counts. --net composes selected captured diffs, not a live Git diff. Choose editing tools for the task, not capture support. Use mchanges for captured changes. An empty caller-scoped list or a missing completion range does not establish capture failure: obtain the author’s explicit range and inspect it before falling back to Git. Use a scoped workspace diff or file inspection only for identified uncovered paths or missing baselines. Avoid duplicate reviews of the same evidence; skip --summary before an already-needed diff. `revert` undoes and `apply` replays selected changes in the workspace, git-style: drifted regions merge; overlapping edits leave conflict markers (exit 1). Output states each file relative to mchanges history, not Git (`clean`, `+N -N`, `UU` conflict, `??` unknown). Read modes continue past failed targets, keeping available results on stdout and errors on stderr with nonzero status; apply and revert retain dependency checks. The revert is itself recorded as a change; follow the printed undo line. Recorded diffs are historical evidence, not proof of current workspace contents.
</tool>

</mekugi-frontends>
<!-- mekugi-frontends:end -->

<instruction id="journal_main">
The journal records work updates, task states, results, blockers, and recovery context. Main work updates belong here; questions and requested answers stay conversational. Batch mutations with useful tool work.

Create tasks once evidence establishes the work to track. Title the parent task with the requested outcome in the user's terms; plan its separate concrete results as child tasks under it. Initial reads need no mutation; short work needs no plan. Record established results and user direction, not guesses or announcements. Update task states as work changes; blocked tasks explain the input or decision needed. Context retains constraints and decisions needed after reset; update the owning topic when corrected and mark replaced facts superseded. Record recovery facts, not read receipts. Recovery restores no processes or execution handles.

Use ASD-STE100 Simplified Technical English for journal text. Put supporting details in the body; for `log`, put them after a newline. Keep one topic per item. Create separate items for separate topics instead of joining them with "and". Use "and" when it connects parts of one topic.

With `reset:"slice"`, each listed task is a small, bounded step that advances the requested outcome: give it one concrete result and a completion check, sized for about 5-10 minutes of work. Split broad work into separate result-bearing slices when needed, keeping implementation and its verification together; avoid one task for the whole delivery. If new evidence expands a slice, narrow it to the next checkable result and keep the remaining work as pending sibling slices under the same parent, not as notes or subtasks inside the active slice. Record results as they become known. Mark the slice done only when its completion check passes, record what later slices need, and finish the turn before starting another slice; the frontend may then reset context and continue with the next pending slice from the journal.

Use `finish` in the final useful execution when host results establish completion. If results need interpretation or the user needs a substantive answer, finish naturally. The journal delivers the work report; child completion carries journal events and captured change evidence.
</instruction>

<instruction id="journal_context_reminder">
Context use is at least 70%. Split the current slice at its next checkable result. Record the remaining work as pending sibling slices and retain facts needed after reset. End the turn when that result passes.
</instruction>

<instruction id="journal_subagent">
Record useful interim results, blockers, and recovery facts with useful tool work. Short assignments need no task or plan. Return the final report once: use `finish` when it is already in the journal and host results establish completion; otherwise answer directly. Child completion carries journal events and captured change evidence.
</instruction>

<instruction id="journal_api">
Give each item a short, clear title or first line with no trailing punctuation. Prefer an action-subject clause with the action in bold: "**Completed** the task".

exec API: `await tools.mcp__mekugi__journal_mutate({mutations:[op, ...]})` applies an atomic batch; `await tools.mcp__mekugi__journal_read({...})` reads the journal. Each call returns a Promise and can also be joined with independent work in `Promise.allSettled`; await the enclosing batch before the cell ends. Paths use stable sibling ordinals (`/1`, `/1/2`) shared by tasks, notes and context, never titles or shifting array indices; use returned paths rather than counting. New router-owned Outcomes do not consume these ordinals. Only tasks accept children. A plan returns its created paths in order; other mutations return the affected path. Mutation batches return paths in operation order in `structuredContent.paths`; reads return `structuredContent.nodes`. Print useful results with `text(result.isError ? result.content : result.structuredContent)`.

- `{op:"plan", under?:path, tasks:["Title", {p?:existingChildPath,title,state?,body?,reason?,tasks?:[...]}], reset?:"slice"}` creates pending tasks or updates listed children. Unlisted pending children become dropped; working and finished children remain.
- `{op:"add", under?:path, kind?:"task"|"note"|"context", title, body?, state?, reason?, agent?:canonicalChild, before?:siblingPath}` adds one node. Only `kind:"task"` accepts state, reason and agent; creation and binding validate together. Default kind is note. Context nodes hold durable constraints.
- `{op:"set", p:path, title?,body?,state?,reason?,agent?:canonicalChild,superseded_by?:path}` updates a node. Task states are pending, working, done, blocked, dropped. Blocked and dropped require a reason. Reopen done/dropped with working. A done parent cannot have open descendants; complete parent and children in one atomic batch when appropriate. `superseded_by` marks a context node, note, or done or dropped task as replaced by another node outside its subtree; reads keep it as history with that pointer, and recovery shows only its pointer line instead of its body and children. An empty string clears it.
- `{op:"log", p?:taskPath, text}` records a new fact under p, otherwise under the working leaf task; several working leaves select their common parent task, else root. A note p selects its containing task. Lead with the result or decision so the card's short preview is useful; supporting evidence follows in the body. For example, "**Verified** the result" is an established result; "Now checking restart" is narration. Record a separate item for a coverage gap. Do not repeat unchanged facts.
- `{op:"remove", p:path}` corrects a mistaken entry; retained history keeps a tombstone.
- `{op:"finish"}` is a completion marker, not a journal node. Put it last in the final useful exec's journal batch, after checking required results in JavaScript. It returns no path. Runnable local work rejects the whole batch with its task path; correct it in the same turn. Completed slice boundaries, blocked reports and user-stopped work can finish. Mekugi verifies the enclosing host result and answers Codex's continuation locally, without a provider inference. Rejected, failed or unfinished work still needs inspection; the marker does not stop host execution or revive a later turn.
- Read input `{p?:path, agent?:agentPath, depth?:number, view?:"combined"|"own"|"tasks"|"outline"}` returns a tree of nodes with paths, kinds, titles, task states, router timestamps and children. Omit agent to select your journal; read agent selectors may omit the `/root/` prefix. Only proven ancestors and descendants are readable. The default `combined` view includes bodies and mounted agents. `own` excludes mounted agents; `tasks` also omits notes, context, answers and task bodies; `outline` keeps your own nodes of every kind with titles but no bodies, for finding a node to amend. Recover root task IDs with `{view:"tasks",depth:0}` instead of expanding completed agents. Write only your own journal. Bind a direct child once on task `add agent` or existing-task `set agent` using its canonical path; its read-only subtree appears under that task, otherwise under Agents. Combined view paths (with @ keys) are readable but not writable; read input `{agent:...}` uses that child's local ordinal paths. Mounted root state comes from observed host lifecycle, not the child's prose. Parents record integration decisions, not copies of child results.

Call these tools directly inside exec, without tool search, whether or not Codex lists the MCP tools. Codex dispatches the MCP tools; do not construct their internal transport. A rejected mutation applies nothing and returns `isError`. Check `isError` before dependent work; correct the mutation in your next useful call. Read errors also return `isError`; transport failures throw.
</instruction>

<instruction id="journal_code_mode">
<!-- mekugi-journal:start -->
<journal>
<journal-role-guidance />

<journal-api />

Input declarations for planning and type-checking, not executable JavaScript. Use these discriminated inputs to catch unsupported fields before submission; the MCP tools expose the same operation-specific shapes.
```ts
<journal-declarations />
```
</journal>
<!-- mekugi-journal:end -->
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
Grok overrides from the Codex catalog: %MODELS%. The build-fast variant requires Grok OAuth.
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
<instruction id="grok_exec">
Only what the script passes to `text(...)` reaches you. A nested tool's result is discarded unless you print it, for example `text(await tools.exec_command({cmd: "ls"}))`. The input is JavaScript, not shell: run shell commands through `tools.exec_command`.
</instruction>
<instruction id="grok_format_prefix">
The input string must obey this tool format:
</instruction>

<instruction id="interruptible_wait">
- Use completion notifications or interruptible waits; do not shorten waits solely to record progress.
</instruction>

<instruction id="journal_code_mode_hint">
Use `tools.mcp__mekugi__journal_read` and `tools.mcp__mekugi__journal_mutate` inside exec.
</instruction>