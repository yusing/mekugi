# Captured Responses metrics

## REQ-METRICS-001 — Captured Responses metrics

`mekugi` MUST create one in-process capturer and MUST keep one HTTP listener. The same listener
MUST serve `POST /v1/responses`, WebSocket upgrades at `GET /v1/responses`,
`GET /v1/models`, and `GET /api/metrics`. Enabling
`--capture-output PATH` MUST append sanitized schema-6 JSONL records at `PATH`; it MUST NOT start or
require a capturer service, listener, proxy, or network hop.

The same listener MUST serve a human-readable dashboard at `GET /`. The dashboard MUST consume the
capturer snapshot and MUST NOT own counters, histories, classifications, or alternate calculations.
It MUST present every aggregate group plus the retained exchange, provider-attempt, provider-tool,
and delivered-tool detail rather than substituting a reduced dashboard-specific metric set.
Provider-attempt detail MUST display the snapshot's transport marker as
`WebSocket`, or `HTTP` when absent, without claiming that every HTTP attempt is
an SSE fallback. Unknown transport markers remain visible rather than being
misclassified as HTTP. The empty-table row MUST span all displayed columns.
Dashboard polling MUST not overlap requests or replace a newer snapshot with an older response.
Polling MUST resume after a failed request.

The capturer MUST observe both the Codex-facing Responses handler and every provider-facing
Responses or Chat Completions attempt made by that request. Correlation MUST remain process-private and MUST NOT add a
header to either observed request. A provider retry MUST retain the logical request identity and use
attempt numbers `1..N` without gaps.

Observation MUST preserve the routed behavior. It MUST preserve streaming flushes, cancellation,
request and response bytes, response headers and status, provider retry behavior, and response-body
ownership. Raw bodies MUST be discarded after measurement. Response observation MUST retain at most
8 MiB per boundary while continuing to forward and count every byte. Crossing that bound MUST mark
capture health incomplete rather than buffering the remaining content or publishing partial token
and structured-response measurements as complete.

A durable record MUST contain only:

- schema version, boundary, private capture identity, logical sequence, and provider attempt;
- mode, model protocol, provider request model, and benchmark correlation fields already supplied
  by Codex;
- complete transport byte counts and framing-independent GPT-5 content estimates plus the terminal Responses `output` array measured once;
- HTTP and Responses status, completeness, duration, a bounded capture-error category, and an optional `transport: "websocket"` marker;
- optional `request_kind`, restricted to `turn`, `prewarm`, or `compaction`, supplied
  from validated router metadata and preserved in the corresponding exchange snapshot;
- provider usage counters;
- the measured `native_request` after history replay/tool projection and before CTP, on provider records;
- decoded assistant `final_text` sizes, separate from complete output arrays;
- bounded private request/routing fingerprints as specified below;
- bounded, allowlisted provider-response evidence as specified below;
- request tool names; and
- tool name, call identity, byte/token sizes, sanitized delivered kind, and an allowlisted stable
  diagnostic reason parsed from the complete router-owned diagnostic envelope.

It MUST NOT contain authorization material, prompts, instructions, message content, tool arguments,
command output, response text, script text, patches, reports, or diagnostics beyond the stable code.

`GET /api/metrics` MUST return `mekugi.capture.metrics.v4`. Its calculations MUST be made by the
capturer, not by the router, engine, plugin, benchmark report, or dashboard. The snapshot MUST expose:

1. logical request and provider-attempt counts, including completed and failed logical requests;
2. provider input, cached input, uncached input, output, reasoning, and usage-bearing attempt counts;
3. the overall provider cache rate from authoritative cached and total provider input, plus cache attribution that separates cold/new uncached input from misses within the immediately
   preceding logical request's final provider attempt for the same nonempty thread; retries within
   one request MUST NOT become cache predecessors, requests without a thread are cold, concurrent
   completions MUST retain request-arrival order, and a final attempt without usage MUST break the
   predecessor chain rather than reuse older evidence;
4. client-request, provider-attempt-request, complete provider-response-stream, and complete
   client-response-stream payload totals, plus terminal provider and client `output` arrays measured once;
   router-generated commentary MUST be excluded from model-origin output accounting by its reserved
   message identity, never by matching text or the commentary phase. This exclusion applies only
   to router-generated client output, not provider output or passthrough responses. All bytes
   remain in transport totals. Chat Completions streams reconstruct their terminal assistant-message
   array and preserve actual function names and argument measurements. Streamed responses whose
   terminal output is empty, omitted, null, or contains only generated commentary MUST reconstruct
   model-origin output in `output_index` order from finalized `response.output_item.done` items,
   excluding generated commentary there as well. A missing terminal event MUST NOT be treated as a
   completed output;
5. signed CTP input byte and token savings between the actual post-replay, post-Mekugi native
   request and its final provider request, never between raw client history and provider input,
   plus signed delivery expansion between their complete model-origin `output` arrays, excluding generated commentary, echoed tools, and all
   other response metadata, so repeated SSE framing and response metadata remain transport evidence
   rather than model-output savings. Tool translation is delivery expansion, not CTP compression
   or a hypothetical stock-model saving. Separate `output_text_tokens_saved` MUST compare only
   decoded assistant `output_text` strings, excluding tool calls and reasoning;
6. provider-emitted and client-delivered tool aggregates;
7. HPATCH call, correction, success, rejection, unmatched, diagnostic, provider-input,
   delivered-carrier-input, and signed delivered-carrier input expansion, not stock-model savings;
8. a bounded recent window of per-logical-request exchanges containing every provider attempt and
   its usage, while cumulative totals remain process-lifetime totals; and
9. capture health for record failures, incomplete records, missing provider records,
   provider-attempt gaps, durable-write errors, skipped requests, and dropped exchange detail.

Provider usage is authoritative for model consumption. Local token estimates MUST count decoded
JSON object keys and scalar values independently, excluding JSON punctuation, field ordering,
whitespace, and string-escape spelling. Equivalent numeric spellings MUST normalize without losing
precision. Arrays retain every element. Strings containing code or JSON remain literal content:
backslashes and escapes inside that content still count. SSE estimates count each decoded event's
content, not event/data framing; repeated events remain stream evidence, not final model output.
Non-JSON text is counted as literal text. Transport byte counts MUST remain exact observed bytes.
These reproducible GPT-5 content estimates include envelope and opaque reasoning values when
present; they MUST NOT be labeled as exact provider input or billed generated tokens.
The HPATCH comparison MUST pair the
actual provider-emitted HPATCH call with the actual delivered native carrier by tool-call identity;
it MUST NOT synthesize an `apply_patch`, `exec_command`, shell command, or stock result.
Apply carriers MUST be recognized by their router-owned leading marker, including when a
change-ID error-reporting wrapper surrounds the host call. HPATCH success here describes
successful translation and carrier delivery, not proof that the host applied the patch.

A benchmark report MUST read these calculations from the snapshot. It MAY independently reconcile
the snapshot against sanitized records and measured result usage, but MUST NOT replace the
capturer's calculations with report-local formulas. A fresh measured arm with any nonzero capture
health error, including dropped detail, MUST fail validation instead of reporting partial evidence
as zero.

Acceptance:

1. A test with one wrapped router listener and a retrying provider observes one logical request,
   consecutive provider attempts, one client record, provider usage, and correlated provider and
   delivered tool calls without retaining private payload text.
2. A streaming test receives the first flushed event before the handler completes.
3. JSON, multiline SSE, and gzip Responses payloads produce the same sanitized observations;
   SSE framing MUST handle LF, CRLF, CR, and an initial BOM consistently across response
   observation and token measurement; framing normalization MUST preserve data-field contents.
   finalized SSE output items MUST produce the same ordered array when the terminal envelope omits
   them, and any number of nonterminal SSE events contributes exactly one terminal output array to
   protocol output savings. JSON and SSE exclude router-generated usage, operation, runtime, and
   subagent commentary while retaining genuine model commentary, even with identical text.
   The real router/capturer integration MUST prove a telemetry-only terminal array does not hide
   finalized model messages or tool calls, and synthetic commentary changes neither output savings
   nor provider usage.
4. Snapshot totals reconcile their exchanges and provider attempts, and benchmark validation rejects
   changed aggregate usage or nonzero capture-health errors.
5. Passthrough, Mekugi with native protocol, CTP/2, and Mentor Handoff use the same capture owner and endpoint;
   none requires another listener.
6. Cumulative metrics remain complete after the detailed exchange window fills, while health marks
   the discarded detail and benchmark validation rejects it.
7. Arbitrary or malformed `text(...)` carrier content never becomes a durable diagnostic, and a
   response larger than the observation bound preserves delivery while failing capture health.

Schema-6 records and metrics v4 identify this content-token and output-accounting contract. Older records cannot be
reinterpreted as corrected measurements because they do not retain the raw output items; benchmark
validation MUST reject them as current comparison evidence.

JSON whitespace, key order, and equivalent string escaping MUST leave all content estimates unchanged while observed bytes may differ. Literal model-visible escape sequences MUST retain their token cost.

The router supplies the actual native request at the projection seam as observation data. The
capturer owns its measurement, discards the bytes immediately, and correlates the sizes with each
provider attempt. Missing CTP baseline observation MUST mark capture incomplete, never fall back to
client history. Native-only forwarding observes the same inference request before transport framing; WebSocket metadata and its `response.create` envelope remain part of measured provider transport costs.
Only paired authoritative provider usage measures actual model-consumption changes. Input CTP
savings and assistant-text CTP savings measure representation changes, not billing predictions.

### Privacy-safe cache diagnostics

Schema-6 records and metrics v4 MAY additionally contain `cache_fingerprint` for observed
request representations, `native_fingerprint` for the actual post-replay/pre-CTP request,
and `client_fingerprint` in exchanges. New captures MUST produce these for valid requests.
The capturer MUST HMAC decoded JSON components and ordered input items with a fresh random
256-bit recorder-lifetime key, retaining only 128-bit digests. It MUST NOT persist that key,
raw content, or raw outgoing routing keys. Fingerprints MUST include a recorder scope;
comparisons across different scopes MUST be unavailable. Equal keys may be correlated only
within that recorder. Field categories MUST come from a fixed allowlist, never arbitrary
user-supplied property names. JSON framing differences MUST NOT change fingerprints.

At most the first 128 input items are retained, with total item count and explicit completeness.
Truncated, malformed, missing, or cross-scope fingerprints MUST NOT claim a stable prefix.
`cache_diagnostics` MUST compare client, native, and final-provider representations against
the immediate same-thread arrival predecessor, and only when that request remains retained and completed.
Requests MUST retain that predecessor sequence even when completions arrive out of order. Arrival
head metadata is bounded to the existing 4096-entry detail limit; evicted head metadata yields
an unavailable comparison, never a guess at an older predecessor.
An unfinished intervening request, failed predecessor, absent thread, or missing observation
MUST break comparison. Retries MUST retain their individual fingerprints and actual outgoing
`Session_id` fingerprints but MUST NOT become logical-request predecessors.

Each stage reports identical, appended, changed, or unavailable, the common leading item count,
and changed fixed field categories. It MUST compare body cache-key and actual outgoing route-key
stability separately. These are observable representation differences, not the provider's hidden
model-token prefix, cache residency, or a guarantee of cache reuse. Reports and the dashboard MUST
show unavailable evidence explicitly and correlate stage changes with authoritative input/cached
usage without labeling inferred shortfalls as proven router-induced cache misses.
Older captures without these additive fields remain valid for their existing metrics but cannot
supply cache-prefix diagnoses. Benchmark validation MUST reconcile retained fingerprints and
independently verify published diagnostic comparisons.

Client and provider request fingerprints MUST additionally observe `x-codex-turn-state` with
the recorder-private HMAC key. `turn_state` is an empty string when no nonempty header value was
observed, a digest for a nonempty header, and omitted when unobserved. The body-only native seam MUST
omit it. No raw sticky-routing token may be retained. `turn_state_forwarding` compares the current
client request with its final provider attempt: absent (both empty), preserved (equal nonempty),
dropped (only the provider empty), changed (other unequal values), or unavailable (missing or
cross-scope evidence). It requires neither a predecessor nor thread identity; a new turn legitimately omits this header.
Reports MUST distinguish this check from `Session_id` stability. Older evidence MUST NOT be
reinterpreted as observed absence. Each retry retains its own observed header fingerprint.

### Provider-response evidence

Each provider attempt MUST retain `provider_response` separately from requested-model identity
and normalized usage. It contains only the provider's `x-request-id`, `openai-model` header,
latest explicitly supplied response-envelope `model`, and terminal cached-token evidence.
The same allowlisted header identifiers are observed in `codex.response.metadata`
events. Current per-response metadata replaces corresponding handshake evidence,
including on reused connections. Metadata observation remains terminal-neutral
and provider-boundary-only; arbitrary metadata headers are not retained.
Identifiers MUST be limited to 256 ASCII letters, digits, `-`, `_`, `.`, `:`, and `/`;
missing or invalid identifiers are omitted. The response model MUST NOT fall back to the
request model. Header and body model values remain separate provider claims, not proof of
the backend identity. Arbitrary headers, credentials, routing tokens, and response content
MUST NOT be retained. Provider request IDs MAY appear in local dashboard details for support
correlation, but MUST NOT appear in benchmark summaries.

`cached_tokens_state` MUST distinguish `present`, `missing`, `null`, `invalid`, and
`unavailable`. Missing or null at any level of `usage.input_tokens_details.cached_tokens`
MUST retain that distinction. Present means a valid unsigned 64-bit JSON integer and MUST
retain its exact `cached_tokens` value, including zero. Other states MUST omit that value.
Only terminal response usage supplies this evidence; nonterminal usage MUST NOT be reused.
An incomplete, malformed, or unobserved terminal response yields unavailable evidence.
JSON, SSE, and supported compressed payloads MUST follow the same rules.

This evidence is additive to schema-6/metrics-v4 and does not change existing normalized
usage counters. Missing older evidence is unavailable, never explicit zero. Reports and
dashboard MUST warn that normalized aggregate counters may default missing telemetry to zero
and MUST show per-attempt field state and explicit counts separately. Benchmark validation
MUST reconcile raw evidence with snapshots, reject unsafe shapes, and reject a present cached
count that disagrees with normalized usage. All retries retain their own response evidence.

Explicit `--metrics-output PATH` writes the capturer's final metrics snapshot during
session shutdown. It is independent of operational logging. Benchmark session
aggregation belongs to this package: complete source snapshots must reconcile their
records, modes/protocols must match, and sessions must have distinct threads.
Combined sequences are rebased while original session exports remain unchanged.

### Instruction and shell-misuse evidence

Eligible router turns record optional `instruction_rewrite` in capture records and detailed
metric exchanges. It contains only fixed categories: `carrier` (`instructions`, `developer`,
`none`), `strategy` (`marked`, `stock-gpt5`, `stock-astra`, `custom-append`, `unchanged`, `rejected`),
`workflow` (`astra`, `default`), and the boolean `custom_configured`. The router supplies the
actual branch outcome, including rewrite rejection; the capturer rejects unknown categories.
Carrier selection follows the real top-level/first-developer fallback. This evidence neither
retains prompt text or configuration paths nor changes forwarding. Missing evidence, including
older captures and requests that bypass rewriting, means unobserved rather than unchanged.
Existing request, thread, model, status, and provider-attempt fields supply correlation and
forwarding context. A successful rewrite does not prove model compliance or later delivery.

Rejected Bash bodies identified as TypeScript/JavaScript carry the allowlisted
`shell-typescript-misuse` code in the delivered call's existing `diagnostic` field, with its
original `call_id`. Native and Code Mode carriers, JSON and SSE, share that classification.
Recovered Code Mode calls instead use `kind: "code_mode_recovery"` and the fixed
`shell-code-mode-recovered` diagnostic, distinguishing recovery from rejection.
Only the fixed code is retained, not the diagnostic prose or script. The delivered diagnostic
carrier is evidence of router rejection, not confirmation that the host ran that carrier.
Both additions use existing process-lifetime retention, detail limits, JSONL capture, and metrics
exports. They are additive to schema-6/metrics-v4; older evidence remains valid but cannot supply
these diagnoses.

### WebSocket capture

Provider WebSocket exchanges use the same request-private correlation, attempt
sequence, sanitized records, snapshots, and offline aggregation as HTTP.
Each sent `response.create` is one provider attempt. An automatic steering
successor is a separate logical exchange and provider attempt with zero request
bytes at both boundaries; capture MUST NOT invent a `response.create` payload. A rejected upgrade that
ends the request is an HTTP-error attempt with zero request-body bytes; an
unsupported handshake followed by HTTP fallback is negotiation, not a separate
inference attempt. The fallback POST remains observed by the HTTP wrapper.

A WebSocket attempt records `transport: "websocket"`. Its `status_code: 101`
describes the established connection, including reused connections; it does not
claim that every exchange performed another handshake. Responses status and
terminal evidence determine success, failure, and completeness. Status 101 alone
MUST NOT classify a request as complete or an HTTP error. Provider error events
are terminal errors rather than missing output or successful responses.

Request bytes MUST measure the exact sent JSON message, including transport
metadata. Response bytes MUST measure the observed provider JSON payloads,
without synthetic SSE framing, WebSocket frame headers, or control frames.
Content token estimates sum each decoded message independently. Finalized-item
reconstruction, terminal output, bounded 8 MiB parsing, usage, and private
provider-response evidence follow the same rules as SSE. First-handshake
response evidence MUST NOT be reused as evidence for later exchanges.
Terminal event types determine captured Responses status even when the embedded
body omits status. Receiver-observed messages MUST be counted before delivery;
early close and cancellation MUST finalize only after already-read queued or
reserved messages have reached that lease's observer.

Codex-facing WebSocket exchanges use the same private correlation and sanitized
records as HTTP clients. The HTTP upgrade itself MUST NOT create a logical
request. Each explicit create or automatic successor owns its restored
downstream JSON observations and correlated provider attempts. Status 101
describes the transport, not successful response completion. Internal SSE
adaptation MUST NOT contribute synthetic framing bytes to either boundary.

An explicit `generate:false` prewarm may complete locally without a provider
attempt. Capture derives this exception from the observed request and persists
`provider_expected:false` on the sanitized client record, so live and offline
aggregation do not report a missing provider. An absent field still means a
provider is expected. Any actual prewarm provider traffic remains measured in provider usage and
transport totals. Because Codex turn usage excludes prewarm, benchmark reconciliation with the
Codex result MUST exclude only logical exchanges whose sanitized client record explicitly retains
`provider_expected:false`.

Application-level control messages, including `response.steer` and
`response.steer.*`, are measured separately from logical response exchanges.
Each observed client or provider control payload MUST immediately contribute
exact payload bytes and decoded-content token estimates to its own boundary and
direction, even if steering fails or the socket closes before a successor.
Sanitized `codex_control` and `provider_control` records MUST contain only
measurement and direction metadata, never raw control payloads. They MUST NOT
increment logical requests, provider attempts, usage, or completion counts.
Snapshots, offline aggregation, and the dashboard MUST expose the four
`transport` measures `client_control_requests`, `client_control_responses`,
`provider_control_requests`, and `provider_control_responses`. These measures
are separate from ordinary request and response totals, not counted twice.

Cache fingerprints, unlike transport measurements, normalize the transport-only
`stream` field and `response.create` discriminator. They also exclude these
established `client_metadata` transport fields in both representations:
`x-codex-turn-state`, `x-codex-turn-metadata`, `thread-id`, `x-codex-window-id`,
`x-openai-subagent`, and
`ws_request_header_x_openai_internal_codex_responses_lite`. Arbitrary metadata
and inference fields remain fingerprinted. Sticky-state forwarding still has its
own private routing fingerprint. Equal inference inputs MUST retain comparable
fingerprints across HTTP and WebSocket transport changes; changed inference
input or arbitrary metadata MUST remain detectable.
