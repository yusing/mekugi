# OpenCode bridge ownership

## CTR-OPENCODE-001 — OpenCode bridge ownership

The launcher owns OpenCode's invocation-local Codex model-catalog entries. The [OpenCode provider](../../providers/opencode/SPEC.md) owns service configuration,
public catalog discovery, the durable metadata cache and immutable snapshots. The router
pins request metadata and owns endpoint selection, translation policy, opaque reasoning
replay, and accounting. Shared protocol translation and stream
normalization belong to the [Codex Responses adapter](../../backends/codex/responses/SPEC.md) through the
[shared provider boundary](third_party.md).

OpenCode Go and Zen credentials, session-affinity identities, caches, and request accounting remain
isolated by service. Chat Completions, Anthropic Messages, and OpenAI Responses adapters share the
shared codec's tool validation while retaining their provider-specific reasoning and usage semantics.
Shared provider boundaries belong to [CTR-THIRD-PARTY-001](third_party.md); observable behavior
belongs to [REQ-OPENCODE-001](../spec/opencode.md).
