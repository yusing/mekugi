# Provider usage ownership

## CTR-USAGE-001 — Per-thread usage accounting

Router thread accounting owns provider-authoritative usage for each stable thread
across router lifetimes. The [usage contract](../spec/usage.md) owns pricing and
incomplete-evidence behavior.

Each response retains its effective model, service tier, and pricing basis before
accumulation. Missing responses preserve known totals with explicit gaps; identity
conflicts and overflow cannot become apparently complete totals. Accounting is
keyed by stable transport thread identity, not routing session or ancestry.

`thread_usage.go` is the calculation owner; `thread_usage_store.go` retains its
priced counters in managed replay storage. Atomic read-modify-write transactions
serialize concurrent router writers. Each thread owns its record independently
of workspace replay ancestry. Restoration reads these facts without forwarding
requests, inheriting a fork source's consumption, or repricing historical usage.
Storage failures preserve live observations with a notice and an incomplete
baseline, without failing response delivery or claiming successful persistence.
Restored records prove the retained amounts, not uninterrupted lifetime coverage;
their totals remain lower bounds. Unknown origins are marked at the accounting
boundary, so correctness does not depend on native history hydration ordering.

The native roster consumes these same counters without a completion-time file
export or usage conversation message. App-server token usage supplies separate
context/exit displays and is never substituted for router consumption totals.

Capture-owned [metrics](metrics.md) use terminal facts but retain independent
calculation and persistence. Local token estimates are not provider usage.
