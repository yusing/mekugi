# Grok bridge ownership

## CTR-GROK-001 — Grok bridge ownership

The launcher owns Grok's invocation-local Codex model-catalog entry and private catalog lifecycle.
The router owns Grok route selection, xAI and Grok CLI authentication adaptation, Chat Completions
translation, provider-specific tool aliases, and provider usage mapping.

Grok authentication remains isolated from OpenAI and other third-party providers. The adapter
implements Grok-specific wire behavior without owning native-agent lifecycle, tool execution, or
conversation state. Shared provider boundaries belong to
[CTR-THIRD-PARTY-001](third_party.md); observable behavior belongs to
[REQ-GROK-001](../spec/grok.md).
