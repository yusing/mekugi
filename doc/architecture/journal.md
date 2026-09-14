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
prefixes; terminal delivery and named journal lookup share the durable workspace snapshot
owner. Repeated initialization and unchanged identity binding read that locked state but
skip redundant publication and synchronization. A failed render releases the lease without marking an item reported.
The complete tree is rendered and size-checked before retaining message IDs or delivery entries.
The shared terminal renderer groups the selected items by their exact question within each
journal message. It renders each question once followed by all its answers, preserving group
first-occurrence order and answer order within each group. Plain milestones stay separate.
Grouping does not change stored content, delivery state, or standalone live notices.

`server.go` performs the terminal continuation and ordering; token arithmetic and capture metrics
remain unchanged. Only explicit journal finish selects a journal terminal, with no follow-up
provider request. Provider final-answer messages are not filtered and never substitute for finish.
Child terminals retain a router-owned result containing their current journal text without
acknowledging revisions or reporting delivery counts. Main terminal delivery snapshots
its journal followed by descendant journals sorted by canonical path, under one delivery lease.
Accepted parent identities persist with journals; conflicting or incomplete chains cannot join the
flush. Acknowledgements target each original child journal, not an auxiliary activity copy. Replay strips router-owned message IDs.

`shell_journal_finish.go` binds translated shell invocations to call-scoped publisher capabilities
and their immutable request question. The carrier transports only the private capability, while
thread-bound discovery still locates the publisher. A finish uses the journal store's existing
receipt transaction, atomically with any final mutations; no thread-wide completion marker is
kept. Request preparation checks the originating call and turn against validated replay and the
matching successful host terminal, following visible host continuation handles without executing
them. Later input, unrelated calls, pending work, failure, and cancellation prevent completion.
Receipts survive restart but are neither inherited by forks nor applicable to another turn.

`journal_question_source.go` selects answer sources from the reconstructed request view.
Actual user messages and plaintext native `NEW_TASK` payloads addressed to the current child
are eligible in history order. Native sender and recipient fields must agree with the task
header. Encrypted assignments block stale-source fallback; messages and completion notices
do not become questions. No thread-wide source cache or decryption is introduced.

`journal_catalog.go`, `wrap.go`, and model-instruction rewriting disable the stock Tasks surface
only in Mekugi mode. Passthrough does not use the store, catalog rewrite, or journal tool.
