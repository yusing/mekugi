# Containerized correctness and capture benchmark

## REQ-BENCH-001 — Containerized paired correctness evaluation

A benchmark must decide correctness before comparing cost or speed. Each fresh arm gets an
independent historical workspace. The runner hides oracle and grader material until after it
captures the candidate's filesystem bytes against a runner-owned baseline. It grades those
captured bytes, not the candidate's mutable Git HEAD, index, configuration, or ignore rules.
Only allowed changed paths may enter the candidate. Mandatory preparation, capture, injection,
and grading failures fail the attempt, retain available artifacts, and must not run a
candidate-supplied substitute grader.

The hidden grader runs in a network-disabled container without host credentials, other arms'
artifacts, or a writable shared dependency cache. Qualification and grading caches are private
to each container and never become agent-visible dependency material. Each build freezes the
actual source inputs, including uncommitted changes, and retains their hash, built binary
hashes, and immutable image ID. Later containers use that image ID, not a mutable tag. Results
retain that identity, task-content fingerprint, instruction hashes, and router mode.

Supported modes are:

- `paired`: stock passthrough control versus Mekugi treatment, alternating order;
- `control-only`: one fresh stock attempt;
- `mekugi-only`: one fresh Mekugi attempt with an explicitly matching current published control;
- `mekugi-diagnostic`: one fresh Mekugi attempt without a control; and
- `mentor-handoff`: Mekugi versus Mekugi with bounded subagent Mentor Handoff enabled only in the treatment.

The default is `paired`, `gpt-6-astra`, `medium` effort, one repetition, with issue reporting and
Mentor Handoff disabled. Single-arm modes require one repetition. Control-only disables issue
reporting and must not invent treatment evidence or an A/B delta. Preparation-only qualifies the
base and oracle without inference and reports phase time separately from agent wall time.

Every result retains a fingerprint of its manifest, visible prompt, and hidden grader files.
Importing a control requires the same fingerprint, stock instruction hash, task ID, model,
effort, and a passing result. Missing or old content evidence rejects the import. The runner
checks that task content is unchanged before launch and around grading; a mid-run change fails.

`BENCHMARK_MAIN_MENTOR=true` is opt-in only for diagnostic runs configured with `gpt-5.6` or
`gpt-5.6-sol`. Main schedule validation uses request order, requires Astra for the first main
turn, and rejects a return to Astra after handoff. Prewarm and compaction do not advance the
schedule. Compaction usage remains in the agent result; prewarm usage remains in aggregate
capture only. Mentor mode independently configures the main model and a Luna or Terra child.
The control router disables both handoff modes, and only the Mentor treatment enables subagent
handoff. Child model traffic must match retained lineage proof and the router's configured
schedule; unproved threads fail validation.

Each fresh arm runs one router and one listener. Codex connects directly to that listener and
the router connects directly to the provider. The listener serves Responses and `/api/metrics`.
No separate capturer process, proxy, listener, or Compose service is permitted. Each attempt
uses a private writable replay-state directory that is read-only to its executor. The trusted
wrapper alone has provider egress. Codex uses the benchmark's explicit no-approval, no-sandbox
option because the container owns the execution boundary. The executor has a fixed non-root
primary group matching its writable workspace, no capabilities, no supplementary groups or
privilege elevation, private mount/PID namespaces, read-only dependencies and trusted mounts,
private executable build storage, and a firewall permitting only its own loopback listener.
Qualification must prove credentials and dependencies are readable, the workspace and build
storage usable, and restrictions effective before inference. Arm networks remain separate.

Both modes relay Codex's `x-codex-turn-state` unchanged on every upstream request and return the
provider response header. The router must not synthesize, cache, or carry that token into a
turn where Codex omitted it. Provider cache hits remain provider-owned.

The in-process capturer writes per-attempt sanitized schema-7 records and metrics-v6 snapshots.
The merger validates each complete attempt before combining distinct-thread sessions, rebases
combined sequence identities, and keeps originals. Collection must not require a live router.
Every fresh report validates:

1. Current schemas, nonempty capture, boundary pairing, consecutive provider attempts, capture
   health, complete exchange detail, and exact raw/snapshot reconciliation.
2. Aggregate provider usage against attempts and each measured root thread's Codex result usage;
   retries remain attempts, not new logical requests.
3. The arm's router mode: `control` is passthrough; `mekugi` and `mekugi-mentor` are Mekugi.
   Wrong-mode but self-consistent evidence fails.
4. Actual provider models, main and child schedules, lineage, and permitted Mentor routing.
5. Provider-emitted and client-delivered tool shapes, observed payload estimates, cache
   attribution, and provider-response telemetry without reinterpreting missing evidence as zero.

Paired and Mentor reports require both current arms. Single-arm reports require their fresh arm;
`mekugi-only` also requires a matching imported control. The summary formats capturer-owned
calculations rather than inventing alternate metrics. Provider usage alone measures model
consumption. Local token estimates and transport sizes are separate. Summaries omit request,
session, thread, call, capture, routing-key, and provider-request identifiers. Cache-prefix
tables show only request ordinals, usage, stage comparison status, turn-state forwarding, and
fixed changed-field categories. Provider-response tables distinguish explicit cached counts
from missing, null, invalid, and unavailable telemetry and label model IDs as provider claims.

Command and file-change events may support structural friction analysis; agent issue reports are
diagnostic artifacts, not metric inputs. Malformed or non-object JSONL records, including
non-JSON numeric constants, fail analysis with a path and line. Production runtime owners must
not emit benchmark-only evidence. A task may opt into versioned commentary coverage profiles
for selected modes and arms. The runner validates retained assistant messages, successful
command markers, and completed item counts separately from functional grading and capture;
missing or malformed evidence fails that attempt. An exact final response is compared with
the last substantive agent message after excluding router token telemetry, not an earlier
convenient message.

Acceptance:

1. Each attempt retains its image/build identity, result, diff, grader evidence, raw capture,
   and metrics snapshot; failures retain available artifacts and stop task-owned resources.
2. Paired, single-arm, and Mentor scheduling use the same current capture and report contract.
3. Altered totals, wrong modes/models, missing baselines, incomplete capture, and unproved child
   traffic fail rather than producing apparent success.
4. Commentary coverage rejects missing messages, unsuccessful commands, missing item counts,
   malformed events, and unsupported modes.
