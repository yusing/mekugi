# Applied changes and live view

`mchanges` is the authoritative retained account of agent changes for users and
agent handoffs. It reports confirmed evidence and its limits, not the live Git
working tree or a claim that unobserved effects did not occur.

## REQ-CHANGES-001 — Durable review of stock edits and command effects

Mekugi observes stock Codex `apply_patch` calls. Codex executes each call once;
Mekugi does not replace the tool, run a hook, apply a second patch, or alter the
argument or host result. The
[duplicate-output projection](execution.md#duplicate-output-projection) may
reference repeated model-visible output only after observation and retention
have consumed the original text. A complete argument may be projected as a
provisional live diff while streaming. Only the actual host result and resulting workspace
state determine a completed change record. A yielded call remains unfinished
until its host continuation is terminal. Saved file differences are applied
changes: a later command or test failure does not undo bytes already written.
Command exit codes and native tool results remain separate diagnostic facts.
They never gate whether a saved file difference participates in review,
composition, or rename tracking. There is no receipt-driven change-state
transition, including when reading older retained records.

The observer captures bounded pre-edit UTF-8 contents for paths named by a
complete patch and compares them with the resulting files. Missing source is
retained as incomplete file evidence rather than an invented diff. Calls with
complete evidence and no file differences remain in command history without a new change ID.
An unfinished call has no completed record. Storage failure must not expose
dependent review evidence as durable.

Completed host results first visible in a compaction request are reconciled before
provider forwarding can discard that input. This
includes stock patches and exec-observed writes. Observation
does not project replay carriers or edit notices into the forwarded compaction
payload. Missing workspace metadata may use only the unique retained workspace
owned by that thread; ambiguity cannot establish capture scope.

New records retain bounded file evidence inline. Workspace snapshots (see
Command effects) are private comparison state, not record evidence: Mekugi
produces no dependency fingerprints, Git baseline reconstructions, or shared
filesystem-snapshot objects. Historical snapshot-backed records remain
readable and retain their storage dependencies until their last owner expires.
Missing or corrupt historical evidence remains unavailable, never an empty diff.

New text captures retain up to ten unchanged lines on each side of a change for
historical Edit dialogs and individual-record reads. Nearby changes share their
context; file boundaries limit it. Older records and completed host-only diffs
show only their retained rows, never context borrowed from the current workspace
or another invocation. Composed net views and apply/revert hunk grouping remain
compact, so wider review context does not join independently replayable changes.

Patches inside one `exec` cell share a pre-cell/post-cell observation window.
Literal complete inputs name their baselines before execution. Host tracing
confirms individual outcomes and repeated literal call occurrences, without
evaluating JavaScript or learning a before-state after execution. A cell whose
command input is not literal is bounded by a workspace snapshot like any other
writer command; dynamic patch inputs that cannot be resolved from source before
dispatch are outside recorded coverage, not confirmed no-ops. Agents keep their
ordinary tools and workflows.
Moves whose endpoints overlap another patch are retained as separate endpoint
differences rather than inferred intermediate renames. Completed patches finalize
independently of sibling command processes that remain running.

### Command effects

Commands submitted through native Shell Mode (`!`) use the same change recorder.
Completed file effects update the saved Diff totals, pane, and Changes history,
including `mchanges revert` and `apply`. Submission acknowledgements and replayed
command history do not create captures; the matching terminal host item completes
the observation. Codex retains execution, output, and interruption ownership.

Mekugi records known edit sources in literal stock `tools.exec_command`
calls. These include literal text/file redirections (`cat`, `printf`,
`echo`, `tee`), coreutils file operations including `mv` and `rm`, in-place `sed`
and `perl`, formatters with individually named file targets, supported
source-derived Python and JavaScript writes, local VCS
operand edits, and `mchanges apply`/`revert`. Shell and interpreter parsing is
read-only: it derives edit operands without executing the program. Recognized
RTK wrappers and literal inline shells preserve the underlying source boundary.
Commands use their explicit shell or the request's session shell, never an
assumed shell. Relative paths require a known absolute working directory.

Paths named by an edit source receive pre-edit evidence. Git ignore status,
filename suffixes, directory names, and generated-file banners are not admission
rules for them. An agent's explicit edit to an ignored `FIXME.md` is recorded, as
is an explicitly authored fixture. An input script is not itself an edit target.
Recursive file operations may name trees.

Tracked Bash and `sh` commands also report paths that `mktemp` created, as
described in [execution](execution.md). Capture compares only each reported path
and its contents; it does not scan the temporary parent directory. Creation and
later deletion use ordinary retained changes, so their net diff cancels. Dry-run,
help, version, and failed invocations name nothing on disk and establish no
creation.

Effects the parsers cannot name are bounded by workspace snapshots, so agents
need not route edits through `apply_patch` to have them recorded. Every command
that is not a recognized read-only reader takes a checkpoint of the selected
workspace before it is forwarded, and its terminal result is compared with a
fresh checkpoint. Changed paths that no edit source named join the call's record
with the command's program as their source. Snapshots live in a private Git
directory and index under the replay store. They never write the user's
repository state or run its hooks or filters. User configuration is read only
to locate the global excludes file and is never applied to snapshot commands.
Snapshots record the bytes on disk: attributes that convert line endings,
encodings, or content through filters do not apply.

- Inside a Git repository the snapshot follows the repository's ignore rules,
  including `info/exclude` and the user's global excludes file, borrows its
  objects, and starts from a copy of its index. Ignored paths such as an ignored
  `node_modules/` are therefore never recorded unless an edit source names them;
  unignored installation output is recorded like any other change. An older
  private checkpoint cannot turn a now-ignored, untracked path into a
  recorded deletion. Files tracked by the repository remain eligible even when
  an ignore pattern matches them.
  Only the selected workspace below the repository's top level is compared. Other VCS
  metadata directories and the replay store are always excluded.
- Without a Git repository there are no authoritative ignore rules. A workspace
  of more than 20000 files takes no snapshot. Later checkpoints admit new files
  only from directories that gained at most 256 of them, whether the directory
  is new or already known, so an installation tree does not become an agent
  edit.

A checkpoint that is not ready within one second is skipped for that call, which
keeps only its source-named evidence; a slow first snapshot continues in the
background for later calls, and a failed one is retried after 30 seconds.
Submodule entries are not compared. A snapshot that cannot be compared is
reported in the call's scope diagnostic, and the call's coverage is partial
rather than exact. A call whose observation window was not seen by this router
process, such as one finishing after a restart, takes no snapshot comparison.

Snapshot state is bounded separately from record retention. A workspace's
private object store that grows past 512 MiB starts over, and a workspace no
router process has used for 30 days loses its private state, which its next
snapshot rebuilds; a call whose checkpoint was lost this way keeps only its
named evidence.

Capture holds and content reads remain bounded, for named operands and snapshot
comparisons alike. An unreadable target produces incomplete evidence, not
invented bytes. Unknown scope alone never allocates a change ID, change notice,
or reviewer handoff.
Complete named comparisons with no file differences also allocate no ID.
Temporary writes restored before return have no recoverable intermediate diff;
the command diagnostic retains repeated-write uncertainty without advertising
an empty change. Recovering intermediate bytes would require host per-write
facts; Mekugi does not instrument or rerun the user's process.

After a terminal host result, the recorder compares only the named targets and
explicit recursive destinations. A nonzero exit does not undo bytes already
written. Missing native outcomes do not invent exit codes. Yielded commands wait
for their terminal continuation; replay does not revive processes. Binary content
is retained as sizes and hashes and symlinks as link targets. Identical nonempty
deletions/additions may form a move, and a created copy may name its source.

Before/after evidence is not per-write attribution. A concurrent writer, hook,
or user edit changing a file while a command runs cannot be separated from that
command without host per-write facts. When calls overlap, the first to finish
records the snapshot changes it observed, and calls still running compare only
later changes to those paths, so one effect is recorded once and records
compose. Sibling calls may share one record and retain overlap diagnostics.
A command still running when a later turn begins takes no snapshot comparison,
because its window spans unrelated work.

Each completed known edit with changed or incomplete file evidence receives a
short durable change ID. Complete no-effect attempts and scope-only diagnostics
remain in call history without an ID. Historical observation-only and tool-managed
records remain available through explicit `--history`, but are absent from
authored diffs, counts, notices, saved Diff, and child handoffs. Admission uses
retained provenance, not file classification.

VCS commands that import, discard, or write out repository content retain their
effects as diagnostic history, not agent-authored changes. These effects are
excluded from authored counts, diffs, notices, saved Diff, live diff, and child
handoffs. This includes redirected or piped repository output.
Command observation uses the shared VCS classification to distinguish possible
writers from read-only commands. In a window containing both a VCS writer and an
interpreter edit, source-named interpreter targets remain authored; unnamed
effects cannot be separated from the VCS operation and remain diagnostic.
Recognized read-only VCS siblings do not exclude unnamed interpreter changes. New captures
persist this provenance for restart; existing retained provenance is not rewritten.

Root and child agents share their inherited namespace; forks and side threads
receive isolated visible records, and resume reads retained evidence after a
fresh router process. Allocation survives restart and never reuses another
workspace's IDs. Retention belongs to [session history](../../modules/sessionhistory/SPEC.md); replay never
repeats a host edit.

### Bounded reads, revert, and apply

The [change-history module](../../modules/changehistory/SPEC.md) owns retained
change selection, bounded reads, recovery, and apply/revert behavior. Host
observation still owns capture of those commands' effects; a recovery continuation
never repeats a mutation.

### Edit receipts

Completed file changes can publish a generated `Create` or `Edit` commentary
summary, including changes left by a command that exits nonzero. Classification
uses the same captured review files as `mchanges`, including whether a path
existed before the edit. Each summary carries a bounded copy of the captured
hunks. Unchanged and unfinished calls publish no edit summary. Move summaries
show both endpoints of moves, compressing shared directory prefixes as
`internal/router/{old.go=>new.go}`. These messages are user-only presentation,
not substituted tool results. After persistence, the
agent-visible completed tool response also receives a separate text part with
the change ID and `mchanges ID --summary` statistics. This bounded notice does
not replace the original host result or alter the user-facing edit display.
The separate duplicate-output projection may reference repeated host text in
model input; it leaves the appended change notice intact.
No-effect and unfinished calls receive no notice; partial edits report only
retained evidence. Continuations receive the notice when they finish the edit.

### Live preview and saved Diff

The [live-preview module](../../modules/livepreview/SPEC.md) owns provisional
source evaluation, baseline reuse, turn retirement and bounded preview publication.
The [native UI contract](native_ui.md) owns live-dock layout, reveal, selection and
settling behavior. The application owns review scope and retained-change delivery;
saved Diff and captured Activity receipts remain available when live preview is
omitted. Preview values never establish retained change evidence.

The [change-history module](../../modules/changehistory/SPEC.md) owns saved-review
composition, attribution, caller filtering and selected-file semantics. Its
terminal view and the [UI contract](native_ui.md) own navigation, viewport anchors,
source copying and layout. Shared source geometry, syntax, word emphasis and file
labels belong to the [diff renderer](../../libraries/diffrender/SPEC.md).

Mekugi owns the native terminal composition, while Codex app-server remains the
execution authority. The [UI contract](native_ui.md)
owns pane layout and input: Main on the left, Diff or Activity on the right,
and the child Agents roster below. `Ctrl-B` followed by `1`, `2`, `3`, or `4`
focuses those panes. Mouse dragging and prefix shortcuts resize panes and the
file navigator without changing evidence.

Diff and agents scrolling use one contract: arrows or `j`/`k` move one line,
PageUp/PageDown or `b`/Space move one page, Home/End or `g`/`G` go to the
beginning/end. The saved Diff has no follow mode or resume-follow shortcut. The wheel scrolls
diff and activity by three lines per event without changing keyboard focus.
Consecutive wheel events accumulate even before the next rendered frame. Stream cards retain
their source windows during manual scrolling and resume their live tips with `r`.
`Ctrl-C` in an auxiliary pane returns focus to Main. Bracketed paste enters the
native composer and cannot activate layout shortcuts. Main history remains
accessible through prefix PageUp/PageDown and the mouse wheel. Interactive
launches inside Herdr expose the invocation-local `HERDR_AGENT=codex` hint before
router startup, without Herdr commands or pane-management APIs.


The wrapper starts no pane processes and requires no external pane manager.
The UI consumes the bounded event hub directly; there are no live-view HTTP
endpoints or connection files in the running application. Terminal mode is
restored after child exit, cancellation, or rendering failure; all owned I/O is
joined. Redirected sessions retain stock input/output and inline activity.

Acceptance:

1. Nested stock patch calls retain exact arguments and host results; only eligible
   model-visible text may use the duplicate-output projection.
2. A streaming preview appears before completion but does not create success
   evidence or a change ID before the host result.
3. Successful, failed, no-op, and partial outcomes are distinguished by
   result and workspace state. Read failures remain visibly incomplete.
4. `mchanges` lists the caller's IDs and reads completed records by ID, range, path, summary, and history
   through the authenticated frontend, with bounded `mread` continuation.
   `mchanges revert` and `apply` merge selected records into the workspace, leave
   markers for conflicts, report each file relative to recorded history, and are
   themselves recorded as revertable changes.
5. Child handoff and resume use durable ownership, not a live process; replay
   never executes an edit again.
6. Live `cat`, `tee`, patch, and interpreter projections are presentation
   only and preserve stock PTY, yield, host result, and `write_stdin` behavior.
   The separate duplicate-output projection changes only eligible model input.
7. A literal nested command produces a record with the
   actual command outcome and reviewable scoped effects. A nonzero exit is retained
   in debug history; saved edits still publish their change receipt.
8. A yielded command is finalized only by its terminal continuation result.
   Replaying the same input neither re-reads the workspace nor allocates a
   second change ID.
9. Moves, copies, binary content, symlinks, and bounded or unreadable paths
   are represented without inventing rows. A link is reviewed by its target
   name; only a write through it reads the file it points to.
10. A destination that an earlier statement creates, removes, or fills is
    captured under every reading, and a `cd` whose failure would not stop later
    statements leaves their relative operands undeclared.
