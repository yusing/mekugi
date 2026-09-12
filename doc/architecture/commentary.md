# Router-owned commentary projection

## CTR-COMMENTARY-001 — Router-owned operation and subagent commentary projection

The Responses router owns optional commentary schema projection for extensible ordinary function
tools, authored commentary for eligible structured calls, removal of only its own argument, assistant
message rendering, and exact replay restoration. Provider-owned and strict schemas remain exact, except for the separately owned opt-in
[third-party collaboration projection](subagents.md).
Collaboration calls remain outside operation commentary and pass through without generated
request notices or commentary-specific buffering. The Mekugi durable replay store retains
original call identity and exact router message provenance under `CTR-BOUNDARY-001`; JSON and
SSE transformers share that owner. Request-local history views restore only visible calls.

The router also owns one bounded authenticated in-process publication broker. Code Mode lowering
uses the JavaScript syntax owner when available and routes the evaluated expression through the
existing shell worker carrier. The CGO-disabled detector only fails closed for the reserved awaited
form. The Bash/POSIX evaluator intercepts the reserved command after ordinary expansion and turns
it into a successful no-output command. Both runtime paths use opaque capabilities and the
same broker; they do not own executor results, shell process status, or Codex session control.
Code Mode retains per-call capabilities. Shell uses a shared thread capability discovered through
private runtime data keyed by inherited `CODEX_THREAD_ID`, with no added command flags or inline
environment assignments. The runtime owner binds discovery to the current worker and owns its
private descriptor cleanup. Discovery and publication failures are silent and auxiliary.
Shell publications retain thread identity, not an inferred original call ID. Deferred and terminal
drains select the originating shell thread as well as the routing session, so a different thread
sharing that session cannot consume its publications. Shell drains use the broker's atomic
consume operation even during concurrent requests; the session-wide concurrency guard applies
only to deferred Code Mode delivery, whose routes remain session-scoped. Shell worker
completion cannot retire a shared thread route; idle expiry and router shutdown own that lifetime.
Rendering admission for a claimed shell publication checks its retained ID against the response's
stable thread, so a concurrent session remap cannot invalidate an already-drained publication.
Live shell replay provenance follows stable thread identity rather than the current routing
session and has a separate bounded budget. Before emitting router-authored commentary, the response
boundary persists exact message provenance in the workspace replay store. Resume and forks strip
only known IDs; message prefixes, phases, and text alone do not authorize removal. Persistence
failure suppresses the auxiliary message, not substantive output. Commentary retention cannot reclaim tool-call history
or prevent tool-call admission. Child terminals prepend ready runtime commentary inside the terminal
response object without emitting standalone completed assistant items after the child's answer.
The response transformer owns each Code Mode subscription until its carrier/history handoff boundary;
thereafter publisher completion and broker expiry own its lifetime. Transform release cancels only
unhanded subscriptions. Publications ready at every stream terminal status are drained before the
terminal event. Deferred shell publications drain on the next originating-thread request without
waiting for other active requests; deferred Code Mode publications require the next non-concurrent
request for the retained session. Token and
session drains share one completion-sensitive primitive: consume queued events once, retain active
publishers, and retire completed Code Mode routes after delivery. Both drain boundaries expire stale routes
and release their queued-event accounting. Limits and publication failures are auxiliary.

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

The terminal response transformer also owns one user-only commentary projection of the provider's
input, cached-input, uncached-input, output, and reasoning usage when a completed root or subagent response
contains a final assistant answer and no client-dispatched tool calls. `token_cost.go` owns the
built-in reference prices, per-response estimates, and the compact Markdown token/cost table.
Intermediate commentary and failed or
incomplete responses do not trigger usage commentary. Final-answer phase identifies the answer;
unphased assistant answers support older clients. Counts from the shared terminal-payload parse
accumulate by stable originating thread, independently of routing-session and compaction lifetimes.
Root and child totals remain separate, and repeated terminal observations within a request count once.
After successful response transformation and usage-message provenance retention, the transformer
also feeds each child usage report into the existing activity collector. Its usage-message ID
is the source identity; reports remain distinct notices rather than coalesced operations.
The collector owns attributed, bounded root delivery and replay removal. The child's final
answer and native completion notification remain unchanged.
`thread_usage.go` owns bounded, non-evicting token and cost totals until router shutdown;
ancestry and author metadata do not own attribution. Each observation retains its effective
provider-request model and requested service tier. The shared terminal parse supplies the
provider's resolved tier when present, and cost is calculated before accumulation using the
response's service tier, input size, and optional cache-write count. WebSocket histories retain
the effective model and reasoning sent for each response so automatic steering successors do
not adopt a Mentor model switch that was never sent upstream.
Unknown prices or inconsistent raw usage categories make the cumulative cost unavailable
without suppressing token counts. Missing or malformed usage instead leaves an irrecoverable
gap in that thread's router-lifetime totals and suppresses its reports. The server finishes each
forwarded inference observation, distinguishing definite HTTP rejection and non-generating prewarm
from missing evidence after possible inference. The shared parse preserves completeness and
inconsistency separately from normalized counts, leaving capture-owned counters unchanged.
Pricing does not read rollouts, fetch a catalog, or change capture-owned metric calculations.
The projection precedes the provider-authored final answer so it cannot replace a collaboration result. The
streaming path buffers final-answer events in `final_answer_stream.go`, while tools and progress
continue streaming. Completed streamed items determine eligibility independently of the terminal
output snapshot. At successful completion, standalone usage precedes the unchanged buffered answer
and terminal, because Codex selects the last completed assistant item as the child result.
Missing usage and failed completion release the answer without a notice. The transport drains
buffered events on EOF or failure, including through composed transforms. The 64 MiB response
buffer limit disables auxiliary usage and releases output rather than rejecting a large answer.
JSON and SSE share the same Codex-compatible text-answer eligibility check. The transport renders
buffered releases with named SSE frames and one data field per payload line, including failure drains.
The provider usage object remains authoritative; the streaming terminal output is not augmented
with usage, and the projection does not participate in model-origin output accounting. It remains
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
