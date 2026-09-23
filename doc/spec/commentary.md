# Router commentary

## REQ-COMMENTARY-001 — User-only operation and subagent commentary

Agent-authored progress, runtime authoring, journal CRUD, and terminal response content
are owned by [REQ-JOURNAL-001](journal.md). This requirement owns the remaining router
notices: observed subagent activity, start notices, received envelopes, critical errors,
and token metrics. `phase: "commentary"` is a router delivery mechanism, not an authoring API.

Router-owned messages have exact retained IDs and are stripped from later provider input.
Generated-looking prefixes, phase, and text alone never prove provenance. Persistence and
capacity failures suppress auxiliary notices with a user-visible explanation, without affecting
execution or provider answers. Durable storage follows the session-retention policy in
[REQ-ROUTER-001](router.md); bounded live queues do not evict replay or recovery records. Required journal terminal delivery follows the stricter journal failure contract.

The authenticated broker, canonical ancestry collector, and thread-bound publisher discovery
remain shared infrastructure. Journal authoring reuses them; other interpreters and passthrough
do not gain a journal surface. Native collaboration schemas and provider-owned tool schemas
remain outside generic journal projection, except for the separately owned opt-in
[third-party native-agent bridge](third_party.md).

Collaboration calls add no router-authored request notices. Codex owns the native spawn,
follow-up, messaging, waiting, and interruption display; schemas, executed arguments, and
streamed call framing remain unchanged. The router never reads encrypted message arguments.

The first accepted `thread_spawn` child request adds one start notice to the root activity
collector. It shows the child's canonical path, observed model, reasoning effort, and effective
requested service tier as inline code, followed by the plaintext spawn prompt from the first
native assignment addressed to that child. Inherited user requests, follow-up tasks, and other agents'
assignments are not spawn prompts. Opaque assignments remain opaque; no prompt is inferred.
The whole notice remains subject to the auxiliary rendering budget. Omitted effort or tier is labelled "not specified", not
inferred from the parent or role. Service tier reflects the configured per-model override;
`fast` is forwarded as `priority`; both aliases display as `fast`, without claiming the provider served that tier.
The notice describes the child request, not successful provider inference. Stable child-thread
identity deduplicates retries, later turns, and routing-session changes. Unknown or conflicting
ancestry suppresses projection. Start notices use the existing bounded activity and replay
provenance; they never replace child answers or add follow-up, message, wait, or interruption notices.

Complete subagent tool calls are forwarded as user-only activity, never as
executable root calls. Native collaboration remains Codex-owned. `send_message`
and journal calls omit generic tool activity because their own commentary
handles them. Other recognized stock calls use useful operation labels such as
`Read`, `Search`, `Inspect`, `Run`, `Send input`, and `Still Running` rather than
raw transport JSON. Unknown calls keep their qualified name and full input.

Direct `exec_command` and statically recognizable Code Mode
`tools.exec_command` or `tools.write_stdin` calls share the same command display.
Transparent `text(result)` and output projections do not hide the command.
Literal `Promise.all` and `Promise.allSettled` batches display nonsuppressed
operations in source order without serializing their execution. A following
result-only `forEach` or indexed `for` that prints a JSON object of result and
index references, including a direct object spread, is transparent.
Recognition uses the JavaScript parse tree and never evaluates expressions. Dynamic
arguments, control flow, and name shadowing fall back to the original JavaScript
source. A top-level static Promise batch remains visible even when later result
presentation is unrecognized; that remainder is marked `Run JavaScript · other
code` rather than silently dropped or mistaken for a batch command.
These presentation rules do not change tool input, result, or replay payload.

A simple literal `cat`, valid `mcat` read, bounded `sed -n` print, or literal
`nl -ba FILE | sed -n RANGES` selection is labeled `Read`; literal `rg` is `Search`; simple listings are `List`; and
`inspect_file` is `Inspect`. Invalid or compound commands retain a `Run`
preview instead of claiming a simpler operation. Mixed command scripts keep
every classified operation and show unclassified neighbors as `Run` in order.
Per-command `Run` excerpts omit statement-terminating semicolons; quoted
semicolons and other executable syntax remain visible.
Literal `printf` section headings between classified reads are omitted as
display decoration; standalone or dynamic headings remain `Run` operations.
Code Mode waits show `Still Running` or `Stop` only when a visible call/result
pair establishes the same cell; missing history is `operation unavailable`.
Native `write_stdin` with characters is `Send input`.

Whole-program previews use fenced code blocks with the selected interpreter
language. Literal interpreter wrappers, including `python -c`,
`python - <<'PY'`, and `node -e`, show the actual program rather than a Bash
wrapper. This same projector supplies provisional streaming previews and final
generated commentary. It never evaluates shell expansions or implies that a
command succeeded. Source line breaks and indentation remain intact.

Stock `apply_patch` does not produce a generic `Run` preview or echo its patch
body into child activity. After the host result and workspace outcome are
recorded, authenticated successful edit receipts classify each changed path as
`Create`, `Edit`, `Delete`, or `Move` with added and removed line counts.
Classification uses the same review files as `mchanges`; it does not guess from
the command text. Paths inside the workspace display relatively; outside paths
remain absolute. Incomplete captures show unavailable counts. Failed and
unfinished patches produce no successful edit summary, and repeated receipts
are deduplicated. The live pane separately owns full provisional and completed
diff display under [REQ-CHANGES-001](changes.md).

MCP calls display their `server.tool` identity and arguments. Other native web,
image, clock, context, goal, and execution helpers use descriptive labels.
Consecutive matching actions from one child may group under one canonical
agent heading while preserving every detail. A different child, notice, or
root delivery boundary ends that group. Grouping is user-only, bounded, and
never waits for another call or shortens a substantive tool result.

The display describes an observed call, not successful execution or agent completion.
JSON output, completed SSE items, and terminal output share source-identity deduplication;
partial calls are not projected. Native child call framing and replay stay unchanged.

Journals own authored progress. If the provider nevertheless emits a completed child commentary
message, the existing activity observer may forward it to the root; this is not an authoring API. Completed assistant messages with `phase: "commentary"`
retain their original child content and identity. Root copies carry the originating agent's
canonical path as inline code, are deduplicated by source identity, and remain user-only. Final answers are
not reclassified as progress.

When a request receives an actual Codex inter-agent envelope addressed to its
canonical agent name, commentary shows `[sender -> recipient]`, with each name wrapped in inline code.
The recipient is `/root` for non-child turns and the canonical child name for child turns.
An absent or malformed child identity never matches an unaddressed envelope. Valid
plaintext `MESSAGE` payloads are shown in full under `Message received`, never as excerpts.
A plaintext `FINAL_ANSWER` produces no router commentary: Codex already displays completion.
It only sets the sender's final-answer marker in the agents pane.
Native completion remains available to the parent;
descendant journal content is delivered by native child completion and is not repeated at main completion.
Messages exceeding the auxiliary rendering budget are omitted from commentary without
changing the original envelope. Encrypted envelopes show receipt
and direction only, including native Codex envelopes containing a plaintext routing
header followed by an opaque encrypted-content part. Malformed items produce no projection. Original envelopes
and substantive answers remain intact in model-visible history.

Received-reply projection considers only envelopes after the latest user message or
assistant output in the request. Earlier envelopes remain model-visible history, not
new activity, including after resume or full-history cache rebasing. Previously missed
historical replies are not flushed into a later turn.

Router-authored subagent commentary uses deterministic router-owned message IDs. The router removes
those messages from later provider-bound input while preserving the original collaboration calls,
tool outputs, and inter-agent messages. A response already accompanied by its deterministic
commentary is not projected again.

A successful explicit main finish includes one token notice before its journal flush and
before the terminal event, including when the journal is empty. Unavailable usage is reported as `n/a`
with an incomplete-usage explanation rather than silently omitting the notice. Child completion never emits a token table.
The `Tokens for this session` notice contains one wide Markdown table with one row per agent
and a `Total` row. Columns are `Agent`, `Role`, `Model`, `Input (cache hit)`, `Cache write`,
`Output`, `Reasoning`, `Input cost (cached + uncached)`, `Output cost`, `Total cost`, and
`Missing usage`. Missing usage counts forwarded responses without usable terminal usage, not
missing tokens. Affected agent rows and the total are labeled `partial`; their numbers and
cache-hit percentages cover only observed usage. The report states that missing usage is excluded.
Counts use compact decimal units, such as `149K`, `1.4M`, and `1.2B`. Input includes its
cache-hit percentage; the total percentage is weighted by input tokens, not averaged
across agents. Input cost displays `$a+$b=$c`, with cached cost first and uncached cost
second. Costs are in USD. Child roles come from explicit native spawn arguments matched to
successful spawn results and the child's proven parent and canonical identity. Retained role
evidence survives router restart and does not depend on the parent remaining live. Missing or
conflicting role evidence is `n/a`, never inferred from the agent's name. Ordinary forks
must not reuse inherited spawn evidence to assign roles to their own children.
Model labels append `fast` for effective `fast` or `priority` usage, such as
`gpt-5.6-sol fast`; a provider-reported downgrade to `default` has no suffix.
Model or tier switches retain the distinct observed labels and original per-response pricing.
Intermediate client-tool responses and failed or incomplete responses do not report tokens.
Eligibility and the child summary are defined by [REQ-JOURNAL-001](journal.md).

JSON and streaming responses use the same cumulative provider-authoritative per-thread
counts. Main combines only proven descendants in its selected workspace with its own row;
unrelated threads and ordinary forks' source trees are excluded. Missing child usage or
unavailable tree evidence must not produce an apparently complete aggregate. A missing main
usage total likewise leaves its row and the aggregate unavailable while retaining known child rows.
Intermediate responses contribute without notices. Thread accounting remains separate;
compaction and routing-session changes do not reset totals. Repeated terminal observations
within one request count once. Totals remain in memory until router shutdown without a
lifetime thread-count ceiling. Arithmetic overflow makes the affected total unavailable.
An accepted or transport-interrupted request without usable terminal usage increments that
thread's missing-usage count once. Missing or null input, cached-input, output, or reasoning
counts likewise exclude that response from observed totals. Earlier totals, known model labels,
and later usable usage MUST remain available; a later success MUST NOT erase the gap.
A thread with only missing responses has zero observed usage, explicitly labeled partial,
not a claim that those responses consumed zero tokens. Definite HTTP rejections, requests
rejected before forwarding, and non-generating WebSocket prewarm do not create usage gaps.
Failed and incomplete terminal responses with complete usage still contribute
to later totals without producing their own notices.

Cost estimates use built-in reference list API prices, not subscription
rates or live billing quotes. No pricing fetch or terminal renderer is required: Codex renders
the Markdown tables. Each response is priced using its effective provider-request model, service
tier, and input size before accumulation, so model switches and long-context rates do not reprice
earlier responses. OpenAI long-context rates begin above 272,000 input tokens, not at that exact count. Grok 4.6 long-context rates begin at 200,000 input tokens.
The terminal provider `service_tier` takes precedence over the request, including a downgrade
from `priority` or `fast` to `default`. These two Fast aliases share model-specific reference
rates; a blanket multiplier MUST NOT be applied to every model. When the response omits the tier,
the explicitly requested tier supplies the reference estimate; omission at both boundaries uses
the standard reference estimate. Unresolved `auto`, malformed or null tier evidence, and
unsupported model/tier/context combinations have unavailable cost, not guessed standard pricing.
Rates follow the [official pricing tables](https://developers.openai.com/api/docs/pricing),
[model pricing notes](https://developers.openai.com/api/docs/models/gpt-6-astra), and the
[xAI model prices](https://docs.x.ai/developers/pricing); prefixed and unprefixed Grok IDs
share each model's table. Grok Build Fast uses its own published rates. Grok has no
service-tier Fast or priority reference rates, and unpublished
cache-write rates remain unavailable rather than inferred. Reference estimates
are not proof of the billed processing mode when the provider omits it.

Cached input is subtracted from ordinary input; reasoning is included in output and MUST NOT be
charged again. Optional `cache_write_tokens` are part of uncached input, not additional input
tokens. For models with published cache-write rates, their premium is included in the uncached
input cost cell. An omitted cache-write field is zero for older providers; explicit null,
invalid, or contradictory evidence is not known zero. Unknown model/service-tier pricing
or inconsistent usage makes the affected row's cost cells `n/a` and its aggregate cost
unavailable, without hiding known token counts or presenting a partial cost as complete.
The report explains overlapping token categories, reference pricing, the router-lifetime
boundary, and unavailable estimates. Root accounting is not mutated when rendering the
tree total. This is auxiliary commentary accounting, not a change to capture-owned metrics
exports. Generated tables retain the existing exact replay filtering and auxiliary budget.

A successful explicit main journal finish emits usage, then its own unflushed journal revisions (including
live-reported updates), and the terminal event. The final journal message is emitted once, after token
metrics, and remains last in both streamed messages and the terminal snapshot.
Child finish emits its native journal result without a token table.
It does not request a separately generated provider final answer. Provider answer events remain
unfiltered and cannot trigger journal completion. Failed or incomplete responses release buffered
output without terminal journal flush or usage notices.
The streaming transport preserves named SSE framing and one data field per payload line.
Ordinary token-usage buffering remains bounded at 64 MiB and releases provider output unchanged
when that bound is exceeded. Token arithmetic and provider usage objects remain unchanged.
Usage is never a child terminal's substantive result.

Child operation and runtime commentary carries a ``[`/root/worker`] `` prefix from the request’s
canonical `agent_name` when `subagent_kind` identifies a child. Root and older unnamed clients
retain unprefixed commentary. An identical existing prefix is not duplicated. Runtime capabilities
bind their author at creation; thread provenance retains that author across route expiry and
session remapping, and deferred publications never borrow the draining request’s identity.
Runtime author admission and rendered publications share the 16 KiB auxiliary byte budget.
An oversized author suppresses capability creation; oversized rendered text is not retained,
while completion handling and substantive tool execution remain unchanged. This local budget
does not restrict valid Codex names or reject requests.
Mekugi retains observed canonical names and parent-thread relationships for bounded
root projection. It never infers ancestry from a name, message payload, or shared
routing-session ID. Missing ancestry, cycles, conflicting identity, or exhausted
auxiliary capacity suppress projection, not child output or tool execution.
Identity observation and start/reply collection begin only after request preparation succeeds.
A prepared request with malformed auxiliary identity or a contradictory thread ID disables
root projection for that stable thread until shutdown. Runtime publications use thread-bound capabilities, so later valid metadata
cannot distinguish delayed work from the ambiguous request. Local runtime
delivery, immutable authors, and replay provenance remain intact. Invalid turn headers and requests
rejected during preparation do not register or invalidate collector identities.
Critical errors from rejected requests still use previously established request-thread identity.

Child-authored commentary, operation, and Code Mode progress enters the same collector as
received inter-agent envelopes, tool-call displays, and existing critical-error notices.
Errors are collected from the originating request before session-level deduplication,
never attributed from another request's retained session queue. Projecting an error
does not acknowledge the original session notice or
change its failure semantics. Each child keeps
its latest ordinary activity plus distinct authored commentary and notices, ordered by observation within
that child. Deduplication uses originating thread and source event identity, not
shared text. This is observed activity, not an inferred task objective or lifecycle
state; provider completion is not agent completion.

Root SSE transforms offer ready activity at response-event boundaries, including
before the terminal. JSON responses offer ready activity before substantive output.
Events observed before the current response use the same formatting without an additional
update heading. No production response is held open, and no polling or model
turn is created. During an idle stream there may be no event boundary to deliver
through; once a response closes, inline updates wait for the next eligible root response.

When an interactive Herdr pane is available, the first child event under a root opens
one Mekugi agents pane for that root and moves that root's child activity there,
including replies addressed to `/root`. The pane receives events as they are observed,
independent of root responses, so updates continue during a native wait. While the pane
owns a root, its root responses carry no child activity copies, only one notice that
activity moved and, once delivery returns inline, one notice that the pane closed. An
event leaves the queue only after the pane's write is flushed. A viewer that
disconnects keeps ownership for 5 seconds and reconnects without duplicates. A pane
that does not attach within 15 seconds, closes, or stays disconnected past that grace
returns its pending events and later activity to inline delivery, and is not
relaunched for the router session. Without Herdr, inline delivery is unchanged. Only
the first root with child activity uses the pane; other roots stay inline. Critical
session notices and main-completion usage stay in the root conversation.

The pane renders child activity natively rather than as commentary Markdown. It
parses the router's own commentary grammar into operations, messages, start
notices, and errors, and shows each with verb colors, path emphasis, and syntax
highlighting in the terminal's theme. Text it does not recognize stays plain.
Consecutive reads by one agent collapse into one row that joins ranges of the
same file. Child text is sanitized before layout, so it cannot emit terminal
controls.

The pane always shows the agents, as a canonical-path tree in observation order,
with each agent's current activity and age. A Code Mode batch shows its latest
operation and the count of the others. The layout follows the pane size. At 100
columns or wider, agent cards sit beside the feed. Narrower panes stack one row per
agent above the feed, and panes with few rows show a one-line strip. The feed groups
consecutive entries by agent under a colored heading. In the shared view it clips
long entries, and its only mode shows one agent in full. Roster markers are
observed facts only:
`◐` an open provider response, `!` a latest error event, `✓` a plaintext
`FINAL_ANSWER` sent, and `·` otherwise. No marker claims that an agent finished.
Agent colors derive from the canonical path, so the live diff pane uses the same
color for a caller.

The collector retains thread and source identities until shutdown without lifetime count ceilings.
It bounds live queues to 1,024 pending events and 64 pending events per child. Queue exhaustion
reports a user-visible notice; it does not disable later updates once the queue drains.
Pending events expire after one hour. Live root copies share a 16 KiB budget per response, after labels
are added. Main journal terminal delivery uses the separate capacity-sized budget in
[REQ-JOURNAL-001](journal.md); a deferred live notice does not block its own terminal flush. Live root-copy IDs remain bound to stable root thread identity until
shutdown, across session remapping and event expiry; emitted message provenance also survives
shutdown in the workspace-scoped replay store. Live queue capacity never
evicts executable-call history or existing replay provenance. Root copies are
removed by exact retained ID from every replay, including a first child request
with inherited root history, without removing original child messages or tool results.

Acceptance:

1. Actual child-authored commentary reaches the root with the originating agent's identity, without
   changing the original child message, substantive result, or model-visible history.
2. Collaboration calls retain exact schemas, arguments, and streaming framing without
   commentary-specific buffering. The first accepted child request produces one root start notice
   with observed model, effort, and service tier; later requests do not repeat it, and other lifecycle events
   produce no added notices.

3. Received inter-agent envelopes identify both parties, including siblings and nested children. Plaintext replies are shown in full or omitted when they exceed the auxiliary rendering budget; encrypted content remains opaque and original model-visible items stay exact.
4. JSON and streaming responses expose equivalent attributed child commentary. Repeated completed
   items and terminal output do not duplicate root copies.
5. Router-authored messages are removed from every later provider request and are not repeated when
   the matching message is already present in Codex history.
6. Eligible main completion reports one row per proven agent plus a total, with compact
   counts, cache-hit percentages, and split input cost. Child completion emits no token table.
   Costs use per-response models and context tiers, do not double-charge cached input or reasoning,
   remain cumulative across compaction, and show `n/a` for unavailable evidence.
   Intermediate client calls, failures, and incomplete responses do not emit token notices.
   Child results are delivered natively and are not flushed again at main completion.
7. Journal authoring, admission, replay, runtime publishing, and terminal acceptance belong to
   [REQ-JOURNAL-001](journal.md). Automatic notices remain distinguishable from authored journal
   mutations in [feature evidence](router.md#feature-usage-debug-evidence).

### Critical session errors

Router failures that block work or require action produce bounded, actionable
user-only notices, not raw request data or event logs. Success and ordinary
cancellation are silent. Deduplication is by routing session and failure category.
Every otherwise-generic failure notice identifies its request phase and includes
an opaque per-process diagnostic reference. Producers may also provide a bounded
safe cause. Distinct causes remain separate; repeats of the same cause retain the
existing repeat count. Unclassified errors retain only their phase and reference
because arbitrary error text can contain prompts, scripts, headers, paths, or
credentials. Provider-controlled values are not safe merely because they resemble
protocol identifiers.
Notices without a writable response remain queued for that session. A failed
render/write does not consume them. Ready root streaming notices precede provider
output; child notices appear before substantive output only in the terminal
response object, never as a later standalone child result. Exact retained IDs are
removed from subsequent provider-bound input, including passthrough requests.
In `mekugi` mode, emitted notice IDs also enter the workspace-scoped durable commentary
store so resume and forks remove them without a live queue or matching routing session.
If that auxiliary retention fails, the notice stays pending and substantive output is
unchanged. Compaction without a usable canonical workspace also leaves notices pending.
Passthrough keeps its existing in-process notice behavior.

The queue retains at most 256 session/category entries until shutdown. Concurrent
responses cannot claim the same pending notice. Repeats after delivery are
summarized only at shutdown; excess distinct entries become one overflow count.
The launcher reports pending notices and repeat counts after Codex exits. Delivery
remains auxiliary: HTTP failures, tool errors, exit codes, and substantive results
are preserved. A queue cannot deliver through an absent or broken transport.
