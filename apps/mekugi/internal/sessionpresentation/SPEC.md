# Session presentation

The application owns displayed-thread identity, Main's input destination and
read-only admission, and child-history pagination here. Activity owns transcript
content. Session control issues backend requests; the host adapter decodes pages
and retains protocol payloads and buffered notifications. The terminal owns
roster focus, geometry, scroll anchors, key bindings and history hints.

Displaying a child changes no execution identity and cancels no host work. Main
remains the input, question and approval destination. Child display makes the
composer read-only; returning to Main restores its existing draft. Replay is
read-only even while Main is displayed. Display and pagination intents name the
view version and target identity, so a stale intent cannot affect a replacement
session. View values are detached data without terminal or backend types.

An attachment's state lives in the application's session store. Each child's
history lives in its thread store; replacement drops these stores rather than
retaining an independent thread registry. A highlighted roster row is not the
displayed thread. Metadata can rename a caller without changing its identity.

Paginated history opens at the newest turn's recent items. An older-page intent
uses the last retained item as an exclusive anchor when available, or the opaque
continuation cursor for an empty page. Pages deduplicate turn and item identities
and reject repeated cursors or foreign items. Already-loaded content survives
failure and remains retryable. Deferred evidence joins its original item when
that page arrives. Older pages cannot replace newer live metadata or promote old
commentary to a final answer. Archived children use the same observation flow.

Only one child-history read is active at a time. Other threads continue normally.
Cancellation retires that observation and releases buffered live events without
interrupting the child. Saturated buffering gives live observations precedence.
Late responses cannot revive a cancelled read or cross an attachment boundary.
History state distinguishes intentionally unloaded content, loading, failure and
exhaustion; the renderer supplies the corresponding controls and labels.

Acceptance includes a non-terminal consumer of display, admission, paging,
retry, cancellation and replay values, plus live-adapter history reconciliation
and unchanged terminal snapshots. Offline replay does not establish live host
coverage.

## Codex history binding

Codex owns lineage, including inherited fork items and archived descendants.
Fresh resume rediscovers history instead of reviving continuation handles.
Paginated children use a metadata-only `thread/read`, then descending
`thread/turns/list` with `itemsView: "notLoaded"` and 100 turns per page, bounded
to eight pages. Turn identity, status, failure and second-resolution timing come
from metadata rather than item text. Legacy children retain full `thread/read`
restoration because Codex's item-list API does not support their rollout history.

Descending `thread/items/list` reads contain at most 100 items from one child and
turn, with optional millisecond timestamps. Item anchors translate to the
exclusive `{type: "item", itemId: ...}` cursor with that same nonempty `turnId`.
An opaque cursor continues an empty page; a null continuation ends the turn.
Errors never trigger an unbounded fallback. Page limits bound item counts rather
than bytes, so one large item can still require a large frame.

Active skills reconstruct from event history without another durable store;
their display contract is [session status](../../../../doc/spec/native_ui.md#session-status).
Skill scans remain independent of lazy transcript pages. Descending thread-wide
item reads include inherited history and stop at the latest completed
compaction. One bounded scan runs at a time; incomplete or failed scans leave
counts unknown with a notice. Live resets override historical scans and session
replacement retires correlation. Observation neither resumes a child nor invokes
a model.

Pending rollout placements stay with their child until the corresponding item
or turn loads. Activity's stable entry IDs retain output and Main links across
older-page insertion. [Activity retention](../../../../modules/activity/SPEC.md)
keeps live commands until completion; displaying history never revives them.
