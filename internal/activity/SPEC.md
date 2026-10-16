# Activity content and retained output

## Purpose and scope

Interpret classified operations and retain bounded host output for activity feeds
and output dialogs. Host adapters supply the facts and retain authority over tool
identity, execution, outcomes and captured changes. Parsing neither evaluates
source nor establishes that an operation ran.

## Behavior

Blank lines separate operations except inside fenced source. Each operation keeps
its verb, label, source language/body, explicit timeout annotations and read
targets. Unknown labels remain available rather than being discarded. Read ranges
belong to file reads; target-only operations retain their literal operands. Skill
program invocations retain program source rather than becoming skill reads.

Edit receipts retain their source across adjacent tool-managed-file summaries.
An edit with no resolved target explicitly says that paths are unavailable.
Generated edit operations omit their diff bodies from the feed; authored Markdown
and retained change evidence are unaffected.

The same content values can carry authored messages and reasoning supplied by host
adapters. Their kind does not establish host execution state.

The parser owns no feed, grouping across invocations or terminal state. Consumers
attach observed timing/outcomes, aggregate read invocations and choose rendering.
Source values contain no styles, geometry, callbacks or live handles. Shared fence
and inline-code recognition belongs to `sourceanalysis`.

## Retained output

Output preserves bounded logical lines independently of the terminal's animated
tail. Carriage returns replace the unfinished line; CRLF completes it. Shared
[text normalization](../../libraries/textsanitize/SPEC.md) removes controls and
expands tabs. Internal blank lines remain available; trailing padding is omitted.

Each output retains up to 1 MiB including line slots, with a 16 KiB line cap.
Dropped lines and line truncation remain distinguishable. The session shares a
16 MiB budget across Main and children, releasing the oldest completed output
first. Running output is protected and may temporarily exceed the shared budget.
Loss of segment attribution releases its separate output rather than fabricating
per-command evidence.

Host completion immediately records the observed outcome and replaces streamed
content with the supplied aggregate, including an explicitly empty aggregate.
Absent exit status remains unknown. Later deltas cannot change completed output.
Truncation records loss within the current stream or replacement aggregate, even
when that line has subsequently been dropped.

Read-only snapshots keep their content and outcome after later append, replacement
or release. Consumers can use the output version to avoid rebuilding unchanged
views. Restored output comes from retained host aggregates or validated segment
reports, never reconstructed unrecorded streams.

The application owns one retention lifetime per attached session and serializes
mutation. Session replacement drops it with the associated feeds and dialogs;
feed trimming alone does not invalidate an open dialog's output. Detached command
reports retain timing and outcomes, but discarded segment output remains
unavailable. Returning to those commands uses the host's combined output.
Terminal pacing, selection and fallback to a released output's display tail remain
consumer-owned.

## Acceptance

Parser cases cover reads, literal operands, timeouts, fenced programs, edit-source
inheritance and unavailable targets. Live/restored feed and output-dialog checks
exercise the same parser, preserving invocation membership, actual rendered rows
and source-copy behavior. Output cases cover chunked CRLF, bounds, shared retention,
aggregate replacement, unknown exits and snapshot stability. Real stream/segment
and dialog consumers cover paced completion, release fallback and source numbering.
