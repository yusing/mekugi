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
store. Presentation cannot acknowledge journals, finalize edits, reconstruct missing
provider usage, or revive replayed processes.
