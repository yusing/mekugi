# Native diagnostic delivery

## CTR-NOTICES-001 — Critical queue and terminal acknowledgement

`CriticalErrors` owns bounded classified diagnostics, repeat counts, delivery claims,
and launcher recovery. The [notice contract](../spec/notices.md) owns visible content
and failure behavior. The queue never transforms provider responses.

Native session integration reserves notices scoped to its main thread and proven
workspace ancestry from the [activity owner](activity.md). Router errors reuse Main's
composer feedback and shared error dialog; cleanup planning and reclaimed-storage
progress reuse its transcript. Each surface acknowledges its own reserved notices
only after a successful terminal paint makes them visible. The complete error batch
stays attached to its claim, so hidden, replaced, dialog-covered or frozen-selection
feedback remains pending even when another surface is painted. Failed or hidden
delivery releases the affected claim; retries restore the error batch and view-local
identity prevents duplicate progress entries. Unknown scope remains pending rather
than being attributed to another root. No durable error history is added.

The launcher consumes pending notices after terminal restoration. Request finalization
and failure-record persistence retain their own authority: display cannot change an
HTTP error, retry a model request, or acknowledge execution. Historical router-message
provenance remains with the [replay owner](boundary.md), not the native notice queue.
