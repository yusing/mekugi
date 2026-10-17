# Session inspection

## Purpose

Explain recorded host calls and their retained outcomes without executing them.

## Scope

The module owns single-rollout queries, retained-evidence joins, corpus filtering
and static friction findings. The application exposes `inspect-session --session`
and `inspect-sessions`; the Codex binding supplies decoded rollout facts.
[Diagnostic inspection](../diagnostics/SPEC.md) owns debug-bundle and
failure-reference queries. Application dispatch and CLI option handling live in
`apps/mekugi/internal/inspectioncli`, separate from live invocation and terminal
coordination. Neither inspection path starts a backend or changes stored state.

## Behavior

### Calls and retained evidence

Calls preserve original tool identities, the last observed output and
first-observed order, including output-only calls. Identical repeated identities
coalesce; conflicting inputs, workspaces or thread identities fail. Missing call identity is invalid, not replaced by a
message identity. Unknown record kinds are ignored.

`--replay-dir` selects retained records, defaulting to the platform state directory.
Each call uses its preceding recorded workspace, or an explicit override. The
inspector never substitutes its working directory or scans other workspaces for
matching records. Existing directories are canonicalized; absent absolute paths
remain usable for archived evidence. Relative metadata requires an override.
Missing metadata yields unavailable workspace evidence.

Retained records must agree with the selected call and workspace. Derived patch
and shell records must also agree with their parent observation. Corruption or
conflicting evidence fails rather than joining records by filename alone. Missing
records retain the original host identity and an unavailable outcome. Outcomes
distinguish `applied`, `no_changes`, `already_satisfied`, `rejected`, `unconfirmed` and
`unavailable`; missing derived shell evidence is unavailable, and shell
observations do not enter patch-based AX edit counts.

Completed host results, observed effects and outcome classification remain
distinct. A missing derived patch outcome is unconfirmed, even when host output
or another operation reports changes. Recorded patch or command failure stays
rejected even when files changed. Shared effects
identify their owner; missing observations do not claim zero files. Unconfirmed
completion and partial coverage cannot establish a successful no-op. Inspection
does not certify semantic correctness, revive processes or references, or grant
authority from retained metadata.

### Selection and output

Single-session output retains `mekugi.session.v1`. Call filtering precedes paging;
`total_calls` describes the filtered selection, `next_offset` identifies the
next page, and missing matches return an empty page. The default page contains at
most 50 calls; `--limit` accepts 1–500. `--field` selects script, report, diagnostic,
output or all; otherwise text is omitted. Selected fields retain original meaning,
byte count and omitted-byte count.
The text bound defaults to 4096 bytes and accepts 1–65536 bytes.
Text is a bounded UTF-8 prefix; non-string host output keeps its JSON form.

AX contributions describe the whole supplied rollout regardless of the displayed
call page. Their calculations and completeness belong to
[agent metrics](../agentmetrics/SPEC.md). An absent contributor leaves ordinary
inspection usable and makes an explicit request for that contribution unavailable.

The backend owns [rollout admission and decoding](../../backends/codex/SPEC.md#supplied-offline-rollouts).
Malformed, conflicting or oversized input fails before a single-session result
is emitted. Queries create no directories, locks, records,
permission changes or recovery references.

### Corpus findings

Corpus inspection discovers regular rollout files beneath the selected sessions
directory, defaulting to `$CODEX_HOME/sessions` or `~/.codex/sessions`. RFC3339
time bounds default to the last 48 hours, inclusive at the start and exclusive at
the end; missing timestamps are counted separately. Model globs select calls,
while an excluded model anywhere in a rollout excludes that rollout. CLI and child
sources outside temporary directories are production candidates; batch sources
or temporary workspaces are probe candidates. Other origins are unknown. These
classifications are metadata-based candidates, not ground truth.

The `mekugi.sessions.v1` report identifies source paths, host call identities and
original line numbers without source bodies. Counts stay per rollout because fork
history may be inherited; inherited calls are not pooled into a global workload.
Malformed or unreadable rollouts are unavailable, not zero activity. Discovery is bounded to 10,000 files. Cancellation, discovery
failure or invalid explicitly selected capture evidence prevents a partial report.

Empty-poll findings require matching empty input and explicitly empty native
output. Supported transparent JavaScript projections can supply these facts;
printed lookalikes cannot. Truncation/reread findings require adjacent selected
calls with an `mread` omission receipt, the same workspace and overlapping
literal `mcat` selections. They remain static candidates, not evidence of repeated
execution, unchanged sources or duplicate delivered bytes. Selection parsing does
not inspect current files; operands requiring the [reader's filesystem disambiguation](../fileread/SPEC.md#authenticated-raw-row-reader)
produce no candidate. Unsupported source is never run
to manufacture evidence. The per-session finding limit defaults to 25 and accepts
1–500; `omitted_findings` reports additional findings.
Matching short names from another namespace or tool kind are not stock-read evidence.

Optional provider usage is filtered by capture time, model and included threads,
not attributed to individual findings. Completeness and duplicate-attempt
validation belong to [transport diagnostics](../transportdiagnostics/SPEC.md).
Local bytes never substitute for provider usage. Missing optional contributors
do not disable unrelated queries.

The inspection slot supplies optional query-local AX collectors and provider-usage
queries. Each contribution retains ownership of its calculations and report
encoding. With zero contributions, ordinary call and corpus queries remain
available, provider usage remains unavailable, and explicit AX or capture requests
fail before opening their sources. Debug export can pass its already validated
read snapshot through the AX owner, avoiding a second log read.

## Lifecycle

Queries are read-only operations bounded by caller cancellation. They restore no
live clocks, sockets, grants, pending controls or writable stores. Retained facts
can outlive their visibility in a compacted host conversation; inspection does
not turn them into request-visible recovery ancestry.

## Codex connection

The required offline-rollout binding supplies decoded source facts and validates
retained tool identities. Host-specific execution evidence stays inside that
binding. Queries fail explicitly until a source is bound; no Codex process,
control request or tool execution is involved.

## Records

None. Inspection reads owner-validated retained records and publishes reports to
the caller. The owning module's codec remains authoritative for each record.

## Failure

Commands return 0 for success/help, 2 for invalid options and 1 for read,
validation or output failure. Missing evidence is represented explicitly where
the report permits it; corrupt evidence is not silently treated as missing.

## Acceptance

Acceptance covers original call identity, changing and absent workspaces,
archived paths, conflicting records, output-only calls, failed and shared effects,
text bounds, pagination, whole-rollout contributions, static corpus findings and
optional-module absence. Read-only checks verify unchanged stored bytes,
permissions and directory contents. Tests require no live backend or provider.
