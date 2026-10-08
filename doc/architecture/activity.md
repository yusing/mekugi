# Activity observation and native presentation

## CTR-ACTIVITY-001 — Activity identity and collection

Native session integration in `internal/router` consumes Codex app-server events;
`internal/ui/activity` owns presentation. The [UI boundary](ui.md) owns their
integration, and the [activity contract](../spec/activity.md) owns classification.
Codex retains execution, scheduling, recipients, interrupts, waits, and lifecycle.

Child attribution uses validated stable thread relationships and canonical agent
identity, never routing-session IDs or message text. Request preparation precedes
identity admission. Deferred observations retain their originating thread rather
than borrowing the identity of a later draining request. Ambiguous ancestry suppresses
projection without suppressing execution or substantive child output.

The native client binds its root to the activity collector for scoped observations. App-server events supply live activity; authenticated recipient-input
observations supplement assignment and directed-message bodies. Those observations
are display data, not assistant speech. Native activity does not wait for a root
provider response or create model turns.

There is no legacy response projection or inline activity fallback. Main activity
comes from app-server.

The roster consumes canonical [per-thread usage](usage.md), not a second accounting
store. Its final edit total consumes the shared saved-review composition, not a sum
of caller activity counters. Caller counters remain a per-agent cumulative
presentation; they are not an outcome-accounting store. Presentation cannot
acknowledge journals, finalize edits, reconstruct missing provider usage, or revive
replayed processes.

Each native feed retains one presentation record per source entry, containing
the entry, its parsed blocks and its cache revision. The feed's record owner
handles append, replacement, in-place metadata and annotation edits, removal,
and history reordering together with cache invalidation. Parsed annotations and
stable entry IDs move with their source;
consumers do not maintain index-aligned entry and block collections. Main's
threaded layout invalidates cross-entry dependent runs on record updates, while
ordinary Activity updates keep unrelated completed runs warm.
The commentary replay record owns the exact provider item IDs contained in an
enriched child result, scoped to workspace, thread, and turn. Native feed records
consume that provenance to replace the source cards in place, preserving stable
links and timestamps. Reversed live arrival and older history pages cannot restore
the replaced cards. Missing provenance preserves host content; text and ID prefixes
do not establish replacement. This presentation does not change host transcripts.

## CTR-ACTIVITY-002 — Native child history pages

The UI owns request-local child pagination state, keyed by stable root
and child IDs. Codex owns visible lineage, including inherited fork items and
archived descendants. Session switches retire response correlation and cursors;
fresh resume rediscovers history instead of reviving continuation handles.

Children with `historyMode: "paginated"` use a metadata-only `thread/read`,
then `thread/turns/list` with `itemsView: "notLoaded"`, descending direction,
and 100 turns per page, bounded to eight pages. Turn identity, status, error,
and second-resolution start/completion timestamps come from these metadata
pages, not item text. Legacy children retain full `thread/read` restoration:
Codex's item-list API does not support legacy rollout history.

`thread/items/list` is scoped to one child and turn, descending, with a 100-item
limit. Responses carry item identity and optional millisecond timestamps.
Older reads use an exclusive `{type: "item", itemId: ...}` cursor with that
same nonempty `turnId`; opaque `nextCursor` continues pages without an item
anchor. Item identities deduplicate overlapping pages; repeated cursors and
out-of-scope items are errors, not end-of-history. A null `nextCursor` ends a turn,
then the next read selects the preceding turn. Pagination errors are never
silently replaced by an unbounded read.

Active skills reconstruct from Codex event history on exit/resume, without a new durable store; see the [display contract](../spec/native_ui.md#session-status).
Paginated child skill scans are independent of lazy Activity reads: descending `thread/items/list` pages cover turns and inherited history, stopping at the latest completed compaction.
Only one bounded scan runs at a time; incomplete or failed scans leave counts unknown with a notice. Live resets override scans; session switches retire correlation. Observation neither resumes children nor invokes a model.

Claude reads one bounded SDK-selected snapshot per owner, separately from its
2,000-message display window. SDK offsets slice a full read rather than page
storage. Forks retain independently bounded complete child UUID selections
through the existing history owner. Existing inherited selections remain the
admission boundary; legacy records with unknown completeness hide skill counts.
Native compact-summary markers and timestamps retire earlier loads, including
pre-compaction records that the SDK relinks after the summary;
canonical native Skill result text and successful Read pairs confirm new ones.
Only complete scans publish counts through the shared skill owner.

Unloaded history, loading, and failed reads have separate presentation states.
Pending rollout placements stay with their child until their item/turn is
loaded. Stable Activity entry IDs survive older-page insertion and continue
to own output and Main links. Transcript retention retires completed records
before live running commands, which survive until completion. A feed may
temporarily exceed its normal retained-record limit when all retained commands
are live; [running-header pinning](../spec/activity_display.md#agents-roster-and-navigation)
does not revive running state on replay.
While an older page is pending, same-child lifecycle notifications reconcile
after the page; deltas are not retained, and buffer saturation cancels the
observational read before applying live events. Cancellation retires only
the pending read, never a host process or turn.
