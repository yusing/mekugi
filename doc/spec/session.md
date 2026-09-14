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
than guessing. Missing records expose native call identity and unavailable outcomes.

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
