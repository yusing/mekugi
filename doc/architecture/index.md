---
pjdoc:
  version: 1
  kind: architecture
  scope: root
  status: draft
  revision: "55"
  files:
    - ../../docs/contracts/provider-routing.md
    - ../../docs/contracts/composition.md
    - ui.md
    - activity.md
    - ../../docs/contracts/record-publication.md
    - ../../apps/mekugi/internal/frontends/SPEC.md
    - ../../backends/codex/SPEC.md
    - ../../libraries/shellinstrumentation/SPEC.md
    - ../../modules/transportdiagnostics/SPEC.md
---
# Mekugi architecture ownership

Each listed file owns one stable component boundary. Observable behavior belongs to
the [interface contracts](../spec/index.md); product intent and journeys remain outside
this governed architecture set. Architecture documents name responsibilities and
collaborators without restating implementation.

Retained session publication and retention are owned by
[session history](../../modules/sessionhistory/SPEC.md).
[Command segments](../../modules/commandsegments/SPEC.md) owns completed report
meaning, publication, compatibility, and adoption by a viewing thread.
[Bounded commands](../../modules/boundedcommand/SPEC.md) own foreground output
selection and return delivery omissions to the frontend host.

The [OpenAI](../../providers/openai/SPEC.md), [Grok](../../providers/grok/SPEC.md)
and [OpenCode](../../providers/opencode/SPEC.md) providers own upstream exchanges;
[HTTP primitives](../../libraries/httptransport/SPEC.md) share response admission
and body lifetime mechanics. Codex request shaping and response framing belong
to the [backend adapter](../../backends/codex/responses/SPEC.md).

[Session preferences](../../modules/sessionpreferences/SPEC.md) owns applied settings,
resume evidence and renderer-keyed persistence.

## Inventory

- [`CTR-WORKSPACE-001`](../../docs/contracts/composition.md): workspace membership, composition, and import boundaries

- [`CTR-UI-001`](ui.md): presentation, app-server client, and router integration boundaries

- [Provider routing](../../docs/contracts/provider-routing.md): provider/backend authority and native-agent continuity
- [Notifications](../../modules/notifications/SPEC.md): bounded delivery, presentation claims and recovery
- [`CTR-ACTIVITY-001`](activity.md): activity identity, collection, and native integration
- [`CTR-ACTIVITY-002`](activity.md): bounded native child history pages
- [`CTR-USAGE-001`](../../modules/usage/SPEC.md): provider accounting ownership
- [Retained publication](../../docs/contracts/record-publication.md): filesystem authority, cross-owner publication, and continuity
- [`CTR-PLUGIN-001`](../../apps/mekugi/internal/frontends/SPEC.md): authenticated executable catalog
- [Codex backend](../../backends/codex/SPEC.md): host execution, protocol, and evidence decoding
- [Shell instrumentation](../../libraries/shellinstrumentation/SPEC.md): shared guard/tracking source and exact recognition
- [Transport diagnostics](../../modules/transportdiagnostics/SPEC.md): capture-owned transport metrics
