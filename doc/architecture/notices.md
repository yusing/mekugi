# Native diagnostic delivery

## CTR-NOTICES-001 — Critical queue and terminal acknowledgement

`CriticalErrors` owns bounded classified diagnostics, repeat counts, delivery claims,
and launcher recovery. The [notice contract](../spec/notices.md) owns visible content
and failure behavior. The queue never transforms provider responses.

Native session integration reserves notices scoped to its main thread and proven
workspace ancestry from the [activity owner](activity.md). It renders them through
the existing Main activity view and acknowledges only after terminal paint succeeds.
A failed write releases the claim; view-local identity prevents duplicate entries
on retry. Unknown scope remains pending rather than being attributed to another root.

The launcher consumes pending notices after terminal restoration. Request finalization
and failure-record persistence retain their own authority: display cannot change an
HTTP error, retry a model request, or acknowledge execution. Historical router-message
provenance remains with the [replay owner](boundary.md), not the native notice queue.
