# Provider usage accounting

## Requirements

- Track cumulative per-thread provider-authoritative token usage, reference API cost estimates, and
  measured output throughput alongside execution without adding usage text to model context or
  writing Markdown usage files.
- Keep missing or partial usage/pricing explicitly labeled (never inferred as zero or confused
  with provider billing quotes).

## Current approach (replaceable)

- Accumulates provider-authoritative usage per thread across compaction and session remapping
  (excluding fork sources and descendants from a thread's own counters; surviving restart via
  [Session history](../sessionhistory/SPEC.md) with asynchronous coalesced writes so lock
  contention never blocks delivery or UI reads).
- **Reference pricing**: applies built-in OpenAI API reference rates per response (honoring
  returned `service_tier`, long-context >272k thresholds, cached-input/cache-write rules, without
  double-charging reasoning) and persists already-priced totals; custom providers and unknown
  models/tiers report unavailable cost while keeping token counts.
- **Output throughput**: pairs authoritative output tokens (including reasoning) against forwarding-
  to-terminal-usage duration per request, exposed to composer captions and roster rows via status
  contributions.
- Native context-occupancy snapshots (from resume/fork/rewind) stay separate from cumulative
  billable usage.
