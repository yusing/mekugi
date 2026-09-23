---
pjdoc:
  version: 1
  kind: architecture
  scope: root
  status: draft
  revision: "49"
  files:
    - journal.md
    - third_party.md
    - grok.md
    - opencode.md
    - commentary.md
    - mentor.md
    - boundary.md
    - plugin.md
    - execution.md
    - metrics.md
---
# Mekugi architecture ownership

Each listed file owns one stable component boundary. Observable behavior belongs to
the [interface contracts](../spec/index.md); product intent and journeys remain outside
this governed architecture set. Architecture documents name responsibilities and
collaborators without restating implementation.

## Inventory

- [`CTR-JOURNAL-001`](journal.md): router-owned journal state and delivery
- [`CTR-THIRD-PARTY-001`](third_party.md): router-owned third-party provider bridge
- [`CTR-GROK-001`](grok.md): Grok bridge ownership
- [`CTR-OPENCODE-001`](opencode.md): OpenCode bridge ownership
- [`CTR-COMMENTARY-001`](commentary.md): router-owned subagent commentary projection
- [`CTR-MENTOR-001`](mentor.md): router-owned main and subagent model schedule
- [`CTR-BOUNDARY-001`](boundary.md): filesystem, history, and output authority
- [`CTR-PLUGIN-001`](plugin.md): authenticated executable plugin registry
- [`CTR-EXECUTION-001`](execution.md): host-owned editing and process lifecycle
- [`CTR-METRICS-001`](metrics.md): capture-owned transport metrics
