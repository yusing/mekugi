# Module reference

Generated from feature descriptors, Codex binding declarations and application
composition. 24 of 24 feature modules are selected.
Regenerate with `make generate`; verify with `make check-generated`.

The [composition contract](../contracts/composition.md) defines ownership and
lifecycle. Declared connections and requirements do not establish installed-host
availability. Each module's specification owns its behavior and acceptance.

## [activity](../../modules/activity/SPEC.md)

Own host-operation feeds, roster derivation and retained output

- Composition: selected; lifetime: `attached-session`.
- Requires: none.
- Contributes: `record-kinds`, `terminal-roster`.
- Hosts: `activity-details`.
- Records: `wait`.
- Codex connections: none.

## [agentguidance](../../modules/agentguidance/SPEC.md)

Assemble agent guidance without changing caller authority

- Composition: selected; lifetime: `stateless`.
- Requires: none.
- Hosts: `guidance`.
- Codex connections: `guidance-projection`.
- Capabilities: `guidance-projection` (required).

## [agentmetrics](../../modules/agentmetrics/SPEC.md)

Measure reader invocations, observed edits and recorded host completion

- Composition: selected; lifetime: `stateless`.
- Requires: none.
- Contributes: `inspection`, `diagnostic-sections`.
- Codex connections: none.

## [boundedcommand](../../modules/boundedcommand/SPEC.md)

Bound one foreground command's output

- Composition: selected; lifetime: `host-operation`.
- Requires: none.
- Contributes: `commands`, `guidance`.
- Commands: `mrun`.
- Codex connections: `executable-frontend`.
- Capabilities: `executable-frontend` (required).

## [changehistory](../../modules/changehistory/SPEC.md)

Capture, retain, review, apply, and revert filesystem change evidence

- Composition: selected; lifetime: `invocation`.
- Requires: `sessionhistory`.
- Resources: private workspace checkpoints and bounded warm-up tasks.
- Contributes: `commands`, `guidance`, `record-kinds`, `references`, `activity-details`, `terminal-auxiliary`.
- Commands: `mchanges`.
- Records: `changes`.
- References: `changes`.
- Codex connections: `executable-frontend`.
- Capabilities: `executable-frontend` (required).

## [commandsegments](../../modules/commandsegments/SPEC.md)

Receive, retain, and restore command-segment observations

- Composition: selected; lifetime: `invocation`.
- Requires: `sessionhistory`.
- Resources: optional private report FIFOs and consumer tasks.
- Contributes: `record-kinds`.
- Records: `commandsegments`.
- Codex connections: none.

## [conversation](../../modules/conversation/SPEC.md)

Own conversation drafts, transcripts, prompts, queues, and side answers

- Composition: selected; lifetime: `attached-session`.
- Requires: none.
- Codex connections: `observation`.
- Capabilities: `observation` (required).

## [diagnostics](../../modules/diagnostics/SPEC.md)

Retain and inspect private diagnostic evidence

- Composition: selected; lifetime: `invocation`.
- Requires: `sessionhistory`, `sessioninspection`.
- Contributes: `record-kinds`.
- Hosts: `diagnostic-sections`.
- Records: `failure`.
- Codex connections: none.

## [fileread](../../modules/fileread/SPEC.md)

Read bounded logical source rows

- Composition: selected; lifetime: `stateless`.
- Requires: none.
- Contributes: `commands`, `guidance`.
- Commands: `mcat`.
- Codex connections: `executable-frontend`.
- Capabilities: `executable-frontend` (required).

## [livepreview](../../modules/livepreview/SPEC.md)

Project provisional source changes and publish bounded live view state

- Composition: selected; lifetime: `attached-session`.
- Requires: `changehistory`.
- Codex connections: none.

## [notifications](../../modules/notifications/SPEC.md)

Deliver bounded notices, failure details and recovery summaries

- Composition: selected; lifetime: `invocation`.
- Requires: none.
- Codex connections: none.

## [outputdeduplication](../../modules/outputdeduplication/SPEC.md)

Project repeated model-input output as visible references

- Composition: selected; lifetime: `stateless`.
- Requires: none.
- Codex connections: `input-projection`.
- Capabilities: `input-projection` (required).

## [outputrecovery](../../modules/outputrecovery/SPEC.md)

Publish and page retained output

- Composition: selected; lifetime: `stateless`.
- Requires: `sessionhistory`.
- Contributes: `commands`, `guidance`, `record-kinds`.
- Hosts: `references`.
- Commands: `mread`.
- Records: `cursor`, `output`.
- Codex connections: `executable-frontend`.
- Capabilities: `executable-frontend` (required).

## [remotewriteapproval](../../modules/remotewriteapproval/SPEC.md)

Own remote-write approval policy and exact session grants

- Composition: selected; lifetime: `invocation`.
- Requires: none.
- Codex connections: `shell-approval`.
- Capabilities: `shell-approval` (required).

## [sessionhistory](../../modules/sessionhistory/SPEC.md)

Own retained session publication, namespaces, allocation, leases, and retention

- Composition: selected; lifetime: `invocation`.
- Requires: none.
- Contributes: `record-kinds`.
- Hosts: `record-kinds`.
- Records: `hooks`, `call`, `snapshot`, `commentary`.
- Codex connections: none.

## [sessioninspection](../../modules/sessioninspection/SPEC.md)

Query retained calls, outcomes and static corpus findings

- Composition: selected; lifetime: `stateless`.
- Requires: none.
- Hosts: `inspection`.
- Codex connections: `rollout`.
- Capabilities: `rollout` (required).

## [sessionnaming](../../modules/sessionnaming/SPEC.md)

Generate optional thread names while preserving manual authority

- Composition: selected; lifetime: `host-operation`.
- Requires: none.
- Codex connections: `thread-naming`.
- Capabilities: `thread-naming` (required).

## [sessionpreferences](../../modules/sessionpreferences/SPEC.md)

Retain applied settings and renderer preferences for a workspace and thread

- Composition: selected; lifetime: `invocation`.
- Requires: `sessionhistory`.
- Codex connections: none.

## [sessionreplay](../../modules/sessionreplay/SPEC.md)

Schedule retained observations with synthetic streaming and read-only playback controls

- Composition: selected; lifetime: `stateless`.
- Requires: `sessionhistory`.
- Hosts: `replay-timing`.
- Codex connections: `rollout`.
- Capabilities: `rollout` (required).

## [sourceoutline](../../modules/sourceoutline/SPEC.md)

Inspect structural declarations and source spans

- Composition: selected; lifetime: `stateless`.
- Requires: none.
- Contributes: `commands`, `guidance`.
- Commands: `inspect_file`.
- Codex connections: `executable-frontend`.
- Capabilities: `executable-frontend` (required).

## [sudoapproval](../../modules/sudoapproval/SPEC.md)

Own sudo approval policy and exact session grants

- Composition: selected; lifetime: `invocation`.
- Requires: none.
- Codex connections: `shell-approval`.
- Capabilities: `shell-approval` (required).

## [symbolquery](../../modules/symbolquery/SPEC.md)

Resolve semantic definitions and references

- Composition: selected; lifetime: `host-operation`.
- Requires: none.
- Contributes: `commands`, `guidance`.
- Commands: `msymbol`.
- Codex connections: `executable-frontend`.
- Capabilities: `executable-frontend` (required).

## [transportdiagnostics](../../modules/transportdiagnostics/SPEC.md)

Observe transport bytes, timing, capture health and provider-response evidence

- Composition: selected; lifetime: `invocation`.
- Requires: none.
- Contributes: `inspection`, `diagnostic-sections`, `replay-timing`.
- Codex connections: none.

## [usage](../../modules/usage/SPEC.md)

Retain provider-authoritative thread usage, reference costs and throughput

- Composition: selected; lifetime: `invocation`.
- Requires: `sessionhistory`.
- Resources: on-demand retained-usage publication worker.
- Contributes: `record-kinds`, `presentation`.
- Records: `usage`.
- Codex connections: none.

