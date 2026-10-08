# Router-owned journals

## CTR-JOURNAL-001 — Journal ownership and delivery

The journal store owns per-thread task/note trees, ordinal paths, events, capacity, atomic mutation
transactions, relationship binding, persistence, replay receipts, and delivery
acknowledgements. New Outcomes have separate identities and do not consume task-tree
ordinals. Existing retained paths stay unchanged, including earlier numeric Outcome paths.
Delivery leases are workspace-scoped and remain held through
downstream write confirmation, preserving ancestry and deletion ordering without
blocking unrelated workspaces. State and replay transactions retain their shared
serialization and session-retention owners.
Journal mutations do not impose a lifetime thread-count limit. Journal state is durable while
its session is retained; a request's completion intent is not.
Task timing belongs to this same store. State transitions and observed host lifecycle
checkpoint accumulated active intervals without creating authored journal events.
Persisted live anchors carry a process owner; a new process discards those anchors
while retaining their last checkpoint. Forks copy stopped timing facts, not clocks.
Both the live plan strip and completed-task renderers consume this timing evidence.

Cross-agent trees are derived views under the journal/replay locks, authorized by
complete durable ancestry in one workspace. Records never contain child snapshots.
Task creation and existing-task updates share the journal owner's binding validator
and transaction; creation emits one event containing the binding, not an intermediate
unbound task. Operation-specific structured schemas and JavaScript input declarations
project through the journal tool guidance owner. Reserved view paths keep foreign
nodes distinct without changing local ordinals. Native
sinks cache immutable composed views only. Child lifecycle comes from accepted
requests and host turn observations, never provider final-answer text. A child's
publication refreshes ancestor views without mutating ancestor event logs or cursors.
Parent completion validates mounted host lifecycles, not child-authored task states.
An owned dropped assignment releases failed or interrupted mounts from that gate;
live and unresolved mounts retain it. This disposition does not change child lifecycle
evidence or terminate a host process.
The child result exposes unchanged open tasks alongside its event delta; only the
child can change those states, and the parent retains the integration decision.

Journal transactions checkpoint the change owner's sequence and durable capture
order when they append events. These counters are separate from journal sequence;
timestamps are never compared across owners to infer coverage. Failed command
observations share the capture-order owner and retain bounded host output through
the managed read store. Summaries read journal, change and failure evidence under
one replay lock, scoped by durable thread/workspace identity. The same renderer
serves v2 hook recovery and compaction synthesis; summary text is not usage evidence.

The request executor selects journal context reset after protocol validation,
before provider preparation. `auto` is the default for manual and context-full
requests, including children; unavailable identity, evidence, storage or rendering
stops the request before provider preparation. `off` restores provider compaction,
and `slice` retains provider handling outside armed slice resets and on reset
failure. Local responses reuse terminal delivery and cancellation,
not tool execution or a second request path. The replay store retains the latest
synthesis identity per workspace/thread, before response publication. Session
leases protect evidence through delivery. The post-compaction hook compares that
identity against `compaction_response_id` in the host's latest compacted rollout
record; inability to prove a match leaves recovery enabled. Capturer owns the
sanitized routing/count metrics and explicitly represents the absent provider
attempt, rather than interpreting synthetic envelope usage as provider usage.
For native compaction's missing workspace metadata, the session ownership catalog
proves the unique prior selected workspace from exact thread-owned records. This
lookup remains valid after restart and rejects multiple historical workspaces.

Native Responses compaction V2 uses the same local interception and recovery
owners. Its router-owned compaction item restores the exact retained summary
before provider preparation, with durable scope authorization. Real provider
compaction items retain their native meaning. This introduces no separate route
or provider compaction request.

The proxy owns the latest per-thread host context-usage snapshot and model window,
not cumulative usage. At 70% or more it
adds a split-slice reminder to the next request's journal tool guidance. Successful
host context-compaction completion clears it; native host-selected context facts
restore it on resume. This observation neither dispatches a reset nor changes
journal continuation intent.

Continuation state belongs to the journal record: turn-start sequence, handled
turn ID and one intent bound to the completed turn. The intent distinguishes
ordinary unfinished-work continuation from a slice boundary; ordinary continuation
never arms compaction. User stops retain the handled turn and per-task stopped-turn
identities before interrupt RPCs. Those pauses copy with forked facts; explicit
later-turn working mutations release the selected task subtree atomically.
Intent transitions persist before dependent app-server RPCs.
A manual standalone compaction consumes only
an armed intent in the requesting thread/workspace, recording its response ID.
Fork initialization does not copy turn checkpoints or intents.

The shared app-server reset driver uses that snapshot for the
[subslice reset threshold](../spec/journal.md#journal-continuation), including
countdown presentation and dispatch. Missing evidence keeps the reset behavior;
the threshold does not change top-level transitions or host-selected compaction.
The driver owns countdown cancellation,
compaction acknowledgement and matching host turn
completion, then `turn/start`. Terminal presentation does not implement a second
policy. A continuation's reserved client-message ID identifies its transcript row;
its acknowledged host turn ID gates pending input. Dispatched intents never replay
RPCs after restart; uncertain outcomes require manual continuation.

The headless adapter uses the same driver with zero countdown delay. Its stdout
is one JSON object per line: host `appserver.Message` objects plus
`mekugi/journal/reset` notifications (`threadId`, `phase`), advisory
`mekugi/journal/notice` notifications (`threadId`, `message`), and terminal
`mekugi/headless/completed` (`threadId`). Empty reset phase means the driver is
idle, not a task state. Content is not sanitized. Errors use stderr and nonzero
exit; successful task completion does not mask a later host-shutdown error.

Journal counters persist with the owning thread without changing task or timer
fields, separate from event and delivery cursors. Mutation counters share
accepted-batch receipt deduplication. Public
read carriers and provider-final observation add measurements without work events;
a bounded window of recent read-call and final-response identities deduplicates
repeated observation without joining the permanent call receipts. Metrics snapshot these
content-free counters at request preparation, mutation acceptance and answer observation, including
runtime helper mutations completed between requests. Counter-only persistence
failure is advisory and cannot replace a successful tool result or final answer.

Retained dedicated journal calls remain replayable. Current journal helper
mutations use a call-scoped authenticated publisher through a stock executable
frontend; that publisher cannot complete a turn or acquire Codex execution
authority. Its finish marker retains a receipt scoped to the originating host
call and turn. The request pipeline selects local terminal delivery only after
visible host results and native trace outcomes confirm completion.
The mutation transaction checks finish against the candidate journal before
saving that receipt. A pure journal-owned continuation selector is shared by
this check and Main continuation; caller identity and dispatch remain separate.
Ordinary runnable work rejects the whole batch with its task path. Completed
slice boundaries, blocked reports and user-stopped work retain their ending behavior.
A native journal array drops only that marker, because its rejection would fail the response.
This restores v1's result-driven completion boundary without its old shell executor.
Codex still owns the continuation request; no provider inference is admitted for
that local response. Its mutation rejections are structured results for the helper, while
authentication, routing and transport failures remain HTTP errors. Replay of an old result cannot finish a later turn.

Delivery snapshots and leases the originating journal before rendering. V2 delivery selects event windows from live and terminal cursors and acknowledges
only after successful downstream delivery. Main's ordinary final-answer receipt
acknowledges only the exact captured answer revision, not the work-report window.
Native presentation combines each frame's live and terminal receipt windows and
persists them in one bounded worker per sink, outside the terminal event loop.
Only the exact in-flight revisions are withheld from repeated presentation;
newer revisions remain visible, failures remain pending, and shutdown drains
the sink's worker before detaching it.
Durable capture does not transfer substantive answer presentation to the journal
renderer. Retained v1 operations preserve legacy
receipt and alias compatibility. The tree is derived inside the journal owner, not
maintained independently by transport or presentation adapters. Native child completion snapshots a revision cursor and a retained-change evaluation
cursor together. Successful downstream completion advances both; failure preserves
the delta for retry. Agent results omit echoed assignments and opaque labels, while
the durable journal retains them for reads and replay. Main does not deliver child
results again.

Root-only Responses consumers read descendant live windows through the same journal
snapshot owner and delivery lease. Each child's `root_live_seq` is independent of its
own live/result cursors; only a confirmed root-stream write advances it. Root-visible
legacy retractions retain per-item root-publication evidence across silent edits;
unrelated milestones cannot establish that evidence. Forks do not inherit it.
Root-authored publication IDs belong to the root's replay provenance. Durable workspace ancestry,
not the live activity collector, selects descendants after restart. An attached native
Main already owns mounted child presentation and does not receive these fallback
response items. Publication observes writable root responses; it cannot dispatch host
effects or wake a model to create one.

Answer association accepts actual user messages and validated plaintext native assignments
addressed to the child. Encrypted or conflicting identity cannot fall back to stale text.
The commentary boundary carries live notices, but does not own durable journal meaning.
Mekugi mode replaces the stock task surface with journals; passthrough does not.
