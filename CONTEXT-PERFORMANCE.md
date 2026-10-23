# Implementation-time performance

Before implementing recurring work or processing growing data, identify operation frequency and
what each invocation scans, decodes, copies, serializes, allocates, or retains. Account for retained
history/store size, request size, and concurrent threads rather than assuming a short fresh session.

Use existing owner revisions and admitted snapshots to skip unchanged work before expensive
materialization. Keep small updates proportional to the changed data where the contract permits;
make correctness-required whole-state work an explicit trigger. Trace affected consumers too:
upstream reuse must not conceal repeated downstream copying, serialization, or tokenization.

## Cost checks

During implementation, cover introduced or changed cost invariants with focused checks using
representative input sizes. Prefer work counts, allocations, retained-growth, or scaling assertions
over machine-specific elapsed-time limits. Use benchmarks or profiles when an implementation
tradeoff remains uncertain.
