# Activity

## Purpose and scope

Own host-operation feeds, agent roster derivation and bounded output, with
renderer-neutral publications and terminal feeds and dialogs. Application adapters
supply typed source facts and reconcile host items; the host retains authority over
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

The parser owns no feed, grouping across invocations or terminal state. Presentation
values enrich parsed content with observed timing, outcomes, identities, questions,
hook facts and bounded output tails. Terminal disclosure selects eligible adjacent
operations; neutral grouping joins their values and keeps flat original membership.
Read merging, reasoning compaction, batch flattening and stage folding preserve
those identities for dialogs and cross-pane navigation.
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

## Lifecycle and integration

The composed activity service keeps feed models, roster state and output retention
in the application's attached session store. Main and children have distinct feeds
and share output retention; separate attachments and offline
replay resets receive distinct stores. Confirmed replacement retires the old store;
a failed replacement keeps it. The application serializes access. Replacement
drops output state with the associated feeds and dialogs;
feed trimming alone does not invalidate an open dialog's output. Detached command
reports retain timing and outcomes, but discarded segment output remains
unavailable. Returning to those commands uses the host's combined output.
Terminal pacing, selection and fallback to a released output's display tail remain
consumer-owned.

## Records and Codex connection

The module stores no durable records and registers no Codex mechanism. Its
application adapter supplies original typed observations; restored observations
come from host history and command-segment records. One record owns each source,
its neutral values and its revision through replacement, mutation, removal and
history reordering. Publication sequence and display order are independent.
Retention keeps the newest 2,000 records while protecting live invocations.

The model derives canonical roster order, observed status, latest activity and
active skills. Missing parents promote their children without reordering siblings.
Main and child identities stay distinct; Main's roster summary reads its existing
feed rather than copying it. Loaded skills come from confirmed reads or submitted
attachments. Failed/declined segments load nothing; independently successful
segments still count after aggregate failure. Feed trimming retains context loads,
compaction clears them, and bounded history restoration cannot overwrite a newer
live reset. Unknown context is not an empty confirmed set.

`presentation` owns records, semantic derivation, plain values and canonical flat
group membership. Typed adapter evidence stays private to source records; published
values contain no callbacks, terminal state or live output handles. `terminalview`
owns disclosure annotations keyed by source identity, geometry, styles and wrapping.
Renderer adapters own focus, offsets, animation and cache invalidation. Main
invalidates dependent layouts; ordinary Activity updates retain unrelated completed
layouts. The [presentation contract](CONTRACT.md) defines observable behavior.

Retained streams stay with their invocation or segment. Feed publications carry
bounded tails and scoped output identities rather than whole retained snapshots.
Feed syntax uses bounded retained context, preserving source headers without
publishing whole streams. Full immutable snapshots are requested for dialog
content. An open dialog retains direct output handles with its flat original pages, so feed
aging does not invalidate them. It refreshes from unpaced source records before
visible feed reveal and follows newly observed segments. Release falls back to the
bounded tail without reviving discarded bytes or inventing a successful exit.

## Failure and absence

Output bounds disclose truncation or release. Omitting the composition entry stops
attached output retention; host execution, command outcomes and bounded display
tails continue. Retention does not introduce a second transcript or execution path.

## Acceptance

Parser cases cover reads, literal operands, timeouts, fenced programs, edit-source
inheritance and unavailable targets. Live/restored feed and output-dialog checks
exercise the same parser, preserving invocation membership, actual rendered rows
and source-copy behavior. Output cases cover chunked CRLF, bounds, shared retention,
aggregate replacement, unknown exits and snapshot stability. Real stream/segment
and dialog consumers cover paced completion, release fallback and source numbering.
