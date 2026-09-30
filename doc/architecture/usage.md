# Provider usage ownership

## CTR-USAGE-001 — Per-thread usage and reporting

Router thread accounting owns provider-authoritative usage for each stable thread
for the router lifetime. The [usage contract](../spec/usage.md) owns report shape,
pricing, incomplete-evidence behavior, and main-completion eligibility.

Each response retains its effective model, service tier, and pricing basis before
accumulation. Missing responses preserve known totals with explicit gaps; identity
conflicts and overflow cannot become apparently complete totals. Main-turn usage
has a separate bounded index keyed by canonical thread/turn identity, not provider
attempt or child turn ID.

Report delivery joins proven workspace descendants without mutating individual
thread counters. Main completion writes a file before journal terminal delivery;
it does not generate a usage conversation message. The native roster consumes
these same counters. App-server token usage supplies separate context/exit displays
and is never added to router cost totals.

TypeSafe consumption occupies a separate per-thread bucket, outside agent-model
cost. Capture-owned [metrics](metrics.md) use terminal facts
but retain independent calculation and persistence. Local output-reduction estimates
are neither provider usage nor evidence of net savings.
