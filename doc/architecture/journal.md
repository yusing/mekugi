# Router-owned journals

## CTR-JOURNAL-001 — Journal ownership and delivery

The journal store owns per-thread task/note trees, ordinal paths, events, capacity, atomic mutation
transactions, relationship binding, persistence, replay receipts, and delivery
acknowledgements. It shares durable workspace coordination and session retention with replay.
Journal mutations do not impose a lifetime thread-count limit. Journal state is durable while
its session is retained; a request's completion intent is not.

Cross-agent trees are derived views under the journal/replay locks, authorized by
complete durable ancestry in one workspace. Records never contain child snapshots.
Typed binding operations associate a parent's task with a direct child; reserved
view paths keep foreign nodes distinct without changing local ordinals. Native
sinks cache immutable composed views only. Child lifecycle comes from accepted
requests and host turn observations, never provider final-answer text. A child's
publication refreshes ancestor views without mutating ancestor event logs or cursors.

Journal transactions checkpoint the change owner's sequence and durable capture
order when they append events. These counters are separate from journal sequence;
timestamps are never compared across owners to infer coverage. Failed command
observations share the capture-order owner and retain bounded host output through
the managed read store. Summaries read journal, change and failure evidence under
one replay lock, scoped by durable thread/workspace identity. The same renderer
serves v2 hook recovery and compaction synthesis; summary text is not usage evidence.

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
