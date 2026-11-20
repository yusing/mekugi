# Documentation

- [README](../README.md): user overview, installation, and quick start.
- [Product requirements and design](product.md): binding outcomes, scope constraints, and
  [authority rules](product.md#intent-and-authority).
- [User guides](../README.md#guides) (`docs/guide/`): day-to-day operation.
- [Contributing](../CONTRIBUTING.md) and [Development](contributing/development.md): builds,
  tests, diagnostics, and profiling.

All `SPEC.md` and `CONTRACT.md` files separate binding **Requirements** (or **Boundary contract**)
from **Current approach (replaceable)**. Internal helper libraries and app-wiring packages are
documented by their code and tests rather than standalone specs.

## Shared boundary contracts

- [Native agent coordination](contracts/coordination.md): delegation, preloads, waits, progress,
  side questions, and cross-session delivery.
- [Workspace composition](contracts/composition.md): Go workspace layout, descriptors, lifetimes,
  and import boundaries.
- [Change evidence](contracts/change-evidence.md): passive capture authority, scope, and delivery.
- [Record publication](contracts/record-publication.md): durability, retention, and cross-owner
  publication ordering.
- [Responses transport](contracts/responses-transport.md): HTTP/WebSocket admission, projection,
  and failure delivery.
- [Provider routing](contracts/provider-routing.md): native OpenAI and custom profile Responses
  routing.

## External and user-facing specs

- [Launch](../apps/mekugi/internal/launch/SPEC.md): CLI invocation, profiles, server lifecycle,
  and host wiring.
- [Executable frontends](../apps/mekugi/internal/frontends/SPEC.md): authenticated session helpers
  and recovery delivery.
- [Headless client](../apps/mekugi/internal/headless/SPEC.md): noninteractive automation and JSONL
  output.
- [Session API](../apps/mekugi/internal/sessionapi/SPEC.md) and
  [wire contract](../apps/mekugi/internal/sessionapi/CONTRACT.md): server-owned runtime control,
  JSON-RPC/WebSocket protocol, and HTTP resources.
- [Connector](../apps/mekugi/internal/connector/SPEC.md): authenticated remote forwarding.
- [Terminal session](../apps/mekugi/internal/terminalsession/SPEC.md): interactive terminal UI.
- [Web UI](../apps/mekugi/internal/webui/SPEC.md) and
  [contract](../apps/mekugi/internal/webui/CONTRACT.md): browser client and asset host.
- [Codex backend](../backends/codex/SPEC.md): host execution authority, RPC transport, shell
  hooks, output deduplication, and rollout decoding.

## Feature modules

The [generated module reference](reference/modules.md) indexes every feature module's `SPEC.md`,
dependencies, lifetime, records, and Codex connections.

## Measurement and host evidence

- [Controlled comparisons](benchmarks.md): pointer to `codex-setup-ab`.
- [Dated Codex checks](codex-router-e2e.md): dated host observations and verification limits.
