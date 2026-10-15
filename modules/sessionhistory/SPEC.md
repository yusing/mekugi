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
workers, and offline inspection. Feature codecs remain in their current owners
until those modules are extracted.

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

## Lifecycle

The invocation composes record kinds before consumers open stores. Opening a
writable store establishes its private directory; attaching an offline view does
not create storage. Operation-scoped transactions cannot be reused after their
callback returns. No transaction spans terminal or network delivery. One maintenance worker per
store prepares plans outside serialization, validates revisions and leases before
bounded removal batches, and releases serialization between batches. Referring
records retire before their dependencies regardless of filename order. Interrupted
retirement preserves the catalog until its final batch, so restart is retryable.

## Codex connection

None. Codex execution remains outside this module.

## Records

Session catalogs, durable namespace identity, and allocation high-water marks
belong here. Other owners supply their record kinds, dependency inspection, and
prepared semantic retirement updates. Existing retained paths and encodings are
preserved during extraction.

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
