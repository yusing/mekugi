# Containerized correctness and capture benchmark

## REQ-BENCH-001 — Containerized paired correctness evaluation

The benchmark MUST create independent historical workspaces for its measured arms, hide oracle and
grader material until after agent changes are captured, enforce the allowed changed-path boundary,
and run the hidden executable grader. Correctness MUST take precedence over performance reporting.

Mandatory preparation, capture, injection, and grading failures MUST fail the attempt even
when execution is invoked conditionally. Injection failure MUST NOT execute a candidate-supplied
grader. Failed attempts MUST retain available artifacts and a failed result.

The runner MUST capture candidate filesystem bytes against a runner-owned baseline, independent
of candidate Git HEAD, index, configuration, and ignore rules. Grading MUST consume the captured
bytes in a network-disabled container with no host credentials, other attempts, authoritative
artifacts, or writable shared dependency cache. Qualification and grading build caches MUST be
private to each container and MUST NOT enter the dependency material exposed to agents.

Each router MUST have a per-attempt writable replay-state directory that is read-only to its
executor. Model-free container checks MUST exercise real router startup and executor restrictions.
Each build MUST freeze and retain its actual source inputs, including uncommitted changes, and
record their hash, built binary hashes, and immutable image ID. Later containers MUST use that
image ID. Results MUST retain that identity alongside instruction hashes and router mode/protocol
so the compiled guidance and effective treatment can be reproduced.

Supported modes are:

- `paired`: stock passthrough/native control versus Mekugi + CTP/2 with alternating order;
- `control-only`: exactly one fresh stock passthrough/native attempt, without a treatment router;
- `mekugi-only`: one fresh Mekugi attempt against an explicitly matching current-schema published control result selected by `CONTROL_BASELINE_DIR`;
- `mekugi-diagnostic`: one fresh Mekugi attempt without a control arm;
- `ctp-only`: Mekugi native protocol versus Mekugi CTP/2 with alternating order; and
- `mentor-handoff`: Mekugi versus Mekugi with the bounded mentor model schedule.

`mekugi-diagnostic` MUST support `DIAGNOSTIC_MODEL_PROTOCOL=native|ctp2`, defaulting
to native, without scheduling or importing a control. The explicit option MUST be
rejected in other modes. Configuration, capture validation, and report labeling MUST
agree on the selected treatment protocol; CTP acceptance criteria remain unchanged.

`BENCHMARK_MAIN_MENTOR=true` MUST be opt-in and limited to diagnostic runs configured
with `gpt-5.6` or `gpt-5.6-sol`. Main mentor validation MUST use request-sequence order,
require Astra for the first main turn, and reject a return to Astra after handoff.
Raw capture and snapshot request kinds MUST reconcile before non-turn requests are
excluded from schedule checks. Compaction usage MUST remain in result accounting;
prewarm usage remains in aggregate capture totals but not agent result usage.

The RangeStream task MUST publish its initial non-CountOnly key budget (at most 10, capped by
a positive request Limit), not leave initial batching implicit. Subsequent batches MUST adapt
toward MaxRequestBytes with a minimum of one key and respect the remaining request Limit.
MaxRequestBytes is a size target, not a hard ceiling for an indivisible value or the initial
sampling batch. Hidden grading MUST check the first response as well as later chunking,
revision pinning, complete ordered data, and final Count/More semantics.

Every fresh result MUST retain a content fingerprint covering its task manifest, visible prompt,
and hidden grader files. Importing a control MUST require the same fingerprint and stock base
instruction hash as the current task, in addition to matching task ID, model, effort, and a
passing result. Missing content evidence MUST reject import rather than treating an older task
contract as a matching control. The runner MUST verify that this content is unchanged before
starting an agent and before and after grading; a mid-run task change MUST fail the run.

The default preset MUST use `paired`, `gpt-6-astra`, `medium` reasoning effort, and one
repetition (one attempt per arm), with issue reporting and Mentor Handoff disabled.

Control-only MUST disable issue reporting, collect only control capture and metrics, and validate
that evidence without requiring or inventing a treatment, Mekugi loop result, or A/B delta.
Preparation-only MUST qualify the historical base and oracle without invoking a model. The runner
MUST expose phase elapsed times separately from measured agent wall time.

The control router MUST explicitly disable both main and subagent Mentor Handoff, including
when it runs in `mekugi` mode. CTP-only arms MUST disable both. In `mentor-handoff` mode,
only the treatment enables subagent handoff; main handoff remains disabled.
Mentor mode MUST permit an independently configured main model and a shared `native` or `ctp2`
protocol for both arms. These selections MUST NOT change the router-owned child mentor schedule.

Each fresh arm MUST run exactly one router process with exactly one listener. The agent MUST connect
directly to that listener, and the listener MUST expose Responses plus `/api/metrics`. The router
MUST connect directly to the provider. No standalone capturer process, proxy, listener, or Compose
service is permitted.

Both router arms MUST relay the Codex-owned `x-codex-turn-state` sticky-routing header unchanged
on each upstream request, including retries, and return the provider's response header to Codex.
The router MUST NOT synthesize that token from `Session_id`, cache it across requests, or carry
it into a new turn when the client omits it. This preserves the Codex transport contract rather
than warming a cache artificially; provider cache hits remain provider-owned.

The runner MUST pass `--capture-output` and `--metrics-output` to each session wrapper
and retain per-attempt exports. Only the trusted wrapper has provider egress. The
Codex MUST be launched with its explicit no-approval, no-sandbox execution option because the
benchmark container owns the complete execution boundary. The executor MUST have a fixed, non-root
primary group matching its writable workspace, no capabilities, no supplementary groups, no
privilege elevation, private mount/PID namespaces, readable read-only dependency material, private
writable and executable build storage, and read-only trusted capture/runtime/configuration mounts.
IPv4/IPv6 firewall rules MUST permit only its assigned loopback listener and reject other
destinations. The launcher MUST make the mounted Codex credential readable after capability removal
without writing it into retained artifacts. Qualification MUST reject unreadable credentials or
dependency material, an unwritable workspace, unusable build storage, or an ineffective restriction
before inference. Separate arm networks remain isolated.

The capturer-owned merger MUST verify each complete session snapshot against its
raw records before creating combined arm exports. It MUST retain originals and
rebase combined request/predecessor sequences without mixing repeated threads or
modes/protocols. Metrics are the same measurements served by the session listener;
collection MUST NOT require the listener to survive Codex exit.

For every fresh arm, report generation MUST:

1. require `mekugi.capture.metrics.v4` and schema-6 sanitized records;
2. reject empty capture, capture errors, incomplete records, boundary mismatches, duplicate client
   records, missing provider records, attempt gaps, write failures, skipped requests, and dropped
   exchange detail;
3. reconcile raw record count with capture health, provider attempts with exchanges, aggregate
   provider usage with exchange attempts, and each measured root thread's usage with
   `results.jsonl`;
4. use capturer-owned provider usage and cache attribution for all model-consumption totals;
5. report signed client-versus-final-provider request savings and model-origin-output-array savings,
   excluding router-generated commentary and echoed response metadata while reporting complete response-stream transport separately;
6. report actual provider-emitted and client-delivered tool shapes, including correlated Mekugi
   success, rejection, correction, unmatched, diagnostic, and carrier-token totals;
7. report actual provider models from exchanges, including parent and child traffic; and
8. omit request, session, thread, tool-call, and capture identities from `summary.md`.

Paired, CTP/2, and Mentor Handoff reports MUST require current baseline and treatment capture plus
snapshots; a missing, empty, or wrong-schema baseline MUST fail. `mekugi-only` and diagnostic modes
MAY omit a fresh baseline. Control-only MUST require a fresh baseline and MAY omit treatment evidence. Each root thread's provider attempts MUST use its
configured parent model. Mentor child traffic MUST match a retained child proof, use only its
configured child model in the baseline, use only the child or mentor model in the treatment, and
include at least one mentor-model request in the treatment. Any unproved thread MUST fail validation.
The treatment's permitted mentor model MUST come from its retained mentor configuration, not from
the independently selected main model. The summary MUST distinguish main, child, and mentor models.

When a CTP task requires input or output compression, report generation MUST evaluate the matching
signed client-versus-provider token savings from the CTP capturer snapshot. A required direction
MUST be positive. Failure MUST retain the measured value in `summary.md` and make the benchmark exit
nonzero.
When Mentor mode uses CTP/2, both arms MUST meet any configured compression requirements.

The report validator MAY recompute exchange sums only to prove that the snapshot is internally
consistent. The summary MUST format the snapshot's values and MUST NOT own alternate metric,
cache, gain, or synthetic-stock calculations. Negative savings MUST remain negative, provider
retries MUST not be counted as new logical requests, retry usage MUST not be discarded, and model
reporting MUST include provider attempts without usage while distinguishing usage-bearing attempts.

The validator MUST bind each arm to its router configuration: `control` is passthrough/native;
`mekugi` in paired and diagnostic modes uses the retained `treatment_model_protocol` (native for historical
records without that field); `mekugi` in mekugi-only mode and `native` are Mekugi/native; `ctp` is Mekugi/CTP2; and both Mentor
arms use Mekugi with the shared protocol selected in the retained benchmark configuration (native
by default). Every raw record
MUST agree with its snapshot mode and protocol. Self-consistent evidence from the wrong configuration
MUST fail before it receives a treatment label.

The benchmark MAY retain command and file-change events for structural loop analysis and agent issue
reports for diagnostics. Those artifacts are behavioral evidence, not metric inputs. Command-loop analysis MUST reject malformed JSON or non-object JSONL records with a path and
line diagnostic, rather than discard damaged evidence and report zero findings. Non-JSON numeric
constants (`NaN`, `Infinity`, and `-Infinity`) MUST reject, including in nested values. It MUST NOT ask
production engine, router, CTP, registry, or plugin code to emit benchmark-only evidence.

A task manifest MAY opt into runner-owned commentary coverage with versioned profiles assigned to
specific benchmark modes and arms. Each profile declares minimum retained assistant-message
matches, successful command markers, and completed item types. Successful command markers cover
runtime commentary carriers that Codex JSONL retains as command executions rather than assistant
messages. The runner MUST validate that evidence from the attempt's Codex JSONL, write a separate
commentary-coverage artifact and result field, and fail the attempt when required evidence is absent
or malformed. Commentary coverage MUST remain separate from hidden functional grading and MUST NOT
become a capturer metric.

An imported historical control without the current capture schema MAY supply correctness context,
but its old metrics MUST NOT be combined with a fresh capture or presented as current authoritative
metrics.

When a task requires an exact final response, the runner MUST compare it with the final substantive
agent message after excluding router-generated `Tokens: i=…, ci=…, o=…, r=…` telemetry. A later
ordinary agent message remains the final response and MUST NOT be ignored.

### Local codec replay

Real-session codec replay MUST consume an explicitly selected frozen private corpus, not discover
new benchmark inputs from mutable conversation history on each run. A one-time freeze operation
MUST select eligible sessions independently of compression results and session token counts, record
its eligibility criteria, selection algorithm, seed, population size, and ordered sample identities,
and preserve exact selected bytes outside the repository. It MUST refuse to overwrite an existing
destination. Multiple eligible rollouts for one logical session MUST contribute only one selected
rollout, chosen independently of token counts and compression results; the manifest MUST record
both identities and the rollout-selection rule. Conversation contents MUST NOT appear in benchmark
diagnostics.

The manifest MUST bind each sample to its session identity, byte length, and content hash. Replay
MUST report the manifest identity, reject missing or changed samples, and never silently substitute
live history. Without an explicit manifest, private replay benchmarks MUST skip. Their results
MUST be described as codec measurements on the recorded eligible local population, not evidence of
task success, latency, provider billing, or universal compression efficiency.

Acceptance:

1. Compose runs each arm as a session-scoped `mekugi codex` container, with no
   persistent router or capturer service; every attempt retains both capture exports.
2. Reports include provider usage, signed arm deltas, cache values, protocol savings,
   Mekugi delivery, and capture health, and reject altered totals, incomplete or
   absent baseline evidence, wrong modes, protocols, or provider models, and failed
   required CTP compression.
3. Paired, CTP/2, Mentor Handoff, `mekugi-only`, control-only, and diagnostic scheduling reuse the same capture
   owner and report schema.
4. A failed attempt or infrastructure check retains available artifacts, stops task-owned Compose
   resources, and returns nonzero.
5. Commentary coverage accepts every configured operation and collaboration profile
   while rejecting missing messages, missing successful-command markers, missing
   completed item types, malformed events, and unsupported modes.
6. Local replay preserves token-independent sample ordering, frozen bytes and
   identities, rejects missing or altered samples, and never overwrites an existing
   destination. Synthetic evidence validates mechanics, not compression efficiency.

CTP input acceptance uses the captured post-replay native request, never incoming client history. CTP output acceptance uses assistant output_text savings, never tool-carrier delivery expansion. Paired provider usage is the only actual model-consumption comparison.

Cache-prefix diagnostic tables MUST display only request ordinals, provider usage, stage comparison statuses, turn-state forwarding status, and fixed changed-field categories; never fingerprint values or routing keys. Missing older observations MUST display unavailable, not stable.

Provider-response evidence tables MUST include every retained attempt, distinguish explicit cached
counts from missing/null/invalid/unavailable telemetry, and label response/header models as
provider-reported. Provider request IDs MUST remain absent from summaries. Raw/snapshot metadata
and present cached counts MUST reconcile; legacy evidence MUST NOT be fabricated from counters.
