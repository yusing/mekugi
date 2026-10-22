# Documentation

[Product scope and journeys](product.md) explain what Mekugi provides.
The [user guide](../README.md) covers setup and operation;
[Development](contributing/development.md) covers builds, tests and diagnostics.

## Features

The [generated module reference](reference/modules.md) links each feature's
specification, dependencies, lifetime, records and backend connections. Detailed
contracts stay with their owning modules.

## Shared contracts

- [Composition](contracts/composition.md): workspace membership, contributions and import boundaries.
- [Record publication](contracts/record-publication.md): filesystem authority, durability, retention and continuity.
- [Change evidence](contracts/change-evidence.md): observed effects, capture coverage and delivery.
- [Responses transport](contracts/responses-transport.md): model-path admission, catalogs and auxiliary integration.
- [Provider routing](contracts/provider-routing.md): provider selection, native-agent continuity and isolation.

## Application

- [Launch](../apps/mekugi/internal/launch/SPEC.md)
- [Executable frontends](../apps/mekugi/internal/frontends/SPEC.md)
- [Headless client](../apps/mekugi/internal/headless/SPEC.md)
- [Request execution](../apps/mekugi/internal/requestexecution/SPEC.md)
- [Execution observation](../apps/mekugi/internal/executionobservation/SPEC.md)
- [Session control](../apps/mekugi/internal/sessioncontrol/SPEC.md)
- [Session presentation](../apps/mekugi/internal/sessionpresentation/SPEC.md)
- [Session API](../apps/mekugi/internal/sessionapi/SPEC.md)
- [Session review](../apps/mekugi/internal/sessionreview/SPEC.md)
- [Terminal session](../apps/mekugi/internal/terminalsession/SPEC.md)

## Backend and providers

- [Codex](../backends/codex/SPEC.md): host execution, protocols and evidence decoding.
- [Responses adapter](../backends/codex/responses/SPEC.md): request shaping and stream normalization.
- Providers: [OpenAI](../providers/openai/SPEC.md), [Grok](../providers/grok/SPEC.md), [OpenCode](../providers/opencode/SPEC.md).

## Shared implementations

- [Shell instrumentation](../libraries/shellinstrumentation/SPEC.md)
- [HTTP transport](../libraries/httptransport/SPEC.md)
- [Reserved MCP transport](../libraries/mcptransport/SPEC.md)

## Measurement and host evidence

[Comparisons](../doc/benchmarks.md) and [dated Codex checks](../doc/codex-router-e2e.md)
describe measured behavior and verification limits.
