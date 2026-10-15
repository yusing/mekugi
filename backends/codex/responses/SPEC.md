# Codex Responses adaptation

## Purpose and scope

Adapt Codex's Responses requests and native tool identities to a selected Chat
Completions, Messages, or Responses endpoint, then emit the Responses lifecycle
Codex consumes. The caller supplies resolved provider policy and diagnostics.
Provider authentication, model selection, HTTP transport, history reconciliation,
and native tool execution remain with their existing owners.

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
