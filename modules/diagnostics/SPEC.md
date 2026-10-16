# Diagnostics

## Purpose

Retain and inspect private diagnostic evidence without changing execution results.

## Scope

The module owns debug bundles, instruction-dump encoding, failure records and
their offline queries. The application owns command parsing, launch and exit
presentation. Transport observations belong to
[transport diagnostics](../transportdiagnostics/SPEC.md); AX calculations belong
to [agent metrics](../agentmetrics/SPEC.md). Diagnostics consumes their facts
without maintaining alternate counters or deciding request outcomes. Optional
diagnostic-section contributions supply owner-specific capture interpretation
and validated AX read snapshots; absent owners leave their existing artifacts
untouched and unread. Explicit debug or failure inspection requires this module.

## Behavior

### Private debug bundles

`--debug` creates a unique private bundle beneath the platform state directory's
debug subtree. It collects request diagnostics, final projected instruction/tool
content and available capture, metrics, read-log and AX artifacts. Capture and AX
remain independently optional. Explicit capture and read-log destinations take
precedence; the bundle records those selections for later inspection.

Bundles contain private content and are not sanitized. Error strings may include
request bodies or credentials. Instruction records distinguish the final local
projection from the outgoing provider subset; a prepared request does not claim
successful provider acceptance. Ordinary messages and tool-call bodies are not
separate instruction-dump fields. Repeated content may use file-local references;
`mekugi.instructions.v2` references resolve within the same file, including
content shared across projected and wire fields. Older
inline records remain readable, and unresolved references are failures,
not empty content. Existing bundles are never rewritten during inspection.

Generated directories use mode 0700 and files use mode 0600. Generated bundles expire after 14 days
of inactivity since close or the last crash lease timestamp, with owner
provenance and process leases protecting active bundles. Cleanup never follows links to external capture destinations or adopts
unowned temporary directories. Copying a bundle outside managed storage retains
it independently of cleanup. Generated bundles are separate from session-history
quotas.

The application reports one shell-quoted diagnosis command after shutdown and
terminal restoration, including startup failures after bundle initialization.
Paths are not printed over the active interactive session. Bundle files survive
normal shutdown.

### Failure references

Request failures retain their time, thread, phase, diagnostic code/reference,
complete error and bounded stream evidence even when debugging is disabled.
`inspect-session --failures [REF]` returns these private records, ordered by time,
as a JSON array. Exact references resolve after restart, including version-1
records and an explicitly selected replay-store directory. Complete errors are
included without a text selector and may contain credentials or request content. Missing references fail
with an expiry-aware explanation. Lookup never revives expired evidence.

### Offline diagnosis

`inspect-session --debug-dir` reads only the selected bundle and its explicit
artifact destinations. Capture follows the bundle link; read logs use their
stored absolute selection. Missing destinations never fall back to another
session. The `mekugi.session.debug.v1` report includes artifact availability,
file sizes, record counts, request outcomes and identities, retained failure
evidence and available module reports. File sizes do not measure cumulative
writes or disk I/O; application-write accounting comes from transport metrics.
Missing failures do not prove a healthy session. A readable artifact still contributes
its result when another fails; failures identify the affected path and produce
nonzero status.

Request selection matches exact request or capture identities across diagnostic,
capture and instruction records. Evidence identifies its source file and line;
`total_evidence`, `next_offset` and `total_requests` distinguish paged evidence
from whole-artifact summaries. Interrupted, incomplete, cancelled and failed
responses remain visible; healthy WebSocket control records are not failures. Private error text requires
the diagnostic field selection; selecting all fields also includes recorded
instruction/tool text. `--text-bytes` bounds text; `--offset` and `--limit` page evidence and the
request index while summary counts describe the complete artifact. Inputs must be regular JSON or
JSONL files no larger than 64 MiB each, with JSONL lines below 32 MiB.
Non-object records, conflicting or unresolved references fail their artifact
with source-line evidence. Inspection writes no files, locks or permissions and scans no unrelated
sessions. Debug selection excludes rollout, replay-store, failure-reference, workspace,
call and AX-generation selectors. Failure lookup excludes rollout, AX, workspace,
call and text selectors. Exit codes are 0 for success, 2 for invalid options and
1 for evidence or output errors.
Cancellation reports an incomplete query even when its selected files or record
directory are empty.

AX export combines one validated read-log snapshot with typed
[session inspection](../sessioninspection/SPEC.md) facts. The application
discovers rollout candidates under its shutdown bound; the backend establishes
exact thread identity, and agent metrics calculates the report. Missing,
ambiguous, incomplete or mismatched discovery remains explicit. Available read evidence survives unavailable rollout-dependent
metrics. Invalid read logs supply no partial counts; unavailable defect
assessments remain unassessed. The AX report contains metrics and coverage,
not scripts or command output. Whole-rollout evidence is distinct from work during this invocation;
read-log-only and unattributed reads are not silently discarded. Known and log-only
thread lists each admit at most 256 identities, with explicit truncation. Discovery
examines at most 100000 directory entries under a five-second shutdown bound.
Export remains bounded and never executes retained inputs.

## Lifecycle

The composed service receives session history and session inspection explicitly.
It acquires no files or background resources during construction.
The application opens an optional bundle during invocation setup and closes it
after final observations and bounded exports, before printing its diagnosis
command. Offline queries acquire no live bundle or backend. Restart restores
retained evidence, not request state, process leases or pending work.

## Codex connection

None. The application supplies finalized request facts. Offline host interpretation
uses session inspection; diagnostic collection never issues host controls.

## Records

Version-1 failure records publish through
[session history](../sessionhistory/SPEC.md), sharing its quota and thread
retention. The module owns their codec and record-kind registration. Debug files
have their own provenance-based bundle retention and privacy boundary; sanitized
capture does not make the containing bundle safe to publish.

## Failure

Failure to initialize explicitly requested diagnostics prevents launch. Later
writes and exports remain auxiliary: failures are visible without replacing
execution results or claiming evidence was retained. Missing optional artifacts
remain unavailable rather than empty successful measurements.

## Acceptance

Acceptance covers private failure lookup after restart, publication failures,
retention, bundle permissions and leases, external destinations, older instruction
records, independent artifact errors, exact request selection, bounded AX export
and optional contributors. Read-only queries are tested for absence of filesystem
mutation and do not require a backend or network.
