---
pjdoc:
  version: 1
  kind: spec
  scope: root
  status: draft
  revision: "73"
  files:
    - router.md
    - native_ui.md
    - transport.md
    - third_party.md
    - grok.md
    - opencode.md
    - ../../modules/notifications/SPEC.md
    - activity.md
    - ../../modules/activity/CONTRACT.md
    - plugin.md
    - ../../modules/agentmetrics/SPEC.md
    - ../../modules/sessioninspection/SPEC.md
    - ../../modules/diagnostics/SPEC.md
    - session_replay.md
    - ../../modules/transportdiagnostics/SPEC.md
    - changes.md
    - execution.md
---
# Mekugi interface contracts

Each listed file owns one observable interface contract and its acceptance cases.
Product intent and journeys belong to the separate repository product specification;
architecture files own component boundaries. Related facts are cited by stable ID or
linked, not copied.

Module-owned contracts live with their implementation:
[session history](../../modules/sessionhistory/SPEC.md),
[completed command-segment reports](../../modules/commandsegments/SPEC.md), and
[bounded commands](../../modules/boundedcommand/SPEC.md).
[Session naming](../../modules/sessionnaming/SPEC.md) owns optional automatic title
policy; the native UI contract owns manual name-save interaction and rendering.

[Session preferences](../../modules/sessionpreferences/SPEC.md) owns applied settings,
resume evidence and renderer-keyed persistence.

## Inventory

- [`REQ-CONTEXT-RESET-001`](../../modules/contextreset/SPEC.md): retained inactive instruction-file loading
- [`REQ-MCP-001`](../../libraries/mcptransport/SPEC.md): reserved invocation-local MCP transport
- [`REQ-ROUTER-001`](router.md): standalone and session-scoped Codex launch
- [`REQ-NATIVE-UI-001`](native_ui.md): UI presentation and client behavior
- [`REQ-TRANSPORT-001`](transport.md): Codex-facing Responses transport
- [OpenAI provider](../../providers/openai/SPEC.md): credential validation, model exchange, HTTP and WebSocket transport
- [`REQ-THIRD-PARTY-001`](third_party.md): shared third-party native-agent routing
- [`REQ-GROK-001`](grok.md): Grok provider route
- [`REQ-OPENCODE-001`](opencode.md): OpenCode Go and Zen provider routes
- [Notifications](../../modules/notifications/SPEC.md): native diagnostic delivery and recovery
- [`REQ-ACTIVITY-001`](activity.md): observed tool classification
- [Activity presentation](../../modules/activity/CONTRACT.md): native operation rows and agent feeds
- [`REQ-USAGE-001`](../../modules/usage/SPEC.md): per-thread provider accounting and reference costs
- [File reading](../../modules/fileread/SPEC.md): authenticated raw-row reading
- [Managed continuation](../../modules/outputrecovery/SPEC.md): retained-output paging
- [`REQ-SYMBOL-001`](../../modules/symbolquery/SPEC.md): routed semantic symbol lookup with raw source rows
- [`REQ-INSPECT-001`](../../modules/sourceoutline/SPEC.md): executable structural file inspection with numeric spans
- [`REQ-PLUGIN-001`](plugin.md): authenticated compiled executable tools
- [Agent metrics](../../modules/agentmetrics/SPEC.md): runtime reads and evidence-backed AX reporting
- [Session inspection](../../modules/sessioninspection/SPEC.md): offline logical calls and corpus findings
- [Diagnostics](../../modules/diagnostics/SPEC.md): debug bundles and failure references
- [`REQ-SESSION-REPLAY-001`](session_replay.md): timed offline UI replay and profiling
- [Transport diagnostics](../../modules/transportdiagnostics/SPEC.md): in-process captured Responses metrics
- [`REQ-CHANGES-001`](changes.md): observed stock edits and command effects, durable change IDs, and bounded review reads
- [`REQ-EXECUTION-001`](execution.md): stock editing, execution, and executable frontends
- [`REQ-EXECUTION-002`](execution.md): Bash and Linux dash-backed sh command timing and per-segment list tracking
- [`REQ-EXECUTION-003`](../../modules/remotewriteapproval/SPEC.md): approval guard for remote VCS writes
- [`REQ-EXECUTION-004`](../../modules/sudoapproval/SPEC.md): sudo command approval and secret password entry
- [Agent guidance](../../modules/agentguidance/SPEC.md): caller-preserving additive tool guidance
