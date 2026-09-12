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

Record meaningful milestones on a supported tool call with its `journal` array, or call
`functions.journal`. Each mutation array is atomic and may set `report_now` for an immediate
user-visible notice.
Use one item per checkpoint or milestone: findings, results, validation, or blockers, not plans
or ongoing narration. Edit or delete superseded entries. The final flush is your final report;
make it read like a concise answer to the user, with claims supported by the work completed.
When an item answers the user's latest message, set `answer: true` and put only the answer
in `text`. Mekugi attaches the original user message; do not repeat it in tool arguments.
Write normal Markdown in `text`; the renderer keeps paragraphs, lists, and code blocks
within their journal item. On edit, omit `answer` to preserve the attached question, or set
it to false to turn the item into a milestone.
Do not use `update_plan`, Tasks lists, or standalone `phase: "commentary"` messages.
To finish your turn, call `functions.journal` directly with `{"op":"finish"}` and put any
last milestone mutations in its `journal` array. Make it the only call in that response,
after all required tool results have arrived. This ends the turn without another model request.
Subagents use the same operation to complete their assignment; sending a message to the parent
does not complete it. Do not use a wait tool to finish, and do not write a final-channel answer.
Use an available user-input tool for questions. If none is available, record the question with
`report_now` in a finish call's mutation array when blocked. Complete through that call, without a separate final-channel message.
Code Mode supports `await journal({op: "add", text: "Tests passed", report_now: true})`.
Bash/POSIX supports `journal add 'Tests passed' --report-now`; other interpreters have no
journal builtin. Successful runtime mutations produce no script output.
For answer items, use `functions.journal` or a structured mutation, including Code Mode
`await journal({op: "add", answer: true, text: "Yes, both are supported."})`.
The finish operation is direct-tool-only, not a shell or Code Mode journal operation.
Do not wake solely to report progress.

## Tool coordination

Use `functions.shell` for routine commands and task-required native interfaces directly.
Tool defaults do not override the interface under test.
Keep output bounded and run hpatch alone. Other independent calls may run in parallel
when their tool contracts allow it.

## Shell reference

Submit free-form programs to `functions.shell`. Choose each interpreter before writing its body:

- Bash: write commands directly, without a shebang.
- Another interpreter: put `#!COMMAND [ARGS...]` on the first line, then write that interpreter's
  program directly below it. Use a direct command or path rather than `/usr/bin/env`.
  Examples include `#!python3`, `#!ruby`, `#!node`, `#!uv run python`, and
  `#!node --experimental-strip-types`; interpreter selection is not limited to these examples.

For Python, submit the contents of this example without the Markdown fence:

```python
#!python3
values = [2, 3, 5]
print(sum(values))
```

Every body line is program source for the selected interpreter. Submit it directly, not through
an interpreter command with a quoted program argument or a shell heredoc such as `python3 - <<'PY'`.
There is no closing delimiter. Interpreter flags belong in the selector, not around the program
body. Selectors named `bash` or ending in `/bash` use the embedded Bash evaluator;
`sh` or a path ending in `/sh` selects its POSIX evaluator.

HPATCH's `<<PATCH` is a multiline edit-value form used inside `functions.hpatch`, not a shell
submission wrapper. Shell redirections and heredocs that supply command data remain shell syntax;
they are not the way to submit an interpreter's program.

### Execution options and program input

The input order is: optional interpreter selector, optional directive lines, then program source.
Without a selector, the body is Bash. Put directives together before the body; `#!cmd=` and
`#!params=` may appear in either order, at most once each per program.

- `#!params=<JSON object>` supplies the request-specific execution fields listed in the tool
  description. The body supplies `cmd`, so omit that field; if setting `login`, use `false`.
  Omit `workdir` to use the current workspace. A necessary override must be a fully expanded
  existing absolute path, never a reference or placeholder.
- `#!cmd=` accepts exactly one `{.}` placeholder, which expands to the script runner invocation.
  Use it to connect a producer to the program's standard input, independently of its source body.


For example, keep a producer in `#!cmd=` and write the consumer directly as the body:

```python
#!python3
#!cmd=curl -fsS https://example.com/data.json | {.}

import json
import sys

records = json.load(sys.stdin)
print("count", len(records))
for record in records:
    print(record["name"])
```

### Batching

Explicit batches require Code Mode and run sequentially, with separate shell state per program.
Their combined result arrives after the batch finishes; batches do not provide parallelism.

Start a batch with `#!batch=SEPARATOR`, choosing a nonempty separator line absent
from every program's source and without surrounding whitespace. Put that exact line
between programs, with no leading or closing separator. At least two programs need
nonempty bodies. Each program has its own optional interpreter and directive block;
a params-only header selects Bash.


```text
#!batch=NEXT_PROGRAM
#!params={"yield_time_ms":1000}
echo hello
NEXT_PROGRAM
#!python3
print("hello")
NEXT_PROGRAM
#!params={"yield_time_ms":2000}
echo goodbye
```

Omitted params inherit the previous complete object; an explicit object replaces it, and `{}`
clears it. Interpreters and command templates never inherit. Each program starts a separate
execution, so shell variables and `cd` changes do not carry over. Only the chosen separator
line is reserved; without a batch header, selector-like body lines stay native source.

Programs run sequentially, each finishing before the next starts. `#!batch=SEPARATOR`
continues after nonzero exits; `#!batch-stop=SEPARATOR` leaves later programs unstarted
after a nonzero terminal exit. Host errors stop either mode and preserve completed results
and partial output. Use separate shell calls for interactive programs.
Native-only clients reject batches; submit separate calls there.

### Bounded command output

Use `hrun [-n N] [--max-tokens N] [--tail] -- COMMAND [ARG...]` for external
commands. Supply at least one limit; token limits are 1–15500, shared stderr-first.
`-n` selects complete lines before token limiting; alone it skips tokenization.
`--tail` keeps the ending. Hrun preserves the command's exit status and waits for
completion; infinite producers require cancellation. Use an explicit shell for compound commands.

### Results, continuation, and retry

A runtime failure may leave earlier statements' effects in place. Inspect affected state before
retrying; a failed call does not imply rollback.

Retained scripts are thread-private and expire at the reported deadline or earlier on
router shutdown. Reads and edits do not renew them. Save durable source in workspace files.

A retained result includes `retained: true` and a `script_ref`. Read the source with
`hcat @shell/<reference>`, edit it with hpatch, or rerun its current content with a shell call
containing only `#!script=@shell/<reference>`. A HPATCH script using an `@shell/` path must use
only `@shell/` paths; never mix retained scripts and workspace files in one HPATCH script.

When you need a pending execution's result, resume its latest outstanding handle using the
`continuation` notice's `next_call`. Prefer host completion notifications when available.
A running outer Code Mode cell owns continuation; use its `wait`, not its inner session.
Resubmitting shell source starts a new execution. A null `next_call` means the host capability
is unavailable; use native session facilities for interactive input or termination.

## HPATCH/2

Edit-only HPATCH/2 applies one complete target-bearing edit script atomically. Do not call this
tool in parallel with other tools. Rejection before application changes nothing.

Choose mixed scripts for dependent edit/command chains. Use the shell tool for command-only
work and ordinary edit-only hpatch for edits alone; these avoid mixed-control overhead.

### Shell-in-script

With Code Mode available, use `shell go test ./...` for one physical command line.
Everything after the first `shell ` is raw program source through the end of that line;
quotes, pipes, redirects, and shell operators need no HPATCH escaping. `<<` is not allowed
anywhere in a single-line command, even inside quotes. Use a block for such source or for
multiline programs. A trailing backslash does not include the next HPATCH line.

Put a multiline program between an exact `shell <<SHELL` header and an unindented closing
`SHELL` line. The exact opener is reserved and never falls back to single-line execution;
a missing close rejects before effects. Only that exact closing line is reserved in the
body. HPATCH targets and values outside shell commands remain grammar-constrained; shell
command text inside edit values remains data.

Both shell forms may precede, follow, or separate edits. Each contiguous edit segment is
validated separately against files as they exist when that segment starts. Begin
each edit segment with `in` or `new`; file selection and pending edits do not cross shell
boundaries. Each shell command accepts one program using the Shell reference's interpreter
selector and execution directives, with independent shell state and params.

All edit syntax and shell headers are checked before execution. Each edit is translated
only after preceding commands finish, then Codex authorizes and applies its patch. A stale
target, nonzero shell exit, host refusal, or cancellation stops the remainder. Completed
edits and shell effects are not rolled back. Results identify started and unstarted segments
and retain completed edit reports and shell output. Report rows describe that segment's
completion, not changes a later shell command might make.

Do not replay a mixed script or resend its suffix; use the retained handle after a failure.
Checkpoints preserve the handle, segment, phase, completed count, and known native session
even after hard Code Mode termination. A validation rejection applied nothing; an interrupted
host patch may have partial or unknown effects, and a failed shell may have changed state.

After the previous Code Mode cell ends, choose:
- `resume HANDLE`: continue pending work, awaiting any known session rather than restarting it.
- `resume HANDLE retry`: retry only the current segment after resolving live work and inspecting
  uncertain effects. Optionally put one replacement segment of the same kind on the next line,
  with its own file selection or `shell` command.
- `resume HANDLE repair`: supply one workspace edit segment after the header to fix the cause,
  retry the failed segment, and continue its suffix in one call. Apply the same live-work and
  uncertain-effect checks as retry. The repair is retained under the same handle if it fails.
- `resume HANDLE accept`: continue after establishing the segment's intended state externally
  and resolving its native work. This records reconciliation, not application success.

Successful recovery automatically runs the retained suffix in the same carrier; no separate
resume call is needed. Remaining edit targets are revalidated. Cancellation of a wait alone
does not establish that its underlying process stopped.

Handles use temporary thread-scoped storage: one hour from creation, no renewal, ending
earlier on router shutdown. Invalid or unavailable handles execute nothing.
`hpatch_recover` remains for ordinary rejected edit-only scripts and directs mixed work to
this continuation interface. Use separate shell calls for interactive programs or explicit
shell batches. Mixed scripts edit workspace files, not `@shell/` sources.
Native-only clients use separate hpatch and shell calls.

Commands:

```text
in PATH
new PATH
mv PATH
rm
type TARGET VALUE
add DESTINATION VALUE
```

`in` selects an existing file. `new` selects a pending empty file. `mv` moves the active
file and preserves its baseline and pending edits. `rm` deletes the active file and clears
the selection. Repeat `in PATH` when switching existing files.

Targets:

```text
LINE:HASH                         complete logical line
LINE:HASH..LINE:HASH              inclusive complete-line range
LINE:HASH "TEXT" [N]              first N exact matches from that row through EOF
"TEXT" [N]                        first N exact matches in the immutable baseline
```

`type` replaces. An empty target-bearing `type` value deletes every target span, including
terminators owned by line and range targets. `add` inserts before a line or text destination;
`add EOF` appends. Ranges are not add destinations. A text target defaults to one match; every
requested non-overlapping match must exist or the script rejects.
When exact known target text spans logical lines or includes a trailing LF, encode that LF as
`\n` (or an equivalent `\u000A`) inside the quoted anchored or unanchored target. Keep the target
on one physical command line. Literal tab is accepted; carriage returns and other controls are
not.
For `N > 1`, verify the literal occurrence count in the baseline using existing evidence or
hgrep. Use separate verified row anchors when position matters.

Use inline JSON-compatible strings for short or single-line values. Include `\n` when an
insertion must form a complete new line:

```text
in parser.go
add 37:8c2f "// parseCommand parses one physical script line.\n"
type "return oldResult, nil" "return newResult, nil"
```

For multiline or escape-heavy values, choose the final-newline behavior explicitly:
`<<PATCH` keeps every body terminator; `<<PATCH-` removes exactly the final body terminator.
Both close with `PATCH` and preserve all other body bytes, including spaces and earlier blank lines.

For protocol examples or other payloads containing delimiter or opener lines, use
`<<TEXT` (keep final terminator) or `<<TEXT-` (remove one final terminator).
Prefix every payload line with one `|`, including blank lines; close with unprefixed
`TEXT`. Only the first bar is removed. Literal `PATCH`, `TEXT`, and nested examples
are safe payloads without quote or backslash escaping:

```text
type "old example" <<TEXT-
|type <<PATCH
|replacement
|PATCH
|type <<TEXT-
||text
|TEXT
TEXT
```

Use row/range targets with `<<PATCH` for whole-line replacements:

```text
in service.go
type 20:2ff7..28:d10b <<PATCH
func calculateResult(input Input) (Result, error) {
	return computeFreshResult(input), nil
}
PATCH
```

For a literal replacement that should keep the existing following newline or inline suffix,
use `<<PATCH-` (or an inline value without a final newline):

```text
in notes.md
type "old paragraph" <<PATCH-
first replacement line
last replacement line
PATCH
```

Literal targets own only their matched bytes. To delete a whole line, use a row target or
include its terminator in the literal target; deleting text alone leaves the line terminator.
For insertions, count separators already at the destination and include only the missing ones.
Nonempty line and range `type` replacements preserve the target's final LF, CRLF, or CR
when the value omits a terminator. Explicit terminators are authoritative.
`add` inserts byte-exact values and does not synthesize newlines.
A chomped body with only one empty line decodes to empty,
so replacing a row with it deletes the row rather than making it blank.
Authored spaces and blank lines are preserved except for language-aware formatting and
indentation correction.

Successful `advisory` lines describe authored whitespace boundaries before neighboring
edits or formatting; they are not errors. Check those boundaries against your intent.

An unindented heredoc body line beginning with `type ` or `add ` and ending with either
heredoc marker is reserved as a nested opener when the marker is its sole operand or follows
a space. Use a line-framed text block for literal HPATCH examples.

Existing-file edits require a target. Targetless `type VALUE` is valid only immediately after
`new`; create a file with at most one such initializer:

```text
new internal/target.go
type "package internal\n"
```

Every existing file has one immutable baseline for the complete invocation. Pending edits
do not shift later targets. Preserve required indentation prefixes in indentation-sensitive
languages such as Python.

Content introduced by a mutation is not targetable in the same call. After every successful
invocation, unchanged saved rows remain valid even when edits shifted their line numbers: mekugi
relocates an exact hash only when it identifies one row. For a routed whole-line or range
replacement, the router resolves that exact pre-edit target after the executor confirms
application.

Overlapping replacements or deletions and insertions strictly inside them reject. Boundary
insertions are valid. Multiple insertions at the same boundary render in script order.

Changed Go files are parsed and formatted before success. Supported Python, JavaScript, and
TypeScript files are syntax-checked when Tree-sitter support is available; supported indentation
corrections are automatic. Relative paths use the selected base directory when available; without
one, relative paths reject; parents for `new` or `mv` must exist.

### Rejected-script recovery

Use `functions.hpatch_recover` to repair the latest retained rejected script, preserving
unrelated prepared edits. Choose one payload form:

- For a wholly row-stale rejection, supply every listed current `C...` handle followed by
  its corrected ordinary HPATCH/2 target.
- For values, framing, paths, conflicting commands, or mixed corrections, use ordinary
  target-bearing `type`/`add` mutations against retained-script text.

Target-only example:

```text
C3:bcde0123456789abcdef0123456789abcdef0123456789abcdef0123456789ab "return oldResult, nil"
```

Put every listed target correction in one payload, copying current handles exactly
and supplying a different target for each.

Script-text mutations edit the retained rejected script, not workspace files. Use the diagnostic's
verified script rows or exact known literals; omit `in`, `new`, `mv`, and `rm`.
For example, `type "bad value" "fixed value"` changes that exact retained text.
Use ordinary value framing and keep the two payload forms separate.

Both forms preserve untargeted script text and reevaluate the complete script atomically.
A re-rejection becomes the next baseline: use its script rows and refreshed command handles.
Invalid corrections leave the workspace and retained baseline unchanged.

## Change handoffs

Hpatch results include `change hp_a1`; recovery keeps that ID. Review captured hpatch
edits with `hchanges read hp_a1..hp_a3`, rather than Git diff. Hand off IDs or inclusive
same-agent ranges instead of copying diffs. Use `--summary` only when you need an
operation/path and added/removed line-count overview, not before an already-needed
diff read; use `--history` to diagnose the full recovery chain.
Git status and Git diff remain useful for untracked, shell-generated, or unrelated
workspace changes; do not routinely pair them with hchanges for the same captured edits.

Reads default to 4,000 tokens. Flags may appear before or after IDs.
Optional `--max-tokens N`, `--path PATH`, and `--workspace DIR` narrow a read.
Workspace file paths accept recorded, workspace-relative, or absolute spellings.
An incomplete read supplies `--cursor HASH:BYTE`; repeat the same selection with that
cursor to continue. These are historical evaluated diffs, not current editable row
references or a record of shell edits. Counts describe each evaluation, not a combined
net change. Unconfirmed results are not proof of application.

## Reading and inspection reference

For ordinary file reads, use `cat` or bounded `sed`. Prefer `hcat` when its verified row
identities are useful for an anticipated edit.

Run one file per command as `hcat PATH [START:END]`. Quote paths with shell syntax and batch
already-known reads as separate commands in one shell script. A bare path reads the complete
file. A start line of `0` begins at line 1 without emitting line 0. An end past EOF warns after
returning available rows; a start past EOF fails. Copy a current `LINE:HASH` directly into an
HPATCH/2 target. If hcat reports an incomplete token-limited result, retain the emitted rows and
request a smaller range for the missing context.

Run hgrep with familiar ripgrep arguments and ordinary shell quoting, redirection, and
pipelines. Combine known patterns and paths with repeated `-e` arguments. Its output is
`"PATH":LINE:HASH TEXT`; copy a current target directly and never reconstruct a row.
Do not follow target-bearing hgrep output with hcat unless nonmatching context outside the
requested bounds is needed. If hgrep reports an incomplete token-limited result, retain the
emitted rows and narrow the patterns, paths, context, or file selection.

Both readers accept leading `--max-tokens N` (1–15500) for a strict stdout token
ceiling and `--preview-bytes N` (1–65536) for long-line inspection. For example,
`hgrep --max-tokens 2000 --preview-bytes 160 -F needle source.ts`.
Preview JSON includes a full-source row identity and an explicit UTF-8 prefix with
omitted-byte counts. Retain the identity, but obtain missing content before using the
preview as literal target text. Budget omissions still report incomplete results.

`hcat [-n N] [--tail]` selects first/last complete rows; `-n` alone skips tokenization.
Tail requires a line or token limit. Omitted rows are incomplete; use native selection,
not `| tail`, to retrieve an ending that a budgeted reader would otherwise omit.

For Go, JavaScript, TypeScript, JSON, and Python, use
`hsymbol refs PATH LINE SYMBOL [N]` for semantic references or
`hsymbol def PATH LINE SYMBOL [N]` for definitions. Supply an already-known
`LINE:HASH` instead of `LINE` to enforce a prior read; plain lines query the
current snapshot without requiring a preliminary verified read.
A leading `--workspace ROOT` chooses resolver scope and relative input paths;
its result paths are absolute. Other results are workspace-relative. `N` counts exact
language tokens on the selected line and may be omitted only when one exists. Copy emitted `"PATH":LINE:HASH TEXT` rows directly
into HPATCH/2 targets. Do not follow a complete hsymbol definition with hcat of the same span
unless non-declaration context is needed. Never treat an incomplete token-limited hsymbol result
as a complete definition or reference set.

Use `inspect_file PATH` for bounded metadata and a structural outline. Each outline entry's
`line` and `line_end` are copyable `LINE:HASH` identities for that inclusive span. Copy a
single-line span as a row target and a multi-line span as `line..line_end` with no spaces.
Paths are absolute or working-directory-relative, like hcat. Use bounded hcat when the
outline does not provide source text needed for the next decision.
<!-- mekugi-model-instructions:end -->
