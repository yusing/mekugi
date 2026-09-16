# Logical session inspection

## REQ-SESSION-001 — Offline logical call inspection

`mekugi inspect-session --session PATH` reads a Codex rollout JSONL
file and the private replay store without starting a router, executing calls, changing
permissions, creating locks, or writing history. `--replay-dir` overrides the usual
state-directory location. Each call's workspace comes from the preceding `session_meta.cwd`
or `turn_context.cwd`. Optional `--workspace` overrides this metadata for all calls.
Existing workspace directories are canonicalized; absent absolute historical paths remain
usable for archived replay lookup. Missing metadata yields `workspace_unavailable`, not a
guess from the inspector's cwd or a scan of other workspaces. Relative metadata requires
an explicit override. Returned calls identify their selected workspace.

The result is `mekugi.session.v1` JSON. It enumerates tool calls and output-only call
identities in first-observed order from `response_item` records, not by scanning all
stored calls. Repeated identical call identities coalesce; conflicting payloads or call workspaces fail.
Replay lookup is workspace-and-call scoped. Matched carriers or original upstream
calls must agree with the stored mapping; corrupt or incompatible records fail rather
than guessing. Missing records expose native call identity and unavailable outcomes. Canonical router
journal result IDs are decoded through the journal provenance owner only for named
`functions.journal` function outputs lacking a call ID. They participate under the
original call ID without inventing execution or success. Matched replay must identify
a journal call; malformed IDs and unrelated missing-call-ID outputs still reject.

Each call exposes original tool identity, correlation/attempt when recorded, rejection
count, outcome, and text byte counts. `--field` selects `script`, `evaluated`, `patch`,
`report`, `diagnostic`, `rejections`, `output`, or `all`; text is omitted by default.
Selected text is an exact UTF-8 prefix capped by `--text-bytes` (default 4096, 1–65536).
Every field reports full byte size and omitted bytes. Rejections use the stored structured
array, not interpretation of diagnostic prose. Output is the last observed tool output;
non-string output retains its JSON representation.

`translated_unconfirmed` never means applied. `confirmed` requires a recorded output
of the expected carrier kind equal to the successful stored report, matching request
replay's confirmation rule. `applied` identifies stored router-owned application;
`already_satisfied`, `rejected`, `unconfirmed`, and `unavailable` remain distinct.
No outcome claims semantic correctness or turns a missing output into success.

`--call-id` filters before pagination. `--offset` and `--limit` (default 50, 1–500)
bound returned calls with `total_calls` and optional `next_offset`. Missing matches
return an empty page. Input must be regular UTF-8 JSONL, at most 64 MiB with lines
below 32 MiB and at most 10000 logical calls. Unsupported record kinds are ignored.
Malformed or oversized input fails before emitting a result. Exit status is 0 for
success/help, 2 for invalid options, and 1 for read, validation, or output failure.

Inspection is local evidence, not request-visible recovery ancestry. A rollout may
contain calls that are no longer visible after compaction. It does not revive processes,
references, or permissions, and does not expose private records over HTTP or metrics.

Acceptance:
1. Dispatch returns original HPATCH and recovery payloads, evaluated scripts,
   structured rejections, patches, reports, and matching executor confirmation.
2. Workspace inference, turn-level workspace changes, overrides, missing metadata,
   corrupt or mismatched records, output-only calls, and workspace isolation preserve
   the documented result and failure behavior.
3. Native, function, and custom carriers work without decoding carrier source.
4. Text selection, UTF-8 bounds, pagination, duplicates, and invalid input follow
   the documented schema.
5. Inspection leaves session, replay records, and directory permissions unchanged.

`--ax`, `--read-log`, and `--defects` add whole-rollout AX reporting under
[REQ-AX-001](ax.md); table selection and pagination do not narrow its evidence scope.

### Corpus friction inspection

`mekugi inspect-sessions` scans regular `.jsonl` files beneath `--sessions-dir`
(default `$CODEX_HOME/sessions`, or `~/.codex/sessions`). It starts no router, executes
no calls, and writes neither rollouts nor replay records. Discovery rejects more than
10000 files; each rollout retains the single-session size and identity validation.
Unreadable or malformed rollouts appear in `unavailable`, never as zero activity.
Cancellation, discovery failure, or invalid capture evidence prevents a partial report.

`--since` (inclusive) and `--until` (exclusive) accept RFC3339 timestamps and default
to the last 48 hours. Calls use their recorded call timestamp and preceding model;
missing timestamps are counted separately. `--model` is a glob applied to calls.
`--exclude-model` excludes a whole rollout when any recorded model matches, so
mixed-provider sessions can be excluded. `--class` selects `all`, `production`,
`probe`, or `unknown`: these are metadata-based candidates, not ground truth.
CLI and subagent sources outside the temporary directory are production candidates;
exec sources or temporary working directories are probe candidates. Unrecognized
source metadata is unknown. Exclusion counts identify the applied filters.

The `mekugi.sessions.v1` JSON report contains per-rollout evidence, absolute paths,
call IDs and original line numbers, with no source bodies. Counts are per rollout;
fork-inherited calls are not summed into a global workload. Each session distinguishes
matched from unavailable replay. Recovery chains use validated replay correlation
IDs and report call/rejection counts and emitted/diagnostic bytes, not token savings.
Empty-poll evidence requires an empty-input call and explicitly empty native output
for the same session; transparent Code Mode projections are recognized, but printed
lookalikes are not. This is offline analysis and does not change continuation behavior.
Truncation/reread findings are explicitly static candidates: adjacent selected calls
must have matched shell scripts, an hread omission receipt, the same workspace, and
overlapping literal hcat path/range selections. They do not establish execution counts,
unchanged files, unnecessary reads, or actual duplicate delivered bytes. Unsupported
scripts/carriers are not decoded or executed to manufacture evidence.

`--limit` bounds findings per session (default 25, 1–500); `omitted_findings` reports
the remainder. Chains retain up to 16 source call references while `call_count` covers
all selected members. `--replay-dir` overrides the read-only replay location.
Optional `--capture` reads bounded sanitized provider capture JSONL through capturer.
Usage remains separate, filtered by capture time/model and included thread IDs;
it is not attributed to individual friction findings. Missing records and incomplete
responses are explicit, duplicate provider attempts reject, and local payload bytes
never substitute for provider-reported token counts.

Capture completeness follows [REQ-METRICS-001](metrics.md). Legacy normalized
counters count as incomplete with `unknown_completeness_records`, not observed
zero-token responses.
