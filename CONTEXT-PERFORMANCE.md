# Implementation-time performance

Size recurring work and growing-data processing for retained history/store size, request size, and
concurrent threads, not a short fresh session.

Use existing owner revisions and admitted snapshots to skip unchanged work before expensive
materialization. Keep small updates proportional to the changed data where the contract permits;
make correctness-required whole-state work an explicit trigger. Trace affected consumers too:
upstream reuse must not conceal repeated downstream copying, serialization, or tokenization.

## Cost checks

Cover introduced or changed cost invariants with focused checks using representative input sizes.
Prefer work counts, allocations, retained-growth, or scaling assertions over machine-specific
elapsed-time limits.
