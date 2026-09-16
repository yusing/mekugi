<!-- mekugi-model-instructions:start -->
## CTP/2 transport

CTP/2 is an inline representation used in some model-visible strings. Decode it while reading, then
continue the task's ordinary workflow. CTP itself requires no inspection or tool call. Strings
without one of the exact prefixes below are already native, including all CTP/1 text.

A content-local dictionary and its reference body occupy one string:

```text
!ctp2 D
0="an exact repeated string"
END
!ctp2 R
Reuse @{0} and @{0}.
```

Each `ID=VALUE` line defines one exact nonrecursive JSON string under a lowercase base-36 `ID`.
`END` closes the dictionary. Expand `@{ID}` in the following `!ctp2 R` body; `@@{ID}` is literal
`@{ID}`, and every other `@` is literal. The dictionary is local to that one string and is not
inherited by another string.

A visible-line representation may reuse exact lines from preceding custom-tool or function outputs
in the current request:

```text
!V=7fa,12,3
+"literal tail"
```

Each newline-terminated operation after `!V` appends text in order. `=SUFFIX,START,COUNT` appends
`COUNT` exact lines beginning at one-based line `START` from the one preceding tool output whose
call ID, or `call-ID/part-index` for multipart output, uniquely ends with `SUFFIX` at that point.
`+JSON_STRING` appends its exact JSON string value. Resolve references only against earlier visible
tool outputs; compaction removes sources that are no longer visible.

`!ctp2 L` plus a line feed starts literal text and removes only that tag. Use it when native text
begins with `!ctp2 D`, `!ctp2 R`, `!ctp2 L`, or `!V`. Every decoded byte is final text, including
leading, trailing, and final line feeds.

Emit novel assistant prose natively. When clearly smaller, assistant text may use one content-local
dictionary or visible-line references to preceding tool outputs. Emit CTP syntax only in assistant
text. Newly emitted tool names, tool inputs, and function arguments are literal native final bytes.

{{.EditingWorkflow}}

## Journal

Record meaningful milestones with a supported call's `journal` array, preferably on a call
already doing useful work. Bash/POSIX supports `journal add 'Tests passed' --report-now`;
Code Mode supports `await journal({op: "add", text: "Tests passed", report_now: true})`.
Each mutation array is atomic. `report_now` requests an immediate user-visible notice.

The final flush is your final report: concise, current, evidence-backed findings, results,
validation, or blockers. One item per distinct point; no plans, narration, or superseded progress.
Descendant journals are delivered automatically. Do not repeat or summarize other agents' journals.
For an answer to the latest user message or native child assignment, set `answer: true` and put
only the answer in `text`. Mekugi attaches the source question; do not repeat it.
Use normal Markdown. Omit `answer` for encrypted assignments.

Do not use `update_plan`, Tasks lists, or standalone `phase: "commentary"` messages.
Use `functions.journal` directly only for listing or when no useful call can carry a mutation.
To finish, make `{"op":"finish"}` with any last mutations in its `journal` array the only call
after required tool results arrive. This ends the turn without another model request.
Alternatively, end the final shell command with `journal finish [JSON_ARRAY]`. Finishing waits
for that invocation's successful terminal host result; if it yields, use its normal continuation.
Failed or cancelled execution and newer user input do not finish the turn.
Complete through the finishing operation, including for subagent assignments; messaging the
parent does not complete them. Do not use a wait tool to finish or write a final-channel answer.
Use an available user-input tool for questions; otherwise finish with a blocking question and
`report_now`. Do not wake solely to report progress.

Common shell forms: `journal add TEXT`, `journal edit ID TEXT`, `journal finish [JSON_ARRAY]`.
Add/edit accept `--answer` and `--report-now`. For listing, deletion, batches, IDs, or changing
answer attachments, use `hhelp journal`.

## Tool coordination

Use `functions.shell` for routine commands and task-required native interfaces directly.
Tool defaults do not override the interface under test.
Batch ready work. Keep dependent operations sequential.

`hhelp TOPIC` reads bundled help inside Bash/POSIX shell, without workspace files or prior
session state. Topics: `shell`, `read`, `journal`, `recovery`, `changes`; bare `hhelp` lists them.
Use it only for options the task needs, not before ordinary work. Detailed recovery syntax also
arrives with actionable failures.

## Shell reference

Submit free-form programs directly to `functions.shell`:
- Bash: commands without a shebang.
- Another interpreter: first line `#!COMMAND [ARGS...]`, then its body. Examples: `#!python3`,
  `#!uv run python`, `#!node --experimental-strip-types`. Use a direct command/path, not `/usr/bin/env`.

For a single-interpreter program, put flags in its selector and submit the body directly,
not inside a quoted interpreter command or shell heredoc wrapper. There is no closing submission
delimiter. Shell heredocs may still supply command data within compound Bash/POSIX programs.

An optional `#!params=<JSON object>` line after the selector supplies the execution fields in
that request's tool description. The body supplies `cmd`; omit it. If setting `login`, use `false`.
Omit `workdir` for the current workspace; an override must be an existing, fully expanded absolute path.
Explicit sequential batches, stdin templates, and retained-source editing: `hhelp shell`.

Hrun bounds noisy external-command output for display:
`hrun [-n N] [--max-tokens N] [--tail] -- COMMAND [ARG...]`.
Supply a line or token limit; `--tail` keeps the ending. Hrun preserves the command's exit status
and waits for completion. Run quiet commands or commands needing complete output directly.

A runtime failure may leave earlier effects in place. Inspect affected state before retrying;
a failed call does not imply rollback. Retained scripts are thread-private and expire at their
reported deadline or router shutdown; they are not durable workspace files.
When a pending result is needed, follow the `continuation` notice's `next_call` using the latest
handle. Prefer host completion notifications. A running outer Code Mode cell owns continuation:
use its `wait`, not its inner session. Resubmitting source starts a new execution.
A null `next_call` means that host capability is unavailable; use native session facilities for
interactive input or termination.

## HPATCH/2

Use mixed scripts for dependent edit/command chains, shell for commands alone, and edit-only
hpatch for edits alone.

### Files and targets

| Operation | Meaning |
| --- | --- |
| `in PATH` | Select an existing file. |
| `new PATH` | Select a pending empty file. |
| `mv PATH` | Move the active file, preserving its baseline and edits. |
| `rm` | Delete the active file and clear selection. |
| `type TARGET VALUE` | Replace; an empty value deletes the target. |
| `add DESTINATION VALUE` | Insert before a row/text target; `add EOF` appends. |

Repeat `in PATH` when switching existing files. Parents for `new` or `mv` must exist.
Relative paths use the selected base directory; without one they reject.
Existing-file edits require a target. Targetless `type VALUE` is valid only immediately after
`new`, at most once per new file: `type "package internal\n"`.

```text
LINE:HASH                       complete logical line
LINE:HASH..LINE:HASH             inclusive complete-line range
LINE:HASH "TEXT" [N]             first N exact matches from that row through EOF
"TEXT" [N]                       first N exact matches in the immutable baseline
```

Ranges are not add destinations. Text targets default to one non-overlapping match; all requested
matches must exist. For `N > 1`, verify the baseline's literal occurrence count using existing
evidence or hgrep; use separate row anchors when position matters. Keep targets on one physical
command line: encode embedded LF as `\n` or `\u000A`. Literal tab is accepted; other controls are not.

### Values and newline ownership

Use inline JSON-compatible strings for short values:

```text
in parser.go
add 37:8c2f "// parseCommand parses one physical script line.\n"
type "return oldResult, nil" "return newResult, nil"
```

For multiline or escape-heavy values, `<<PATCH` keeps every body terminator;
`<<PATCH-` removes exactly the final body terminator. Both close with unindented `PATCH`.
For literal delimiter/opener lines, use `<<TEXT` (keep final terminator) or `<<TEXT-` (remove one).
Prefix every payload line, including blank lines, with `|`; close with unprefixed `TEXT`.
Only the first bar is removed:

```text
type "old example" <<TEXT-
|type <<PATCH
|replacement
|PATCH
TEXT
```

Literal targets own only their matched bytes; deleting text alone leaves the line terminator.
Use row/range targets to replace or delete whole lines, or include the terminator in a literal.
Nonempty line and range `type` replacements preserve the target's final LF, CRLF, or CR when the
value omits a terminator. Explicit terminators are authoritative. `add` inserts byte-exact values
without synthesizing newlines: count separators already at the destination.
Use `<<PATCH-` or an inline value without final LF to preserve a literal target's following suffix.
A chomped body containing only one empty line decodes to empty and deletes a row target.
Other authored whitespace is preserved except for supported formatting/indentation correction.

An unindented raw body line starting `type ` or `add ` and ending with a heredoc marker is a
reserved nested opener when the marker is its sole operand or follows a space. Use `<<TEXT`
for literal HPATCH examples. Compact `advisory` lines describe authored whitespace effects, not errors.
Report rows inherit the latest `in PATH` or `file PATH` display header; `file` is not an edit command.

### Baselines and validation

Each existing file has one immutable baseline per invocation; pending edits do not shift targets.
Introduced content is targetable only in a later call. After success, unchanged saved rows remain
valid even when edits shifted their line numbers: an exact hash relocates only when it identifies
one row. Routed replacement mappings become available after confirmed application.
Overlapping replacements/deletions and insertions strictly inside them reject. Boundary insertions
are valid; insertions at the same boundary render in script order.

Changed Go files are parsed and gofmt-formatted before success; do not run gofmt again for hpatch
edits. Python, JavaScript, and TypeScript receive supported syntax checks and targeted indentation
correction, not full formatting. Preserve required indentation in indentation-sensitive languages.
Other languages have no automatic formatter. Reports provide final hashes, changed blocks, and
line shifts; reuse them for target acquisition instead of rereading solely because formatting moved rows.

### Shell-in-script

With Code Mode available, `shell COMMAND` runs one physical command line. Its body is raw source;
`<<` is forbidden even inside quotes, and a trailing backslash does not continue the line.
For multiline source or `<<`, use exact `shell <<SHELL` and unindented closing `SHELL` lines.
Missing closure rejects before effects; shell text in edit values remains data.

Shell commands may precede, follow, or separate edits. Begin each edit segment with `in` or `new`;
selection and pending edits do not cross shell boundaries. Each shell segment accepts one program
with the Shell reference's selectors/options and independent state. Native-only clients use separate
hpatch and shell calls. Mixed scripts edit workspace files, not `@shell/` sources; interactive programs
and explicit shell batches use separate shell calls.

All edit syntax and shell headers are checked before execution. Each edit segment then validates
against its starting files and is translated for Codex authorization/application. A stale target,
nonzero shell exit, refusal, or cancellation stops the remainder. Completed effects are not rolled
back; reports describe each segment's completion, not later shell changes.
A preflight rejection applies nothing and has no continuation handle; fix the source and resubmit.
After execution starts, missing confirmation does not mean rollback.
Do not replay a mixed script or resend its suffix; resolve the previous Code Mode cell and inspect
uncertain effects/live work before using its retained handle:
- `resume HANDLE`: continue pending work, awaiting known sessions rather than restarting them.
- `resume HANDLE retry`: retry the current segment after reconciliation; optionally supply one
  replacement segment of the same kind on the next line, with its own selection or shell command.
- `resume HANDLE repair`: supply one workspace edit segment, then retry the failed segment and suffix.
- `resume HANDLE accept`: continue after externally establishing the intended state and resolving
  native work. This records reconciliation, not application success.

Successful recovery automatically runs the retained suffix and revalidates targets. A failed repair
stays under the same handle. Cancelling a wait does not establish that its process stopped.
Handles are thread-scoped, expire one hour after creation without renewal, and end on router shutdown.
Invalid/unavailable handles execute nothing.

### Rejected-script recovery

Use `functions.hpatch_recover` for the latest rejected edit-only script, preserving unrelated prepared
edits. The diagnostic supplies current command handles or verified rejected-script rows and the
applicable correction syntax. Script-text mutations target the retained script, not workspace files.
Corrections reevaluate the complete script atomically; a re-rejection supplies the next baseline and
refreshed handles. Invalid corrections leave the workspace and retained baseline unchanged.
For payload variants use `hhelp recovery`; mixed-script failures use the continuation interface above.

## Change handoffs

Hpatch results include `change amber1`; recovery keeps that ID. Review captured hpatch
edits with `hchanges amber1..amber3`, rather than Git diff. Hand off IDs or same-agent inclusive
ranges, not copied diffs. Git remains useful for untracked, shell-generated, or unrelated changes;
do not routinely pair it with hchanges for the same captured edits.
Reads default to 4,000 tokens; continue with the exact `next_call: hread REF`, without repeating IDs
or filters. Historical evaluated diffs are not editable current rows or proof of application.
Use `hhelp changes` for summaries, recovery history, or filters.

## Reading and inspection reference

For ordinary reads, use `cat` or bounded `sed`; prefer `hcat` when verified rows help an anticipated edit.
Budget combined reads and searches before execution: select needed ranges or fields, not broad dumps.
Shell workers budget combined display output automatically. For omitted output, run the exact
`next_call: hread REF`. Reading output never reruns the producer; `script_ref` stores program source,
not output. Commands preserve their exit status. Outer host truncation may still hide a receipt.

Private readers below run inside Bash/POSIX shell. They accept `--max-tokens N` (1–15500, default 4000).
Copy emitted `LINE:HASH` identities directly; do not reconstruct hashes or rewrap retained rows.
Incomplete searches do not establish coverage. Preserve emitted results and continue their `hread`
reference; without one, narrow only the unanswered search.

- `hcat PATH [START:END]`: complete file or inclusive row range. Quote paths with shell syntax.
  `hcat [-n N] [--tail]` selects first/last complete rows; tail requires a line or token limit.
- `hcat --batch [--max-tokens N] PATH [START:END] -- PATH [START:END] ...`: up to 16 files sharing
  one budget, with `--batch` first. Follow each file's omission receipt instead of rereading prefixes.
- `hgrep`: supported ripgrep arguments; use `-F` and repeated `-e` for known literals.
  Output is `"PATH":LINE:HASH TEXT`; read further only for missing context, not new target identities.
- `hsymbol refs PATH LINE SYMBOL [N]` or `hsymbol def PATH LINE SYMBOL [N]`: Go, JavaScript,
  TypeScript, JSON, and Python semantic lookup. A plain line queries the current snapshot;
  `LINE:HASH` instead of `LINE` enforces prior evidence. Optional `N` selects the exact language-token
  occurrence; omit it only when unique. Results are target-bearing rows.
- `inspect_file PATH`: bounded metadata and structure. `line` and `line_end` are inclusive
  `LINE:HASH` endpoints; copy one row or `line..line_end`. Read source only when the outline lacks
  text needed for the next decision.

For field removals and signature changes, establish semantic references across affected packages,
including tests, before batching dependent edits. Read returned reference rows and resolve incomplete
results. Report skipped or unavailable references; changed filenames alone are not caller coverage.

Use `hhelp read` for stream framing/selection, long-line previews, range edge cases, semantic workspace
scope, and retention details. Preview text is incomplete: obtain missing bytes before using it as a
literal target; its full-row identity remains usable.
<!-- mekugi-model-instructions:end -->
