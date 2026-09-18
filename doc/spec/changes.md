# Tracked agent changes

## REQ-CHANGES-001 — Shared change records and review reads

Shell `hpatch` results prepend one compact `change amber1` line. A complete edit
reserves a workspace-scoped ID before evaluation. Each originating agent thread has
a word-named stream and increasing decimal sequence, such as `amber1`, `amber2`,
and `apple1`. Evaluated recovery attempts reuse the explicitly named rejection's
correlation and change ID. Invalid corrections do not evaluate or allocate changes.

Word-based change indexes use a separate storage namespace. Earlier indexes remain
owned storage for accounting and reclamation only; they are not migrated or read as current changes.
Only the word-based ID format is accepted; earlier `hp_` IDs are unsupported.
IDs and stream allocation persist across router restarts. The original hpatch, all
recovery inputs and diagnostics, and any successful evaluated review diff are available
through the one ID. Published attempts are never removed or overwritten; replay does not duplicate attempts.
A fork continuing an inherited recovery retains its original ID. Separate new calls
in the fork use its own thread stream. Concurrent branches under one ID retain every
outcome rather than overwriting an earlier successful diff.

The shell worker applies validated edits through the engine and retains immutable
attempt evidence before returning its result. Application failure is not success.
Live-diff notifications carry references to committed evidence through the existing
authenticated runtime publication endpoint; publication failure never changes edit
execution or replaces its result. Each attempt records its executing thread, which
may differ from the original change stream when a fork performs recovery.

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

References accept individual IDs or inclusive ranges such as `amber1..amber4`. Range endpoints
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
`--summary` omits diff bodies and renders a Git-style diffstat per evaluation:
aligned paths, changed-line counts, proportional `+`/`-` bars capped at 40 characters,
and a footer with file, insertion, and deletion totals. Moves show both paths with `=>`;
empty-file changes and pure moves remain visible with zero changed lines. Control
characters in paths are quoted. Totals include only selected files. Attempt statuses
remain visible, and repeated evaluations stay separate, not a synthetic net diff or
a current workspace status.
`--history` additionally returns original inputs, recovery amendments, rebuilt scripts
when different, and full diagnostics. Each path after `--` matches either recorded before or after
path, accepting equivalent lexical absolute and workspace-relative spellings for workspace
files. It does not consult current filesystem contents or resolve file symlinks. Historical private
shell-script paths match exactly. Filtering affects diff/file entries, not attempt history.
Multiple paths select the union of matching files in recorded order.
A file matching multiple filters is emitted once per evaluation, not once per filter.
A selection with no matching files explicitly lists the requested filters.

Review diffs are captured by the engine from immutable original content and final
formatted content before external effects. They include additions, deleted contents,
moves, empty files, CR bytes, and explicit missing-final-newline markers. Later workspace
edits cannot alter these diffs. They describe evaluated changes, not a second inspection
of the executor's resulting bytes: the host line-ending behavior in `REQ-OUTPUT-001`
still applies. Scripts generated by Python or other programs and passed to hpatch are tracked through
the same evaluated-input and completed-diff records.

Bash/POSIX shell workers also retain operation-owned review diffs for regular-file
redirections (`>`, `>>`), `touch` creation, `mv`, and `rm`. Each completed operation
with file changes emits a `change ID` notice on the shell result's stderr, outside
command redirections, pipelines, and substitutions. `touch` of an existing file
changes only timestamps and has no content diff. Moves record names without reading
unchanged source contents; overwritten destinations and deleted files retain their
removed contents. Directory moves enumerate affected names, not workspace contents.
Partial command failures retain only completed effects. Redirection effects remain
tracked even if the command itself fails. Captured shell bytes are not formatted.

Overwrite capture and truncation use the same open descriptor, preserving hard links,
permissions, and existing open handles. Deletion and destination-replacing moves are
owned operations, not read-before-external-command observers. Unrelated concurrent
writers are outside this guarantee. No watcher or whole-workspace snapshot runs.
Persistence failure after an effect is reported explicitly, never as complete evidence.
When old or resulting bytes cannot be read, an otherwise permitted operation proceeds.
The change notice, retained review, summary, and live diff explicitly report incomplete
history, with unavailable line counts rather than invented contents or zero counts.
Actual filesystem-operation failures still fail; only completed effects are recorded.

The worker owns direct `rm` and `mv` commands. It supports ordinary operands, `--`,
force/verbose flags, recursive/directory removal, and no-clobber/target-directory move
options. Unsupported options reject before effects. Cross-filesystem moves copy
into an owned temporary directory before committing the destination replacement,
then remove the unchanged source entries. Failed copies or installs leave source
inodes intact; partial source-removal failures retain copy versus move evidence.
Temporary-copy cleanup failures identify the retained artifact path.
Absolute executable paths, nested external shells, external formatters, and arbitrary program-internal
writes are not intercepted. Existing-content editing remains an instruction-level
hpatch requirement, not a runtime write prohibition. Unrelated workspace changes are
not tracked.

Historical edits to retained `@shell/` scripts are labeled separately from workspace
files; their recorded paths are relative to that private script store. Reads do not
combine several invocations into a synthetic net diff.

An attempt is `rejected`, `no-op`, `prepared (application unconfirmed)`, or `applied`.
A translated patch alone is never proof of application. Exact successful-report replay
confirms application only after the entire incoming history validates. Historical records
of direct private application preserve their original confirmation. A report may be bare or in the matching host
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

The live pane is a read-only view of this session's captured hpatch and supported
shell file operations, plus transient streaming previews. It does
not include Git changes, untracked external writes, or retained shell scripts. Updates arrive
as authenticated events from the owning router, never through filesystem watching or
polling. The viewer does not write the store, evaluate edits, or start another router.
Manual live viewing of existing sessions is unsupported; durable history remains
available through `hchanges`. `mekugi live-diff --simulate` runs a deterministic,
isolated demonstration of the same worker, authenticated event transport, captures,
and terminal UI without Codex or Herdr. `--speed` controls playback from 0.1 to 20;
`--repeat` loops the scenario until exit. Re-running the command replays from fresh
state. Simulation rejects caller workspace, replay, and connection selectors, writes
only owned temporary files, and removes them on exit. It covers progressive input,
a wrapped line, a burst, completion, and interruption, including standalone
`functions.shell` streaming and a display-only recovery correction overlay.

### Session and lifetime

Interactive `mekugi codex` inside Herdr arms one automatic pane launch per router
process when stdin and stdout are terminals and `herdr` is available. The first
successfully prepared non-subagent turn selects the canonical workspace, not wrapper
cwd or parsed command arguments. Preparation and read-only turns do not open UI.
The first shell input fragment or complete call emitted for an observed thread
triggers the launch, including a subagent's call or a rejected edit; replayed history
alone does not.
Herdr starts the pinned viewer executable directly, without initializing an
interactive shell, and moves its pane to the caller's right without changing focus.
Launch is asynchronous, silent, limited to five seconds, and canceled with the router.
Failure neither blocks Codex nor triggers an automatic retry.

Scope includes only durable thread streams observed on successfully prepared turns
in that router, including subagents and later thread/workspace switches. A new thread
starts empty; a resumed thread retains its captured history. External absolute edit
paths are included, but unrelated sessions sharing a workspace are not.

The connection is private to the owning router. Viewers never discover or switch
routers. Router exit revokes the connection and closes only its own pane, with bounded
cleanup. A fresh router may open a new pane without replacing existing viewers.
Quitting the viewer restores terminal state without closing the pane or affecting Codex.

### Streaming previews

Incoming shell input deltas, including WebSocket events, display before input completion.
Literal standalone `hpatch` arguments and heredoc input show a provisional file diff through
the engine's bounded in-memory preview, including unfinished edit values and heredocs.
Once a call is recognized as an edit, its representation stays an edit preview for
that call. If a later fragment cannot be decoded or projected, keep the last valid
diff and label it as such rather than flashing back to the shell script. If no valid
diff exists yet, show an unavailable status, not the literal edit script.
Shell headers select the preview directory and interpreter using the shared header parser.
Calls with dynamic expansions, input files, composed commands, command templates,
or `hpatch --recover` remain script previews when no earlier prefix was recognized
as an edit. A later incompatible fragment invalidates the current projection but
never turns a retained provisional diff into a claim about the completed command.
Previews never execute shell code, apply files, format source, run hooks, or publish durable
changes. The actual post-expansion edit report and captured diff arrive after host execution.

Previews remain separately labeled, never composed into applied history or treated
as receipts. The pane defaults to a full-height stream view. `v` switches between
stream and captured diff views; there is no split layout or automatic mode switch
when calls finish or captures arrive. The footer identifies the selected view and
the switch key. Stream mode ignores captured-diff navigation, flushing, and wheel
input. Switching views preserves captured-diff navigation and follow/pause state.
Streaming keeps the newest changed source row's final wrapped fragment visible,
independently of captured-diff follow/pause state, and fills available rows through
that tip rather than leaving centering padding below it.

Concurrent calls share the stream view as separate vertically stacked cards in
first-seen order. Each heading identifies the caller (canonical agent name when
available, otherwise thread identity) and a short call identifier, including parallel
calls from the same thread. Caller text is terminal-safe and width-bounded. Updates
replace only that call's snapshot, never another caller's card; each card follows its
own latest source row. Tiny regions show a
count of additional calls rather than switching callers on each delta; enlarging the
pane reveals them. Provisional edits from different callers are not merged into a
speculative combined file result.

Completion, rejection, interruption, or transform closure marks that call's preview
complete without hiding its last frame on a timer. Stream view persists between
calls until the owning session closes. A new call replaces completed cards, but
never active cards; completion of one call cannot hide another. Retention stays
bounded by the active-card limit rather than accumulating session history.
New captures, explicit resume, and terminal resize recenter the captured change
when following; paused views retain their scroll position. Reconnect clears
transient display state and restores only currently active router-local previews
after the durable snapshot barrier. No preview survives router restart or history replay.

Preview computation is asynchronous and coalesces bursts without waiting for input
completion. Each worker samples the latest buffered input without queuing old work;
completion cancels in-flight preview output. The viewer consumes queued
snapshots before painting and limits preview paints to a 33 ms frame cadence, keeping
only the latest snapshot rather than playing back intermediate frames. Preview updates
do not recompose or syntax-render captured history. Preview rendering lays out only
visible source rows and a bounded leading context window for best-effort syntax
highlighting. Unchanged source windows reuse syntax decoration. The heading identifies
streaming without repeating validation disclaimers. Shell source uses its
selected interpreter, including batch switches outside shell constructs; scrolling
past a selector or clipping a long input does not lose its language. Display-only
syntax boundaries retain interpreter changes across explicit shell batches.
Completion retains any clipped-tail label. Incomplete shell input remains visible;
the completed call owns rejection diagnostics.
Scope expansions precede their authorized previews even when snapshot updates are
coalesced separately from durable events.

Input and total source/result are bounded to 256 KiB per preview, target expansion
to 1,024 mutations, and retained display payloads to 48 KiB each across at most 16
active previews. Capacity or target failures do not block complete edits. Inputs that cannot be decoded without execution or recovery state remain script source,
with a labeled tail when clipped. Shell program content, including nested heredocs,
is preserved. It never guesses post-shell file
state or reads private continuation storage. Oversized file projections retain the last
useful frame when one exists.

### Update integrity

Captures and application receipts become visible only after durable publication.
Each completed hpatch attempt publishes its retained edit receipt through the authenticated
runtime publisher. Translation alone never confirms application. Delivery must not
block edits or assume execution or continuation authority.

Initial snapshots and concurrent events must reconcile without lost or duplicate
updates. Scope additions include newly eligible durable history. Reconnects require
a fresh durable snapshot while preserving navigation, acknowledgements, and recency.
Within a snapshot, immutable attempts are loaded once. Receipt updates must not trigger
capture-history rereads. A failed auxiliary publication does not change the edit result;
the retained attempt remains available to the next snapshot. Subscriber queue overflow
forces reconnection rather than silently dropping updates. Missing, inconsistent, or
removed evidence fails explicitly, never as an empty successful view.

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

In diff mode, all files share one continuous viewport. Following is enabled initially and targets
the final changed row, including those deep inside a combined hunk or long new file. It prefers the
latest update's marked region when composed coordinates are ambiguous and centers the
target row's final wrapped fragment vertically so available context appears above and below it. At the start
of the view, show available rows from the top instead of inserting blank padding.
Near the end, clamp the viewport to the last full page so earlier diff content fills
the pane instead of leaving empty rows below it. This applies after resize as well as new captures; paused views retain their manual offsets.
Manual scrolling or file navigation pauses following and preserves file-relative
offsets, clamped when content shrinks. `n`/`p` navigate files, `g`/`G` the complete view,
and `r` resumes following, including changes received while paused. The file at the
top of the viewport is current for `f`.

The footer identifies FOLLOW or PAUSED. Updates received while paused preserve the
viewport and show `new changes available`; resuming or flushing all marked files clears
the notice. New captures mark every affected file and touched net hunk with a cyan
gutter. Receipt-only updates preserve those marks; another capture update or flush
replaces them. Fully reverted or flushed files disappear from the viewport and file navigation;
retained captures remain available for later composition. When all files are hidden,
the viewer shows a single empty-state message without a file header.
Markers indicate recent hunks, not line- or word-level attribution. Neighboring composed
regions share context without displaying any source coordinate twice.

File headings and the sticky current-file header share an aligned gutter, bold titles,
green `+N` and red `-N` source-line counts, and a width-filling separator. Counts describe
the visible combined result plus prepared captures, exclude context and headers, and
become zero when flushed. Navigation must not rescan diffs merely to update counts.
Syntax decoration is reused for identical captured source, path, and theme within a
viewer. Cache storage is bounded; eviction changes performance, not display or follow state.
File actions appear once per file or prepared capture, with both rename endpoints.
Deleted files retain their action heading and removal count, but omit source diff rows.
Heading blocks wrap without losing actions or paths; the sticky title stays on one row.

Source uses one line-number column: old coordinates for deletions and new coordinates
for additions and context. Explicit change markers remain, without repeated hunk headings
or applied-status banners. The column uses only the required digits and disappears in
very narrow panes; blank source rows keep their numbers.
Prepared status appears once per capture. Paths within the display workspace are relative;
external paths stay absolute. Display formatting never changes captured paths or source.

Source lines always wrap to the pane width, preserving whitespace, syntax colors, and change-row
backgrounds. Continuations retain the gutter and change marker with a blank coordinate.
`h`/`l`, Left/Right, and horizontal wheel reports are ignored without pausing
following. Resizing preserves the logical row at saved viewport positions.
The viewer enables SGR mouse reporting while active and explicitly disables its
mouse reporting modes on exit, including in terminals without private mode save/restore. Vertical wheel reports scroll rows. Wheel modifiers are accepted. Clicks, releases, motion,
and malformed reports do not trigger navigation or review commands. Fragmented
reports are decoded incrementally with bounded storage.

Frames use synchronized terminal output so row clearing and replacement paint together
on supporting terminals.

Resize must preserve safe Unicode clipping, leaving room for the gutter and final
terminal column. Only text and terminal color/text styling (SGR) may reach the viewport.
Added and removed source rows have green and red backgrounds through the available
row width, independent syntax colors, explicit +/- markers, and missing-final-newline
markers. Context and chrome retain the terminal background. Syntax palettes preserve
the lexer's token categories,
including functions, built-ins, operators, and language-specific names, rather than
only keywords and literals. Color decoration does not infer semantic types from names. Bash command words use
shell grammar to distinguish external commands from arguments and literal content;
built-ins retain their own syntax category. Incomplete input keeps best-effort
highlighting without executing or validating the script.

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
