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
hchanges ID[..ID] ... [--summary|--history] [--workspace DIR] [--max-tokens N] [-- PATH ...]
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

Each initial read snapshots index membership and receipts under a shared, read-only lock,
then releases it before reading immutable replay facts and rendering the projection.
Reads default to 4,000 GPT-5 stdout tokens; `--max-tokens` accepts 1–15,500.

When output is omitted, the executor persists a small selection descriptor using
[managed read continuation](read.md#managed-read-continuation) and supplies the exact
`hread REF` next call. The descriptor holds IDs, filters, the position, and the full
projection fingerprint, not another copy of the diff. Later pages rebuild and validate
the same selection without repeating arguments. Changed projections fail explicitly;
a new `hchanges` invocation selects the current state.
The read continuation is raw diff text and may end within a hunk or line; it is not
verified source-row evidence. A budget too small for one character fails explicitly.
Persistence is limited to incomplete reads and must complete before the page is exposed.

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
   Corrupt read references, malformed ranges, unavailable records, and quota failures are explicit.
7. Both shell interpreter modes execute the private reader; sole-command optimization never
   dispatches it through PATH. Model-visible tool schemas remain unchanged.

## Live terminal view

The live pane is a read-only view of this session's captured hpatch edits. It does
not include Git changes, shell-only edits, or retained shell scripts. Updates arrive
as authenticated events from the owning router, never through filesystem watching or
polling. The viewer does not write the store, evaluate edits, or start another router.
Manual live viewing is unsupported; durable history remains available through `hchanges`.

### Session and lifetime

Interactive `mekugi codex` inside Herdr arms one automatic pane launch per router
process when stdin and stdout are terminals and `herdr` is available. The first
successfully prepared non-subagent turn selects the canonical workspace, not wrapper
cwd or parsed command arguments. Preparation and read-only turns do not open UI.
The first complete hpatch call emitted for an observed thread triggers the launch,
including a subagent's call or a rejected edit; replayed history alone does not.
The pane opens to the caller's right without changing focus. Launch is asynchronous,
silent, limited to five seconds, and canceled with the router. Failure neither blocks
Codex nor triggers an automatic retry.

Scope includes only durable thread streams observed on successfully prepared turns
in that router, including subagents and later thread/workspace switches. A new thread
starts empty; a resumed thread retains its captured history. External absolute edit
paths are included, but unrelated sessions sharing a workspace are not.

The connection is private to the owning router. Viewers never discover or switch
routers. Router exit revokes the connection and closes only its own pane, with bounded
cleanup. A fresh router may open a new pane without replacing existing viewers.
Quitting the viewer restores terminal state without closing the pane or affecting Codex.

### Update integrity

Captures and application receipts become visible only after durable publication.
One publication is one display update, including multi-call responses. Mixed scripts
publish each edit segment without waiting for the whole script; ordinary host receipts
arrive when Codex next returns results to the router. Translation alone never confirms
application. Delivery must not block edits or assume execution or continuation authority.

Initial snapshots and concurrent events must reconcile without lost or duplicate
updates. Scope additions include newly eligible durable history. Reconnects and
interrupted worker publication require a fresh durable snapshot before claiming live
coverage, while preserving navigation, acknowledgements, and recency state.
Within a snapshot, immutable attempts are loaded once. Receipt updates and clean worker
shutdown must not trigger capture-history rereads.

A missing publisher is shown as waiting; a disconnect or failed publication is
unavailable. Queue overflow invalidates coverage rather than silently dropping updates.
Publisher coverage that cannot be tracked within capacity remains unavailable for the
router session. Missing, inconsistent, or removed evidence fails explicitly, never as
an empty successful view. Restarting the viewer selects the remaining store.

### Composition and review

Applied captures form one original-to-latest result per file, following captured moves
across threads and workspaces. Composition uses durable capture order, unchanged by
restart, replay, metadata updates, or receipt arrival. Order gaps are allowed; this
display order does not control host execution. Legacy records retain stream-local
order, but mixed-stream legacy history reports unavailable shared order rather than
guessing a combined result.

Composition uses captured source only. Missing context, inconsistent source, or capacity
failure is reported instead of inventing context or substituting individual applied
patches. Prepared captures remain separately labeled until application is confirmed.

`f` acknowledges the current file and `F` all files, without deleting or rewriting
captures. A later overlapping edit revives the original-to-latest region; a full revert
removes it. Unrelated reviewed regions remain hidden through line shifts and repeated-line
alignment. A late receipt alone cannot revive a flushed attempt. Acknowledgements and
recency are viewer-local; restart treats retained history as a baseline, not a new update.

### Navigation and display

All files share one continuous viewport. Following is enabled initially and targets
the latest changed rows, including those deep inside a combined hunk. It prefers the
latest update's marked region when composed coordinates are ambiguous and centers the
target row when file and viewport boundaries allow it. Manual scrolling or file navigation
pauses following and preserves file-relative offsets, clamped when
content shrinks. `n`/`p` navigate files, `g`/`G` the complete view, and `r` resumes following,
including changes received while paused. The file at the top of the viewport is current
for `f`.

The footer identifies FOLLOW or PAUSED. Updates received while paused preserve the
viewport and show `new changes available`; resuming or flushing all marked files clears
the notice. New captures mark every affected file and touched net hunk with a cyan
gutter. Receipt-only updates preserve those marks; another capture update or flush
replaces them. Fully reverted or flushed files disappear from the viewport and file navigation;
retained captures remain available for later composition. When all files are hidden,
the viewer shows a single empty-state message without a file header.
Markers indicate recent hunks, not line- or word-level attribution.

File headings and the sticky current-file header share an aligned gutter, bold titles,
green `+N` and red `-N` source-line counts, and a width-filling separator. Counts describe
the visible combined result plus prepared captures, exclude context and headers, and
become zero when flushed. Navigation must not rescan diffs merely to update counts.
File actions appear once per file or prepared capture, with both rename endpoints.
Deleted files retain their action heading and removal count, but omit source diff rows.
Heading blocks wrap without losing actions or paths; the sticky title stays on one row.

Source uses one line-number column: old coordinates for deletions and new coordinates
for additions and context. Explicit change markers remain, without repeated hunk headings
or applied-status banners. The column uses only the required digits and disappears in
very narrow panes; blank source rows keep their numbers.
Prepared status appears once per capture. Paths within the display workspace are relative;
external paths stay absolute. Display formatting never changes captured paths or source.

Source lines wrap by default, preserving whitespace, syntax colors, and change-row
backgrounds. Continuations retain the gutter and change marker with a blank coordinate.
`h`/`l` and Left/Right pan source horizontally, pausing following and unlocking wrapping
while the horizontal offset is nonzero. Returning to the left edge (`x=0`) relocks
wrapping without resuming following. Coordinates and chrome remain fixed. Switching
wrap mode preserves the logical row at saved viewport positions.
The viewer enables SGR mouse reporting while active and explicitly disables its
mouse reporting modes on exit, including in terminals without private mode save/restore. Horizontal wheel reports use the same pan/unlock/relock behavior; vertical
wheel reports scroll rows. Wheel modifiers are accepted. Clicks, releases, motion,
and malformed reports do not trigger navigation or review commands. Fragmented
reports are decoded incrementally with bounded storage.

Resize must preserve safe Unicode clipping, leaving room for the gutter and final
terminal column. Only text and terminal color/text styling (SGR) may reach the viewport.
Added and removed source rows have green and red backgrounds through the available
row width, independent syntax colors, explicit +/- markers, and missing-final-newline
markers. Context and chrome retain the terminal background. Syntax palettes preserve
the lexer's token categories,
including functions, built-ins, operators, and language-specific names, rather than
only keywords and literals. Color decoration does not infer semantic types from names.

The viewer selects a light or dark palette from an asynchronous
terminal background query (OSC 11), without delaying the initial display or input.
A valid later reply recolors existing content without changing navigation or review
state. Until a valid reply arrives, a recognized `COLORFGBG` background is used;
otherwise the viewer uses the dark syntax palette and paired dark change-row fills.
Replies are bounded, consumed separately from navigation, and never
interpreted as review commands. Syntax, change markers, and recency gutters use the
same selected mode. Lexer theme backgrounds and error fills never reach the viewport;
only the renderer owns change-row fills, which reset before the next row.
Highlighting uses each hunk side independently; unknown languages, lexer failures, and
oversized sides fall back to plain safe text. No external renderer or pager is required.
Delta/Git styling, side-by-side layout, and word-level emphasis are unsupported.

### Resource limits

| Resource | Limit | On exhaustion |
| --- | --- | --- |
| Session workspace/thread membership | 1 MiB | Disable the view rather than publish partial scope |
| Captured source rows per composed file | 1,048,576 | Report composition failure |
| Source and rendered output | 64 MiB each | Fail explicitly |
| Syntax-highlighting input per hunk side | 256 KiB | Display plain safe text |

Acceptance must cover real-terminal updates, resize, navigation, and cleanup; concurrent
snapshot/publication and reconnect races; resumed and cross-thread histories; prepared
moves and delayed receipts; and flush/revert behavior with shifted or repeated lines.
