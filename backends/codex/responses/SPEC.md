# Codex Responses adaptation

## Purpose and scope

Own native Codex Responses envelopes, context projection, response delivery and
connection-local history. Provider authentication, model selection, transport and
native tool execution remain with their existing owners. The
[provider-routing contract](../../../docs/contracts/provider-routing.md) owns
route admission and credential isolation. Dedicated Chat Completions/Messages
translation, wire aliases and provider-specific reasoning adapters are removed.

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

Execution-free admission and session-shell decoding share this protocol owner.
Request-local response contracts preserve the tools and tool choice captured before
execution guidance projection, including absent and explicit-null fields. Restoring
a response never adds a contract field the provider did not return.

Incremental encoding slices only the workflow-supplied cached prefix and
can remove its previous-response dependency when rebasing. The caller selects
whether its provider uses that connection-local cache. Connection-local history
supplies the confirmed prefix; execution admission and guidance policy remain
with the consuming workflow.

## Request and stream behavior

Native call identities, namespaces, argument bytes and result attribution remain
unchanged. Named native tool outputs without a call ID are context, not tool
completions. After original-evidence consumers finish, the shared request path
projects them as assistant messages with namespace/name attribution and separate
payload parts. Correlated outputs and retained native records keep their shape.

Provider Responses events pass through the shared delivery and observation path;
there is no provider-specific stream translator. Original provider failure causes
and cancellation remain observable. Compatibility admission rejects known retired
opaque envelopes before inference; ordinary text is not interpreted as a route.

## OpenAI request shaping

Turn metadata decoding retains the host's permissive object decoding while
requiring one distinct, nonblank ASCII metadata value. Malformed auxiliary
ancestry disables attribution without invalidating otherwise usable routing
metadata. Header thread identity and routing affinity remain separate; metadata
does not authorize filesystem access.

The adapter selects the existing Codex identity, capability, and routing headers.
OpenAI Responses, WebSocket, model-catalog and auxiliary requests preserve the
incoming `Originator` and `User-Agent` values rather than substituting a client
identity. Missing values remain unspecified. Unrelated caller headers are excluded.
The host's Guardian reviewer header is preserved with its other routing facts.
Capture correlation remains local and adds no provider header.
Full-input WebSocket requests omit HTTP's
stream field and reject incremental previous-response dependencies. Existing
client metadata is preserved; turn metadata and sticky turn state travel in the
message rather than a reused handshake. Current sticky state takes precedence.
The handshake uses stable thread correlation and excludes per-exchange capture
identity. Valid session affinity remains separate from caller session headers.

Transport receives prepared bytes and headers and does not reinterpret Codex
policy. The [OpenAI provider](../../../providers/openai/SPEC.md) owns credential
validation, model-catalog exchange, HTTP forwarding, and upstream connections.

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
The [application gateway](../../../docs/contracts/provider-routing.md) owns sockets,
deadlines and provider selection; its consuming request pipeline owns execution
admission and dispatch.

Accepted steering remains pending until successor creation, including acknowledgements
that arrive after request preparation. Creation commits the accepted input before
downstream delivery, preventing a failed delivery from replaying it. Automatic
successors inherit the parent's translation context and send no new request.
Their projected input must match all already-admitted provider input. Explicit
continuations use their own settings.

Reconstructed input is always an array, including empty startup prewarm and
continuations without new items. Empty history remains empty rather than becoming
a null value or a synthetic message.

The confirmed prefix fingerprints complete projected provider input and raw
completed output. Only successful, steered or interrupted provider terminals
confirm it; fingerprints grant no filesystem authority. A matching prefix sends
only remaining items, including missing application-owned results before new user
input. A matching continuation after interruption reuses the interrupted response
ID. A changed, shortened or unconfirmed prefix sends full projected history
without `previous_response_id`. Reconciliation never reruns tools.
Accepted steering is not resent against the same parent; pending tool-result
continuations retain that parent's response identity. Original host results stay
in the prefix after completion. Changed projected input or tools require rebasing.

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

The adapter creates no process, credential store, transcript or agent registry.
Shared wire facts and exact JSON encoding belong to
[modelprotocol](../../../libraries/modelprotocol/SPEC.md). Native OpenAI transport
belongs to its [provider](../../../providers/openai/SPEC.md); custom Responses
transport belongs to the application's pinned provider connection.

Package tests cover retained request/history projection, tool identity, delivery,
usage decoding and native connection confirmation. Application checks cover
actual transport, provider errors, cancellation and capture. Installed-Codex
checks establish the host consumption boundary separately from offline encoding.
