# OpenCode bridge ownership

## CTR-OPENCODE-001 — OpenCode bridge ownership

The launcher owns OpenCode's invocation-local Codex model-catalog entries. The router owns
OpenCode provider configuration, public catalog discovery, the durable model-metadata cache,
immutable runtime snapshots, endpoint selection, protocol translation, and request-scoped price
selection.

OpenCode Go and Zen credentials, session-affinity identities, caches, and request accounting remain
isolated by service. Chat Completions, Anthropic Messages, and OpenAI Responses adapters share the
router's tool validation while retaining their provider-specific reasoning and usage semantics.
Shared provider boundaries belong to [CTR-THIRD-PARTY-001](third_party.md); observable behavior
belongs to [REQ-OPENCODE-001](../spec/opencode.md).
