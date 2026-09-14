# Tracked hpatch changes

## REQ-CHANGES-001 — Shared change records and review reads

Routed hpatch results prepend one compact `change hp_a1` line. A new complete hpatch
evaluation reserves a workspace-scoped ID before evaluation. Each originating agent
thread has an alphabetic stream and increasing decimal sequence, such as `hp_a1`,
`hp_a2`, and `hp_b1`. Recovery attempts reuse the original correlation's ID, including
invalid recovery amendments. An invalid recovery after a completed call stays under that call's ID. A recovery
with no visible hpatch ancestry or no tracked chain in the current workspace allocates no new ID. Transport failures before evaluation or delivery need not expose a result ID.

IDs and stream allocation persist across router restarts. The original hpatch, all
recovery inputs and diagnostics, and any successful evaluated review diff are available
through the one ID. Published attempts are never removed or overwritten; replay does not duplicate attempts.
A fork continuing an inherited recovery retains its original ID. Separate new calls
in the fork use its own thread stream. Concurrent branches under one ID retain every
outcome rather than overwriting an earlier successful diff.

Mixed HPATCH/shell results expose the same identity as a structured `change_id`;
edit reports and diagnostics retain the compact `change` line. The original plan,
valid-handle continuation inputs, and every runtime edit evaluation share that ID.
Plans are labeled `execution plan (see segment attempts)`, not applied changes.
Each edit evaluation publishes immutable evidence before returning its patch, and
the carrier confirms only after the host reports successful application. An interrupted
or failed application remains unconfirmed; explicit `accept` does not invent a receipt.
Repairs and retries retain each evaluation separately, without including shell effects.
Durable review evidence survives expiration of the temporary continuation handle.

The shell-private command is:

```text
hchanges ID[..ID] ... [--summary|--history] [--workspace DIR] [--max-tokens N] [--cursor HASH:BYTE] [-- PATH ...]
```

Explicit change IDs or ranges are required. Flags may precede, follow, or be
interleaved with IDs before `--`; duplicate flags reject. Every argument after `--`
is a literal path, including names that look like flags or change IDs. Omitting
`--`, or supplying it with no paths, selects all files in the requested changes.
There is no `read` subcommand or `--path` option.

It has no standalone model-visible tool schema or installed executable. It runs inside
the Bash/POSIX shell worker, including when it is the sole shell command. It performs
no evaluation or workspace mutation. The authenticated worker manifest pins the router's
durable store directory, rather than accepting a child environment override.

Default workspace identity is the canonical current shell directory. `--workspace DIR`
selects another canonical directory, relative to shell cwd when needed. An explicit empty
directory selects the router's no-directory state. IDs resolve only within the selected
workspace, never through conversation history or a Git diff. Isolated executor deployments
must expose the router's replay directory at the same absolute path.

References accept individual IDs or inclusive ranges such as `hp_a1..hp_a4`. Range endpoints
must be ordered within one stream, so concurrent edits from another agent are excluded.
Duplicate IDs are emitted once in first-requested order. One read accepts at most 256
distinct IDs; an individual range cannot exceed that bound. Missing, invalid, corrupt,
or cleaned-up records produce explicit failures, not a current-workspace reconstruction.
A reserved ID without a published attempt is reported as pending.

Default output contains a compact attempt/outcome summary and each successful evaluation's
review diff, once, with three context lines per hunk. A single attempt uses one ID/status
line; multiple attempts retain their numbered outcomes. Unified diff headers replace the
redundant operation header; empty-file changes and pure moves retain an operation header.
Rejected attempts do not carry proposed changes as applied diffs.
`--summary` omits diff bodies and lists operations, paths, and added/removed line counts
per evaluated file. An unchanged path appears once per entry. Repeated evaluations remain
separate, not a synthetic net diff or a current workspace status.
`--history` additionally returns original inputs, recovery amendments, rebuilt scripts
when different, and full diagnostics. Each path after `--` matches either recorded before or after
path, accepting equivalent lexical absolute and workspace-relative spellings for workspace
files. It does not consult current filesystem contents or resolve file symlinks. Retained
shell-script paths match exactly. Filtering affects diff/file entries, not attempt history.
Multiple paths select the union of matching files in recorded order.
A file matching multiple filters is emitted once per evaluation, not once per filter.
A selection with no matching files explicitly lists the requested filters.

Review diffs are captured by the engine from immutable original content and final
formatted content before external effects. They include additions, deleted contents,
moves, empty files, CR bytes, and explicit missing-final-newline markers. Later workspace
edits cannot alter these diffs. They describe evaluated changes, not a second inspection
of the executor's resulting bytes: the host line-ending behavior in `REQ-OUTPUT-001`
still applies. Shell commands, external formatters, and unrelated workspace changes are
not tracked. Edits to retained `@shell/` scripts are labeled separately from workspace
files; their recorded paths are relative to that private script store. Reads do not
combine several invocations into a synthetic net diff.

An attempt is `rejected`, `no-op`, `prepared (application unconfirmed)`, or `applied`.
A translated patch alone is never proof of application. Exact successful-report replay
confirms application only after the entire incoming history validates; direct private
application can confirm immediately. A report may be bare or in the matching host
carrier's completed output envelope: native execution requires exit code zero, and
Code Mode requires `Script completed`. The body must equal the entire retained report.
Code Mode may supply a separate metadata-only completed header followed by one report
block. Failed, running, truncated, or extra output does not confirm application. Persisted receipts support cross-agent review but
never supply recovery or alias ancestry. Unknown or failed executor outcomes remain
unconfirmed. Final cancellation and crash limitations remain those of `REQ-OUTPUT-001`.

If replay survived an interrupted attempt publication, confirmation repairs its missing
index membership from the matching durable record before recording the receipt. The
repaired call is inserted by recovery-attempt order without reordering existing calls.
Missing or mismatched durable evidence fails reconciliation without publishing a partial
repair. Idempotent publication and confirmation retries synchronize the store directory
before reporting success.

Each read snapshots index membership and receipts under a shared, read-only lock. It
releases that lock before reading immutable replay facts and rendering the projection,
so large reads do not hold up writers during rendering. Later pages still verify their
cursor against a newly selected snapshot; no hidden page cache changes the read contract.

Reads default to 4,000 GPT-5 stdout tokens; `--max-tokens` accepts 1 through 15,500.
The existing bundled tokenizer selects a UTF-8-safe prefix. An incomplete result returns
nonzero and a stderr continuation cursor; repeat the same read with that cursor to obtain
the next bytes without repeating the previous output. Cursor identity covers the complete
selected projection, so a recovery or confirmation that changes that projection requires
restarting the read. A page may end within a hunk or line, explicitly marked incomplete.
The fixed continuation diagnostic is outside the stdout budget. A budget too small for
one character fails without a nonadvancing cursor.

Index files and replay records use private permissions, cross-process locking, synced
atomic replacement, bounded capacity, and no automatic eviction. Each index is at most
32 MiB; the change-index collection uses the replay store's configured byte limit.
A rendered read is bounded to 64 MiB before token selection; exceeding it requires a
narrower range, view, or path selection. These failures never claim a complete review.

Acceptance:

1. Interleaved agents receive separate sequential streams; inclusive ranges contain only
   their named stream and eliminate duplicate references.
2. Initial rejection, invalid amendment, re-rejection, and success are retrievable under
   one ID after restart and from another agent. Replay adds no duplicate attempt.
3. Deletions, moves, empty files, formatter effects, and newline differences survive capture.
   Later edits to the workspace do not change retrieved content.
4. Prepared results stay unconfirmed until a valid complete input confirms execution.
   Failed reconciliation publishes no partial receipt.
5. Summary, history, and path selection keep their specified scope; default reads omit
   repeated scripts and full diagnostics.
6. Token-bounded pages concatenate to the selected projection without byte loss or repetition.
   Stale cursors, malformed ranges, unavailable records, and quota failures are explicit.
7. Both shell interpreter modes execute the private reader; sole-command optimization never
   dispatches it through PATH. Model-visible tool schemas remain unchanged.

## Live terminal view

`mekugi live-diff [--workspace DIR] [--replay-dir DIR]` reads the durable change index
and immutable review files without starting a router, writing the store, evaluating
edits, or reconstructing changes from Git. Atomic index replacement provides a consistent
membership/receipt snapshot. It refreshes once a second and keeps application status
separate from translation success. Missing or inconsistent evidence fails explicitly.

Interactive `mekugi codex` arms one automatic Herdr launch per router process when
stdin/stdout are terminals and both Herdr and delta are available. The first
successfully prepared ordinary non-subagent turn supplies the canonical workspace;
wrapper cwd and argument parsing do not select it. Launch is asynchronous, bounded
to five seconds, canceled with the router, and emits no terminal output. Failure
does not affect Codex execution and is not retried automatically. A fresh router
can open a new pane; unrelated existing viewers are neither replaced nor closed.
The automatic pane is session-scoped: only durable thread streams observed on
successfully prepared turns in this private router are included. This includes
subagents and later thread/workspace switches, not unrelated sessions sharing
the workspace. A new thread starts empty, while a resumed thread retains its own
captured history. Absolute edit operands outside the selected workspace are included.

The router publishes a private, atomic scope file containing workspace/thread
membership and passes it to the viewer with `--session-file`. It is auxiliary
display state, bounded to 1 MiB. Exhaustion disables the view rather than publishing
a partial scope or retaining more memberships. It is not replay authority: durable indexes and records still supply
the content and application receipts. Removing the file ends the viewer. On
Codex exit or router cancellation, the router removes its scope and closes only
the pane it created, with bounded cleanup. A manually launched viewer without a
session file remains workspace-wide and independent of Codex's lifetime.

The view groups workspace review projections by file identity, following captured moves
and excluding retained shell scripts. The engine's review composition consumes applied
captures in observed order, retains sparse original/current source regions, and renders
original-to-latest hunks through the same review renderer. It never reads current files
or invents missing context. Prepared attempts remain separately labeled and cannot alter
the applied baseline. A late receipt alone does not revive a flushed attempt.

`f` flushes the current file and `F` flushes all files: the viewer acknowledges existing
capture IDs but never deletes or rewrites durable records. A subsequent edit overlapping
a reviewed changed region revives its original-to-latest hunk; a full revert removes it.
Unrelated reviewed regions stay hidden, including when intervening insertions/deletions
shift their line numbers. Composition preserves exact line endings and newline markers.
Acknowledgements are local to this viewer process.

Each composed file is bounded to 1,048,576 captured source rows. Unknown source, identity
gaps, inconsistent context, or capacity failure stops composition for that chain. The
viewer then labels and displays the original attempts rather than claiming a verified
net diff; a new attempt conservatively revives the file's captured history. Captures
spanning workspace/thread streams always remain uncomposed because their records do not
establish durable execution order. Refresh order is not execution evidence.

The viewer renders all captured files in one continuous viewport, not only the selected
file. Short multi-file edits remain visible together. It follows new edits by default,
scrolling to the latest newly observed file's changed hunk, even near the top of the file.
Manual scrolling or file navigation pauses following and preserves each file's vertical
offset, clamped when content becomes shorter. Scrolling crosses file boundaries; `n`/`p` jumps between
files, and `g`/`G` goes to the start/end of the complete view. The header identifies the
file at the top of the viewport, which is the current file for `f`. `r` resumes
following, including edits received while paused. The footer shows FOLLOW or PAUSED.
Navigation uses the keyboard controls shown there. Mekugi's file headings, including the
current-file header, use bold text, green `+N` and red `-N` source-line counts, and a
width-filling separator. File-heading blocks wrap when needed so their actions and
rename endpoints are not truncated; the sticky current-file title stays on one row.
Counts describe the displayed projections: combined visible regions for composed files,
or the separately labeled captures otherwise. They exclude
headers/context and become zero when the file is fully flushed. Counts are cached with
the rendered view rather than rescanning diffs on every navigation key. Delta's duplicate
file and hunk headings are suppressed through invocation-local options. Line numbers appear
beside source rows, not in separate headings. The viewer owns their inline formats,
even when delta is configured with empty number formats. Omitted-heading padding is
removed without trimming blank source rows or their styling. Normal combined applied
diffs have no status banner. New/deleted files and both names of renamed files appear once in the file heading.
Separate captures retain their own action and application status once per capture, not
per hunk; uncomposed-capture warnings remain explicit. Path-only changes use the same
heading or capture caption without a duplicate operation row.
Terminal resize clips colored Unicode text to the available columns. Only text and SGR styling
reach the live viewport. Delta's invocation-local added/removed-line styles use the
terminal's normal background, with syntax-colored text and bold emphasis. Displayed
header paths are relative only for files inside the selected workspace; paths outside
it remain absolute. Source hunk text and durable paths are unchanged.

The latest refresh containing new capture IDs marks every affected file and touched
composed hunk with a cyan gutter, without recoloring source text or adding backgrounds.
A bold `LATEST UPDATE` label appears once in the file heading, not between hunks.
The marker denotes a recently touched hunk, not line- or word-level attribution.
Multiple captures observed together form one display
update; this does not establish execution order across streams. Initial history is a
baseline, not a fresh update. Receipt-only refreshes preserve the current marks rather
than creating a new update. Marks remain until another capture update or a flush.
Prepared and uncomposed captures retain their explicit application/ordering labels.
A full revert can mark the file's empty state but never invents a surviving changed hunk.

Manual navigation preserves the viewport when new captures arrive and adds a
`new changes available` footer notice. Resuming follow or flushing all marked files
clears that notice. Recency and acknowledgement state are viewer-local; restarting
does not replay old captures as new updates. Delta runs once per render at the content
width after reserving the gutter and final terminal column. Decoration remains within
the rendered-output bound.

The viewer always invokes `delta` from `PATH`, with normal delta/Git styling
configuration, nested paging disabled, and viewport width supplied by Mekugi.
It does not select a renderer through `GIT_PAGER`, `PAGER`, or Git pager settings.
It requires terminal input/output. When delta is unavailable, `--herdr` reports
a skipped view and returns successfully without creating a pane; direct invocation
reports the missing dependency.

`--herdr` creates a sibling pane to the right of the caller in the selected workspace
without changing focus and starts the same executable there. It never switches to a
bottom pane based on geometry.
It requires a Herdr-managed caller and uses returned pane identities, not focused-pane
defaults. Closing the viewer restores terminal state but does not close the pane or
affect the agent/router. An unsuccessful launch identifies the newly created pane.

The viewer bounds source and rendered diff output independently to 64 MiB and fails on
explicit removal of previously observed change IDs; restarting selects the remaining
store. Its view state is process-local, while captured edits survive router restarts.

Acceptance:
1. A multi-file capture displays every file in the continuous view, with short diffs
   visible together. Following scrolls to new edits; manual navigation pauses it, so
   other-file edits and application receipts do not steal selection or reset scrolling. `r` resumes following
   the latest observed edit, including after updates while paused.
2. Delta preserves configured source styling regardless of Git pager selection, with
   invocation-local overrides for normal source backgrounds, inline line numbers, and
   suppressed file/hunk headings. Multiple regions in a newly created file show one
   file-action label, no repeated hunk captions, and no extra heading padding. Blank
   source rows, separate-capture identity, and rename endpoints remain visible.
   Missing delta skips Herdr pane creation without affecting the agent.
3. A real terminal process renders updates, handles resize, accepts navigation and quit,
   and exits without retaining terminal ownership.
4. A fresh read-only store view sees durable edits and confirmation receipts; missing
   records do not appear as an empty successful view.
5. Flush hides reviewed hunks without deleting captures; an overlapping fix shows
   original-to-latest content, a full revert disappears, and unrelated reviewed
   regions remain hidden through line shifts and repeated-line alignment.
6. Late confirmation of a flushed prepared attempt does not revive it by itself.
   Discontinuous source chains are explicitly uncomposed rather than guessed.
7. Latest-update markers cover all newly observed files and touched net hunks, including
   overlapping edits, deletions, moves, line shifts, and repeated-line reverts. Older
   unrelated hunks keep their normal styling. Receipt-only updates and initial history
   do not create new recency, and flushing removes the corresponding markers.
8. Unified and side-by-side delta layouts retain syntax colors and normal backgrounds
   beside the gutter. Unicode clipping, resize, pause/resume, and real-terminal cleanup
   remain correct with highlighted updates.
