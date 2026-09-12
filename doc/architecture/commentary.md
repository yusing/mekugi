# Router-owned commentary projection

## CTR-COMMENTARY-001 — Router-owned operation and subagent commentary projection

[CTR-JOURNAL-001](journal.md) owns progress authoring, journal state, replay restoration of
router-owned journal calls, and terminal journal delivery. This contract retains automatic
notice rendering, token arithmetic, exact message provenance, and observed subagent activity.

The Responses router owns the shared bounded authenticated publication broker. Runtime
publishers reuse private thread discovery through inherited `CODEX_THREAD_ID`; the shell
runtime owns descriptor cleanup. Journal operations do not own executor results, shell process
status, or Codex session control. Handed-off Code Mode capabilities survive transform release
until completion or expiry, and finishing one shell worker does not cancel its shared thread route.

Before emission the response boundary retains exact router-owned IDs in the workspace replay
store. Resume and forks strip known IDs only. Auxiliary notice retention cannot reclaim executable
history or prevent unrelated calls. Required journal terminal retention failures are errors,
not silent suppression of the substantive result.

Child operation and runtime commentary carries a `[/root/worker] ` prefix from the request’s
canonical `agent_name` when `subagent_kind` identifies a child. Root and older unnamed clients
retain unprefixed commentary. An identical existing prefix is not duplicated. Runtime capabilities
bind their author at creation; thread provenance retains that author across route expiry and
session remapping, and deferred publications never borrow the draining request’s identity.
Runtime author admission and rendered publications share the 16 KiB auxiliary byte budget.
An oversized author suppresses capability creation; oversized rendered text is not retained,
while completion handling and substantive tool execution remain unchanged. This local budget
does not restrict valid Codex names or reject requests.
`subagent_activity.go` owns bounded observation and root-copy provenance, independent
of executable-call recovery. The request boundary supplies only observed canonical
identity and parent-thread metadata. Stable thread relationships, never session IDs
or path-looking message text, select the root. Only successful request preparation commits
identity observations and collected starts/replies. Preparation-failure critical errors still
use previously established request-thread identity. Conflicting identities fail closed. Accepted
malformed auxiliary identity or contradictory thread metadata retains a bounded conflicted
thread marker, suppressing root copies from shared runtime capabilities until shutdown without
changing their local authors or replay provenance.
The commentary producer and publication broker feed the collector without consuming
child output. Runtime capabilities bind their originating thread at creation.
The collector does not call back into the broker or proxy while holding its lock.

The collector coalesces ordinary child operations and retains distinct completed child-authored
commentary, received replies, and critical-error notices. It deduplicates source identities per
thread and expires pending
events. Non-evicting thread/source and exact root-copy provenance budgets prevent
replay leakage after session remapping or expiry without displacing tool history.
Exact retained root-copy IDs are stripped from any provider replay, including
new child requests inheriting root history before their ancestry is registered.
Error collection observes the originating request's safe description at record
time, before session deduplication, without acknowledging the original notice.
The root transformer drains atomically at JSON and SSE event boundaries under a
per-response rendered byte budget. Root copies precede substantive output; idle or
closed streams defer delivery rather than extending stream lifetime. Concurrent
root responses cannot drain the same event twice. Codex retains scheduling,
recipient selection, interruption, waiting, and assignment lifecycle ownership.

The accepted child-request boundary also contributes a start notice using that request's model
and reasoning effort, never the parent's requested spawn override or inferred role defaults.
The existing collector deduplicates the fixed start source per child thread, retaining attribution,
bounded admission, deferred root delivery, and exact replay filtering without another lifecycle
store. Start notices do not modify child output or synthesize other lifecycle events.

Codex owns collaboration-call display. The router leaves collaboration schemas, executable
arguments, and streaming call framing exact and never reads encrypted message arguments.
Completed child assistant commentary enters the existing collector at JSON output and SSE
completed-item boundaries. The original child item remains unchanged; root copies use observed
ancestry and source identity without replacing final answers or entering provider replay.

The input boundary recognizes actual inter-agent envelopes addressed to the current canonical
agent, including sibling and nested traffic. Plaintext replies are shown in full, never excerpted;
replies exceeding the auxiliary rendering budget are omitted. Encrypted receipt is direction-only, including
native two-part envelopes with a plaintext routing header and opaque encrypted content.
Original model-visible envelopes remain unchanged. Only envelopes following the latest user
message or assistant output are eligible for receipt projection; full-history replay cannot
turn earlier, previously undisplayed replies into fresh activity. Deterministic router IDs suppress repeated
local commentary on replay.

`thread_usage.go` owns bounded, non-evicting cumulative provider-authoritative token and cost
totals by stable thread until router shutdown, separately for root and children. Routing remaps
and compaction do not reset them; repeated terminal observations within a request count once.
Ancestry and author labels do not determine attribution. `token_cost.go` owns built-in reference
prices, per-response estimates, and the compact Markdown token/cost table.

Each observation retains its effective provider-request model and requested service tier.
The shared terminal parse supplies the provider's resolved tier when present, and cost is
calculated before accumulation using the response's service tier, input size, and optional
cache-write count. WebSocket histories retain the effective model and reasoning sent for each
response so automatic steering successors do not adopt a Mentor model switch that was never
sent upstream. Unknown prices or inconsistent raw usage categories make cumulative costs
unavailable without suppressing token counts. Missing or malformed usage instead leaves an
irrecoverable gap in that thread's router-lifetime totals and suppresses its reports.
The server finishes each forwarded inference observation, distinguishing definite HTTP rejection
and non-generating prewarm from missing evidence after possible inference. The shared parse
preserves completeness and inconsistency separately from normalized counts. Pricing does not
read rollouts, fetch catalogs, or change capture-owned metric calculations.

The terminal transformer places eligible usage after any main journal flush and before the child
saved-summary. Child usage remains live activity; child journals flush only at main completion.
After successful delivery, child usage reports enter the activity collector in that order.
Their usage-message IDs are source identities; they remain distinct notices with attributed,
bounded root delivery and exact replay removal. Child costs never enter root usage totals.

`final_answer_stream.go` buffers provider final events only for token-usage ordering, releasing
them unchanged at the terminal, on failure, or when its buffer fills. Completed streamed items
determine eligibility independently of the terminal output snapshot. Journal completion uses
an explicit finish call and never filters provider messages. Failed and incomplete responses
do not terminal-flush or emit tokens. The transport drains buffered events on EOF or failure,
including through composed transforms. The 64 MiB response buffer limit disables auxiliary usage
and releases output rather than rejecting a large answer. JSON and SSE share the same
Codex-compatible text-answer eligibility check. Buffered releases use named SSE frames and one
data field per payload line, including failure drains. Token notices do not participate in
model-origin output accounting; provider usage remains authoritative. They remain
present in transport byte and token totals. `internal/commentaryid` owns the reserved operation/runtime and subagent/usage
message ID namespaces shared by rendering, replay, and capture classification; message text and
phase do not establish generated provenance.

The router owns a separate bounded critical-notice queue because request failures
may occur before tool-call history or publication capabilities exist. The launcher
owns that queue's lifetime through router shutdown and terminal fallback. The
response transformer reserves notices by routing session, confirms only successful
writes, and strips exact generated IDs on replay. It never changes provider or
executor failure semantics. Operational log sinks are not part of this boundary.
Request-path producers attach bounded, display-safe cause metadata to errors whose
dynamic values they understand. The queue uses that metadata for notice text and
cause-level deduplication. Every otherwise-generic failure identifies its phase.
For all unclassified errors the queue derives an opaque reference with a
process-random key and does not retain or render the original error text. A
provider-controlled value requires explicit semantic recognition before it can be
included in a safe cause; lexical validation alone is insufficient.
In `mekugi` mode, the transport's notice transform retains exact message provenance through
the same workspace replay store before delivery, even though it runs after the tool transform.
Failed provenance retention leaves notices pending without replacing substantive output.
Passthrough does not acquire a durable replay store.
