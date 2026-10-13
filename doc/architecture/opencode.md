# OpenCode bridge ownership

## CTR-OPENCODE-001 — OpenCode bridge ownership

The launcher owns OpenCode's invocation-local Codex model-catalog entries. The router owns
OpenCode provider configuration, public catalog discovery, the durable model-metadata cache,
immutable runtime snapshots, endpoint selection, translation policy, opaque reasoning
replay, and request-scoped price selection. Shared protocol translation and stream
normalization belong to `internal/providercodec` through the
[shared provider boundary](third_party.md).

OpenCode Go and Zen credentials, session-affinity identities, caches, and request accounting remain
isolated by service. Chat Completions, Anthropic Messages, and OpenAI Responses adapters share the
shared codec's tool validation while retaining their provider-specific reasoning and usage semantics.
Shared provider boundaries belong to [CTR-THIRD-PARTY-001](third_party.md); observable behavior
belongs to [REQ-OPENCODE-001](../spec/opencode.md).
