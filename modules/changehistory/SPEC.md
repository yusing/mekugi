# Change history

## Purpose

Capture, retain, review, apply, and revert observed filesystem change evidence.

## Scope

The module owns passive baseline acquisition, filesystem reconciliation, private
workspace checkpoints, change identities, stream allocation, attempt membership,
retained review and mutation semantics, frozen read selections, semantic retirement,
and saved-review presentation. The application binds selected caller/workspace
authority and decoded host completion to capture, then publishes immutable evidence
before its change membership. Host execution and provisional live previews remain
separate. The terminal view owns the saved-Diff pane over the shared diff renderer;
application evidence loading remains independent of terminal interaction.

## Behavior

### Passive capture

The `capture` API acquires bounded before-state for literal patch and command
inputs, then reconciles observed endpoint differences. The shared command-scope
classifier supplies source facts; capture owns merged command scope, filesystem
reads, limits, incomplete evidence, rename/copy pairing and authored-versus-managed
provenance. Named operands and workspace checkpoints share admission, so stock
calls, terminal continuations, and locally submitted shell commands use the same
recorder. Preview-only capture suppresses workspace checkpoints even when a caller
supplies that owner. Reconciliation returns evidence and comparison claims with
their scope, coverage and snapshot diagnostics. Missing fallback narrows coverage
without discarding named evidence. Preview consumers use the same assessment
without publishing provisional values as retained changes.

Workspace checkpoints are private comparison state. Overlapping application
windows exchange owner-issued claims so a later comparison does not record an
already claimed effect. Capture merges sibling observations without mutating their
inputs or losing command order and repeated-write uncertainty. It selects the
earliest witnessed checkpoint from supplied request sequences and reduces admitted
host outcomes without treating an outer cell's success as nested-command success.
The application coordinates window completion with host and command-segment facts;
replay cannot reopen those windows. Patch batches share
one aggregate capture bound and one endpoint window. The
[change-evidence contract](../../docs/contracts/change-evidence.md) owns scope,
coverage, attribution, fallback limits, and acceptance cases.

### Bounded read command

The session-private `mchanges` executable lists the current thread's change
IDs or reads selected IDs and ranges:

```text
mchanges --list [ID[..ID] ...] [--workspace DIR] [--max-tokens N]
mchanges [--mine | ID[..ID] ...] [--summary|--history|--net] [--workspace DIR] [--max-tokens N] [-- PATH ...]
mchanges revert|apply ID[..ID] ... [--workspace DIR] [--max-tokens N] [-- PATH ...]
```

A bare `mchanges` or `--mine` selects every allocated ID owned by the calling
thread, including explicit markers for retired evidence. Forked threads inherit
their visible stream under the fork's identity; resume uses the durable identity.
`--list` without operands selects the calling thread; explicit IDs and ranges may
select captured changes across agents. It shows IDs and counts, without execution outcomes or capture diagnostics.
It compresses consecutive comparable complete IDs and
shows known direct `+N -N` capture counts, not a composed net total. Partial or unswept IDs remain separate;
`?` marks unknown authored counts. Observation-only and tool-managed IDs
are omitted from the authored list. Pending and retired IDs remain visible;
sibling threads' IDs are not exposed by implicit selection. Explicit IDs can still be read across
agents in the shared namespace. The default view shows each ID and unified
file diff. `--summary` prefixes each path's added and removed line counts with the same
file status as the diff pane (`A`, `M`, `D`, `R`, `RM`, `UU`, or `?` for incomplete evidence), aggregating across
selected records in durable capture order, regardless of argument or author order.
Counts come from the composed file differences, not the sum of repeated attempts.
Only authored evidence participates in this composition; historical managed
records remain diagnostic history rather than authored edits. Inconsistent chains have unknown counts and a retained reason.
A created-then-deleted file has no summary row, matching the empty saved diff.
Paths inside the selected workspace are shortened. Pending selections get per-ID status rows. Unavailable selections, including
retired or never-allocated IDs, report target-qualified stderr and nonzero status
without suppressing available results in any read mode. Explicit ranges extending
past the latest ID still return available IDs. When no selected ID is readable and
IDs are missing, stdout instead reports `available IDs:` ranges retained in those
IDs' streams. These are recovery suggestions, not substituted diffs or a claim of
complete evidence; retired gaps are not bridged and unrelated streams are not
listed. This recovery is shared by all read modes, including `--list` and `--net`.
`--net` composes available evidence
and reports skipped or uncomposable targets rather than claiming complete coverage.
Successful output stays on stdout. `apply` and `revert` retain dependency checks
and do not skip failed dependencies. It is not a net workspace diff;
binary or incomplete files have unknown counts. Incomplete path rows append quoted retained
reasons after the path column. Missing evidence cannot establish a modification, even when
the record contains both path names. A sequence containing an incomplete file
capture stays `?`. Historical observation-only directory records are diagnostic history, not authored
changes. Native edit receipts show incomplete named targets separately from
confirmed edits and link to `mchanges --summary` for their retained reasons.
Changing admission rules does not recover missing historical baselines.
Numeric range ends such as
`amber1..3` are equivalent to `amber1..amber3`. A path operand belongs after `--`;
unknown options are diagnosed as options. Missing-ID errors distinguish retired
from never allocated and give the latest allocation for a never-allocated ID in a
known stream. If the stream is unknown, the error identifies the selected
workspace so callers can correct `--workspace`. Consecutive missing IDs in one
stream with the same failure reason share one range-qualified diagnostic.
Ranges do not bridge available IDs, unselected IDs, or different failure reasons.

`--net` composes selected completed captures in recorded capture order using the
same review composition as the live view. It follows moves; diff output and edit
dialogs show workspace-relative paths inside the owning workspace and preserve
absolute paths outside it. Retained evidence keeps canonical paths. It never reads
the live workspace. Pending or retired history and
inconsistent, incomplete, partial-coverage, or binary capture chains
fail rather than claim a complete net diff; ordinary reads preserve that evidence.
Complete captured diffs remain composable regardless of command exit status,
including older retained records. Normal views
describe captured file differences, not command outcomes; execution and capture
diagnostics are available in `--history` only.
Path filters apply to the composed files. An empty composition is explicit and
distinct from no selected captures or a path filter matching no captured files.
An empty own-thread selection names the workspace and suggests explicit IDs for
other agents; it does not imply that their captures are missing.
Mutations still require explicit IDs; `--mine` never selects writes. `--history` includes the original observed patch or command input, the
host result, and for a command the observed scope. Paths after `--` filter review files without re-reading the
current filesystem. `--workspace ..` selects the owning workspace index when
the command runs from a subdirectory; paths after `--` only filter entries in
the selected record. `--max-tokens` bounds displayed output. When output is
omitted, an `mread` reference retrieves the retained remainder on complete-row
boundaries. The receipt freezes the selected attempts, allocation state and view
under the read lock, and retains those exact attempt dependencies. Appended
attempts or a pending change completing cannot change an existing continuation;
its original pending/retired markers remain visible. Older receipts without a
frozen selection retain their digest-check behavior. Pending, expired, or incomplete
history is explicit; no missing evidence becomes an empty successful diff.
In `--history`, a record with file changes is `applied`; a no-effect attempt
is `no changes`. The original command's results and errors are separate debug
information. Only unfinished work is `pending`.

### Revert and apply

`mchanges revert` undoes the selected completed records in the workspace, latest
capture first; `mchanges apply` replays them in capture order. Paths after `--`
select files within the records. Records hold only hunk context, not source
files, so each hunk is located near its recorded position: first exactly, then
by its leading and trailing context, dropping outer or inner context lines while
at least one line anchors each side that is not a file boundary. A located
region that has drifted is merged three ways against the requested side.
Changes on one side apply cleanly; overlapping or adjacent changes on both sides
leave git-style `<<<<<<< workspace`, `=======`, and `>>>>>>> mchanges revert ID`
markers. A hunk whose context cannot be found is printed and left unapplied, not
guessed. A hunk already in the requested state is counted as already reverted
or applied. Undoing a creation whose file gained other content keeps the file
as a conflict, like git's modify/delete. Binary, incomplete, symlink (a link on
either recorded side), and unreadable entries are skipped with their reason. A file with a conflict takes
no further hunks from later records in the same command. All merges complete in
memory before the workspace is written; creations and updates are written
before removals, and a move's source is removed only after its destination is
written. A write that replaces a directory, or that needs a file removed where
its parent directory belongs, waits until the removals are done. A directory is
replaced only once it is empty, so a file swapped for a directory, or the
reverse, is restored without deleting unrelated files. Recreated files are written with mode 0644, since records do not hold
modes.

Each touched file reports its state relative to recorded mchanges history, not
version control. The file's retained captures, in store-wide capture order, are
composed with the command's own effect, following moves, deletion, and
re-creation. `clean` means the composed net change is empty. Otherwise the line
shows ` M`, ` A`, ` D`, or ` R` and the net `+N -N` rows. `UU` marks conflicts,
and `??` marks a file whose stat is unknown: its content before the command
disagrees with the composed history, such as after an unobserved edit; its
history cannot be read; or its write failed. History loading is limited to
records connected to the selected paths through moves, and a failure there
degrades the stat rather than blocking the mutation. The command exits 1 when
any file conflicts, is skipped, or leaves a hunk unapplied. The summary or undo
line comes first so that paging cannot hide it. A continuation retains the
report as plain output and never repeats the mutation; when the report cannot
be paged or retained, it is printed in full with the reason on stderr.

The command-scope classifier resolves `mchanges revert` and `apply` as declared
writers through a retained-change resolver. Their scope is every path named by
the selected records, read before the call is forwarded. The host runs the command once; its observed
effects become a new change record like any declared command. A revert is
therefore revertable, and a clean command prints `undo: mchanges apply ID ...`
or `undo: mchanges revert ID ...`, with the same paths after `--`. When some hunk was already in the requested
state, skipped, or conflicted, the inverse command is not an exact undo, so the
report instead points to reverting the command's own change. Without a readable
change index, or when the subcommand or a mutation operand is dynamic, the
command has no source-resolved recording scope. A read with dynamic operands
stays neutral.

### Saved review presentation

Saved review consumes immutable captured evidence, including visible children's
changes, without reading current workspace content. Capture order determines
composition and change navigation; receipt arrival and caller identity do not
reorder effects. Files retain identity through moves, deletion and recreation.
All authored captures participate regardless of command exit status.

The presentation owner also supplies bounded source-text edit receipts for live
observations and retained review. Receipts preserve captured provenance, group
confirmed directory removals, and distinguish evidence gaps from confirmed edits.
Full per-path evidence remains available through the retained change identity.

Each attached session publishes one immutable retained-review snapshot for saved
Diff, exact-operation details and edit totals. Detail requests use that snapshot
without reopening storage. [Terminal-session loading](../../apps/mekugi/internal/terminalsession/SPEC.md#boundary)
owns admission, replacement and responsiveness.
The publication includes workspace, coverage, loading failure and revision alongside
its immutable evidence. The descriptor's terminal contribution consumes that publication in
memory; disabling the view leaves detail and edit-total consumers available.

The model preserves selected file and change identities across updates. Initial
history is a baseline; later captures carry recent-update marks. A caller filter
composes other callers' edits as baseline, preserving the selected caller's exact
effect and its provenance. Unknown callers remain separately selectable. Changes
can be queried by ID, caller display name or source, and files by their displayed
workspace-relative paths. Filtering does not reorder retained capture history.

Inconsistent chains show their separate captured edits with change IDs instead
of hiding evidence or inventing a combined result. Unknown, binary and incomplete
history produce unavailable net counts. Project totals describe the unfiltered
composed outcome; caller totals describe known captured edit activity. Captures
outside their owning workspace contribute to neither total. No selected
project evidence remains distinct from a known zero net result.

The [Activity-detail contribution](../activity/SPEC.md#edit-evidence) resolves exact host thread/item identity,
including recorded grouped-call membership. It returns the original captured
hunks, never later composed edits or a nearby matching path. Move targets select
their destination; directory-deletion targets select a captured file within that
directory.

The terminal view owns Files/Changes navigation, focus, filters, viewport anchors
and painting. It supplies source-selection snapshots with the same file identity,
attribution and pinned headings as the displayed content. The application supplies
retained evidence and performs selected editor actions; painting never reads the
workspace or retained store. [Terminal-session](../../apps/mekugi/internal/terminalsession/SPEC.md)
input, composition and selection consume these values over the shared
[diff renderer](../../libraries/diffrender/SPEC.md).

### Terminal review

New changes badge Diff rather than opening it. Saved review never follows streams;
updates preserve the selected file and scroll position. Tab switches Files/Changes,
`t` toggles tree/flat navigation, `/` filters, `n`/`p` visits matching files, and `[`/`]`
jumps hunks. `s` shows/focuses or hides the navigator without covering the diff.
Narrow layouts stack navigation above content. List and content focus are separate:
arrows act on the focused region, list clicks retain list focus, and content clicks
focus content. Enter opens the selected file at its heading; Escape returns to
the list. Selection is shaded and hover underlines text, excluding graph/tree lanes.

Files use stable path order, folders first in tree mode. Tree/flat choice and
collapsed folders survive filters; matching descendants remain selectable.
Clearing a filter restores prior navigation, and next/previous reveals destinations
inside collapsed folders. Changes uses one graph lane per caller branching from
Main at its first change. Rows retain ID, source, file count and known line counts;
unavailable counts use `?`. Single-file changes show their file beside a wide diff
or inline when stacked. Caller selection retains its prior filter for Escape.

Cursor movement previews without leaving list focus. File/hunk jumps remember the
file-local position they leave; ordinary scrolling does not. File headings pin
while scrolled. Updates retain file/cursor/top-row identities; resizing preserves
the logical source anchor and keeps focused entries visible. Help consumes no
permanent rows. Shared diff labels preserve status and rename sources.

In the Files list, `e` opens the selected file's current on-disk contents with
`$EDITOR`, then `$VISUAL`, then `vi`, supporting editor arguments. Folder rows do
nothing; filtering keeps `e` literal. The application releases the terminal and
returns to the same selection after exit; missing files/editor failures report
notices. This explicit current-file read does not replace captured evidence.

Clicking an Activity edit opens its exact-operation details in the shared dialog.
Closing preserves pane filters, scroll and focus; arriving changes remain available.
Only the pointed edit text underlines, not its gutter, gaps or stat bar. Dialog copy/selection uses
the shared frozen-source lifecycle.

## Lifecycle

The invocation service owns checkpoints and releases their background work at
shutdown. Capture workers remain independently bounded so slow reads cannot hold
host forwarding. Commands and offline readers use retained operations without
starting capture resources or creating another capture owner.
Saved-review publications and terminal viewports are attachment-local and restore
no transient state on replay.
Composition supplies session history;
callers select the retained namespace, thread, and workspace explicitly. No
operation restores a process, approval, or execution handle. The authenticated
frontend holds its session lease through recovery publication and stream delivery.

## Codex connection

The required executable frontend is `mchanges`, run by stock execution. Read and
mutation results use the shared frontend delivery boundary. The module returns
captured output or a pure plan; the host publishes recovery before exposing its
reference. Application adapters pass decoded stock-call and Native Shell Mode
completion facts to the passive recorder. Submission acknowledgement and yielded
results keep observation windows open; only matching terminal completion permits
retained reconciliation.

## Records

Bounded capture observations and outcomes retain their existing call-envelope
encoding. Provisional file stamps are excluded from durable comparison and storage.
Private checkpoints never substitute for retained inline evidence.

Change indexes retain identities, ordered attempt membership, allocation state,
and retirement markers. Historical record names, namespace ownership, and
selection encodings remain readable. Immutable call envelopes remain the source
of captured evidence, decoded through the shared historical codec.

Repair adopts only verified durable attempts. Evidence, index and fork publication
follow the shared [publication contract](../../docs/contracts/record-publication.md#cross-owner-publication).

Retention removes membership only for confirmed deletion facts. Allocation
high-water marks survive payload expiry. Frozen selections retain their exact
attempt dependencies; older selections keep digest-checked recovery. Neither
expired records nor a presentation snapshot authorizes a new effect.

## Failure

Missing, corrupt, incomplete, and retired evidence remain distinct. Independent
read targets retain successful output and qualified errors. Mutations require
all selected dependencies before changing the workspace. Recovery failure keeps
admitted read output; completed mutations keep their full report and original
status. Cancellation suppresses delivery without repeating completed effects.

## Acceptance

- Capture acceptance covers bounded and failed reads, overlapping and partial
  effects, yielded completion and unchanged host execution at the real consumers.
- Retained command and fault checks cover restart, cross-agent selection, frozen
  paging, mutation conflicts/dependencies, exact-once effects, usable references,
  retryable publication and dependency-safe retention.
- Saved-review acceptance covers exact-operation details, absence and host fallback,
  immutable publication, caller/project counts and scope retirement. Actual renderer
  and application checks preserve selection, move anchors, filtering, source copy
  and replay without click-time storage reads.
