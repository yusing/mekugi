# Codex Responses adaptation

## Purpose and scope

Own Codex Responses request adaptation, native collaboration projection and
response framing. Endpoint translation supports Chat Completions, Messages and
Responses. The caller selects eligible turns and supplies resolved provider policy,
spawn guidance and diagnostics. Provider authentication, model selection, HTTP
transport and native tool execution remain with their existing owners.

The [native-agent continuity contract](../../../docs/contracts/provider-routing.md#plaintext-collaboration)
defines plaintext collaboration, history preservation and native-call restoration
for both ordinary OpenAI and third-party requests.

## Request envelopes and catalogs

The request envelope retains received bytes separately from projected fields.
Untouched fields preserve their original JSON spelling, whitespace and ordering.
An unchanged duplicate member remains byte-identical; changing that member
replaces every occurrence with the projected value. Added members have stable
ordering. Removing an original member through ordinary projection fails.
Existing permissive duplicate-name and invalid-UTF-8 decoding remains supported;
background requests and multiple top-level values remain rejected.

Catalogs describe the fields at the point of tool discovery. Presence, null,
malformed sections and additional-tool groups stay distinct so each consumer can
apply its own admission policy. Raw tool fields are authoritative, including unknown extensions.

Incremental encoding slices only the workflow-supplied cached prefix and
can remove its previous-response dependency when rebasing. The caller selects
whether its provider uses that connection-local cache. Connection-local history
supplies the confirmed prefix; execution admission and guidance policy remain
with the consuming workflow.

## Request and stream behavior

Translation validates explicit history and tool identities before inference.
Only supported representations are translated; opaque foreign history and
unsupported content fail with the caller's compatibility diagnostic. Stable wire
aliases preserve native names, namespaces, custom input, tool choice, and replay.
Known native wait-schema corrections affect advertised parameters, leaving
submitted argument bytes unchanged. Multipart text retains separate source parts,
and empty text arrays retain empty-string content. Custom tool grammars remain
explicit input instructions, validated by their existing owners. Third-party
`exec` guidance identifies JavaScript input and the requirement to print nested
results with `text(...)`; Codex models need neither note.

Endpoint adapters normalize their wire framing into one validated stream.
Complete arguments for every parallel call are validated before any executable
item is released. The first terminal choice seals content and calls; missing,
conflicting, or malformed terminals cannot become successful execution results.
Responses item identities and ordering stay stable through added, argument/input
completion, and item completion events. Function argument completion carries the
restored function name. Usage-only trailers and the final stream marker remain
valid after the first terminal choice.

Visible reasoning and answer text follow Codex's active-item lifecycle. Reasoning
that begins before an answer completes before that answer item opens; reasoning
received after text starts completes after the message instead of interleaving
items. Reasoning replay, effort, output-budget compatibility, and usage interpretation follow the
resolved provider policy. Provider error details stay in caller-facing failures;
sanitized diagnostic classification remains separate. A consumer write failure
prevents a second terminal write. Stream failure and optional upstream closure
preserve the original error for request accounting.

The response bridge exposes either the successful terminal JSON object or the
Responses SSE stream. Closing a streaming bridge cancels the request before
closing its source and joins the producer. A failed terminal retains the original
producer error separately, so a consumer can finish terminal delivery without
draining upstream or losing cancellation/timeout accounting. Non-stream producer
failure returns an error instead of a successful response.

## OpenAI request shaping

The adapter selects the existing Codex identity, capability, and routing headers.
Unrelated caller headers are excluded. Full-input WebSocket requests omit HTTP's
stream field and reject incremental previous-response dependencies. Existing
client metadata is preserved; turn metadata and sticky turn state travel in the
message rather than a reused handshake. Current sticky state takes precedence.
The handshake uses stable thread correlation and excludes per-exchange capture
identity. Valid session affinity remains separate from caller session headers.

Transport receives prepared bytes and headers and does not reinterpret Codex
policy. The [OpenAI provider](../../../providers/openai/SPEC.md) owns credential
validation, model-catalog exchange, pooling, retries, and upstream connections.

## Response delivery

The backend preserves JSON and SSE framing through ordered response transforms.
Stream detection replays inspected bytes; JSON responses are bounded to 64 MiB.
SSE retains comments, metadata fields, multiline payloads and line endings, and
stops at a terminal event without waiting for provider EOF. A downstream
disconnect remains cancellation even when its error also wraps EOF.

Raw payload observations precede transformation. Streaming delivery confirmation
follows successful writes and flushes. Non-stream callers stage and validate the
result before exposing headers or confirming delivery. The application owns body
closure, provider-failure reporting, accounting and byte-receipt diagnostics;
these responsibilities do not change protocol framing or tool execution.

## Connection-local history

Each connection has one synchronous owner for native ancestry, steering admission,
request reconstruction and provider-confirmed prefixes. Its state is memory only;
retained user history cannot restore provider confirmation on a fresh connection.
The application owns sockets, deadlines, provider selection and execution dispatch.

Accepted steering remains pending until successor creation, including acknowledgements
that arrive after request preparation. Creation commits the accepted input before
downstream delivery, preventing a failed delivery from replaying it. Automatic
successors inherit the parent's translation context and send no new request.
Their projected input must match all already-admitted provider input. Explicit
continuations use their own settings.

The confirmed prefix fingerprints complete projected provider input and raw
completed output. Only successful, steered or interrupted provider terminals
confirm it; fingerprints grant no filesystem authority. A matching prefix sends
only remaining items, including missing application-owned results before new user
input. A matching continuation after interruption reuses the interrupted response
ID. A changed, shortened or unconfirmed prefix sends full projected history
without `previous_response_id`. Reconciliation never reruns tools.
Accepted steering is not resent against the same parent; pending tool-result
continuations retain that parent's response identity. Historical guidance on a
yielded result stays in the prefix after completion; later results establish
whether its handle remains active. A changed tool catalog can replace obsolete
guidance and require rebasing.

Raw provider terminals confirm transport history independently of transformed
native output. Completed streamed items survive partial terminal snapshots, with
matching item identities updated in place. Native records and temporary provider
output share the connection's conservative 64 MiB budget; reconstructed requests
are limited to 32 MiB. An overflowing native terminal replacement preserves the
previous retained output and budget. No history is evicted to make a request fit.

## Provider usage decoding

Terminal JSON and stream payloads decode into neutral provider usage facts.
Absent, null, malformed and contradictory categories stay distinguishable;
reported zero totals remain known. Only terminal stream events supply usage.
An absent service tier permits request-tier fallback; unsupported or malformed
provider tiers remain unknown. Accounting and reference pricing belong to the
[usage module](../../../modules/usage/SPEC.md).

## Ownership and acceptance

Each translated request owns its immutable tool mapping and stream state. The
adapter creates no process, credential store, transcript, or agent registry.
Shared wire facts and exact JSON encoding belong to
[modelprotocol](../../../libraries/modelprotocol/SPEC.md).
Provider-specific obligations remain in the [Grok](../../../providers/grok/SPEC.md)
and [OpenCode](../../../providers/opencode/SPEC.md) specifications; cross-owner
obligations remain in [provider routing](../../../docs/contracts/provider-routing.md).

Package tests cover native collaboration and tool identity, custom input, wait
aliases, fragmented and parallel calls, terminal integrity, reasoning replay,
endpoint settings and caller diagnostics. Router acceptance covers selected policy,
actual transport, provider errors, cancellation and capture. Installed-Codex checks establish the
host consumption boundary separately from offline encoding tests.
