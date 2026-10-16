# Transport diagnostics

## Purpose

Explain transport costs and failures using exact bytes, provider evidence and
capture health, without changing execution or inference behavior.

## Scope

The module owns request correlation, boundary observations, bounded stream
metadata, sanitized capture, metrics snapshots and retained provider-usage
inspection. It observes HTTP, SSE and WebSocket traffic at the existing
client/provider boundaries, including retries and automatic steering successors.

It does not select providers, execute tools, retry requests, reconstruct output
content, estimate transport tokens, or own connection reuse. Per-thread retained
accounting belongs to [usage](../usage/SPEC.md), local projection estimates to
[output deduplication](../outputdeduplication/SPEC.md#local-savings-estimates), and
reader/edit evidence to [agent metrics](../agentmetrics/SPEC.md).

[Diagnostics](../diagnostics/SPEC.md) owns debug bundles; the application owns
the existing HTTP listener. Its metrics endpoint and [session dialog](../../doc/spec/native_ui.md#session-metrics) consume
the same detached snapshot. Rendering and exports do not maintain alternate
counters or calculations. Observation adds no listener, network hop or private
correlation header.

## Behavior

### Measurement and coverage

Each downstream inference request has one private logical identity. Provider
attempts retain that identity and consecutive attempt numbers. The snapshot
reports request outcomes, retries, provider usage, observed cache rate, exact
payload bytes at both boundaries, and capture health. Recent exchange details
are bounded; eviction never reduces cumulative totals and is reported explicitly.
Duration, HTTP status, response status and completeness remain separate facts.

Provider usage is authoritative. Complete, incomplete, unknown legacy and missing
usage observations remain distinct, including explicit zero-token observations.
Attempt coverage reconciles with request attempts; an exchange without usage
keeps usage absent. Incomplete capture is never presented as zero consumption.
The inspection contribution supplies provider-usage summaries and owner-encoded
token evidence. Offline corpus totals exclude incomplete or unknown usage evidence. Stream
receipt timing and paired output tokens follow the shared
[throughput contract](../usage/SPEC.md#output-throughput). The launch average is
summed paired output tokens divided by summed durations, excluding unmeasured
requests, rather than an average of individual request rates.

Transport observations count every forwarded byte while retaining at most 8 MiB
for response parsing per boundary. Exceeding the bound preserves delivery and
marks structured evidence incomplete. JSON, compressed bodies and supported SSE
framing produce equivalent observations without rewriting delivered bytes.
Raw content is discarded after observation.

Deduplication totals aggregate supplied repetition and savings receipts across
all threads and requests in the invocation. Negative savings and unmeasured
projections remain explicit. These estimates are separate from provider usage
and billing and are not restored on resume.

When debug write accounting is supplied, snapshots expose successful byte counts
from instrumented writes in this process, including partial writes and temporary
files whose publication fails. They exclude other processes, metadata, caches
and physical device I/O. Each snapshot excludes its own serialization and later
writes. Disabled accounting is absent, not zero.

### Private cache evidence

A recorder creates a fresh private 256-bit HMAC key and retains only scoped
128-bit digests. Neither the key nor raw content or routing tokens is persisted.
Fingerprints ignore JSON framing and established transport-only fields while
preserving inference settings and arbitrary metadata. Categories use a fixed
allowlist, never arbitrary input property names.

Client, projected and provider representations are compared with the immediate
same-thread arrival predecessor, only while that predecessor is retained and
complete. Out-of-order completion cannot change the predecessor. Missing,
unresolved, failed, evicted or cross-scope evidence makes comparison unavailable.
Retries remain attempts, not predecessors. Arrival heads are bounded to 4096
entries; fingerprints retain at most 128 ordered input items with explicit total
count and completeness. Incremental-history suffixes cannot prove a full prefix.

Comparisons distinguish identical, appended, changed and unavailable evidence,
with common item counts and changed setting categories. Body cache keys, outgoing
session routing keys and sticky turn-state forwarding are separate observations.
Unobserved sticky state differs from observed absence. The actual post-replay
request must supply projected evidence; its absence cannot be replaced by the
provider wire request. Representation changes do not prove provider cache
eligibility, residency, hidden token-prefix equality or router-caused misses.

### Provider and stream evidence

Provider attempts retain allowlisted request/model identifiers, returned service
tier and terminal cached-token evidence separately from requested-model identity
and normalized usage. Header and body model claims remain distinct. Identifiers
are limited to 256 ASCII letters, digits and `-_.:/`; invalid values and URLs are omitted.
Private provider request IDs may appear in local details, not public summaries.
Per-response metadata replaces handshake evidence on reused connections.

Service tier comes from complete returned evidence, never requested settings or
a default. Terminal cached-token evidence distinguishes present, missing, null,
invalid and unavailable, retaining exact unsigned counts including zero only
when present. Nonterminal or malformed evidence cannot supply a terminal fact.
Older records with missing additive evidence remain valid but unavailable for
that interpretation. Aggregate normalization may default missing categories to
zero, so per-attempt evidence state remains visible separately.

Stream diagnostics retain bounded metadata, allowlisted event/error categories,
framing, receipt times, termination origin and pending-call counts. They retain
neither tool arguments nor response text. At most 32 pending calls are tracked;
truncation is visible. Upstream termination, downstream delivery failure,
translation failure and host-control rejection stay distinct. A framing-complete
body without a terminal response is not successful inference.

### WebSocket boundaries

An HTTP upgrade does not create a logical inference request. Each explicit
`response.create` creates an attempt; an automatic steering successor is a new
logical exchange with zero request bytes, not an invented create payload.
A rejected upgrade ending the request is an HTTP-error attempt with no request
body. Unsupported negotiation followed by HTTP fallback is not an extra attempt.

Status 101 identifies WebSocket transport, including reused connections; terminal
evidence determines completion. Measurements count exact observed JSON payloads,
including sent metadata, without WebSocket control frames or synthetic SSE
framing. Already-received queued messages reach their observer before close or
cancellation finalizes capture. Handshake evidence is not reused for later
responses.

Application control payloads are counted separately by boundary and direction,
even when steering fails or the connection closes. They contribute neither
logical requests nor provider usage and are not counted twice. Explicit local
`generate:false` prewarm retains that no provider was expected; missing legacy
expectation still means a provider was expected. Any actual traffic remains
measured. Reconciliation excludes only explicitly provider-free exchanges.

### Replay timing

An optional replay-timing contribution reads provider-boundary records only for
selected loaded threads. Each interval ends at `captured_at` and begins that time
minus `duration_ms`; missing timestamps, negative durations and durations over
24 hours fail. The regular input is at most 64 MiB, with records below 16 MiB.
This read preserves historical timing records without requiring newer usage or
capture-identity fields, and does not infer provider usage from duration.
Cancellation, malformed input and consumer errors propagate without a partial
success claim. It opens no recorder and makes no provider requests.

## Lifecycle

The composed service is invocation-scoped and resource-free. Live launch opens
one recorder after debug paths and write accounting are ready. The caller closes
that recorder after provider teardown and final metrics publication, before debug
shutdown and bundle release. Reader workers and offline readers acquire no
recorder or output resources during composition. Restart restores no live metrics,
keys, correlations or request clocks.

## Codex connection

None. Application adapters supply provider/downstream observations and bounded
termination categories. Observation never controls Codex operations or retries.

## Records

Explicit capture appends sanitized schema-7 JSONL; metrics exports use
`mekugi.capture.metrics.v7`. Code owns their schemas. Records contain bounded
identity, timing, status, usage, byte counts, request kind/tool names and the
allowlisted evidence above. They never contain credentials, prompts,
instructions, arguments, command output, response text, patches or raw errors.
These opt-in artifacts are outside managed session-history retention. Private
debug instructions and AX logs have separate owners and privacy boundaries.

Request identities, model and tool names are bounded operational identifiers;
invalid values and URLs are omitted from capture and metrics without changing
forwarded requests. Response status is a known protocol category, never arbitrary
provider text. Omitted identity cannot establish a same-thread predecessor.

## Failure

Failure to initialize an explicitly requested artifact fails startup. Later
capture, write and observation failures remain visible auxiliary health evidence
without changing routed results. Streaming flushes, exact bytes and headers,
cancellation, retries and response-body ownership are preserved.

Removing the composition entry removes capture wrappers, metrics route and stream
trackers. Execution, usage/throughput, AX and unrelated debug artifacts continue.
Existing artifacts remain untouched; ordinary inspection and replay ignore them.
Explicit capture-output or capture-inspection requests report the missing
capability before opening their files. The session dialog reports unavailable
capture rather than empty or zero metrics.

## Acceptance

Module tests exercise sanitized record compatibility, retries and correlation,
exact boundary bytes, framing/compression, provider evidence, private fingerprints,
bounds, eviction, accounting and detached snapshots. Descriptor acceptance reads
frozen records and opens/closes a recorder without the application or a network.
Application acceptance covers HTTP/WebSocket delivery, stream failure origin,
metrics presentation, debug publication, corpus/replay consumption and omission
of the module while independent behavior remains available.
