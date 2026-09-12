# Router-owned journals

## CTR-JOURNAL-001 — Journal ownership and delivery

`internal/router/journal.go` owns journal state, per-thread IDs, capacity, atomic mutation
transactions, replay receipts, persistence, and delivery acknowledgements. The store uses the
replay store's filesystem lock for cross-process serialization but never evicts executable replay.
Acquisition of the in-process state lock is request-cancellable. Delivery, state, and replay
locks are acquired in that order; each state transaction keeps its lock through persistence.
Thread-capacity scans run only for new journal files, under the replay lock.

`internal/router/commentary.go` retains the existing argument projection and exact replay
provenance machinery, generalized so eligible function tools receive `journal` mutations.
`internal/router/journal_tool.go` handles `functions.journal` directly in the response transform.
It records local router calls and ordinarily supplies their results through a request continuation without
dispatching a host executor. A successful direct finish operation instead selects terminal
delivery in the same response when no client-dispatched calls remain. Completion intent stays
on that response transform, not in durable journal state; replay cannot finish another turn.
Codex receives a named `function_call_output` without `call_id`,
using an `fco_` item ID correlated against durable replay. Request preparation restores the
original provider call and paired result, and rebases any incremental WebSocket input whose
client prefix differs from the restored provider prefix.

`journal_delivery.go` snapshots and leases delivery, emits immediate notices and terminal flushes,
and acknowledges only the rendered revision after a successful downstream write. Live delivery
sets `reported`; terminal delivery also sets the independent `flushed` flag. Edits reset both
for the new revision. Delivery metadata, not message text, identifies terminal acknowledgements
and descendant flush acknowledgements. A separate
filesystem delivery lock excludes concurrent mutations and deliveries across router processes;
replay transactions remain independently lockable while the delivery lease is held. The existing
commentary broker and child activity collector carry live user-only delivery and canonical child
prefixes; terminal journal delivery reads the durable journals directly. A failed render releases the lease without marking an item reported.
The complete tree is rendered and size-checked before retaining message IDs or delivery entries.

`server.go` performs the terminal continuation and ordering; token arithmetic and capture metrics
remain unchanged. Only explicit journal finish selects a journal terminal, with no follow-up
provider request. Provider final-answer messages are not filtered and never substitute for finish.
Child terminals retain a router-owned saved-summary without flushing. Main terminal delivery snapshots
its journal followed by descendant journals sorted by canonical path, under one delivery lease.
Accepted parent identities persist with journals; conflicting or incomplete chains cannot join the
flush. Acknowledgements target each original child journal, not an auxiliary activity copy. Replay strips router-owned message IDs.

`journal_catalog.go`, `wrap.go`, and model-instruction rewriting disable the stock Tasks surface
only in Mekugi mode. Passthrough does not use the store, catalog rewrite, or journal tool.
