# Change evidence

## REQ-CHANGES-001

This contract connects host observation, application capture, retained
[change history](../../modules/changehistory/SPEC.md), and
[live preview](../../modules/livepreview/SPEC.md). Captured evidence describes
observed file differences and its coverage, rather than the live Git diff or
proof that unobserved effects did not occur.

## Authority and publication

Codex owns editing, execution, permissions and continuations. Observation never
repeats an effect or substitutes tool arguments or host results. A yielded call
remains unfinished until its terminal continuation. Submission acknowledgement,
segment completion, file effects and terminal host completion are separate facts.
A later command or test failure does not undo bytes already written or exclude
those differences from review, composition or rename tracking.

Evidence is retained before change membership, references or completed receipts
become usable. Storage failure cannot advertise durable evidence. Completed
results first visible in a compaction request are reconciled before forwarding
can discard them, without injecting replay carriers or edit notices into that
payload. Missing workspace metadata permits only the thread's unique retained
workspace; ambiguity cannot establish scope.

New records retain bounded inline file evidence. Private workspace checkpoints
are comparison state, not record evidence. Historical snapshot-backed records
remain readable and retain their dependencies until their last owner expires;
missing or corrupt evidence is unavailable, never an empty diff.

## Capture scope

Complete stock patches and literal command operands establish pre-edit targets
without executing source. Supported commands include text redirection and
`cat`/`printf`/`echo`/`tee`, file operations including `mv` and `rm`, in-place
`sed`/`perl`, individually named formatter targets, source-derived Python and
JavaScript writes, local VCS operands, and `mchanges apply`/`revert`. Recognized
RTK wrappers and inline shells preserve the underlying source boundary.

Commands use their explicit shell or the session shell. Relative targets require
a known absolute working directory. Earlier statements that create, remove or
fill a destination remain part of its scope. A `cd` whose failure would not stop
later statements cannot establish their relative targets.

Named targets remain eligible despite ignore rules, file suffixes, directory
names or generated-file banners. An input script is not itself an edit target;
recursive operations may name trees. Tracked Bash/sh commands report paths
created by `mktemp`; capture compares only those paths and their contents, never
the temporary parent. Dry-run, help, version and failed invocations establish no
creation. Recorded creation followed by deletion cancels in the net view.

Patches within one JavaScript `exec` cell share a pre-cell/post-cell window.
Literal complete inputs establish baselines before execution; host tracing
distinguishes repeated literal calls without evaluating JavaScript or learning a
before-state afterward. Dynamic patches lacking pre-dispatch targets are outside
recorded coverage, not no-ops. Overlapping move endpoints remain separate endpoint
differences rather than invented intermediate renames. Completed patches finalize
independently of sibling commands still running.

An `exec` cell with nonliteral command input receives workspace snapshots;
that fallback does not establish pre-edit baselines for dynamic patches.

Native Shell Mode uses the same recorder, including for apply/revert. Only its
matching terminal host item completes capture; acknowledgements and replayed
history do not create captures. Recovery never repeats a mutation.

## Workspace fallback

Commands not recognized as read-only also receive a bounded selected-workspace
checkpoint before forwarding, compared with a fresh checkpoint at completion.
Previously unnamed differences join the command's evidence with its program as
their source. Private snapshot
Git state never modifies the user's repository, runs its hooks or filters, or
applies attributes that transform on-disk bytes. User configuration is read only
to locate global excludes.

- In Git workspaces, repository, `info/exclude` and global ignore rules apply.
  Tracked files remain eligible even when ignored. Ignored untracked paths need
  explicit named scope; an older checkpoint cannot turn a newly ignored path
  into a deletion. Only the selected workspace below the repository root is
  compared. VCS metadata and the replay store are excluded; submodules are not
  compared.
- Without Git ignore authority, workspaces over 20,000 files take no snapshot.
  Later checkpoints admit new files only from directories gaining at most 256
  files, whether those directories are new or existing.
- A checkpoint not ready within one second is skipped for that call. A slow
  first checkpoint can serve later calls; failures retry after 30 seconds.
  Unavailable comparisons leave named evidence and explicit partial coverage.
- Private objects over 512 MiB restart their snapshot store. Workspaces unused
  by any router process for 30 days lose private snapshot state. A call whose
  checkpoint is lost keeps only named evidence.

Calls without a witnessed before-window, including completion after restart,
take no snapshot comparison. A call still running when a later turn begins also
loses snapshot comparison because its window spans unrelated work.

## Evidence and attribution

Reads and capture holds are bounded. Unreadable targets retain incomplete
evidence. Binary content retains sizes and hashes; symlinks retain link targets.
Review reads a link's target name, while a write through it can read the pointed-to
file. Identical nonempty additions/deletions may form moves; copies may retain
their source. Text captures include up to ten unchanged rows on each side, shared
by nearby changes and limited by file boundaries. Older records use only retained
rows, never current workspace contents. Net composition and mutation grouping
remain compact so wider review context cannot join independent replayable edits.

Before/after evidence cannot separate concurrent user, hook or process writes
without host per-write facts. Overlapping calls attribute snapshot differences
to the first finisher; remaining calls compare only later differences, so an
effect is recorded once. Siblings can share a record with overlap diagnostics.
Writes restored before return have no recoverable intermediate diff; repeated-write
uncertainty remains diagnostic. Observation neither instruments nor reruns the
process to reconstruct those bytes.

Changed or incomplete named evidence receives a durable change ID. Complete
no-effect attempts and scope-only diagnostics remain in call history without an
ID; unknown scope alone allocates none.
Missing outcomes invent no exit status. Historical observation-only and
tool-managed records remain explicitly inspectable with `--history`, but do not
enter authored counts, diffs, receipts, saved review or child handoffs.

VCS operations that import, discard or write repository content are diagnostic
history, not agent-authored changes, including redirected or piped repository
output. In mixed VCS-writer/interpreter windows, named interpreter targets stay
authored; inseparable unnamed effects stay diagnostic. Read-only VCS siblings do
not exclude unnamed interpreter changes. Admission follows retained provenance,
never file classification; new policy does not rewrite historical provenance.

[Session history](../../modules/sessionhistory/SPEC.md) owns durable namespaces,
allocation and retention. Root/child ownership, isolated forks and resume use
retained identity, not a live parent. Replay does not execute edits, revive
processes or reuse another workspace's allocation.

## Delivery and presentation

Completed changes can publish a bounded user-visible Create/Edit summary from
the same retained files and hunks as `mchanges`, including effects of failed
commands and both endpoints of moves. After persistence, a separate text part
adds the change ID and summary statistics to the agent-visible result; original
host content remains intact. Partial edits expose only retained evidence.
Unchanged or unfinished calls produce neither summary nor change notice;
continuations receive theirs when the edit finishes.

[Duplicate-output projection](../../modules/outputdeduplication/SPEC.md) can
reference eligible repeated model-input text only after observation and retention
consume its original content; it preserves the change notice. Provisional values
never establish durable evidence. Omitting live preview leaves saved review and
captured Activity receipts available. The module contracts own preview and saved
review behavior; the [terminal UI contract](../../doc/spec/native_ui.md) owns
layout, navigation, source copying and input.

## Acceptance

Consuming checks preserve exact nested tool arguments/results, one host effect,
streaming before completion, and finalization only on terminal continuation.
They distinguish successful, failed, no-effect, partial and unreadable captures;
cover overlap, moves, copies, binaries, symlinks, named ignored targets and
snapshot limits; and prove publication-before-reference. Fresh-process resume
and replay use durable identity without rereading current files, allocating
duplicate IDs or repeating effects. Command reads, recovery and mutation have
their acceptance at the change-history and authenticated frontend owners.
