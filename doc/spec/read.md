# Shell-routed verified-row reader

## REQ-READ-001 — Shell-routed verified-row reader

In Mekugi mode, the model receives `hpatch` and `shell` as standalone custom
tools. The [agent-guidance contract](guide.md) owns persistent workflow guidance.
Hcat, hgrep, hsymbol, and inspect_file are private commands available only inside
the shell execution boundary: their specifications are not sent as model-visible
tools, direct model calls to their names are not routed, and no executable frontend
is installed for them.

The private `hcat` command accepts one or more files:

```text
hcat [--max-tokens N] PATH [START:END] [PATH [START:END] ...]
```

The shell owns quoting and argument separation. A path containing whitespace is therefore one
ordinary quoted shell argument. `START:END`, when present, is an inclusive logical-line range
whose positive one-based base-ten endpoints must be ordered. The start line must exist. An end
past EOF returns through the final line. Each range applies to the preceding path.
For example, `hcat first.go 1:200 second.go third.go 200:300` selects three files.
A numeric `START:END` argument after a path is a range; prefix a range-like filename
with `./` to read it as a file. `--` ends option parsing, not a file selection.
Single-file reads also accept `-n N`, `--preview-bytes N`, and `--tail` as described below.

The shell boundary resolves the authenticated private commands for the current
thread. These names are not filesystem entries and do not depend on `PATH`.
Deployments with separate router and executor filesystems must make the authenticated
runtime available at the same absolute location on both sides.

Hcat runs in the shell carrier's actual working directory. Relative and absolute paths keep
their ordinary process meaning. Codex, not the router or hcat, owns sandbox and filesystem
permissions. The worker accepts only regular UTF-8 files and never mutates them. It emits only
the requested logical lines:

```text
LINE:HASH TEXT
```

`LINE` is the positive one-based logical line number. `TEXT` is exact logical-line content
without its terminator. `HASH` is lowercase hexadecimal for the first two bytes of SHA-256
over that exact content, including leading spaces and tabs. A trailing file terminator does
not create an additional empty line. Missing, inaccessible, non-regular, non-UTF-8,
reversed-range, and start-past-EOF reads return concise stderr and nonzero status.

Readers share `--max-tokens N`: a strict GPT-5 stdout ceiling from 1 through 15,500,
defaulting to 4,000. Options may surround operands, stop at `--`, and cannot repeat.
Ripgrep option values remain values even when their spelling matches a reader flag.
Missing or invalid budgets reject before reading source content or starting a resolver.
Outer host budgets remain independent.

Verified-row commands admit only complete rows. The first row that does not fit and all
later rows are omitted; truncation preserves admitted stdout and returns nonzero.
Hcat retains omitted rows through the shared `hread` interface below, including omitted
prefixes from tail selection. Recovery reads the captured snapshot without reopening the
source. If a row exceeds the inspection bound or recovery exceeds its 16 MiB capacity,
the result reports that recovery is unavailable and suggests narrowing the source range.
Valid selected output remains usable, including tail rows after a source-bound row.

Hcat and hgrep additionally accept `--preview-bytes N`, at most once.

`--preview-bytes` accepts 1 through 65,536 and changes each stdout row to a JSON
record with `row` (the complete source's verified `LINE:HASH`), `preview` (a UTF-8
prefix no larger than N bytes), `source_bytes`, and `omitted_bytes`. Hgrep also
includes `path`. This is an explicit inspection format, not exact source-row text.
The row reference remains usable as a whole-row target; preview text must not be
treated as a complete literal replacement or match. Hashing still covers every
source byte, never just the prefix. A prefix may end before N to avoid splitting
a Unicode character. Preview records themselves count against the same stdout
token budget. Preview byte omissions are intentional and counted in each record;
omitting an entire record at the token ceiling is still incomplete and nonzero.

Exact token counting must remain practical for long unbroken words and whitespace up to
the bounded candidate size. The pinned model's token identities and splitting rules
remain unchanged; large pieces must not require quadratic repeated merge scans.

Hcat additionally accepts `-n N` and `--tail`, each at most once. Line counts
are canonical positive safe integers. `-n` selects first/last N complete logical source
lines within the requested range, before any explicit token ceiling. Without a token
ceiling, line mode bypasses tokenization and the default token admission rule; selected
exact line lengths determine storage. Omitted lines retain the incomplete/nonzero contract.
`--tail` requires `-n` or `--max-tokens`. It selects a suffix of complete formatted rows
from the file or requested range, in original source order, within any supplied limits. It never cuts a row or
skips an oversized final row to show earlier content. Preview mode still takes each
selected source row's prefix. Omitted earlier rows use the existing incomplete/nonzero
contract; a complete suffix covering every selected row is successful. Empty files succeed.
Tail reads scan and validate the whole file, including source beyond the requested range. A source-bound row clears earlier tail candidates;
later verifiable rows may still be retained. Hgrep does not accept this option.

Hcat retains its bounded whole-row candidate storage in token-limited and preview modes. A source
row exceeding 1,984,000 UTF-8 bytes cannot be verified by this reader; it is omitted
with a distinct source-bound diagnostic and nonzero status. Use a byte-window
reader when such a file needs content inspection. Whole-file UTF-8 validation
still runs even after stdout admission stops.

Acceptance:

1. A whole-file or bounded read emits exact UTF-8 rows. Equal lines at different positions
   have distinct row references, and indentation changes the hash.
2. `hcat PATH`, `hcat PATH START:END`, and a shell-quoted path containing whitespace work.
   Additional paths select coordinated reads; a second range for the same path fails.
3. Several hcat commands in one shell call execute in authored shell order.
   One selected file keeps the unframed single-file output.
4. Reading and whole-file UTF-8 validation use bounded streaming storage and observe
   cancellation. Token-limited output retains only admitted complete rows without a second read.
5. Success and failure reach Codex through the model-visible shell carrier. Replay retains
   the original shell call and output; it never synthesizes a model-visible hcat call or
   includes the shell call in editable rejected-script recovery history.
6. Router startup validates hcat inside the immutable built-in snapshot without installing a
   frontend. Passthrough mode loads and exposes none of these replacement surfaces.

7. Hcat and hgrep enforce a caller's strict token ceiling identically, including
   complete-record admission, preserved prefixes, and explicit nonzero incompleteness.
8. Preview records retain exact full-source identities, bounded UTF-8 prefixes,
   and byte omission counts for long rows. Default exact output is unchanged.
9. Invalid and duplicate options reject before source content is read. Retained
   path resolution can precede option validation. Quoted paths, line ranges,
   and thread-private retained reads work with options before or after operands.
10. Tail selection works with either option order, quoted paths, ranges, previews,
    and retained descriptors. Missing limits and repeated `--tail` reject before reading.
    In token-limited mode, long and source-bound rows cannot cause unbounded storage or prevent retaining later
    rows; invalid UTF-8 anywhere in the file still fails.

### Managed read continuation

Shell output, searches, symbol references, and change reviews use one read continuation:
`hread REF [--stdout|--stderr] [--max-tokens N]`. An incomplete result supplies the exact
`read: incomplete; next_call: hread REF` command. There is no separate cursor flag or
caller-composed hash/offset. References use short lowercase word handles, such as
`maple`, with a decimal suffix when needed. The same visible format is used for change,
recovery, continuation, and journal handles. Handles are feature-scoped locators, not
integrity hashes or secrets; full snapshot fingerprints remain internal. Earlier `r_`
references are unsupported. Their stored files remain accounted for and protected by
existing session ownership until normal retention cleanup reclaims them.

An initial reference owns only omitted output or a descriptor of existing durable evidence,
never an executable script. Change-review descriptors retain their selection and full
fingerprint; they do not duplicate diffs and reject changed projections. A subsequent
reference stores only the original reference, two stream positions, the stream selection,
and the full original-record fingerprint. It does not duplicate output. Repeated reads
produce identical pages and reuse next references while those continuations remain
retained. If a continuation is reclaimed but its source is still retained by another
session, reading the source may allocate a new next handle; the expired handle is
not reassigned or revived. A continuation inherits its selection; a
different stream selection must start from the initial reference. Budgets may change.

Empty streams have no frame. A page containing only stdout is unframed. Stderr is always
framed, and a page containing both streams frames both. Frames open with `[stdout bytes]`
or `[stderr bytes]` and close with `[/stdout]` or `[/stderr]` on their own lines.
The unit is `bytes`, `rows`, or `json`; one separator newline before the closing frame
is not payload. Framing is not source content, and raw
byte fragments are not verified rows. A rows page never cuts a row; a JSON page is a valid
array of complete entries. A unit that cannot fit fails explicitly without a nonadvancing
reference. The budget includes frames, defaults to 4,000 GPT-5 tokens, and accepts 1–15,500.
Actual frame size determines minimum usable budgets; there is no separate fixed cutoff.
Page completion returns status 0, and an incomplete page returns status 1 with the next
call on stderr. The original producer's exit status is preserved independently.

The authenticated executor persists records through the existing managed replay-store
locking and atomic write/fsync path before exposing references. There are no standalone
temporary output dumps. Omitted data is bounded to 16 MiB, encoded records to the existing
replay record limit, and all read records to a separate 256 MiB quota. Capacity or storage
failures are explicit. Storage pressure uses the session-retention policy in [REQ-ROUTER-001](router.md),
never arbitrary record eviction. Active readers pin complete change-review and source dependencies.

A reference is portable through visible history across fork, side-thread, agent/model
switch, and router restart, without depending on a live parent or routing-session ID.
It remains valid while its session data is retained under that policy. Missing, corrupt, altered, or
out-of-range records fail rather than replay producers. These durable read references do
not extend the lifetime of executable recovery handles or native sessions.

### Coordinated multi-file reads

The shell-private `hcat [--max-tokens N] PATH [START:END] [PATH [START:END] ...]`
command automatically composes coordinated reads when 2–16 paths are selected.
No `--batch` flag or inter-file separator is used. Single-file calls retain their
existing output and options. Multi-file reads support only `--max-tokens`, which
may surround operands before `--`; single-file-only options reject before reads.
The default total display budget is 4000, with the usual 1–15500 token option bounds.
The bundle reserves conservative framing space based on
quoted path lengths, then divides the remaining budget equally among readers. If
framing cannot fit, it rejects before reading any file. Source parsing, permissions,
logical rows, bounds, and verified identities remain owned by hcat.
No new source-selection semantics are introduced.

A manifest precedes all bodies and reports each input path, displayed and retained
omitted inclusive line ranges (or `none`), completion state, and an optional `next_call`
using hread. Bodies are labeled by manifest index. Diagnostics and omitted rows are
persisted before exposing the manifest. Unrecoverable omissions say `unavailable`,
never complete. Each actual hcat execution participates in AX read observation.
Any failed or incomplete reader makes the bundle nonzero, but other files are still read.
Cancellation and storage failure stop delivery with an error. For direct shell display,
the bundle fits its complete manifest and whole preview rows within the smaller of its
requested limit and the shell's remaining escaped-token budget, after framing reserves
and preceding stdout/stderr. Rows removed from previews remain behind per-file hread
receipts. Display-only trimming preserves the readers' exit statuses and shell control
flow. Redirected files, pipelines, and command substitutions retain the requested
reader budget. If even the manifest cannot fit, normal outer retention still applies;
the outer limiter and host budget remain independent safeguards.

Acceptance: multiple files receive preview space under one total budget; omissions
remain recoverable after source changes and router restart; invalid files and empty
files have distinct manifest states; no executable basename or model-visible tool
is installed. Pipelines and redirections retain ordinary shell behavior.
