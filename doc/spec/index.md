---
pjdoc:
  version: 1
  kind: spec
  scope: root
  status: draft
  revision: "66"
  files:
    - journal.md
    - router.md
    - native_ui.md
    - transport.md
    - third_party.md
    - grok.md
    - opencode.md
    - notices.md
    - activity.md
    - activity_display.md
    - usage.md
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
- [`REQ-NATIVE-UI-001`](native_ui.md): UI presentation and client behavior
- [`REQ-TRANSPORT-001`](transport.md): Codex-facing and provider Responses transports
- [`REQ-THIRD-PARTY-001`](third_party.md): shared third-party native-agent routing
- [`REQ-GROK-001`](grok.md): Grok provider route
- [`REQ-OPENCODE-001`](opencode.md): OpenCode Go and Zen provider routes
- [`REQ-NOTICES-001`](notices.md): native diagnostic delivery and recovery
- [`REQ-ACTIVITY-001`](activity.md): observed tool classification
- [`REQ-ACTIVITY-DISPLAY-001`](activity_display.md): native operation rows and agent feeds
- [`REQ-USAGE-001`](usage.md): per-thread provider accounting and reference costs
- [`REQ-READ-001`](read.md): authenticated raw-row reading and managed continuation
- [`REQ-SYMBOL-001`](symbol.md): routed semantic symbol lookup with raw source rows
- [`REQ-INSPECT-001`](inspect.md): executable structural file inspection with numeric spans
- [`REQ-PLUGIN-001`](plugin.md): router-local authenticated executable tools
- [`REQ-DIAGNOSE-001`](diagnose.md): opt-in agent issue reports
- [`REQ-AX-001`](ax.md): runtime reads and evidence-backed AX reporting
- [`REQ-SESSION-001`](session.md): offline logical session inspection
- [`REQ-SESSION-REPLAY-001`](session_replay.md): timed offline UI replay and profiling
- [`REQ-METRICS-001`](metrics.md): in-process captured Responses metrics
- [`REQ-CHANGES-001`](changes.md): observed stock edits and command effects, durable change IDs, and bounded review reads
- [`REQ-EXECUTION-001`](execution.md): stock editing, execution, and executable frontends
- [`REQ-EXECUTION-002`](execution.md): Bash and Linux dash-backed sh command timing and per-segment list tracking
- [`REQ-EXECUTION-003`](execution.md): approval guard for remote VCS writes
- [`REQ-GUIDE-001`](guide.md): caller-preserving additive tool guidance
