# Authenticated raw-row reader and managed continuation

All frontends accepting `--max-tokens N` also accept `--max-tokens=N` before
their operand terminator or command boundary. Missing, invalid, out-of-range,
and repeated token budgets use the same diagnostic:
`--max-tokens requires one integer from 1 to 15500 and cannot repeat`.
Continuation commands use `next_call: COMMAND`, never an equals-sign attribute.

## REQ-READ-001 — Authenticated raw-row reader

In Mekugi mode, `mcat` is a session-private executable on the wrapped Codex
`PATH`. Stock `tools.exec_command` owns its execution. The router authenticates
the executable against the pinned tool snapshot, but does not expose `mcat` as a
model-visible custom tool or intercept it as a private shell command. The
[agent-guidance contract](guide.md) owns persistent workflow guidance.

`mcat` accepts one or more files:

```text
mcat [-n N] [--max-tokens N] [--tail] [--number] PATH [START:END ...] [PATH [START:END ...] ...]
```

The process host owns quoting and argument separation. A path containing
whitespace is one quoted argument. `START:END` is an inclusive logical-line
range; `START-END` is equivalent. Bounds use a canonical nonnegative start and positive base-ten end. `0:END` is
accepted as `1:END`. The start line must exist; an end past EOF returns through the final
line without a diagnostic. Each range applies to the preceding path. A numeric range after a path is
therefore an operand, so prefix a range-like filename with `./`. `--` ends
option parsing. Several ranges may follow one path, each counting toward the maximum of
16 reads. Before reading sources, `A:+N`, `A,B`, and bare numeric operands reject with a
corrected command. A nonexistent `PATH:N` operand suggests `mcat PATH N:N`; an existing
literal file retains its ordinary meaning. `-n A:B` suggests `mcat PATH A:B`.
Start-past-EOF messages use colon ranges and the observed row count.

The executable inherits the stock executor's working directory and environment.
Relative and absolute paths retain their ordinary process meaning. Codex owns
sandbox and filesystem permissions. The worker accepts only regular UTF-8 files
and never mutates them.

### Raw logical rows

By default, single-file output contains only the selected source text, without line numbers,
JSON records, or other prefixes. `--number` prefixes every selected row, including blank rows,
with its absolute one-based source line number in a six-column right-aligned field followed by a tab,
matching `nl -ba`. Numbering is applied before token and line limits and retained in omitted
rows for `mread` continuation. Multi-file bodies use the same numbering for each source.
CR, LF, and CRLF are recognized as
logical terminators. Every selected logical row is emitted with one LF,
including an unterminated final row. A trailing source terminator does not create
an extra empty row. Empty files succeed with empty stdout. The UTF-8 BOM, when
present in the first logical row, remains source content.

`mcat` output is contextual source, not a verified edit identity.

Missing, inaccessible, non-regular, non-UTF-8, reversed-range, and
start-past-EOF reads return concise stderr and nonzero status. An end past EOF
is an ordinary clamp: it returns the available rows with successful status and no stderr,
so only an invalid start reports an EOF error.
Whole-file UTF-8 validation continues after stdout admission stops.

### Bounds, head, and tail

`--max-tokens N` sets a strict GPT-5 stdout ceiling from 1 through 15,500,
defaulting to 6,000. Options may surround operands before `--` and cannot
repeat. Missing or invalid budgets reject before source content is read. Outer
host output budgets remain independent.

Only complete raw rows are admitted. The first row that does not fit and all
later rows are omitted; admitted stdout remains usable and the command exits
nonzero. Omitted rows are retained through `mread` before their reference is
exposed. Recovery reads the captured bytes without reopening the source. If a
row exceeds the 1,984,000-byte inspection bound or retained output exceeds
16 MiB, the result reports that recovery is unavailable and suggests narrowing
the source range or using `mrun` with a byte-oriented command such as `head -c`.

Single-file reads additionally accept `-n N` and `--tail`, each at most once.
Line counts are canonical positive safe integers. `-n` selects the first N rows,
or the last N rows with `--tail`, while retaining the default token ceiling unless overridden.
An incomplete recoverable read reports the absolute shown range, selected row count, budget,
and continuation in one line. A nondefault budget is carried into that command.
`--tail` requires `-n`
or `--max-tokens`, preserves source order, and never cuts a row or skips an
oversized final row to expose earlier content. Tail scans still validate the
complete file. Multi-file reads reject `-n` and `--tail`.

The executable frontend owns one AX read observation per invocation, including
invalid arguments and failed reads. It records the inherited thread identity but
does not invent call correlation for a stock external command.

Acceptance:

1. Whole-file, range, head, and tail reads emit raw UTF-8 logical rows with the
   newline behavior above and no line prefixes.
2. Quoted, absolute, relative, option-like, and range-like paths keep their
   documented process meanings.
3. Token and line limits retain only complete rows; `mread` reconstructs omitted
   rows after source changes and router restart.
4. Streaming storage is bounded, cancellation-aware, and validates the complete
   source. Invalid UTF-8 outside the displayed selection still fails.
5. Router startup validates `mcat` in the immutable snapshot and installs one
   session-private frontend. No alternate reader name is installed.
6. Stock execution preserves cwd, environment, argv, stdout, stderr, status,
   pipes, and redirections without shell-source transformation.

### Managed read continuation

Bounded command output, `mcat`, symbol references, and change reviews use the
authenticated `mread` executable frontend for retained continuations:
`mread REF [REF ...] [--stdout|--stderr] [--max-tokens N]`. An incomplete result supplies
the exact `read: incomplete; next_call: mread REF` command. There is no separate
cursor flag or caller-composed offset. References use short lowercase word
handles, such as `maple`, with a decimal suffix when needed. Handles are
feature-scoped locators, not secrets; full snapshot fingerprints remain internal.

Read and recovery handles allocate within one durable session namespace shared
by the root thread and its subagents. Unrelated sessions restart the sequence.
Forks and side threads snapshot the source's handles and allocation position
once, then allocate independently without changing inherited references.
Routing keys, model switches, request truncation, compaction, and router restart
do not change the namespace. Equal handles in different sessions cannot address
each other's records.

An initial reference owns only omitted output or a descriptor of existing
durable evidence, never an executable script. A subsequent reference stores the
original reference, two stream positions, the stream selection, and the original
record fingerprint without duplicating output. Repeated reads produce identical
pages and reuse next references while retained. Expired handles are not
reassigned or revived. A continuation inherits its stream selection; selecting a
different stream starts from the initial reference. Budgets may change.

Empty streams have no frame. Source reads report `[rows START:END]`; multiple handles
have per-handle labels, share one total budget, and produce one combined next call.
Generated producer-limit diagnostics are not stored as omitted source stderr; actual
captured command stderr remains evidence and is not removed by text matching.
A page containing only generic stdout is unframed. Stderr
is always framed, and a page containing both streams frames both. Frames use
`[stdout UNIT]` or `[stderr UNIT]` and matching closing markers, where `UNIT` is
`bytes`, `rows`, or `json`. Framing is not payload. A rows page never cuts a
complete LF-framed row; a JSON page is a valid array of complete entries. A unit
that cannot fit reports its estimated token requirement and an exact retry command, without
a stack trace or nonadvancing reference. Units above the accepted maximum instead name
byte-oriented recovery. Malformed path/range operands explain that REF must be a returned handle.
The token budget includes frames, defaults to 8,000, and accepts 1 through 15,500. Page
completion returns status 0; an incomplete page returns status 1 with the next
call on stderr. The producer's status is preserved independently.

The authenticated executor persists records through the managed replay-store
locking and atomic write/fsync path before exposing references. There are no
standalone output dumps. Omitted data is bounded to 16 MiB, encoded records to
the replay record limit, and all read records to a separate 256 MiB quota.
Storage failures are explicit. Cleanup follows the session-retention policy in
[REQ-ROUTER-001](router.md) and does not reclaim active dependencies.

Acceptance: the session basename `mread` resolves through the authenticated
pinned frontend and stock executor, preserves stdout, stderr, and status, and
does not route through a private command dispatcher. The worker binds
`CODEX_THREAD_ID` before reading, so unrelated sessions cannot use equal handles
while inherited fork and side-thread ownership survives restart and cleanup.

### Coordinated multi-file reads

For 2–16 reads, the same executable frontend composes one coordinated result.
No `--batch` flag or extra basename exists. Multi-file reads support only
`--max-tokens`. The total budget defaults to 6,000 and retains the usual
1–15,500 bounds. The compositor reserves framing space based on actual path token cost,
divides the remaining budget equally, then redistributes unused shares from complete files
to incomplete reads in one reread pass. It verifies the complete rendered result against the
budget, retaining any extra rows removed to fit framing. Insufficient manifest allowance
rejects before reading. The generated `mcat` implementation remains the sole owner
of source parsing, UTF-8 validation, logical rows, selection, and token admission.

A manifest precedes the bodies and has a row for each failed, incomplete,
empty, or continued input, reporting its input index, path, displayed and
retained omitted inclusive line ranges or `none`, and completion state. One combined
`next_call: mread A B ...` follows an incomplete bundle, with one retained source per read.
A complete nonempty read has no row.
Each body header names its input index, path, and displayed range. Source
diagnostics appear on stderr with each line prefixed by the source path; redundant
per-file token-limit lines are omitted because the manifest already reports incompleteness.
Omitted rows are persisted before the manifest is exposed. Unrecoverable
omissions say `unavailable`. Any failed or incomplete source makes the
invocation nonzero, but other sources are still read. Cancellation or storage
failure stops delivery.

The requested `mcat` budget bounds the complete manifest and bodies. Stock host
budgets are independent safeguards and may retain outer command output without
changing `mcat`'s source-selection result. Pipes and redirections receive the
ordinary executable bytes. Omissions remain recoverable after source changes and
router restart; invalid and empty files have distinct manifest states.
