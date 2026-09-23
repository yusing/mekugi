---
pjdoc:
  version: 1
  kind: spec
  scope: root
  status: draft
  revision: "62"
  files:
    - journal.md
    - router.md
    - third_party.md
    - grok.md
    - opencode.md
    - commentary.md
    - read.md
    - symbol.md
    - inspect.md
    - plugin.md
    - diagnose.md
    - ax.md
    - session.md
    - metrics.md
    - changes.md
    - execution.md
    - mentor.md
    - guide.md
---
# Mekugi interface contracts

Each listed file owns one observable interface contract and its acceptance cases.
Product intent and journeys belong to the separate repository product specification;
architecture files own component boundaries. Related facts are cited by stable ID or
linked, not copied.

## Inventory

- [`REQ-JOURNAL-001`](journal.md): durable per-thread milestone journals
- [`REQ-ROUTER-001`](router.md): standalone and session-scoped Codex launch
- [`REQ-THIRD-PARTY-001`](third_party.md): shared third-party native-agent routing
- [`REQ-GROK-001`](grok.md): Grok provider route
- [`REQ-OPENCODE-001`](opencode.md): OpenCode Go and Zen provider routes
- [`REQ-COMMENTARY-001`](commentary.md): user-only subagent activity details
- [`REQ-READ-001`](read.md): authenticated raw-row reading and managed continuation
- [`REQ-SYMBOL-001`](symbol.md): routed semantic symbol lookup with raw source rows
- [`REQ-INSPECT-001`](inspect.md): executable structural file inspection with numeric spans
- [`REQ-PLUGIN-001`](plugin.md): router-local authenticated executable tools
- [`REQ-DIAGNOSE-001`](diagnose.md): opt-in agent issue reports
- [`REQ-AX-001`](ax.md): runtime reads and evidence-backed AX reporting
- [`REQ-SESSION-001`](session.md): offline logical session inspection
- [`REQ-METRICS-001`](metrics.md): in-process captured Responses metrics
- [`REQ-CHANGES-001`](changes.md): observed stock edits and command effects, durable change IDs, and bounded review reads
- [`REQ-EXECUTION-001`](execution.md): stock editing, execution, and executable frontends
- [`REQ-MENTOR-001`](mentor.md): main and subagent Mentor Handoff schedule
- [`REQ-GUIDE-001`](guide.md): caller-preserving additive tool guidance
