# Transport diagnostics

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): capture content-safe
transport, timing, cache-prefix, and provider-stream diagnostics alongside execution without
altering routed payloads, adding network hops, exposing credentials/prompts/raw errors in sanitized
artifacts, or affecting request outcomes.

## Current approach (replaceable)

- Observes HTTP, SSE, and WebSocket traffic at client/provider boundaries to power `/session`
  metrics, `/api/metrics`, opt-in `--capture-output PATH` (`mekugi.capture.metrics.v7` sanitized
  JSONL), and offline corpus/replay timing.
- **Sanitized metrics & privacy**: records authoritative provider usage/tiers, exact byte counts,
  retries, bounded stream-termination causes, and HMAC-256 prefix-comparison digests (key discarded
  at shutdown), never prompts, credentials, patches, or raw error text.
- **Retained request timing**: always-on per-thread record via
  [Session history](../sessionhistory/SPEC.md) retains the latest 64 completed requests' transport
  phase timestamps (64 MiB ceiling, 14-day expiry; published asynchronously off the delivery path)
  for `inspect-session --ax` without requiring `--debug`.
- **Optionality**: removing the module disables capture/metrics while keeping execution, usage,
  and AX working (missing metrics stay explicitly unavailable, never zero).
