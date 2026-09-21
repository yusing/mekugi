<!-- mekugi-model-instructions:start -->
{{.EditingWorkflow}}

## Journal

Record meaningful milestones with journal.
Bash/POSIX supports the `journal` command; Code Mode supports `await journal(...)`.
Each mutation array is atomic; `report_now` requests immediate user-visible delivery.

Finish with `{"op":"finish"}` plus any last mutations as the only call after required results, or end a
successful final shell invocation with `journal finish [JSON_ARRAY]`.
Main finish shows unflushed journal entries as the final report. Child finish sends its current
journal, automatically collected hchange ranges, and aggregated numstat to its native completion
audience. Finish without repeating milestones in a separate final answer or collecting change IDs.

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

Each new column-zero `#!interpreter` line splits the input into a separate sequential program;
use `#!bash` to start another Bash program, not to continue the current shell. Split programs
have isolated variables and working directories. Use a split batch when the interpreter, options,
or desired shell state differs; keep ordinary dependent commands in one program. Batches continue
after nonzero exits. Host errors stop the batch while preserving completed results and partial output.
Use separate shell calls for interactive programs.

### Output and continuation

`hrun [-n N] [--max-tokens N] [--tail] -- COMMAND [ARG...]` bound noisy external output.
`--tail` keeps the ending.

For pending execution, follow the latest `continuation` notice's `next_call`; prefer host completion
notifications. A running outer Code Mode cell owns continuation, so use its `wait`, not an inner
session. Resubmitting source starts a new execution. If `next_call` is null, use native session
facilities for interactive input or termination.

## HPATCH/2

Run `hpatch PATH [SCRIPT]` through `functions.shell`; omit SCRIPT to read the edit from stdin.
For atomic multi-file edits, supply `hpatch PATH SCRIPT [PATH SCRIPT ...]`.
Apply generated scripts with `hpatch notes.txt "$(python3 generator.py)"` or
`hpatch notes.txt < prepared.hpatch`. Generators emit script bytes, not target-file writes. Edits
retain change IDs, hchanges history, and completed live diffs; dynamic input has no speculative
preview.

### Files, targets, and values

| Operation | Meaning |
| --- | --- |
| `type TARGET VALUE` | Replace; an empty value deletes the target. |
| `add TARGET VALUE` | Insert before a row/text target. |
| `append VALUE` | Insert at the immutable baseline end. |

Filenames belong only in shell arguments; quote paths there as usual and use `--` before flag-like paths.
Every target file must exist.
There is no active-file state, filename syntax, file-management command, or `EOF` keyword.

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
Report rows inherit the latest `file PATH` header; `file` is not an edit command.

### Baselines and validation

`type` and `add` require targets. `append` also works on an empty file created by the shell.
Each file has one immutable invocation baseline, so pending edits do not shift later targets;
introduced content is targetable only in a later call. Multi-file edits in one invocation are atomic. Unchanged saved rows
remain valid after line shifts when one exact hash identifies them, and routed replacement reports
provide confirmed mappings.

Overlapping replacements/deletions and insertions strictly inside them reject. Boundary insertions
are valid and same-boundary insertions render in script order. Relative paths require the selected
base directory. Shell creation, movement, and removal are outside the hpatch transaction.

Changed Go files are parsed and gofmt-formatted. Python, JavaScript, and TypeScript receive syntax
checks and targeted indentation correction, not full formatting; preserve required indentation.
Other languages are not formatted. Reports contain final hashes, changed blocks, and line shifts;
reuse them rather than rereading solely because formatting moved source.

### Rejected-script recovery

Use `hpatch --recover HANDLE [SCRIPT]` with the rejected edit's recovery handle; omit SCRIPT to read corrections from stdin.
For a rejection with multiple scripts, use `--script N` to select the original 1-based path/script pair, even when paths repeat. Recovery reevaluates the whole batch:

- For a wholly row-stale rejection, submit every diagnostic `HANDLE TARGET`, for example
  `maple "return oldResult, nil"`.
- For a parsed command's target or value, use `HANDLE target TARGET` or `HANDLE value VALUE`; values use normal
  quoted strings or heredoc and each handle appears once.
- For framing, conflicts, or other script changes, use target-bearing `type`/`add` mutations
  against retained-script text. Generated-source line numbers are not recovery targets.

Keep the two payload forms separate. Script-text mutations edit the retained rejected script, not
workspace files. Correct an outer filename by issuing a new invocation, not by editing the script.
Script mutations use pathless `type TARGET VALUE` / `add TARGET VALUE`, without `append`. Both forms preserve untargeted
text and reevaluate the complete script atomically. A re-rejection becomes the new baseline; use its
script rows and refreshed command handles. Invalid corrections change neither workspace nor retained
baseline.

## Change handoffs

Hpatch reports include `change amber1`; recovery keeps that ID. Review captured edits with
`hchanges amber1..amber3`, rather than Git diff, and hand off same-agent inclusive ranges. Use `--summary` when only aggregated numstat is needed; skip it before an already-needed diff read.
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
