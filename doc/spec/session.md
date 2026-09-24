# Logical session inspection

## Failure reference lookup

`mekugi inspect-session [--replay-dir PATH] --failures [REF]` reads retained sanitized
failure records without a rollout, router, or state writes. It returns a JSON array ordered
by time; an optional exact reference selects matching records, including records from a
previous router process. Each version-1 record includes `time`, `thread`, `phase`, `code`,
`reference`, and optional bounded `stream` diagnostics. It contains neither original error
messages nor request content. Missing references fail with an expiry-aware error. This mode
cannot be combined with rollout selection, AX, workspace, call, or text-field options.
Failures follow the [router retention policy](router.md); lookup does not revive expired data.
Exit status is 0 for success, 2 for invalid options, and 1 for read, validation, or output errors.

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
Replay lookup is workspace-and-call scoped. Matched observed calls must agree with retained identity; corrupt or
incompatible records fail rather than guessing. Missing records expose native call identity and unavailable outcomes.
Canonical router-local result IDs are decoded through the journal provenance owner only
for named `functions.journal` or opt-in `functions.report_issue` outputs lacking a call ID.
They participate under the original call ID without inventing execution or success. Matched
replay must identify the corresponding local function call; malformed IDs and unrelated
missing-call-ID outputs still reject.

Each call exposes original tool identity, observed outcome, and text byte counts. `--field` selects `script`,
`report`, `diagnostic`, `output`, or `all`; text is omitted by default.
Selected text is an exact UTF-8 prefix capped by `--text-bytes` (default 4096, 1–65536).
Every field reports full byte size and omitted bytes. Diagnostics use retained result evidence rather than interpretation of model prose. Output is the last observed tool output;
non-string output retains its JSON representation.

`applied` requires a completed observed stock patch result and workspace outcome.
`already_satisfied`, `rejected`, `unconfirmed`, and `unavailable` remain distinct;
partial workspace effects may accompany a failed result without becoming
successful edits. Missing output is not success. No outcome claims semantic
correctness.

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
1. Inspection returns original stock call identity, observed patch input,
   durable result and review evidence when available, without executing effects.
2. Workspace inference, turn-level workspace changes, overrides, missing metadata,
   corrupt or mismatched records, output-only calls, and workspace isolation preserve
   the documented result and failure behavior.
3. Direct native and Code Mode calls retain their observed identities without
   decoding or executing source.
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
matched from unavailable replay. Empty-poll evidence requires an empty-input call and explicitly empty native output
for the same session; transparent Code Mode projections are recognized, but printed
lookalikes are not. This is offline analysis and does not change continuation behavior.
Truncation/reread findings are explicitly static candidates: adjacent selected calls
must have an `mread` omission receipt, the same workspace, and overlapping
literal `mcat` path/range selections in stock execution input. They do not establish execution counts,
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
