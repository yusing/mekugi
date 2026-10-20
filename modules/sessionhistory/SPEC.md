# Retained session history

## Purpose

Own durable session namespaces, allocation, publication, leases, and retention.
Feature owners retain the meaning and encoding of their records.

## Scope

One process-independent publication boundary protects each retained store.
Record kinds are supplied by composition. Removing a kind must preserve its
records and potentially shared dependencies. Retained facts do not restore host
processes, live grants, or operation authority.

The invocation composes this owner for live sessions, authenticated frontend
workers, and offline inspection. Each descriptor contributes its own record kinds;
feature owners supply their codecs without application compatibility registration.

## Behavior

Publication retains dependencies before exposing referencing values. Equal bytes
do not create a new revision. A failed operation may leave completed writes and
conservative ownership; it cannot report unavailable data as published. A stale
retention plan cannot remove records from a newer publication view. The kernel
owns flushing; successful publication guarantees visibility and process-restart
recovery, not power-loss durability. Missing facts never authorize rerunning effects.

The default shared budget is 4 GiB, including ownership catalogs. Encoded records
are bounded to 32 MiB. Owners contribute additional budgets; commentary uses a
separate 16 MiB budget and retained output is bounded to 256 MiB. Admission
reports required bytes, the limiting budget, and an actionable error.

Inactive session data expires after 14 days. Budget pressure selects the least
recently active eligible sessions. Active process leases and shared dependencies
prevent reclamation. Recent uncatalogued records are retained; older records use
last-write time without inventing a thread owner. Cleanup never touches Codex
transcripts, workspaces, external outputs, or exported debug bundles. Compaction
and request truncation are not expiry signals.

Catalogs bind dependencies to stable threads across workspaces, routing remaps,
forks, and restarts. Inherited references gain another owner. Namespace bindings
and allocation high-water marks outlive reclaimed payloads, so resuming does not
recycle handles. Subagents share their root namespace; a fork freezes inherited
allocation ranges and publishes its new scope after inherited records succeed.

Retention recognizes only contributed record kinds. An unregistered record,
including an uncatalogued historical record, keeps its catalog and potentially
shared payloads intact. Because that owner's dependency graph is unavailable,
cleanup conservatively preserves the store's payloads until the owner returns.
Unknown files are never opened or removed by retention.
Record owners declare separate budgets, aggregate limits, and records requiring
semantic retirement instead of deletion. Admission requests background cleanup;
it never reclaims data while publishing an operation.

Independent preference files use a separate single-file publication operation,
without store locks, catalogs, revisions or retention enrollment. It preserves
atomic last-completed replacement and the existing directory-link behavior.
Their owner decides paths, encoding and when publication is needed.

## Lifecycle

The invocation composes record kinds before consumers open stores. Opening a
writable store establishes its private directory; attaching an offline view does
not create storage. Operation-scoped transactions cannot be reused after their
callback returns. No transaction spans terminal or network delivery. One maintenance worker per
store prepares plans outside serialization, validates revisions and leases before
bounded removal batches, and releases serialization between batches. Referring
records retire before their dependencies regardless of filename order. Interrupted
retirement preserves the catalog until its final batch, so restart is retryable.

### Application integration

Mekugi tool replay uses `$XDG_STATE_HOME/mekugi/replay`, defaulting to
`~/.local/state/mekugi/replay`; a relative `XDG_STATE_HOME` is invalid. This private
correctness state survives shutdown and restores model-visible tools on resume
and fork. Initialization failure prevents launch. Passthrough opens storage only
to retain failures and does not retain tool replay state.

Cross-process leases cover active turns, host handoffs, workers and yielded calls.
Session admission resolves the durable namespace before acquiring the thread lease,
then enrolls the active owner under publication serialization. Failed enrollment
releases that lease. Native metadata and request observation share this admission.
The selected session retains its store, thread, resolved namespace and lease as one
handle. Replacement closes that handle; offline playback acquires none.
A delivered terminal without a tool handoff releases its turn lease independently
of answer text or optional UI tracking; shutdown releases owned leases. Cleanup
never stops host processes or changes continuation lifetimes.
Request pins and handoff leases share this owner's invocation lifetime. A request
reserves its explicit thread before acquiring storage; only the newest admitted
request can release an idle handoff, after every request for that thread has
unpinned. Stale completion and concurrent preparation cannot release newer work.

The application schedules age cleanup at startup and hourly without blocking
startup, UI handling or request preparation. Admission coalesces pressure into
prompt background work rather than waiting for the next age sweep. Response
translation retains completed facts in memory while waiting cancellably up to
30 seconds for maintenance, then retries persistence only. Insufficient space,
stalled cleanup or timeout yields a capacity diagnostic, never a repeated host
operation or upstream request. Age-cleanup failures issue invocation-wide notices
without failing unrelated requests.
User-only notices or worker stderr report removals; errors identify the cleanup
phase and underlying cause without claiming complete reclamation. Shutdown
cancels and joins maintenance, including a lazily opened failure-only store.
Missing recovery references explain expiry or pressure without replaying effects.

### Read-only storage inspection

`mekugi inspect-storage` validates one explicitly selected retained call or
snapshot object without opening writable storage, starting a router, acquiring
storage locks, changing permissions or executing inputs. Its `mekugi.storage.v1`
JSON reports target, decoded bytes, content hash and dependency count. Private
text requires field selection; UTF-8-aligned windows are bounded to 64 KiB with
a continuation offset. Missing objects, invalid references, corrupt compression,
hash mismatches and invalid offsets produce target-qualified stderr and nonzero
status. No scan of other sessions substitutes for unavailable selected evidence.
Record owners remain responsible for decoding and validation.

## Codex connection

None. Codex execution remains outside this module.

## Records

Session catalogs, durable namespace identity, allocation high-water marks and
published host facts belong here. Retained calls, their historical snapshot
dependencies and generated-commentary provenance use the shared frozen
[historical codec](../../libraries/legacycall), preserving identity and encoding.
Commentary keeps its independent budget. New feature records use their owner's
codec rather than extending the historical mixed envelopes. Cross-owner ordering
follows the [publication contract](../../docs/contracts/record-publication.md#cross-owner-publication).

The invocation's bounded call cache uses the same neutral historical facts.
Ordered batch admission is atomic, protects the arriving calls, and evicts older
eligible entries without evicting pinned sessions. Request-local confirmation,
native process references and preview watches never enter the cache. Request
handles couple cache pins to the same owner's handoff lifetime without deriving
thread identity from cache-key spelling.

Host-record publication preserves original translation facts while merging
completed status, provider passthrough metadata and generated-commentary identities.
Other conflicting payload changes fail. Capture consumers decide whether a new
record needs an order; this owner allocates and preserves it under publication.

Hook records bind exact workspace and thread identity, preserve recorded output
and outcomes, and disclose dropped earlier observations. Read-only attachment
neither creates storage nor revives unfinished hooks. The live hook schema belongs
to the Codex backend. Feature owners supply record kinds, dependency inspection
and semantic retirement updates. Existing retained paths and encodings stay
readable; session history does not reinterpret feature records.

## Failure

Publication failures preserve completed facts. Lock acquisition is cancellable.
Revision mismatch invalidates outside-lock preparation. Unknown record kinds
must never be treated as proof that their dependencies are unused.

## Acceptance

Consumer checks cover publication ordering, peer revisions, failed writes,
no-op writes, scoped transaction lifetime, process-independent leases, bounded
retirement, retained fork/restart identity, and interrupted fork retry. Feature
codec checks remain with their owners. The application checks those guarantees through
composed live-session, worker, and offline-reader entrypoints.
