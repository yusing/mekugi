---
pjdoc:
  version: 1
  kind: architecture
  scope: root
  status: draft
  revision: "43"
  files:
    - journal.md
    - subagents.md
    - commentary.md
    - mentor.md
    - ctp.md
    - syntax.md
    - core.md
    - correct.md
    - state.md
    - boundary.md
    - plugin.md
    - metrics.md
    - translate.md
    - compare.md
    - bench.md
---
# mekugi architecture contract

Each listed file owns one ownership contract. Related facts are cited by stable ID or linked; they are not copied.

## Inventory

- [`CTR-JOURNAL-001`](journal.md): router-owned journal state and delivery
- [`CTR-SUBAGENTS-001`](subagents.md): router-owned third-party provider bridge
- [`CTR-COMMENTARY-001`](commentary.md): router-owned subagent commentary projection
- [`CTR-MENTOR-001`](mentor.md): router-owned main and subagent model schedule
- [`CTR-CTP-001`](ctp.md): router-owned compact provider representation
- [`CTR-SYNTAX-001`](syntax.md): shared compact-script framing
- [`CTR-CORE-001`](core.md): virtual workspace and immutable-baseline edit planning
- [`CTR-CORRECT-001`](correct.md): router-only rejected-script recovery
- [`CTR-STATE-001`](state.md): bounded final-state projection
- [`CTR-BOUNDARY-001`](boundary.md): filesystem and output boundary
- [`CTR-PLUGIN-001`](plugin.md): tool registry and executor carrier boundary
- [`CTR-METRICS-001`](metrics.md): capture-owned transport metrics
- [`CTR-TRANSLATE-001`](translate.md): patch rendering
- [`CTR-COMPARE-001`](compare.md): independent comparison cases
- [`CTR-BENCH-001`](bench.md): benchmark trust and execution boundary
