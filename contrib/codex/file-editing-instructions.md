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

Record meaningful milestones with journal.
Bash/POSIX supports the `journal` command; Code Mode supports `await journal(...)`.
Each mutation array is atomic; `report_now` requests immediate user-visible delivery.

Finish with `{"op":"finish"}` plus any last mutations as the only call after required results, or end a
successful final shell invocation with `journal finish [JSON_ARRAY]`.
User then see all unflushed journal as a final report.

Use one item per distinct point; no plans, narration, or superseded progress.
Do not repeat or summarize other agents' journals.
Set `answer: true` only when answering the latest user message or plaintext native assignment,
and put only the answer in `text`; `false` clears it. Use normal Markdown.

Shell forms:

```text
journal list [AGENT]
journal add TEXT
journal edit ID TEXT
journal delete ID
journal batch JSON_ARRAY
journal finish [JSON_ARRAY]
```

Add/edit accept `--answer` or `--clear-answer`; add/edit/delete accept `--report-now`. Add writes
its assigned item ID, and list returns JSON; Other successful shell mutations are silent.

Examples:
shell: `journal add 'Tests passed' --report-now`.
Code Mode: `await journal({op: "add", text: "Tests passed", report_now: true})`.

## Shell reference

Submit free-form programs to `functions.shell`:

- Bash (default): write commands directly, without a shebang.
- Another interpreter starts with `#!COMMAND [ARGS...]`, then its body. Use a direct command or
  path, not `/usr/bin/env`; examples are `#!python3`, `#!uv run python`, and
  `#!node --experimental-strip-types`.

Submit a single-interpreter body directly, without closing delimited; avoid patterns like:
- `python3 - <<'PY'`
- `python3 -c '...'`

Write compound Bash/POSIX programs directly, using ordinary shell pipelines and redirections.

### Execution options and batches

Optional interpreter selector and optional directive lines starts first, then program source.

| Directive | Meaning |
| --- | --- |
| `#!params=<JSON object>` | Request fields from the tool description. Omit `cmd`; `login`, if set, is `false`. Omit `workdir` to use the current workspace; overrides are existing absolute expanded paths. |

Explicit Code Mode batches run programs sequentially in separate shell state and return together:

```text
#!params={"yield_time_ms":1000}
echo hello
#!python3
print("hello")
```

Each program owns its selector and directives. Omitted params inherit the previous complete object;
`{}` clears them, and interpreters never inherit.

Each new column-zero `#!interpreter` line starts a program; use `#!bash` for another
Bash program. Prefer these batches over separate shell calls for noninteractive programs.
Batches continue after nonzero exits.
Variables and `cd` do not carry over. Host errors stop the batch while preserving completed
results and partial output. Use separate shell calls for interactive programs.

### Output and continuation

`hrun [-n N] [--max-tokens N] [--tail] -- COMMAND [ARG...]` bound noisy external output.
`--tail` keeps the ending.

For pending execution, follow the latest `continuation` notice's `next_call`; prefer host completion
notifications. A running outer Code Mode cell owns continuation, so use its `wait`, not an inner
session. Resubmitting source starts a new execution. If `next_call` is null, use native session
facilities for interactive input or termination.

## HPATCH/2

It provides convenience for dependent edit/command chains by supporting mixed script.

### Files, targets, and values

| Operation | Meaning |
| --- | --- |
| `in PATH` | Select an existing file. Repeat when switching files. |
| `new PATH` | Select a pending empty file. |
| `mv PATH` | Move the active file with its baseline and edits. |
| `rm` | Delete the active file and clear selection. |
| `type TARGET VALUE` | Replace; an empty target-bearing value deletes the target. |
| `add DESTINATION VALUE` | Insert before a row/text destination; `add EOF` appends. |

Targets:

`LINE:HASH`: complete logical line
`LINE:HASH..LINE:HASH`: inclusive complete-line range
`LINE:HASH "TEXT" [N]`: first N exact matches from that row through EOF
`"TEXT" [N]`: first N exact matches in the immutable baseline

Encode an embedded LF as `\n` or `\u000A` and keep targets on one physical line.
Literal tab is accepted; other control characters are not.

Values: JSON-compatible strings or heredoc.

Literal targets own only matched bytes, so deleting text alone leaves its line terminator. Delete
whole lines with row/range targets or include the terminator. `add` is byte-exact and synthesizes no newlines; count separators already at the destination.
Nonempty line and range `type` replacements preserve the target's LF, CRLF, or CR when the value
omits a terminator. Other authored whitespace is preserved except supported formatting and indentation correction.

Compact `advisory` lines report nonzero authored whitespace effects and are not errors.
Report rows inherit the latest `in PATH` or `file PATH` header; `file` is not an edit command.

### Baselines and validation

Existing-file edits require a target. Targetless `type VALUE` is allowed only immediately after
`new`, once per file. Each existing file has one immutable invocation baseline, so pending edits do
not shift later targets; introduced content is targetable only in a later call. Unchanged saved rows
remain valid after line shifts when one exact hash identifies them, and routed replacement reports
provide confirmed mappings.

Overlapping replacements/deletions and insertions strictly inside them reject. Boundary insertions
are valid and same-boundary insertions render in script order. Relative paths require the selected
base directory; parents for `new` and `mv` must exist.

Changed Go files are parsed and gofmt-formatted. Python, JavaScript, and TypeScript receive syntax
checks and targeted indentation correction, not full formatting; preserve required indentation.
Other languages are not formatted. Reports contain final hashes, changed blocks, and line shifts;
reuse them rather than rereading solely because formatting moved source.

### Shell-in-script

With Code Mode available, use `shell go test ./...` for one physical raw-source line, or a
heredoc for multiline source or source containing `<<`.
Shell text in edit values remains data.

Shell commands may surround edit segments. Begin every edit segment with `in` or `new`; selection
and pending edits do not cross shell boundaries. Each shell segment accepts one program with
independent state. Native-only clients use separate hpatch and shell calls. Interactive programs
and explicit shell batches also remain separate.

All syntax and shell headers validate before execution. Each edit segment then validates against its
starting files before Codex authorization/application. A stale target, nonzero shell exit, refusal,
or cancellation stops the suffix. Completed
edits and shell effects are not rolled back; reports describe completed segments, not later shell changes. A preflight rejection applies nothing.
After execution starts, inspect uncertain effects; missing confirmation does not mean rollback.

Do not replay a mixed script or resend its suffix. Resolve the previous Code Mode cell and inspect
live work, files, and uncertain effects, then use its retained handle:

- `resume HANDLE`: continue pending work.
- `resume HANDLE retry`: retry the failed segment after reconciliation; an optional next segment of
  the same kind replaces it.
- `resume HANDLE repair`: apply one workspace edit segment, then retry the failed segment and suffix.
- `resume HANDLE accept`: continue after externally establishing intended state and resolving native
  work; this records reconciliation, not application success.

Successful recovery runs the retained suffix and revalidates remaining targets. A failed repair
stays under the same handle. Cancelling a wait does not prove its process stopped. Handles are
thread-scoped, expire one hour after creation without renewal, and end at router shutdown;
invalid handles execute nothing.

### Rejected-script recovery

Use `functions.hpatch_recover` for the latest rejected script, preserving unrelated text:

- For a mixed script whose preflight failed before carrier retention, use only script-text
  mutations; once preflight succeeds, use its retained continuation.
- For a wholly row-stale rejection, submit every diagnostic `HANDLE TARGET`, for example
  `maple "return oldResult, nil"`.
- For a parsed command's target or value, use `HANDLE target TARGET` or `HANDLE value VALUE`; values use normal
  quoted strings or heredoc and each handle appears once.
- For framing, paths, conflicts, or other script changes, use target-bearing `type`/`add` mutations
  against retained-script text. Generated-source line numbers are not recovery targets.

Keep the two payload forms separate. Script-text mutations edit the retained rejected script, not
workspace files. Script mutations omit `in`, `new`, `mv`, and `rm`. Both forms preserve untargeted
text and reevaluate the complete script atomically. A re-rejection becomes the new baseline; use its
script rows and refreshed command handles. Invalid corrections change neither workspace nor retained
baseline.

## Change handoffs

Hpatch reports include `change amber1`; recovery keeps that ID. Review captured edits with
`hchanges amber1..amber3`, rather than Git diff, and hand off same-agent inclusive ranges. Use `--summary` when only a diffstat is needed; skip it before an already-needed diff read.
Use `--history` for a recovery chain. Git remains appropriate for untracked, shell-generated, or
unrelated changes; do not routinely pair it with hchanges for the same edits.

Reads default to 4,000 tokens. An incomplete read supplies exact `next_call: hread REF`; continue it
without repeating IDs or filters. Historical evaluated diffs are not current editable rows or
proof of application, and counts are per evaluation rather than combined net change.

## Reading and inspection reference

Use ordinary `cat` or bounded `sed` unless verified rows help an anticipated edit. Budget combined
reads and searches before execution. Shell workers budget combined display output automatically.
For omitted output, run the
exact `next_call: hread REF` without repeating producer arguments. Reading never reruns a producer;
commands retain their exit status. Outer host truncation can still hide a receipt.

Private readers run in Bash/POSIX and accept `--max-tokens N` (1–15500, default 4000). Copy emitted
rows directly; never reconstruct hashes. Incomplete results do not establish coverage.

| Tool | Compact form and rules |
| --- | --- |
| `hcat` | `hcat PATH [START:END]`; bare path reads all. `hcat [-n N] [--tail]` selects first/last complete rows; tail needs a line/token limit. |
| multi-file hcat | `hcat [--max-tokens N] PATH [START:END] [PATH [START:END] ...]`; ranges apply to the preceding path. Shares one budget across at most 16 files and reports each omitted range. Use `./` for range-like filenames; `-n`, `--tail`, and `--preview-bytes` require one file. |
| `hgrep` | Ripgrep arguments; output is `"PATH":LINE:HASH TEXT`. Do not follow complete target-bearing output with hcat unless outside context is needed. |
| `hsymbol` | `hsymbol refs PATH LINE SYMBOL [N]` or `hsymbol def PATH LINE SYMBOL [N]` for Go, JavaScript, TypeScript, JSON, or Python. Plain lines query the current snapshot; use `LINE:HASH` instead of `LINE` to enforce a prior read. `N` selects an exact language-token occurrence and may be omitted only when unique. `--workspace ROOT` selects resolver scope and makes result paths absolute. |
| `inspect_file` | Use `inspect_file PATH` for bounded metadata and an outline whose inclusive `line`/`line_end` hashes are direct row/range targets. Read source only when the outline lacks needed text. |
| preview | Hcat/hgrep accept `--preview-bytes N` (1–65536). Preview JSON has a full-row identity and UTF-8 prefix with omitted-byte counts; obtain missing bytes before using literal text. |

Empty output is omitted. Stdout-only retained pages are unframed; stderr/mixed pages use
`[stream unit]` frames. Initial `hread` accepts `--stdout` or `--stderr`; later references bind
stream and position. Pages hold raw bytes, verified rows, or JSON entries; fragments and framing are
not verified rows. If one complete unit cannot fit, increase the budget or use preview. Reads are
repeatable and retained with their session; missing references fail explicitly.

For field removal or signature change, acquire semantic references across affected packages and
tests, read all returned reference rows before batching dependent edits, and resolve incomplete output. Saved hashes
describe the query snapshot. Report skipped or unavailable references; filenames or incomplete results
are not caller coverage.
<!-- mekugi-model-instructions:end -->
