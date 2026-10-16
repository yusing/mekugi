---
pjdoc:
  version: 1
  kind: spec
  scope: root
  status: draft
  revision: "74"
  files:
    - ../../providers/grok/SPEC.md
    - ../../providers/opencode/SPEC.md
    - ../../docs/contracts/provider-routing.md
    - ../../apps/mekugi/internal/launch/SPEC.md
    - ../../apps/mekugi/internal/headless/SPEC.md
    - native_ui.md
    - ../../docs/contracts/responses-transport.md
    - ../../modules/notifications/SPEC.md
    - activity.md
    - ../../modules/activity/CONTRACT.md
    - ../../apps/mekugi/internal/frontends/SPEC.md
    - ../../modules/agentmetrics/SPEC.md
    - ../../modules/sessioninspection/SPEC.md
    - ../../modules/diagnostics/SPEC.md
    - ../../modules/sessionreplay/SPEC.md
    - ../../modules/transportdiagnostics/SPEC.md
    - ../../modules/livepreview/SPEC.md
    - ../../docs/contracts/change-evidence.md
    - ../../modules/changehistory/SPEC.md
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
- [Invocation launch](../../apps/mekugi/internal/launch/SPEC.md): command selection and session-scoped Codex lifecycle
- [Headless client](../../apps/mekugi/internal/headless/SPEC.md): single-prompt JSONL automation and host outcomes
- [`REQ-NATIVE-UI-001`](native_ui.md): UI presentation and client behavior
- [`REQ-TRANSPORT-001`](../../docs/contracts/responses-transport.md): Responses admission, transport, catalog and auxiliary integration
- [OpenAI provider](../../providers/openai/SPEC.md): credential validation, model exchange, HTTP and WebSocket transport
- [Provider routing](../../docs/contracts/provider-routing.md): native-agent continuity and cross-provider isolation
- [Grok](../../providers/grok/SPEC.md): authentication, models and Codex adaptation policy
- [OpenCode](../../providers/opencode/SPEC.md): Go/Zen catalog, inference and Codex adaptation policy
- [Notifications](../../modules/notifications/SPEC.md): native diagnostic delivery and recovery
- [`REQ-ACTIVITY-001`](activity.md): observed tool classification
- [Activity presentation](../../modules/activity/CONTRACT.md): native operation rows and agent feeds
- [`REQ-USAGE-001`](../../modules/usage/SPEC.md): per-thread provider accounting and reference costs
- [File reading](../../modules/fileread/SPEC.md): authenticated raw-row reading
- [Managed continuation](../../modules/outputrecovery/SPEC.md): retained-output paging
- [`REQ-SYMBOL-001`](../../modules/symbolquery/SPEC.md): routed semantic symbol lookup with raw source rows
- [`REQ-INSPECT-001`](../../modules/sourceoutline/SPEC.md): executable structural file inspection with numeric spans
- [`REQ-PLUGIN-001`](../../apps/mekugi/internal/frontends/SPEC.md): authenticated compiled executable tools
- [Agent metrics](../../modules/agentmetrics/SPEC.md): runtime reads and evidence-backed AX reporting
- [Session inspection](../../modules/sessioninspection/SPEC.md): offline logical calls and corpus findings
- [Diagnostics](../../modules/diagnostics/SPEC.md): debug bundles and failure references
- [Session replay](../../modules/sessionreplay/SPEC.md): timed offline UI replay and profiling
- [Transport diagnostics](../../modules/transportdiagnostics/SPEC.md): in-process captured Responses metrics
- [Live preview](../../modules/livepreview/SPEC.md): provisional source changes, scope and bounded live publication
- [`REQ-CHANGES-001`](../../docs/contracts/change-evidence.md): observed stock effects, capture coverage and publication
- [Change history](../../modules/changehistory/SPEC.md): durable review, frozen reads, recovery, apply and revert
- [`REQ-EXECUTION-001`](execution.md): stock editing, execution, and executable frontends
- [`REQ-EXECUTION-002`](execution.md): Bash and Linux dash-backed sh command timing and per-segment list tracking
- [`REQ-EXECUTION-003`](../../modules/remotewriteapproval/SPEC.md): approval guard for remote VCS writes
- [`REQ-EXECUTION-004`](../../modules/sudoapproval/SPEC.md): sudo command approval and secret password entry
- [Agent guidance](../../modules/agentguidance/SPEC.md): caller-preserving additive tool guidance
