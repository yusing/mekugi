# Session naming

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): support manual
session naming (`/title`) plus non-invasive automatic session naming for new untitled Main threads
that never overrides manual names or blocks conversation work.

## Current approach (replaceable)

- Observes the first user prompt (text + supplied file/directory snapshots, capped at 4,096 runes)
  on a new untitled Main thread (children, side threads, and resumed/named threads are excluded)
  and triggers at most one auxiliary naming request after the first successful upstream exchange.
- Uses `gpt-6-luna` (medium reasoning, 1-minute deadline) with authentication captured from the
  triggering exchange; usage counts toward session totals. Missing credentials, unavailable model,
  or errors fail silently without retries or fallbacks.
- Manual `/title` or host name updates take immediate precedence; candidates are rechecked before
  calling `thread/name/set`. Removing the module disables auto-naming while keeping manual `/title`.
