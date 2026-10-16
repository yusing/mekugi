---
pjdoc:
  version: 1
  kind: architecture
  scope: root
  status: draft
  revision: "54"
  files:
    - workspace.md
    - ui.md
    - third_party.md
    - grok.md
    - opencode.md
    - notices.md
    - activity.md
    - boundary.md
    - plugin.md
    - execution.md
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

- [`CTR-WORKSPACE-001`](workspace.md): workspace membership, composition, and extraction boundaries

- [`CTR-UI-001`](ui.md): presentation, app-server client, and router integration boundaries

- [`CTR-THIRD-PARTY-001`](third_party.md): third-party provider bridge ownership
- [`CTR-GROK-001`](grok.md): Grok bridge ownership
- [`CTR-OPENCODE-001`](opencode.md): OpenCode bridge ownership
- [`CTR-NOTICES-001`](notices.md): native diagnostic delivery and recovery
- [`CTR-ACTIVITY-001`](activity.md): activity identity, collection, and native integration
- [`CTR-ACTIVITY-002`](activity.md): bounded native child history pages
- [`CTR-USAGE-001`](../../modules/usage/SPEC.md): provider accounting ownership
- [`CTR-BOUNDARY-001`](boundary.md): filesystem, history, and output authority
- [`CTR-PLUGIN-001`](plugin.md): authenticated executable catalog
- [`CTR-EXECUTION-001`](execution.md): host-owned editing and process lifecycle
- [Transport diagnostics](../../modules/transportdiagnostics/SPEC.md): capture-owned transport metrics
