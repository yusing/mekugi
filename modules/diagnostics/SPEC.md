# Diagnostics

## Current capability

Retain diagnostic inspection without replacing execution results.

Bundle layout, retention defaults, report formats and inspection bounds below are
current design.

## Scope

The module owns debug bundles, instruction-dump encoding, failure records,
retained instructions and their offline queries. The application owns command parsing, launch and exit
presentation. Transport observations belong to
[transport diagnostics](../transportdiagnostics/SPEC.md); AX calculations belong
to [agent metrics](../agentmetrics/SPEC.md). Diagnostics consumes their facts
without maintaining alternate counters or deciding request outcomes. Optional
diagnostic-section contributions supply owner-specific capture interpretation
and validated AX read snapshots; absent owners leave their existing artifacts
untouched and unread. Explicit debug or failure inspection requires this module.

## Behavior

### Private debug bundles

`--debug` enables detailed operational logging and creates a unique private bundle
under `$XDG_STATE_HOME/mekugi/debug`, defaulting to `~/.local/state/mekugi/debug`.
It collects request diagnostics, final projected instruction/tool
content and available capture, metrics, read-log and AX artifacts. Capture and AX
remain independently optional. Explicit capture and read-log destinations take
precedence; the bundle records those selections for later inspection.
Main retains the explicit destinations. Independent child runtimes write under
`<root-capture>.runtimes/<unique-id>/` (or `<root-read-log>.runtimes/` when only a
read log is selected), keeping captures and read logs separate across the family.

Bundles contain private content and are not sanitized. Error strings may include
request bodies or credentials. The adapter supplies final local projection and
the prepared outgoing provider subset separately; a prepared request does not
claim successful provider acceptance. Cached-prefix counts mean actual reuse;
automatic successors have no prepared wire request, and Grok observations precede
Chat Completions conversion. Ordinary messages and tool-call bodies are not
separate instruction-dump fields. Repeated content may use file-local references;
`mekugi.instructions.v2` references resolve within the same file, including
content shared across projected and wire fields. Older
inline records remain readable, and unresolved references are failures,
not empty content. Existing bundles are never rewritten during inspection.

`request_complete` records monotonic elapsed milliseconds and available immutable
`capture_id`/`request_sequence`. Request-scoped debug and instruction records use
that capture ID as `request_id`, or a local random ID without capture.
`tool_observation` joins this identity to safe logical call identity and tool name
at observation, not execution. AX and patch evidence join by call identity;
neither transport receives a correlation header. Bounded stream and cancellation
fields follow [transport diagnostics](../transportdiagnostics/SPEC.md#provider-and-stream-evidence).

Generated directories use mode 0700 and files use mode 0600. Generated bundles expire after 14 days
of inactivity since close or the last crash lease timestamp, with owner
provenance and process leases protecting active bundles. Cleanup never follows links to external capture destinations or adopts
unowned temporary directories. Copying a bundle outside managed storage retains
it independently of cleanup. Generated bundles are separate from session-history
quotas.

After the child and router stop and the terminal is restored, the application
prints one `To diagnose this session, ask an agent to run:` heading and one
shell-quoted `mekugi inspect-session --debug-dir DIR --field diagnostic` command
to stderr, including startup failures after bundle initialization.
Paths are not printed over the active interactive session. Bundle files survive
normal shutdown.

### Detached server evidence

Automatically started local servers retain private process evidence independently
of the launching client and `--debug`, under `$XDG_STATE_HOME/mekugi/server`
(default `~/.local/state/mekugi/server`). Each generated directory contains
`process.jsonl` with start/exit time, server PID, socket and running build identity,
including available revision/modified settings. Exits include status and signal
when available. Missing exit evidence does not establish a clean shutdown.

`stderr.previous.log` followed by `stderr.log` retains the latest stderr segments,
at most 1 MiB per server. These unsanitized files may contain private content.
They use the same private permissions, ownership leases and 14-day inactive
retention as debug bundles, in separate storage. New detached launches attempt
cleanup; maintenance failures are recorded without suppressing new collection.
Copy evidence elsewhere to retain it independently. Inspect these files
directly; session-bundle inspection does not query process evidence.

A detached supervisor owns collection and process waiting after clients leave,
including normal shutdown, panic/fatal exit and signal termination. Collection
failures remain auxiliary: startup reports unavailability without private error
text, and later stderr write failures are recorded with exit evidence when storage
permits. Ordinary client output includes no retained stderr contents. Terminating
the supervisor itself can prevent the final exit record.

### Failure references

Request failures retain their time, thread, phase, diagnostic code/reference,
complete error and bounded stream evidence even when debugging is disabled.
`inspect-session --failures [REF]` returns these private records, ordered by time,
as a JSON array. Exact references resolve after restart, including version-1
records and an explicitly selected replay-store directory. Complete errors are
included without a text selector and may contain credentials or request content. Missing references fail
with an expiry-aware explanation. Lookup never revives expired evidence.

### Retained instructions

Without `--debug`, each thread retains the final projected content Mekugi
forwards for its model requests: base instructions (including environment facts
and Mekugi guidance), developer messages, tool descriptions and additional tool
items. Prewarm, Codex approval-reviewer and thread-less requests are excluded. As
with debug instruction dumps, a forwarded request does not claim provider
acceptance. Selection reads only each input item's top-level role and type and
tolerates duplicate names and invalid UTF-8 elsewhere in admitted JSON; a request
whose selection cannot be decoded retains nothing rather than a partial revision.

A thread's index lists its revisions and use entries. A use entry records the
first request that used a revision after a different one, with its time, model,
Codex turn ID and, when transport capture is active, the capture `request_id`
and `request_sequence`; later requests use that revision until the next use
entry. A request whose content matches the revision the invocation last recorded
for the thread, while that index file is unchanged, does no locked read or write;
a replaced or removed index makes the next request read it and republish as needed.
Each revision's ordered digest list and each base-instruction value, developer
message or tool item is stored once per store, content-addressed by the SHA-256
of its exact JSON bytes, and shared by every thread that used it, so the index
grows by one entry per revision or use.

The application reports a failed publication as a thread notice without failing
the request. The next request publishes its own content: unchanged content
retries the failed revision, while changed content supersedes it and the failed
revision is not retained. Session inspection joins the report as
`instructions`, with status `available`, `unavailable` (the rollout has no thread
identity, or nothing is retained for the thread, including sessions recorded
before retention, expired after inactivity or reclaimed under storage pressure)
or `invalid`. Revisions reference `content` by digest so shared text appears
once; text requires the `instructions` or `all` field selection and follows the
session text bound. Invalid indexes, missing records and digest mismatches fail
the instruction report without hiding call evidence, and make the command exit 1.

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
[session inspection](../sessioninspection/SPEC.md) facts. The application uses
[thread discovery](../../backends/codex/SPEC.md#supplied-offline-rollouts) within
its shutdown bound; the backend establishes exact metadata identity, session inspection validates selected rollouts in full, and
agent metrics calculates the report. Invalid or unreadable candidates make
discovery incomplete. Missing, ambiguous, incomplete or mismatched discovery
remains explicit. Available read evidence survives unavailable rollout-dependent
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

Version-1 failure records and retained instructions publish through
[session history](../sessionhistory/SPEC.md), sharing its quota, thread expiry
and cleanup. The module owns their codecs and record-kind registration:
`failure-<hash>.json`; the per-thread `instructions-<sha256(thread)>.json` index,
which depends on `instruction-revision-<sha256>.json` digest lists, which depend
on `instruction-content-<sha256>.json` values. Shared records survive until their
last owning thread expires. Index and revision records are bounded to 4 MiB each.
Debug files
have their own provenance-based bundle retention and privacy boundary; sanitized
capture does not make the containing bundle safe to publish.

## Failure

Failure to initialize explicitly requested diagnostics prevents launch. Later
writes and exports remain auxiliary: failures are visible without replacing
execution results or claiming evidence was retained. Missing optional artifacts
remain unavailable rather than empty successful measurements.

## Acceptance

Acceptance covers private failure lookup after restart, publication failures,
retained-instruction deduplication, sharing, restart, capacity retry and expiry,
retention, bundle permissions and leases, external destinations, older instruction
records, independent artifact errors, exact request selection, bounded AX export
and optional contributors. Read-only queries are tested for absence of filesystem
mutation and do not require a backend or network.
