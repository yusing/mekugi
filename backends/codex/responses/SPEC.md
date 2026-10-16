# Codex Responses adaptation

## Purpose and scope

Adapt Codex's Responses requests and native tool identities to a selected Chat
Completions, Messages, or Responses endpoint, then emit the Responses lifecycle
Codex consumes. The caller supplies resolved provider policy and diagnostics.
Provider authentication, model selection, HTTP transport, history reconciliation,
and native tool execution remain with their existing owners.

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
whether its provider uses that connection-local cache. Provider-history
reconciliation, execution admission and guidance policy remain
with the consuming workflow.

## Request and stream behavior

Translation validates explicit history and tool identities before inference.
Only supported representations are translated; opaque foreign history and
unsupported content fail with the caller's compatibility diagnostic. Stable wire
aliases preserve native names, namespaces, custom input, tool choice, and replay.
Known native wait-schema corrections affect advertised parameters, leaving
submitted argument bytes unchanged.

Endpoint adapters normalize their wire framing into one validated stream.
Complete arguments for every parallel call are validated before any executable
item is released. The first terminal choice seals content and calls; missing,
conflicting, or malformed terminals cannot become successful execution results.
Responses item identities and ordering stay stable through added, argument/input
completion, and item completion events.

Visible reasoning and answer text follow Codex's active-item lifecycle. Reasoning
replay, effort, output-budget compatibility, and usage interpretation follow the
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

## Ownership and acceptance

Each translated request owns its immutable tool mapping and stream state. The
adapter creates no process, credential store, transcript, or agent registry.
Shared wire facts and exact JSON encoding belong to
[modelprotocol](../../../libraries/modelprotocol/SPEC.md).
Provider-specific obligations remain in the [Grok](../../../doc/spec/grok.md)
and [OpenCode](../../../doc/spec/opencode.md) contracts; transport and routing
obligations remain in the [third-party contract](../../../doc/spec/third_party.md).

Package tests cover tool identity, custom input, wait aliases, fragmented and
parallel calls, terminal integrity, reasoning replay, endpoint settings, and
caller diagnostics. Router acceptance covers selected policy, actual transport,
provider errors, cancellation, and capture. Installed-Codex checks establish the
host consumption boundary separately from offline encoding tests.
