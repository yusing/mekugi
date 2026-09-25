# Explore output filter

## REQ-EXPLORE-FILTER-001 — Decision-model filtering of search output

Mekugi mode enables the filter by default when the TypeSafe API key is non-empty.
`[typesafe].api_key` in `mekugi/config.toml` takes precedence over
`TYPESAFE_API_KEY`, including an explicitly empty file value.
Without the key, in passthrough mode, or with `--explore-filter=false`, stock
results are unchanged and nothing is sent to TypeSafe. An explicit
`--explore-filter` or `--explore-filter=true` without the key, or in passthrough
mode, rejects startup.

The filter is the one exception to unchanged stock result bytes. It never
runs, repeats, or changes a command; it only rewrites the model-visible text of a
completed `exec_command` result before the request is forwarded.

Eligibility is decided in code. The call is native `exec_command`, or a Code
Mode `exec` that transparently prints the complete result of exactly one literal
`tools.exec_command` call, for example `text(await tools.exec_command({...}))`.
Code Mode must finish in the same call and return either a text header followed
by one JSON result text block, or a single string containing both. The nested
result must contain a terminal exit code and no live session ID. Its metadata
values and the host header are preserved; only its `output` string is filtered.
Printing the result via a local binding or `JSON.stringify` is also transparent.
The existing metadata-copy form `Object.assign({}, result, {retained:false})`
is accepted: it changes neither stdout nor completion metadata, and its
`retained` value is preserved rather than treated as a recovery guarantee.
Dynamic arguments, output-only printing, other transformed results, multiple calls,
batches, mixed media, malformed or outer-truncated JSON, and continuation calls
stay unchanged. JavaScript is inspected, never evaluated or rewritten.

The command's `cmd` is exactly one literal invocation, with no pipeline, list,
redirection, assignment, expansion, glob, or leading `~`. An `npx`, `bunx`, or
`uvx` prefix is looked through. Each family defines its units:

| Family | Commands | Unit | Accepted exits |
| --- | --- | --- | --- |
| Paths | `rg`, `grep`, `egrep`, `fgrep`, `find`, `fd`, `fdfind`, `git grep`, `git ls-files` | rows sharing an existing leading path | 0 |
| Diagnostics | `go vet`, `go build`, `staticcheck`, `golangci-lint run`, `gopls check`, `tsc`, `ruff check`, `mypy`, `flake8`, `pylint` | rows sharing an existing leading path, `path:` or `path(line,col):` | 0, 1, 2 |
| Commits | `git log`, `git reflog` | one full entry from its `commit` row, or one `--oneline` row | 0 |
| Diffs | `git diff`, `git show` | one file diff from its `diff --git` row | 0 |
| Help | any `--help`, `<tool> help …`, `man <page>` | one indented option paragraph | 0 |

Formats whose rows do not begin with a unit key are ineligible: `--json`,
`--heading`, `-p`/`--pretty`, `-0`/`--null`/`-z`, `--graph`, `-exec`, and similar.
Path units resolve against the call's working directory; indented rows and `--`
separators continue the preceding path unit. More than 128 path units group by
parent directory. Rows in no unit, such as diagnostics headers, section
headings, a `git show` commit header, or truncation markers, are always kept.

A result is judged only when it follows the latest model output, has at least
1 KiB of output, and splits into 4 to 384 units.

The router sends TypeSafe's `jev-1.13.0` one Noul per unit, in batches of up to
16 units and about 6,000 sample characters. Each state holds the latest user
request, the assistant text and readable reasoning summaries that preceded the
call, the command, its working directory, and each unit's key with a sample: up
to 6 rows, or up to 12 hunk headers and changed rows for a file diff. The Noul
asks whether the agent needs to see that result to make progress on its intent.

A unit is kept when its probability is at least 0.25 or it ranks in the top 3.
The filtered result replaces the stock one only when it saves, net of the
omission line, at least 512 bytes and a quarter of the output. Any judgment
failure, a missing answer, or the 10-second budget leaves the result unchanged.

A filtered result keeps the stock header and the kept rows in their original
order, and ends with one line naming the omitted count, omitted unit labels with
their row counts (bounded to a quarter of the omitted bytes, at most 600 bytes,
then `+N more`), and `mread REF` for the complete original output. File and
directory labels are never shortened. The full output is persisted before the
reference is exposed.

Each decision, including a decision to keep the stock result, is recorded by
call ID and output digest for the router process. A later request carrying the
same output forwards the same bytes without another judgment. After a router
restart, historical results are forwarded unchanged.

With `--debug`, each judged result records an `explore_filter` event with its
family, outcome, row, byte, and unit counts, omitted and saved counts, input
tokens, and elapsed time, without content.

Each newly filtered result also emits a pane-only `output_filter` activity event
for its validated originating agent, including `/root`. The event carries the
call ID, a command preview bounded to 512 bytes, family, before/after stdout byte
and local token counts, removed/total lines and units, elapsed milliseconds, and
TypeSafe usage. Token counts use `o200k_base` on decoded stdout, including the
omission/recovery footer in the after count. They are not billed provider tokens;
unavailable counts are absent, not zero. Recovery is durable before emission.

The pane attaches a muted compact line after the matching command event, such as
`~tokens 1.2K→700 (-41.7%) · −24/49 lines · 0.6s`. Token displays use the shared
K/M/B usage formatter; structured counts remain exact. If the original command is no
longer in the feed, its bounded preview precedes that line. The displayed line
does not repeat byte counts, unit counts, or TypeSafe usage. Full structured
counts remain in the authenticated pane event. Kept results, failed judgments,
and cached replays emit no compaction event. Pane unavailability never adds these
metrics to model-visible commentary or changes filtering.

TypeSafe consumption is accumulated separately per originating transport thread
for the router lifetime and included in the [token usage report](commentary.md).
Every HTTP attempt counts, including retries and judgments whose results are
kept or whose answers are unusable. Missing or invalid provider usage creates an
explicit gap; local token estimates never replace it. Cached decisions make no
request and add no usage. Costs and net monetary savings remain unavailable
without configured TypeSafe pricing.

Acceptance:

1. Without the key, in passthrough mode, or with `--explore-filter=false`, no
   request is sent to TypeSafe and results are unchanged.
2. An eligible result whose units are judged unrelated loses only those units'
   rows; its header, kept rows, and rows in no unit are unchanged, and
   `mread REF` returns the complete original output.
3. Pipelines, unaccepted exits, running sessions, short outputs, other
   programs, and judgment failures leave the result byte-identical.
4. A filtered result is always smaller than the stock one, including path lists
   whose units are single rows.
5. Replaying the same output does not judge again and forwards identical bytes.
6. Outputs before the latest model output are never judged.
7. Transparent single-command Code Mode results use the same judgments and
   durable recovery as native results. Their metadata and header survive;
   unsupported or ambiguous Code Mode output remains byte-identical.
8. Pane events retain origin and metrics through delivery retries and reconnect;
   they never fall back into model context. A filtered command is shown once with
   a muted metrics line after it, not a second verbose event.
9. TypeSafe totals retain known consumption across retries, answer failures, and
   unchanged outputs, without double-counting decision replay or other threads.
