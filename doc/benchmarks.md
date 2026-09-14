# End-to-end benchmark

The benchmark compares actual Codex runs from independent historical workspaces. Hidden executable
tests and a changed-path boundary decide correctness. The capture path then reports provider usage,
transport savings, tools, and HPATCH delivery from observed traffic rather than production counters
or hypothetical baselines.

## Requirements

The runner checks its host dependencies. You need Docker Compose, Codex authentication at
`$CODEX_AUTH_PATH` or `$CODEX_HOME/auth.json`, and the task's local source repository under
`benchmarks/repos/`.

## Run it

Default A/B preset: one attempt per arm, `gpt-6-astra` at `medium` effort.
A is stock passthrough/native; B is Mekugi + CTP/2. Mentor Handoff and issue reporting
are disabled in both arms. The default task is `etcd-range-stream`.

Default paired run:

```sh
bash benchmarks/bench.sh
```

For a matching Sol A/B pair, run `MODEL=gpt-5.6-sol bash benchmarks/bench.sh`.

One stock control attempt, with no Mekugi router or treatment attempt:

```sh
MODEL=gpt-5.6-sol BENCHMARK_MODE=control-only bash benchmarks/bench.sh
```

`control-only` requires one repetition and issue reporting disabled. Its report validates only
stock capture and usage; it contains no A/B delta or HPATCH delivery section.

For local preparation and hidden-grader qualification without any model calls:

```sh
MODEL=gpt-5.6-sol BENCHMARK_MODE=control-only BENCHMARK_PREPARE_ONLY=true \
  bash benchmarks/bench.sh
```

Preparation builds the image, prepares dependencies, and proves that the historical base fails
and oracle passes the hidden grader. It does not run an agent. The runner prints phase elapsed
times separately from agent wall time; a later measured invocation repeats preparation in its
own isolated workspace and qualifies router/network isolation before invoking Codex.

Builds retain `build-inputs.tar`, its SHA-256 hash, binary hashes, and the immutable Docker image
ID. The archive includes uncommitted build inputs but excludes Git metadata, task source clones,
and benchmark results. Later containers use the image ID rather than a mutable tag. Together with
instruction hashes and each arm's mode/protocol, this identifies the implementation and compiled
guidance used in the run.

Candidate changes are captured against a runner-owned baseline, without trusting the candidate's
Git index, HEAD, or ignore rules. Hidden graders run against that captured copy in a network-disabled
container, not on the host. Dependency material is read-only; compilation caches are private and
never passed from oracle qualification to an agent. If capture or grader injection fails, the
attempt remains failed and no substituted grader runs.

To check real startup and isolation without model calls, use an already built benchmark image:

```sh
BENCH_TEST_IMAGE=mekugi-bench:your-built-tag bash benchmarks/runtime_isolation_test.sh
```

This starts the real router and Codex `--version`, checks the executor's read-only replay state,
and exercises the isolated grader and private caches. It does not need provider credentials.

One Mekugi attempt against a matching published control:

```sh
CONTROL_BASELINE_DIR=/absolute/path/to/current-control-run \
  MODEL=gpt-5.6-sol BENCHMARK_MODE=mekugi-only REPETITIONS=1 bash benchmarks/bench.sh
```

Set `CONTROL_BASELINE_DIR` to a published control from the current runner with matching
model, effort, task-content fingerprint, and instruction evidence. The fingerprint covers the
manifest, visible task prompt, and hidden graders. Results without it, or from a different task
contract, cannot be imported; collect a fresh control instead. Keep task files unchanged during a
run: content checks before agent launch and around grading fail a run if those files change.
Historical results remain unchanged; their older metrics schema is not accepted by the current runner.

One diagnostic run without a control:

```sh
BENCHMARK_MODE=mekugi-diagnostic REPETITIONS=1 bash benchmarks/bench.sh
```

To run only the Mekugi + CTP/2 treatment used by the paired preset:

```sh
MODEL=gpt-6-astra REASONING_EFFORT=low BENCHMARK_MODE=mekugi-diagnostic \
  DIAGNOSTIC_MODEL_PROTOCOL=ctp2 REPETITIONS=1 bash benchmarks/bench.sh
```

`DIAGNOSTIC_MODEL_PROTOCOL` accepts `native` (the standalone default) or `ctp2`,
and is valid only in `mekugi-diagnostic` mode. No control attempt is launched or
imported. Historical comparisons across models or effort levels are descriptive,
not controlled measurements of a router change. Paired defaults are unchanged.

To measure Astra mentoring the main Sol agent, without CTP/2:

```sh
MODEL=gpt-5.6-sol REASONING_EFFORT=high BENCHMARK_MODE=mekugi-diagnostic \
  BENCHMARK_MAIN_MENTOR=true DIAGNOSTIC_MODEL_PROTOCOL=native \
  REPETITIONS=1 BENCHMARK_REPORT_ISSUES=false bash benchmarks/bench.sh
```

`BENCHMARK_MAIN_MENTOR` defaults to `false`. Enabling it requires diagnostic mode and
`gpt-5.6` or `gpt-5.6-sol`. It enables `--main-mentor-handoff` while leaving subagent handoff off. The normal router
mapping starts these requests on Astra with
one lower reasoning level, capped at xhigh, then hands back to the configured main model.
The report records the configured schedule and checks requests in sequence order, requiring
main turns to start on Astra with no return after handoff. Captured request-kind evidence
excludes prewarm and compaction from schedule progression. Compaction usage remains in the
agent result; prewarm usage remains in aggregate totals only. Comparing this run with an earlier Astra/CTP/2 baseline changes both
the model schedule and protocol; it is a descriptive comparison, not an isolated handoff test.

Native Mekugi versus CTP/2:

```sh
TASK_ID=batch-diagnostic-collapse BENCHMARK_MODE=ctp-only \
  BENCHMARK_REPORT_ISSUES=false REPETITIONS=4 bash benchmarks/bench.sh
```

Mekugi versus Mentor Handoff:

```sh
MODEL=gpt-5.6-luna REASONING_EFFORT=xhigh REPETITIONS=2 \
  BENCHMARK_MODE=mentor-handoff BENCHMARK_REPORT_ISSUES=false \
  bash benchmarks/bench.sh
```

To keep Astra as the main agent and enable CTP/2 alongside Mentor Handoff:

```sh
MENTOR_PARENT_MODEL=gpt-6-astra MENTOR_MODEL_PROTOCOL=ctp2 \
  MODEL=gpt-5.6-luna REPETITIONS=2 BENCHMARK_MODE=mentor-handoff \
  BENCHMARK_REPORT_ISSUES=false bash benchmarks/bench.sh
```

In Mentor mode, `MODEL` selects the requested child model (Luna or Terra), while
`MENTOR_PARENT_MODEL` selects the main agent (default Sol, high effort). The router's default
Sol/high-to-child handoff schedule is unchanged by the main model. `MENTOR_MODEL_PROTOCOL`
selects `native` (default) or `ctp2` for both arms; only the treatment enables Mentor Handoff.
Two repetitions produce four graded attempts. The summary names the main model separately from
the child and mentor models, and checks required CTP/2 compression in both CTP/2 arms.

Exhaustive commentary coverage across diagnostic, native/CTP, and Mentor Handoff arms:

```sh
bash benchmarks/run-commentary-coverage.sh
```

The commentary suite runs one repetition in each mode and continues through all three modes so one
failed arm does not hide later evidence. It intentionally permits repeated edits because stale-target
recovery is part of the task. `MODEL` may select `gpt-5.6-luna` or `gpt-5.6-terra`, and
`REASONING_EFFORT` overrides the default `medium` effort.

Coverage includes HPATCH apply and recovery, optional issue reporting, Bash and POSIX runtime
publications, provider-owned exec invocation, Code Mode runtime publication, subagent start and
response projection, and terminal token telemetry. Runtime publications and exec invocation are
proven by successful command markers because Codex JSONL does not reliably retain their user-only
messages. Host-only continuation events are outside this retained event boundary.

`MODEL`, `REASONING_EFFORT`, and paired-mode `REPETITIONS` override the defaults. CTP/2 and Mentor
Handoff disable issue reporting so the reporting tool does not confound either treatment.

## One-listener topology

Each measured arm runs one `mekugi` process with one listener:

```text
Codex ──HTTP──> mekugi ──HTTP──> provider
                 │         │
                 └─ in-process capturer
```

The capturer wraps the existing Responses handler and provider transport. It is a Go subpackage,
not a service. It opens no port. The router's same listener exposes:

```text
POST /v1/responses
GET  /v1/models
GET  /api/metrics
GET  /                 # human-readable view of /api/metrics
```

Compose defines task-scoped `control-agent` and `mekugi-agent` containers. Each
runs `mekugi codex` with one random loopback listener and fixed provider egress.
The image requires Linux iptables and util-linux. Only the trusted launcher has
NET_ADMIN and SYS_ADMIN. Docker’s default AppArmor profile is disabled to permit
the trusted launcher’s private mount setup. Before inference, it rebinds the mounted Codex
credential through an ephemeral readable copy, then starts Codex with its internal command
sandbox and approval flow disabled. The benchmark container is the execution boundary:
Codex runs in private mount/PID namespaces with no capabilities or supplementary groups and
no privilege elevation. Its fixed, non-root primary group matches the writable candidate workspace
and is allowed TCP access only to its own listener; IPv4 and IPv6 external traffic are rejected.
Trusted capture/config/runtime mounts and the image filesystem are read-only to the executor.
Preloaded dependencies remain readable but read-only. Private executable temporary storage holds
build output and caches. A fail-closed probe verifies credential and dependency access, candidate
writability, build execution, and the restrictions before launching the real Codex binary.

Each attempt writes its own sanitized `capture.jsonl` and final `metrics.json`.
The benchmark-only `mekugi-merge-captures` command validates each pair and uses the
capturer's live calculations to combine distinct-thread sessions into arm exports,
rebasing only combined sequence identities. Original per-attempt evidence remains
unchanged. Collection needs no running router or metrics endpoint.

## What differs between arms

Paired control uses passthrough mode and the pinned stock instructions. The paired treatment enables CTP/2 as well as Mekugi. `mekugi` mode replaces the
supported Code Mode editing owner with `hpatch` and `shell` while preserving unrelated tools. Each arm
gets a separate workspace and alternates execution order across repetitions.

CTP-only uses Mekugi in both arms; only the model protocol and owning guidance differ. Mentor
Handoff uses Mekugi in both arms; only the treatment router enables its bounded subagent model
schedule. Parent and child traffic remains visible through actual model names in capture exchanges.

Tool-specific guidance may teach effective use of Mekugi capabilities, including batching related
edits atomically and reusing verified targets. It must not add treatment-only general policies for
autonomy, approvals, prose length, or task validation. Those stay in the common host/task guidance.
Historical diagnostic runs compare the tool and its usage guidance without a fresh control; they
do not isolate the effect of one instruction change.

## Capture and metrics

New captures include keyed per-stage prefix diagnostics, route-key stability, and current-request
turn-state forwarding. The latter checks whether Codex's `x-codex-turn-state` reaches the provider;
it is separate from the session key. Both arms relay that provider-issued token unchanged and leave
its per-turn lifecycle to Codex. The report locates changes before replay, during replay/projection,
or during CTP, but does not claim visibility into provider cache routing. Older captures without
turn-state evidence show unavailable. Run `python3 benchmarks/cache_diagnostics_test.py` for the
model-free evidence-validation checks.

Provider response evidence additionally shows per-attempt cached-token field state and explicit
counts, plus provider-reported body/header model identifiers. Missing telemetry is not an explicit
zero; existing normalized aggregate counters can still contain default-zero values. The provider
request ID is retained in capture/dashboard details for support correlation but omitted from
benchmark summaries. Older captures cannot reconstruct these observations.

`capturer` records schema-6 JSONL at both boundaries without storing credentials, prompts,
instructions, tool arguments, command output, response text, diagnostics, scripts, reports, or
patches. Records contain sizes, token estimates, status, duration, provider usage, tool identities,
and sanitized delivery kinds or diagnostic codes. Correlation is process-private Go context; no
private header crosses the network.

Each response boundary retains at most 8 MiB for parsing while the complete stream remains forwarded
and byte-counted. Overflow is incomplete evidence. Diagnostic capture accepts only stable allowlisted
reason codes from a complete router-owned envelope; arbitrary `text(...)` content is discarded.

`GET /api/metrics` returns `mekugi.capture.metrics.v4`. Schema-6 capture records and metrics v4
exclude router-generated commentary from model-origin output, while transport still includes it.
Streamed output is rebuilt from finalized items when the terminal array is empty, absent, null,
or contains only generated commentary. Genuine model commentary is retained, including text
that resembles token telemetry. Payload estimates are not billed output-token counts.

Interpret local token estimates according to the [capture metrics contract](spec/metrics.md). Provider usage remains authoritative.

Replay restores provider-native calls before CTP measurement. Input savings compare that actual projected request with its encoded request. Output compression measures assistant text only; whole-output and carrier differences are delivery expansion, not stock-model savings.

Older records cannot be repaired from retained counters, since raw output items are not saved.
Their functional grading and provider usage remain historical evidence, but current comparison
validation rejects their output-accounting version. Fresh captures are required.

It is authoritative for:

- logical requests and provider retry attempts;
- provider input, cached input, uncached input, output, and reasoning tokens;
- cold/new and eligible-prefix cache attribution between logical requests from each final attempt;
- client and provider payload bytes and GPT-5 token estimates;
- signed protocol input and output savings;
- provider-emitted and client-delivered tool shapes;
- correlated HPATCH calls, corrections, deliveries, rejections, diagnostics, and carrier savings;
- actual provider model for every attempt, including attempts without usage; and
- capture completeness, dropped-detail, and write health.

Cumulative totals cover the router lifetime. Detailed exchanges retain the latest 4,096 requests;
if that window fills, totals remain complete but dropped-detail health invalidates benchmark use.

The report validator requires both fresh arms in paired, CTP/2, and Mentor modes. It reconciles raw
records, exchanges, aggregate usage, capture health, each measured root thread's provider usage and
configured model, and Mentor child lineage and model schedules. It rejects partial or unproved
evidence. Configured CTP/2 compression requirements use signed snapshot savings and retain a failed
value in the summary before the run exits nonzero. The report formats snapshot values rather than
calculating another notion of gain. This means retries remain retries, negative expansion stays
visible, and Mekugi is compared with the native carrier actually delivered to Codex.

## Artifacts

A retained run includes:

```text
results.jsonl
summary.md
benchmark-config.json
control-metrics.json                 # when a fresh baseline arm ran
mekugi-metrics.json
captures/control.jsonl               # when a fresh baseline arm ran
captures/mekugi.jsonl
artifacts/                            # per-attempt result, events, patch, and grader evidence
agent-issue-reports.jsonl             # when issue reporting collected records
```

An opt-in commentary task also writes `commentary-coverage.json` beside each attempt's
`result.json`. Functional correctness remains in the hidden grader record; commentary coverage is a
separate result field derived from retained assistant messages, successful command markers, and
completed item types in Codex events.

Mentor Handoff names its treatment snapshot `mekugi-mentor-metrics.json`. Child event and content-free lineage proof artifacts remain under the
attempt directory. Summary output intentionally omits request, session, thread, call, and capture
identities.

## Fixed local CTP replay

Codec replay measures compression of reconstructed requests from real Codex sessions. It does
not call a model or measure task success, provider billing, response quality, or task completion
time. Use the paired task modes above for those end-to-end questions.

Freeze a private sample once, then reuse it for comparisons. The destination must be a new
directory outside the repository. It contains raw conversations, including any sensitive text
they contain: do not commit, upload, or share it. Set `corpus_root` to an absolute private path:

```sh
corpus_root=/absolute/private/ctp-replay
MEKUGI_CTP_REPLAY_FREEZE="$corpus_root" \
  go test ./internal/router -run '^TestFreezeCTPReplayCorpus$' -count=1 -v

MEKUGI_CTP_REPLAY_MANIFEST="$corpus_root/manifest.json" \
  go test ./internal/router -run '^$' -bench '^BenchmarkCTPCorpusReplay$' -benchtime=1x -count=1 -v
```

Freezing scans `sessions` and `archived_sessions` under `$CODEX_HOME`, or `~/.codex` when unset.
Eligibility requires a completed session with stock editing and execution guidance, no Mekugi
guidance, and a custom `exec` tool call. The active `CODEX_THREAD_ID` is excluded. When several
eligible rollouts share a logical session identity, a seeded hash selects one rollout for that
session. Logical session identities are then ranked by a seeded hash; up to 50 are copied without
altering their bytes. Neither selection uses token counts or compression results. All eligible
logical sessions are included when fewer than 50 exist, with one rollout per session.

The private manifest records selection criteria, seed, eligible session and rollout counts, ordered
session and rollout identities, byte lengths, and content hashes. Replay reports the manifest hash and verifies the
frozen files before using them. Missing or changed files fail rather than silently replacing the
sample from live history. Preserve the corpus and record the code revision alongside results;
use a different new destination when deliberately selecting another sample. Without an explicit
manifest, replay benchmarks skip.

This is a sample of eligible local stock-execution sessions, not all Codex workloads. Hash-based
selection avoids choosing sessions because they compress well, but does not remove that coverage
limit. Report both the sample definition and manifest identity with compression measurements.
Synthetic test inputs cover replay mechanics only and are not efficiency evidence.

## Validate reporting

```sh
bash benchmarks/commentary_coverage_test.sh
bash benchmarks/expected_final_response_test.sh
python3 benchmarks/main_mentor_test.py
bash benchmarks/diagnostic_protocol_test.sh
bash benchmarks/report_test.sh
bash benchmarks/control_only_test.sh
bash benchmarks/task_contract_test.sh
```

The commentary fixture covers profile selection, operation and collaboration messages, successful
command markers, event minimums, missing evidence, malformed event streams, and unsupported modes.
The expected-response fixture proves router token telemetry remains auxiliary while later ordinary
assistant text remains authoritative. The reporting fixture covers every report mode and falsifies capture health,
aggregate usage, baseline presence and schema, configured provider models, Mentor lineage, and
required CTP compression. Go tests under `capturer/` prove retry correlation, privacy, streaming,
gzip, multiline SSE, and bounded detail with complete cumulative totals.
