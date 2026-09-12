# Router commentary

## REQ-COMMENTARY-001 — User-only operation and subagent commentary

In `mekugi` router mode, every non-strict function tool in the ordinary Responses `tools` catalog
with an object parameter schema receives one optional string property named `commentary`. A
nonblank authored value is shown as assistant commentary immediately before the call and is removed
before execution. An omitted or blank value produces no operation commentary. Strict tools,
provider-owned `additional_tools`, and tools that already own a `commentary` property keep their
schemas and arguments unchanged and receive no generic operation commentary. The opt-in [third-party subagent bridge](subagents.md) separately projects
collaboration schemas and restores native identities before commentary handling. Collaboration tools and tools whose
purpose is user messaging receive no generic operation commentary.

The central model instructions direct the agent to attach progress only through a supported tool's
commentary field or documented runtime mechanism. The agent does not originate standalone assistant
messages with `phase: "commentary"`; those messages are router-owned.

JSON and streaming responses preserve the same ordering. The streaming path buffers an eligible
function call until its complete arguments can be validated and stripped. The router retains the
provider's exact original call in durable replay history, removes only its generated
message from later input, and restores the original call before provider replay. Malformed
router-owned commentary fails before the tool call is exposed.

Code Mode may publish runtime progress with `await commentary(value)`. The JavaScript parser
replaces only the reserved awaited call while preserving ordinary strings, comments, and unrelated
identifiers. If the commentary parser cannot parse a Code Mode program, it leaves the program
unchanged for the executor to validate and report errors through the normal tool result. It never
partially rewrites a recovered parse tree or rejects the response transport for a syntax error.
Bash and POSIX shell programs may publish expanded text through the reserved
`commentary` command; the command writes nothing and succeeds without changing surrounding shell
control flow, redirections, output, or exit status. Other interpreters receive no runtime
commentary handling, and shell calls without an authored command receive no default.

Runtime publications use authenticated routes on the router's existing HTTP server.
Code Mode routes retain per-call identity. Bash and POSIX shell commentary is thread-scoped,
not attributed to an original tool-call ID. Workers discover private connection details using
the inherited `CODEX_THREAD_ID` and its thread-bound runtime. The shell transformation MUST NOT
add flags or inline environment assignments to carry those details. Concurrent shell workers
in one thread share delivery without guessing which original call produced a publication;
one worker finishing must not retire the other workers' publisher. Distinct threads sharing a
routing session cannot consume each other's shell publications, either on request preparation
or at a streaming terminal.
Ready streaming publications precede completed, failed, and incomplete terminal responses.
Deferred shell publications appear at the next request for their originating thread, even while
other requests share the routing session. Request-start and terminal drains atomically consume
each publication once, including when concurrent responses belong to the same thread.
Deferred Code Mode publications still require a non-concurrent request for the same session.
Once a carrier has been handed off, an interrupted
provider response or early transform release does not cancel its publisher. Code Mode routes
prepared for carriers that were never handed off are cancelled instead. Shell thread routes
survive individual call and response lifetimes and expire when idle or at router shutdown.
Draining ready publications consumes each message once without retiring a still-running publisher;
subsequent publications remain deliverable through the same route. Code Mode completion retires
its route after queued publications are drained. Expiry releases queued events and route capacity.
Routes, events, request bodies, and retention time are bounded. Capacity, network, publication, and
rendering failures remain auxiliary and do not replace the tool result.
A shell publication already claimed by a response remains renderable after its thread changes
routing sessions; rendering validates stable thread provenance, not the current session mapping.
Shell commentary delivery follows stable thread identity across routing-session changes. Exact
message provenance is durably recorded before emission so inherited commentary is also removed
after restart or a fork within the same selected workspace. Persistence failure suppresses that
auxiliary message without replacing a tool result. A generated-looking ID without retained
provenance is not sufficient reason to remove a message.
Its retention is independently bounded: exhausted commentary capacity suppresses new commentary,
never evicts executable-call replay or recovery records or prevents later tool calls.
The live broker retains shell provenance until shutdown, including across publication-route expiry,
for at most 256 threads and 16,384 message IDs in total. Reaching either bound leaves existing
replay provenance intact rather than reclaiming it for new commentary.
At a subagent stream terminal, ready shell commentary appears before substantive output inside
the terminal response object, not as a later standalone completed assistant item that could
replace the subagent's final answer.

Collaboration calls add no router-authored request notices. Codex owns the native spawn,
follow-up, messaging, waiting, and interruption display; schemas, executed arguments, and
streamed call framing remain unchanged. The router never reads encrypted message arguments.

The first accepted `thread_spawn` child request adds one start notice to the root activity
collector. It shows the child's canonical path, observed model, and reasoning effort as inline
code; an omitted effort is labelled "not specified", not inferred from the parent or role.
The notice describes the child request, not successful provider inference. Stable child-thread
identity deduplicates retries, later turns, and routing-session changes. Unknown or conflicting
ancestry suppresses projection. Start notices use the existing bounded activity and replay
provenance; they never replace child answers or add follow-up, message, wait, or interruption notices.

Complete subagent tool calls are also forwarded as user-only activity, never as executable
root calls. Agent `send_message` calls omit generic tool activity because messaging has its own
commentary render. Known tools use operation labels rather than raw transport arguments. Shell calls
and transparent, statically recognized Code Mode shell wrappers share a `Run` display.
An `exec` call recovered through the built-in shell pipeline uses the shell display only
after recovery is recorded, without changing its original replay identity.
Whole-script nonempty `Run` previews use fenced code blocks even for single-line commands, tagged with
the selected interpreter language: default/Bash uses `bash`, Python/Python3 uses `python`,
and Node/Bun/Deno uses `javascript`. Common executable aliases normalize to renderer language
names: PyPy/Pythonw to `python`, QuickJS to `javascript`, ts-node/tsx to `typescript`,
JRuby/TruffleRuby to `ruby`, LuaJIT to `lua`, tclsh/wish to `tcl`, Rscript to `r`,
runghc/runhaskell to `haskell`, pwsh to `powershell`, ash/dash/ksh to `bash`, and
gawk/mawk/nawk to `awk`. Numeric version suffixes on these known executable families,
Python, Ruby, Perl, PHP, Lua, and PowerShell are normalized too, such as `python3.12`
and `php8.3`. Other interpreter names pass through unchanged after path and case
normalization; unavailable or unsafe language tags use an untagged fence. Source text remains intact.
Transparent result wrappers include inline `text(await tools.exec_command(...))` and
`text(await tools.write_stdin(...))`, as well as `text(result)`, `text(result.output)`, and JSON result
projections, with the matching local binding name. Recognition uses the JavaScript parse tree,
not source-text matching, so whitespace variations do not affect it. The output-only projection displays the
decoded command, preserving its line breaks rather than showing the JavaScript wrapper.
Code Mode recognition accepts literal JavaScript objects with identifier or quoted keys and
recursively static JSON-compatible values. It never evaluates source; computed keys, spreads,
calls, references, and other dynamic expressions retain a `Run JavaScript` display with the
original source in a `javascript` fence. A literal `write_stdin` call with no characters is
shown as `Still Running` with a short excerpt of the actual command, matching native
`write_stdin` polls. Stored shell references display `Running stored script` with the
resolved command excerpt, not transport directives or reference IDs. These excerpts
use the first source line, at most 120 characters including an ellipsis when shortened.
For an explicit batch, they use the first program's body and an ellipsis for the remaining
programs, rather than exposing the batch header as the command.
Polls correlate only with visible call/result pairs that include execution metadata in the
same request; output-only Code Mode projections are not session evidence. Missing command
history or unavailable stored source is labelled `command unavailable`, never guessed.
These presentation rules do not change execution, validation, or replay payloads. Calls that send nonempty characters display `Send input`.
MCP function names in Codex's `mcp__<server>__<tool>` form and native calls with
namespace `mcp__<server>` display `MCP` with the `server.tool` identity and full
arguments. MCP resource listing, template listing, and reading, clock, context,
goal, execution-wait, web, and image-generation helpers use descriptive operation
labels with full arguments. `update_plan` retains its existing generic display.
Transparent Code Mode wrappers use the same display, including bound results,
inline awaited calls, and `generatedImage(result)` for image generation.
Static sequential calls and literal `Promise.all`/`Promise.allSettled` batches
display every operation in source order, grouped unless the batch contains patches;
patch files retain independently identified messages. Batches do not
establish shell-session result metadata. Dynamic arguments, control flow, runtime
name shadowing, unknown tools, or unrelated executable statements retain the
complete JavaScript preview, never a partially simplified subset. Rendering never
evaluates a call or claims success, and collaboration display stays Codex-owned.
Code Mode `wait` calls display `Still Running` with the originating operation or source,
or `Stop` with that operation when `terminate` is true, without transport arguments.
Cell identity comes only from a visible matched call/result pair with leading host execution
metadata; subsequent waits preserve that association and terminal results retire it.
Missing history is labelled `operation unavailable`, never inferred from another cell.
Stop describes the requested operation, not successful termination.
Simple literal `cat` and `hcat` calls display `Read <file>`. Literal bounded
`sed -n 'START,ENDp' <file>` reads display `Read <file> START:END`, with positive decimal
line numbers and an end not before the start. Only this single-file print form is classified;
other sed programs, options, stdin operands, and dynamic commands retain their source.
These reads can share a script with other classified operations without forcing a `Run` fallback.
`skills-mgr get <skill-name>` and reads of a named skill's `SKILL.md` display
`Skill Read <skill-name>`.
`skills-mgr get <skill-name>/<reference-path>` displays `Skill Reference Read` with the
full skill/reference operand. Optional read ranges remain visible for both forms.
Simple listing, search, and structural inspection commands use `List`, `Search`, and `Inspect`
labels, retaining search flags and operands. Search and listing previews preserve shell wildcard
patterns verbatim without expanding them; substitutions still retain the original `Run` source. Native web/file search, image viewing/generation,
code execution, input sending, and editing calls use descriptive operation labels.
Hcat and inspect_file previews validate literal option bounds, duplicates, and operand
placement before classification; invalid forms retain their source-level `Run` display.
A native `apply_patch` call unwraps its string or structured patch argument for display.
A successfully translated `hpatch` or `hpatch_recover` call uses the already-retained translated
patch for display. Framed patches show one commentary per file with an inline-code path and an operation
heading: `Write` for additions, `Edit` for updates, `Delete` for removals, and `Move`
for renames. Each nonempty body follows in a `diff` fence. Patch begin/end markers, file headers,
and end-of-file metadata are omitted; moves retain both source and destination paths in the
heading. Literal added, removed, and context lines remain intact. Unrecognized patch framing
retains the original source-level diff display. Display never executes or retranslates an edit.
Rejected, unavailable, and already-satisfied translations retain a truthful source-level
fallback rather than claiming a patch was applied.
Valid explicit shell batches classify each program independently, in order, using
that program's interpreter and directives. Batch headers and separator lines are
transport framing, not displayed commands. Malformed batches retain the complete
source-level fallback; marker-like lines inside ordinary programs remain source.

Mixed scripts of simple commands classify each command independently. An unclassified command
retains its source as a `Run` action without hiding neighboring `Search`, `Read`, or other
classified operations. Single-line `Run` details in these mixed summaries use inline code;
multiline details use fenced code blocks. Scripts with no classified commands retain the
whole-script `Run` preview. Unsupported compound commands retain their complete statement as a
`Run` action rather than splitting control flow into independent operations. Literal searches with
discarded stderr and pipelines of searches, bounded `head`/`tail` (`-n N` or `-N`), and output-only `sort`
(with optional `-n`, `-r`, and `-u` flags) retain the complete pipeline under `Search`. `find` actions
that execute commands, delete files, or write result files retain `Run`. Literal
`command -v` lookups, including an `|| true` guard, use `Inspect`; `ls` keeps its flags and paths
under `List`. Standalone Bash/POSIX `commentary` commands are omitted from tool previews;
a commentary-only script produces no tool activity. Commands with executable substitutions or
redirections retain their source. Heredoc scripts retain a whole-source preview, excluding
standalone commentary commands, so bodies and delimiters are not lost at statement boundaries.
Runtime progress delivery is unchanged. Dynamic commands are never labelled as simpler operations.
Multiline source previews preserve line breaks and indentation in fenced code blocks, including
language-tagged fences and literal backticks. Transformed displays retain every operation and its
full detail without preview truncation. Unknown tools retain their qualified name and full input.
Collaboration and user-messaging arguments remain opaque: only their tool identity is displayed.
Calls without textual input show only the operation or tool name.
Different files in one patch have independently identified commentary messages, in patch order,
and are never grouped into one message. Their identities include the source call and file-section
index, so repeated completed-call observations do not duplicate files, including repeated paths.
Each file independently follows the existing delivery budget, deferral, and replay rules.
Single-item root copies retain the inline agent prefix without an `In` heading or bullet wrapper.
Consecutive matching actions from the same child collapse into one action heading, retaining
every operand and source block in order. A single resulting action uses the inline agent prefix,
without an `In` heading or bullets. Two or more resulting actions share an
`In <canonical path>` heading with nested bullet items, including mixed action kinds. Grouping uses only calls already pending
at a root delivery boundary and never waits for more calls. A different child, notice, or
deferred/current boundary ends the group. Multiline details keep their nested code fences.
Each source call remains independently deduplicated; grouped root copies remain user-only
and preserve the existing auxiliary rendering budget and replay rules. Over-budget displays
are deferred or omitted under those rules, never shortened.
The display describes an observed call, not successful execution or agent completion.
JSON output, completed SSE items, and terminal output share source-identity deduplication;
partial calls are not projected. Native child call framing and replay stay unchanged.

Actual child-authored commentary is forwarded to the root through the shared activity collector,
alongside authored tool and runtime progress. Completed assistant messages with `phase: "commentary"`
retain their original child content and identity. Root copies carry the originating agent's
canonical path as inline code, are deduplicated by source identity, and remain user-only. Final answers are
not reclassified as progress.

When a request receives an actual Codex inter-agent envelope addressed to its
canonical agent name, commentary identifies both recipient and sender, each wrapped in inline code.
The recipient is `/root` for non-child turns and the canonical child name for child turns.
An absent or malformed child identity never matches an unaddressed envelope. Valid
plaintext `MESSAGE` and `FINAL_ANSWER` payloads are shown in full as received replies,
never as excerpts. Replies exceeding the auxiliary rendering budget are omitted
from commentary without changing the original envelope. Encrypted envelopes show receipt
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

A completed root-agent or subagent final answer with provider usage includes one commentary
message before the provider-authored final answer. It uses a `Tokens:` heading and one compact
Markdown table with `Category`, `Tokens`, and `API USD` columns. Rows use full labels:
`Input`, `Cached input`, `Uncached input`, `Output`, `Reasoning`, and `Total`.
Counts use decimal thousands separators. Costs use four decimal places for cached input,
uncached input, output, and total; input and reasoning have `—` cost cells because they overlap
other rows. The total token cell is `—`. One short footer explains thread scope, overlapping
categories, and reference API rather than subscription pricing.
Intermediate tool-call responses, commentary-only responses,
and failed or incomplete responses do not report tokens. A final answer uses the `final_answer`
phase, or an unphased assistant answer for older clients, without accompanying client-dispatched
tool calls. Completed provider-executed tools may accompany the final answer.
JSON and streaming responses report the same cumulative provider-authoritative input, cached-input,
output, and reasoning totals for the originating thread. Intermediate responses contribute to
these totals without producing notices. Root and child threads remain separate; compaction and
routing-session changes do not reset totals. Repeated terminal observations within one request
count once. Totals remain in memory until router shutdown, with at most 256 tracked threads;
capacity exhaustion preserves existing totals and suppresses new-thread reports. Arithmetic
overflow suppresses reporting for the affected thread rather than showing a partial total.
An accepted or transport-interrupted request without usable terminal usage leaves a permanent
gap in that thread's router-lifetime totals. Missing or null input, cached-input, output, or
reasoning counts likewise suppress current and later thread reports until router shutdown;
they MUST NOT appear as known zeros or recover into apparently complete totals. Definite HTTP
rejections, requests rejected before forwarding, and non-generating WebSocket prewarm do not
create usage gaps. Failed and incomplete terminal responses with complete usage still contribute
to later totals without producing their own notices.

Cost estimates use built-in reference list API prices, not subscription
rates or live billing quotes. No pricing fetch or terminal renderer is required: Codex renders
the Markdown tables. Each response is priced using its effective provider-request model, service
tier, and input size before accumulation, so model switches and long-context rates do not reprice
earlier responses. Long-context rates begin above 272,000 input tokens, not at that exact count.
The terminal provider `service_tier` takes precedence over the request, including a downgrade
from `priority` or `fast` to `default`. These two Fast aliases share model-specific reference
rates; a blanket multiplier MUST NOT be applied to every model. When the response omits the tier,
the explicitly requested tier supplies the reference estimate; omission at both boundaries uses
the standard reference estimate. Unresolved `auto`, malformed or null tier evidence, and
unsupported model/tier/context combinations have unavailable cost, not guessed standard pricing.
Rates follow the [official pricing tables](https://developers.openai.com/api/docs/pricing) and
[model pricing notes](https://developers.openai.com/api/docs/models/gpt-6-astra); reference estimates
are not proof of the billed processing mode when the provider omits it.

Cached input is subtracted from ordinary input; reasoning is included in output and MUST NOT be
charged again. Optional `cache_write_tokens` are part of uncached input, not additional input
tokens. For models with published cache-write rates, their premium is included in the uncached
input cost cell, and the footer states the cache-write count. An omitted cache-write field is
zero for older providers; explicit null, invalid, or contradictory evidence is not known zero.
Unknown model/service-tier pricing or inconsistent usage makes
all four billable cost cells `n/a`, without hiding token totals or presenting a partial
cost as complete. The report explains its thread scope, overlapping token categories,
reference-price source, router-lifetime boundary, and any unavailable estimate. Root reports do not sum child threads.
This is auxiliary commentary accounting, not a change to capture-owned metrics exports.
Eligible child reports also enter the existing root activity collector as distinct notices,
deduplicated by originating thread and usage-message identity. Root copies carry the child's
canonical path and retain that child's totals, without adding them to root usage. They follow
the same bounded, deferred delivery and exact replay filtering as other child activity.

For root and subagent streams, final-answer item events are buffered until the terminal.
A successful completion emits usage as `response.output_item.done`, then the unchanged buffered
answer events, then the terminal event. Eligibility comes from streamed completed items, even
when the terminal `response.output` is empty. JSON and streaming notices require a Codex-consumable
text answer; unsupported content such as refusal parts passes through without a token notice.
The terminal output snapshot is not augmented with usage. Tool calls and progress commentary continue streaming normally. Missing usage, failed or
incomplete completion, and upstream interruption flush the buffered answer without a token notice.
Buffered answer releases retain the event type as the SSE event name and prefix every physical
payload line with `data:`, including when EOF or a transport/transform error triggers the release.
Buffering is capped at 64 MiB per response; exceeding that budget flushes the answer and disables
usage commentary for that response without rejecting provider output.
Usage commentary cannot become the terminal substantive result.

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
root projection for that stable thread until shutdown. Shell workers share thread capabilities,
so later valid metadata cannot distinguish delayed work from the ambiguous request. Local runtime
delivery, immutable authors, and replay provenance remain intact. Invalid turn headers and requests
rejected during preparation do not register or invalidate collector identities.
Critical errors from rejected requests still use previously established request-thread identity.

Child-authored commentary, operation, shell, and Code Mode progress enters the same collector as
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
through; once a response closes, updates wait for the next eligible root response.
This guarantees attributed deferred inline updates, not continuous wait-time display.

The collector retains at most 256 thread identities, 16,384 source identities,
1,024 pending events, and 64 pending events per child. Pending events expire after
one hour. Rendered root copies share a 16 KiB budget per response, after labels
are added. Live root-copy IDs remain bound to stable root thread identity until
shutdown, across session remapping and event expiry; emitted message provenance also survives
shutdown in the workspace-scoped replay store. Capacity exhaustion never
evicts executable-call history or existing replay provenance. Root copies are
removed by exact retained ID from every replay, including a first child request
with inherited root history, without removing original child messages or tool results.

Acceptance:

1. Actual child-authored commentary reaches the root with the originating agent's identity, without
   changing the original child message, substantive result, or model-visible history.
2. Collaboration calls retain exact schemas, arguments, and streaming framing without
   commentary-specific buffering. The first accepted child request produces one root start notice
   with observed model and effort; later requests do not repeat it, and other lifecycle events
   produce no added notices.

3. Received inter-agent envelopes identify both parties, including siblings and nested children. Plaintext replies are shown in full or omitted when they exceed the auxiliary rendering budget; encrypted content remains opaque and original model-visible items stay exact.
4. JSON and streaming responses expose equivalent attributed child commentary. Repeated completed
   items and terminal output do not duplicate root copies.
5. Router-authored messages are removed from every later provider request and are not repeated when
   the matching message is already present in Codex history.
6. A completed root-agent or subagent final answer with provider usage reports input, cached input,
   uncached input, output, and reasoning tokens exactly once before provider-authored final-answer
   output, using full labels in one compact table combining tokens and estimated API costs.
   Costs use per-response models and context tiers, do not double-charge cached input or reasoning,
   remain cumulative across compaction, and show `n/a` for a thread containing unpriced usage.
   Intermediate tool calls and commentary,
   failed responses, and incomplete responses remain silent. The terminal substantive result,
   provider usage object, and captured metrics remain unchanged.
7. Extensible ordinary function tools accept optional authored commentary, while strict,
   provider-owned, pre-owned-commentary, collaboration, and user-messaging schemas remain exact.
8. JSON and streaming calls show authored commentary before the executable item, remain silent
   without it, execute without the router-owned argument, and restore the exact provider call during replay.
9. Code Mode transforms nested reserved awaited forms inside-out without transforming occurrences
   in strings, template text, comments, regular expressions, properties, or unrelated identifiers.
10. Bash and POSIX shell commentary publishes expanded text without changing stdout, stderr,
    control flow, or exit status; absent publisher capacity leaves the command a successful no-op.
11. Ready and deferred runtime publications retain their routing identity: thread-scoped for shell,
    per-call for Code Mode. They remain bounded, are removed exactly on replay, and cannot replace
    a successful or failed tool result. Identical concurrent shell calls do not require guessed
    call attribution; completion of one does not interrupt the others' commentary.
12. Central model instructions keep agent-authored progress on supported tool calls and reserve
    standalone assistant commentary messages for router output.

Explicit in-tool use has debug-only [feature evidence](router.md#feature-usage-debug-evidence).
Authored fields, Code Mode lowering, runtime acceptance, and response preparation are separate
observations. Replay provenance, generated-looking IDs, automatic notices, and enabled runtime
descriptors alone do not establish authored use.

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
