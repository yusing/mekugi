# Provider usage ownership

## CTR-USAGE-001 — Per-thread usage accounting

Router thread accounting owns provider-authoritative usage for each stable thread
for the router lifetime. The [usage contract](../spec/usage.md) owns pricing and
incomplete-evidence behavior.

Each response retains its effective model, service tier, and pricing basis before
accumulation. Missing responses preserve known totals with explicit gaps; identity
conflicts and overflow cannot become apparently complete totals. Accounting is
keyed by stable transport thread identity, not routing session or ancestry.

The native roster consumes these same counters without a completion-time file
export or usage conversation message. App-server token usage supplies separate
context/exit displays and is never added to router cost totals.

Capture-owned [metrics](metrics.md) use terminal facts but retain independent
calculation and persistence. Local token estimates are not provider usage.
