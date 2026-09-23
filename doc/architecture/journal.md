# Router-owned journals

## CTR-JOURNAL-001 — Journal ownership and delivery

The journal store owns per-thread entries, IDs, revisions, capacity, atomic mutation
transactions, relationship binding, persistence, replay receipts, and delivery
acknowledgements. It shares durable workspace coordination and session retention with replay.
Journal mutations do not impose a lifetime thread-count limit. Journal state is durable while
its session is retained; a request's completion intent is not.

The router intercepts the dedicated journal tool and returns its result through
the current response flow rather than a host executor. A valid direct finish
can select terminal delivery when no client-dispatched work remains. Code Mode
mutations use a call-scoped authenticated publisher through a stock executable
frontend; that publisher cannot complete a turn or acquire Codex execution
authority. Replay of an old result cannot finish a later turn.

Delivery snapshots and leases the originating journal before rendering. Live delivery
marks a revision reported; terminal delivery separately marks it flushed, and edits reset
both for the new revision. Main terminal delivery groups its own answers by their exact source question and acknowledges
only after successful downstream delivery. Native child completion exposes the child's current
journal text; main completion does not deliver it again.

Answer association accepts actual user messages and validated plaintext native assignments
addressed to the child. Encrypted or conflicting identity cannot fall back to stale text.
The commentary boundary carries live notices, but does not own durable journal meaning.
Mekugi mode replaces the stock task surface with journals; passthrough does not.
