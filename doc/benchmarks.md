# End-to-end benchmark

The benchmark grades real Codex runs in independent workspaces before comparing usage or
latency. Hidden tests and an allowed-path boundary decide correctness. Capturer reports actual
provider usage, cache evidence, payload estimates, tool shapes, and capture health. It does not
infer savings from a hypothetical model response.

## Requirements

You need Docker Compose, Codex authentication at `$CODEX_AUTH_PATH` or
`$CODEX_HOME/auth.json`, and any task-owned local source repository under
`benchmarks/repos/`. The runner checks its host dependencies and isolates the agent from
provider credentials, oracle material, other arms, and writable shared dependency caches.

## Run a benchmark

The default is one stock control and one Mekugi treatment, both using `gpt-6-astra` at
`medium` effort, with Mentor Handoff and issue reporting off. The default task is
`etcd-range-stream`.

```sh
bash benchmarks/bench.sh
```

Set `MODEL`, `REASONING_EFFORT`, `TASK_ID`, or `REPETITIONS` as needed. Paired repetitions
alternate arm order. For one stock attempt or qualification without inference:

```sh
BENCHMARK_MODE=control-only MODEL=gpt-5.6-sol bash benchmarks/bench.sh
BENCHMARK_MODE=control-only MODEL=gpt-5.6-sol BENCHMARK_PREPARE_ONLY=true bash benchmarks/bench.sh
```

To compare a fresh Mekugi attempt with a matching published control:

```sh
CONTROL_BASELINE_DIR=/absolute/path/to/current-control-run \
  BENCHMARK_MODE=mekugi-only MODEL=gpt-5.6-sol REPETITIONS=1 bash benchmarks/bench.sh
```

The imported control must match the current task-content fingerprint, stock instructions,
model, effort, and passing-result contract. A different or older result is not a valid
control. A diagnostic Mekugi run has no control or A/B delta:

```sh
BENCHMARK_MODE=mekugi-diagnostic REPETITIONS=1 bash benchmarks/bench.sh
```

For the opt-in main-agent Astra-to-Sol schedule, use diagnostic mode with `gpt-5.6` or
`gpt-5.6-sol`:

```sh
BENCHMARK_MODE=mekugi-diagnostic MODEL=gpt-5.6-sol \
  BENCHMARK_MAIN_MENTOR=true REPETITIONS=1 bash benchmarks/bench.sh
```

For a paired subagent Mentor Handoff run, `MODEL` selects the Luna or Terra child and
`MENTOR_PARENT_MODEL` selects the main agent:

```sh
BENCHMARK_MODE=mentor-handoff MODEL=gpt-5.6-luna REPETITIONS=2 \
  BENCHMARK_REPORT_ISSUES=false bash benchmarks/bench.sh
```

The treatment alone enables the bounded child schedule. Child lineage and actual provider
models must match the retained proof. For stock editing, execution, journal delivery,
optional issue reporting, and token-commentary coverage:

```sh
bash benchmarks/run-commentary-coverage.sh
```

This runs diagnostic and Mentor arms, continuing to the second even if the first fails.
`MODEL` may select Luna or Terra; `REASONING_EFFORT` overrides its default `medium`.
Coverage is separate from the hidden functional grader and the capturer metrics.

For real startup and isolation checks without model calls, use an already built image:

```sh
BENCH_TEST_IMAGE=mekugi-bench:your-built-tag bash benchmarks/runtime_isolation_test.sh
```

## Evidence and reports

Each arm runs one `mekugi codex` wrapper and one router listener. Capturer observes the
existing Codex-facing and provider-facing boundaries in-process, without another service or
network hop. The same listener serves Responses and `/api/metrics`.

Every attempt retains its captured filesystem diff, changed-path list, hidden-grader result,
Codex events, sanitized schema-7 `capture.jsonl`, and metrics-v6 snapshot. The runner
freezes the actual build inputs, including uncommitted changes, and records their hash,
binary hashes, immutable image ID, task-content fingerprint, instruction hash, and router
mode. Later containers use the image ID, not a mutable tag. If capture or grader injection
fails, the attempt remains failed and no substituted grader runs.

The merger validates each per-attempt capture before producing arm metrics and records.
`summary.md` reports correctness, provider usage, cache attribution, observed payload
estimates, tool shapes, model traffic, and capture completeness. Provider usage is the only
model-consumption measure. Local token estimates are not billed tokens. The summary omits
private request, thread, call, capture, routing-key, and provider-request identifiers.
Missing or incomplete evidence is not a zero or a passing result.

A retained run includes `results.jsonl`, `summary.md`, `benchmark-config.json`, per-arm
metrics and capture files, and `artifacts/` with each attempt's grader and event evidence.
Opt-in issue reports are collected separately in `agent-issue-reports.jsonl`.

## Model-free checks

The focused checks do not need a model invocation:

```sh
bash benchmarks/scheduler_test.sh
bash benchmarks/router_config_test.sh
bash benchmarks/commentary_coverage_test.sh
bash benchmarks/report_test.sh
python3 benchmarks/cache_diagnostics_test.py
python3 benchmarks/analyze_commands_test.py
```

See the [benchmark requirement](spec/benchmark.md) and [capture contract](spec/metrics.md)
for validation and privacy boundaries.
