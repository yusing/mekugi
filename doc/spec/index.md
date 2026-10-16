---
pjdoc:
  version: 1
  kind: spec
  scope: root
  status: draft
  revision: "78"
  files:
    - ../../docs/reference/modules.md
    - ../../providers/grok/SPEC.md
    - ../../providers/opencode/SPEC.md
    - ../../docs/contracts/provider-routing.md
    - ../../apps/mekugi/internal/launch/SPEC.md
    - ../../apps/mekugi/internal/headless/SPEC.md
    - ../../apps/mekugi/internal/sessioncontrol/SPEC.md
    - ../../apps/mekugi/internal/sessionpresentation/SPEC.md
    - ../../apps/mekugi/internal/terminalsession/SPEC.md
    - ../../docs/contracts/responses-transport.md
    - ../../apps/mekugi/internal/frontends/SPEC.md
    - ../../docs/contracts/change-evidence.md
    - ../../backends/codex/SPEC.md
---
# Mekugi interface contracts

Each listed file owns one observable interface contract and its acceptance cases.
Product intent and journeys belong to the [product specification](../../docs/product.md);
architecture files own component boundaries. Related facts are cited by stable ID or
linked, not copied.

The [generated module reference](../../docs/reference/modules.md) links every
feature specification and its declared dependencies, lifetime, records and Codex
connections. Module specifications link to their detailed contracts.

## Inventory

- [`REQ-MCP-001`](../../libraries/mcptransport/SPEC.md): reserved invocation-local MCP transport
- [Invocation launch](../../apps/mekugi/internal/launch/SPEC.md): command selection and session-scoped Codex lifecycle
- [Headless client](../../apps/mekugi/internal/headless/SPEC.md): single-prompt JSONL automation and host outcomes
- [Session control](../../apps/mekugi/internal/sessioncontrol/SPEC.md): submission, interruption, settings and session operations
- [Session presentation](../../apps/mekugi/internal/sessionpresentation/SPEC.md): resume, displayed identity, title, status and metrics
- [Terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md): input, panes, source selection, clipboard and renderer preferences
- [`REQ-TRANSPORT-001`](../../docs/contracts/responses-transport.md): Responses admission, transport, catalog and auxiliary integration
- [OpenAI provider](../../providers/openai/SPEC.md): credential validation, model exchange, HTTP and WebSocket transport
- [Provider routing](../../docs/contracts/provider-routing.md): native-agent continuity and cross-provider isolation
- [Grok](../../providers/grok/SPEC.md): authentication, models and Codex adaptation policy
- [OpenCode](../../providers/opencode/SPEC.md): Go/Zen catalog, inference and Codex adaptation policy
- [`REQ-PLUGIN-001`](../../apps/mekugi/internal/frontends/SPEC.md): authenticated compiled executable tools
- [`REQ-CHANGES-001`](../../docs/contracts/change-evidence.md): observed stock effects, capture coverage and publication
- [Codex execution authority](../../backends/codex/SPEC.md#execution-authority): stock editing, execution and native approval outcomes
