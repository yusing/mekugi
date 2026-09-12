# Agent-experience evidence

## REQ-AX-001 — Runtime reads and evidence-backed AX reporting

The shell worker observes actual invocations of private `hcat`, `hgrep`, `hsymbol`,
and `inspect_file` at its dispatch boundary. An absolute `MEKUGI_AX_OUTPUT` opts into
a local `mekugi.ax.read.v2` JSONL journal (v1 remains readable). The worker inherits this environment value;
no router process, transport request, or static source scan supplies an executed-read count.

`--debug` implies AX instrumentation without an additional flag or environment setting.
It creates a journal in the debug directory unless `MEKUGI_AX_OUTPUT` is already set,
pins its path in the authenticated worker manifest as well as supplying it to the
wrapped executor, and includes an automatic `ax.json` report
and the journal among the printed artifact paths. Automatic rollout discovery and
missing-evidence states follow [REQ-ROUTER-001](router.md). The ordinary manual
journal/inspection workflow remains available without enabling other debug artifacts.

Each invocation emits a random identity, thread ID, reader name, UTC timestamp,
and start/finish phase. Finish includes elapsed monotonic nanoseconds, success,
a fixed allowlisted `failure_class` on failure, and an optional observed process exit
code. The host validates private reader classification metadata; dispatch separately
classifies retained-file, execution, output-write, cancellation, and deadline failures.
Unclassified failures use `unknown`; v1 failures are never reclassified from prose.

With AX enabled, generated shell carriers may carry the opaque logical call identity
in `MEKUGI_AX_CALL_ID`, not a capability, path, script, or publication route.
Instrumented carriers start with `# mekugi:ax:call_id=ID` on its own line. Offline
command correlation requires this explicit router marker; a bare environment
assignment, even before `shell`, is not sufficient evidence. Direct external commands
and templated workers share the marker. It is local correlation, not authentication.
In a command template, `env MEKUGI_AX_CALL_ID=ID` prefixes the substituted worker,
not the whole template, so wrappers and pipelines preserve the worker's identity.
Normal uninstrumented carriers remain unchanged. Journal events include safe `call_id` when
available and a worker-local random `shell_id`. Start and finish must agree on schema,
thread, reader, and all correlation fields. A call ID is not inferred from an arbitrary
shell command or shared thread. Child workers use their own runtime thread identity;
missing identities remain unattributed. Repository router tests clear inherited AX
output at process startup and explicitly opt in only to test-owned journals.
Loops count each actual invocation. Skipped branches and literal source examples count
none. Reader failures, including invalid arguments or retained-file acquisition, remain
failed invocation attempts. These counts are not physical filesystem-open counts.
Other interpreters and external programs' internal reads remain outside coverage.

The journal is optional, private (`0600`), regular, append-only, and limited to 64 MiB.
New journals receive `0600` even under a restrictive umask; existing files with other
permissions are rejected without changing their mode.
Capacity checking and append share a cross-process lock acquired with nonblocking attempts
and a 200 ms retry budget. Router startup rejects journal aliases of capture or metrics outputs, with or without debug.
It contains no source paths, scripts, arguments, credentials, or command output.
An existing symlink or nonregular path is rejected without blocking; no parent is created.
Observation failures cannot stop, repeat, or change the exit status/stdout of a command.
Auxiliary stderr identifies unavailable or incomplete evidence without raw error content.
A missing finish is incomplete; missing or unattributed evidence is unavailable.
The journal is local evidence, not a tamper-proof audit or a claim that all processes
in the session had instrumentation enabled.

`mekugi inspect-session --session PATH --ax` returns additive AX data with
`scope` explicitly covering the entire supplied rollout, independent of table pagination
or call filtering. `--read-log PATH` and `--defects PATH` imply `--ax`.
The capturer package owns the following offline calculations:

- **Edit observations:** count matched original HPATCH/recovery calls, recorded attempts
  above one, emitted payload bytes, rejected attempts, and unconfirmed translations.
  Missing replay is counted separately, not reconstructed from carrier code.
- **Repeated bytes:** sum exact physical-line bytes also present in the preceding matched
  edit's original emitted payload, with multiplicity capped by that preceding payload.
  LF bytes count; changed lines and router-rebuilt scripts are not substituted. This is
  line-aligned re-emission, not a judgment that those bytes were unnecessary.
- **Read observations:** filter runtime events by the rollout's recorded thread ID, pair
  starts/finishes by invocation identity, and expose started/completed/succeeded/failed,
  incomplete, per-reader counts, measured duration, and failure-class counts. Expose up
  to 256 failure details per thread with journal/call/shell identities and a dropped-detail
  count. Other-thread and anonymous start counts are explicit exclusions, not discarded
  noise. Validate the entire journal once before attribution; invalid input yields no
  partial journal result. Reject invalid UTF-8 before JSON decoding, as well as malformed,
  duplicate, unpaired, oversized, or arithmetically invalid evidence. No source-derived read estimate
  substitutes for runtime evidence.
- **Completion observations:** pair `task_started`/`task_complete` or
  `turn_started`/`turn_complete` by turn identity and matching event family using rollout timestamps.
  Cross-family events remain unpaired. Report completed-turn count,
  summed observed intervals (milliseconds saturated at the signed 64-bit maximum), and
  unpaired events. Missing events do not imply completion;
  these intervals include all activity between recorded start and completion.
- **Command observations:** observe `item_started`/`item_completed` events whose item
  type is `CommandExecution`. Persisted completion records may carry `started_at_ms`
  and `completed_at_ms` as Unix epoch milliseconds; use those recorded endpoints rather
  than the envelope's write time. Legacy paired events use envelope timestamps only
  when neither embedded timing key is present. Null, missing, invalid, conflicting,
  duplicate, or backwards endpoints do not establish a complete interval. Matching
  start evidence is counted once. Retain item identity, optional literal carrier
  correlation, observed exit status, and measured duration, accepting at most 10000
  command identities. Derive gaps from the chronological union of observed activity,
  not completion arrival order; incomplete activity suppresses uncertain gaps.
  Summed millisecond intervals saturate at the signed 64-bit maximum. These gaps
  include all intervening activity, not inferred router overhead. Commands and output
  are never retained in AX results.
- **Defect assessments:** accept a JSON array of unique known edit call IDs, explicit
  `defect`/`no_defect` verdicts, and real evidence-artifact paths. Relative paths resolve
  from the assessment file. Evidence must be nonempty, regular, and at most 1 MiB; record
  its absolute path and SHA-256, not its content. Report assessed/unassessed calls and
  reported defects separately from observed rejection/application outcomes. The supplied
  judgment is not converted into independently proven causality or semantic correctness.

Assessment JSON is bounded to 1 MiB. Runtime journals are bounded to 64 MiB, 4096 bytes
per event, and 100000 invocation identities. Explicitly supplied journals are validated even when
thread metadata is absent; attribution remains unavailable rather than skipping validation. Missing sources remain unavailable;
invalid supplied sources fail the query before any result is emitted. No AX query writes
the session, journal, evidence artifacts, or replay store.

These additions do not alter schema-6 transport captures, metrics-v4 endpoints, existing
benchmark calculations, or dashboard ownership. AX journals and assessment artifacts are
separate explicit inputs, not unsanitized content added to transport metrics.

Acceptance:
1. Real worker tests distinguish executed reads, repeated loop iterations, skipped code,
   literal examples, and failures while retaining command output and exit behavior.
2. Missing instrumentation, interrupted readers, other-thread events, concurrent writers,
   malformed evidence, and observation-write failures cannot create false success.
3. Original/recovery payloads produce reproducible byte/retry measurements; repeated lines
   are multiplicity-bounded and actual rebuilt edits are not counted as re-emitted.
4. Completion timing comes from paired recorded events, not wall-clock guessing.
5. Every defect verdict references a bounded real artifact with a checked fingerprint;
   missing, duplicate, unknown-call, and invalid-verdict assessments fail.
6. The installed CLI exercises runtime journal and defect reporting end to end without
   requiring a workspace argument or changing inspected state.
