---
pjdoc:
  version: 1
  kind: architecture
  scope: root
  status: draft
  revision: "57"
  files:
    - ../../docs/reference/modules.md
    - ../../docs/contracts/provider-routing.md
    - ../../docs/contracts/composition.md
    - ../../apps/mekugi/internal/terminalsession/SPEC.md
    - ../../apps/mekugi/internal/sessioncontrol/SPEC.md
    - ../../apps/mekugi/internal/sessionpresentation/SPEC.md
    - ../../docs/contracts/record-publication.md
    - ../../apps/mekugi/internal/frontends/SPEC.md
    - ../../backends/codex/SPEC.md
    - ../../libraries/shellinstrumentation/SPEC.md
---
# Mekugi architecture ownership

Each listed file owns one stable component boundary. Observable behavior belongs to
the [interface contracts](../spec/index.md); intent and journeys belong to the
[product specification](../../docs/product.md). Architecture documents name responsibilities and
collaborators without restating implementation.

Feature ownership, dependencies, lifetimes, records and Codex connections are
listed in the [generated module reference](../../docs/reference/modules.md).
Each feature's specification owns its behavioral boundary.

The [OpenAI](../../providers/openai/SPEC.md), [Grok](../../providers/grok/SPEC.md)
and [OpenCode](../../providers/opencode/SPEC.md) providers own upstream exchanges;
[HTTP primitives](../../libraries/httptransport/SPEC.md) share response admission
and body lifetime mechanics. Codex request shaping and response framing belong
to the [backend adapter](../../backends/codex/responses/SPEC.md).

## Inventory

- [`CTR-WORKSPACE-001`](../../docs/contracts/composition.md): workspace membership, composition, and import boundaries
- [Terminal session](../../apps/mekugi/internal/terminalsession/SPEC.md): input, panes, source selection and responsive rendering
- [Session control](../../apps/mekugi/internal/sessioncontrol/SPEC.md): identity-bound host operations and settlement
- [Provider routing](../../docs/contracts/provider-routing.md): provider/backend authority and native-agent continuity
- [Session presentation](../../apps/mekugi/internal/sessionpresentation/SPEC.md): displayed thread, input admission and bounded child history
- [Retained publication](../../docs/contracts/record-publication.md): filesystem authority, cross-owner publication, and continuity
- [`CTR-PLUGIN-001`](../../apps/mekugi/internal/frontends/SPEC.md): authenticated executable catalog
- [Codex backend](../../backends/codex/SPEC.md): host execution, protocol, and evidence decoding
- [Shell instrumentation](../../libraries/shellinstrumentation/SPEC.md): shared guard/tracking source and exact recognition
