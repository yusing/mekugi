# Router-owned journals

## CTR-JOURNAL-001 — Journal ownership and delivery

The journal store owns per-thread entries, IDs, revisions, capacity, atomic mutation
transactions, relationship binding, persistence, replay receipts, and delivery
acknowledgements. It shares durable workspace coordination and session retention with replay.
Journal mutations do not impose a lifetime thread-count limit. Journal state is durable while
its session is retained; a request's completion intent is not.

The router intercepts the journal tool and returns its result through the current response
flow rather than a host executor. A valid direct finish can select terminal journal delivery
when no client-dispatched work remains. Shell-origin finish uses a call-scoped capability
and durable receipt tied to the originating turn and successful host terminal; it cannot
complete a later, unrelated, failed, or still-pending turn.

Delivery snapshots and leases the complete selected tree before rendering. Live delivery
marks a revision reported; terminal delivery separately marks it flushed, and edits reset
both for the new revision. Main terminal delivery includes accepted descendants in stable
agent-path order, groups answers by their exact source question, and acknowledges only
after successful downstream delivery. Child completion can expose current journal text
without consuming main's delivery state.

Answer association accepts actual user messages and validated plaintext native assignments
addressed to the child. Encrypted or conflicting identity cannot fall back to stale text.
The commentary boundary carries live notices, but does not own durable journal meaning.
Mekugi mode replaces the stock task surface with journals; passthrough does not.
