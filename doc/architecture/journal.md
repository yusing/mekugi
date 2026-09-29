# Router-owned journals

## CTR-JOURNAL-001 — Journal ownership and delivery

The journal store owns per-thread task/note trees, ordinal paths, events, capacity, atomic mutation
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

Delivery snapshots and leases the originating journal before rendering. V2 delivery selects event windows from live and terminal cursors and acknowledges
only after successful downstream delivery. Retained v1 operations preserve legacy
receipt and alias compatibility. The tree is derived inside the journal owner, not
maintained independently by transport or presentation adapters. Native child completion snapshots a revision cursor and a retained-change evaluation
cursor together. Successful downstream completion advances both; failure preserves
the delta for retry. Agent results omit echoed assignments and opaque labels, while
the durable journal retains them for reads and replay. Main does not deliver child
results again.

Answer association accepts actual user messages and validated plaintext native assignments
addressed to the child. Encrypted or conflicting identity cannot fall back to stale text.
The commentary boundary carries live notices, but does not own durable journal meaning.
Mekugi mode replaces the stock task surface with journals; passthrough does not.
