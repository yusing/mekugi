# Provider routing and thread continuity

## Requirements

- Support native OpenAI (`mekugi codex`) and explicitly configured custom Responses providers
  (`mekugi -p NAME`) while preserving Codex execution, tools, permissions, batching, continuation,
  and per-thread usage.
- Native OpenAI must not depend on a proxy service; custom providers must use the profile's actual
  provider, model, endpoint, and auth settings rather than model-name prefix routing.

## Boundary contract

- **Pinning & isolation**: Launch captures the selected provider ID and base URL before pointing
  host inference at the invocation-local gateway. Requests forward only to that pinned endpoint
  (preserving path prefixes, encoded queries, headers, HTTP/SSE, and configured WebSockets), never
  follow credential-bearing redirects elsewhere, never leak OpenAI-only headers/policies to custom
  routes, and never fall back to OpenAI on custom-route errors.
- **Authentication & incompatibility**: Codex owns credential acquisition (`auth.command`, env keys,
  managed login, caching/refresh). Combinations incompatible with base-URL/body interception (AWS
  SigV4, `gateway_oauth`, prohibiting managed policy, or built-in OpenAI remote-catalog overrides)
  fail closed before inference.
- **Catalogs, tiers, and errors**: custom routes keep upstream model IDs unchanged (unknown models
  need no OpenAI login/discovery) and leave unknown prices unknown. Service-tier choices scope by
  thread and exact model (`fast` maps to `priority`). Actual provider status/errors are preserved;
  sanitized metrics exclude credentials and raw error text.
- **Retries & rate-limit waits**: Codex owns retries (no extra gateway retries). Recognized capacity
  errors honor `Retry-After` or exponential backoff (1s doubling to 30s); `usage_limit_reached`
  during a user turn waits until reported reset/`Retry-After` (or pauses for steer/interrupt)
  without consuming stream retries.

## Current approach (replaceable)

- Profile selection and family lifetime live in
  [Launch](../../apps/mekugi/internal/launch/SPEC.md#codex-profile-overrides); `inferencegateway`
  owns pinned HTTP/WS forwarding and custom remote-catalog endpoints; `providers/openai` owns native
  OpenAI transport.
- Provider connect/idle timeouts govern upstream operations within Mekugi's outer operation
  timeouts.
