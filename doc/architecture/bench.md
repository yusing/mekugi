# Benchmark trust and execution boundary

## CTR-BENCH-001 — Benchmark trust and execution boundary

The benchmark runner owns immutable historical workspaces, task and instruction identity,
arm scheduling, isolation, Codex invocation, pre-grader capture, hidden grading, artifact
retention, and report validation. Agents cannot access the oracle or hidden grader before
their candidate changes are captured. Correctness and allowed paths are decided before
timing or token comparisons are interpreted.

Each measured attempt receives its own workspace, runtime, network boundary, and captured
evidence. Candidate Git metadata is not authoritative; the runner compares a trusted
baseline with a captured filesystem copy and grades that copy without provider access.
Credentials and dependencies remain outside retained artifacts, and qualification fails
before inference if the isolation boundary is not established.

One router and listener serve each fresh arm. The in-process capturer observes client and
provider boundaries and remains the sole calculation owner. The benchmark validates raw
records, snapshots, model schedules, protocol modes, usage, and completeness before
formatting comparisons. Imported controls must match the current task, instructions,
model, effort, and evidence schema.

Finalization preserves the original attempt status while collecting available evidence and
stopping owned resources. Private replay corpora and commentary coverage remain separate,
explicit evidence sources: neither replaces graded provider-backed task evaluation or
becomes a production metric surface.
