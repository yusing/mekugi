# Offline diagnostic inspection

## Debug bundle diagnosis

`mekugi inspect-session --debug-dir DIR --field diagnostic` diagnoses one debug
bundle without starting a router or executing retained inputs. It returns
`mekugi.session.debug.v1` JSON with per-artifact availability, file bytes and record counts,
request outcome counts, a request index, failure evidence, capture-owned metrics, and the stored
AX report. The report does not infer that an issue is absent from a lack of recorded
failures. Successful output from readable artifacts remains available when another
artifact is missing or invalid; each failed artifact reports its path on stderr
and the command exits nonzero.

Evidence identifies its source file and line, request/capture ID, thread, phase,
status, and diagnostic reference where recorded. `--request-id ID` selects exact
request or capture identities across router, capture, and instruction records.
`--field diagnostic` includes original error text; `--field all` also includes
selected request instruction/tool text. Compact instruction references resolve within
the selected bundle; older inline-only records remain readable. Unresolved or conflicting
references fail that artifact with a source line, without suppressing other artifacts.
Capture-owned metrics include measured application-write accounting when available;
artifact bytes describe current file sizes, not cumulative writes or physical disk I/O.
Without a field selection, private text
is omitted. These files and selected text are not sanitized.
Interrupted responses and failed, incomplete, cancelled, or error terminal
states remain visible; healthy WebSocket control records are not failed response streams.
Text uses the existing `--text-bytes` bound. `--offset` and `--limit` paginate
evidence, with `total_evidence` and `next_offset`. The request index uses the same
page bounds and exposes `total_requests`; summary counts cover the entire artifact.
Capture overrides resolve through the bundle's capture link; the AX
report selects an absolute overridden read log. Missing override destinations
remain unavailable rather than being replaced by another session's evidence.

Inputs must be regular JSON/JSONL files no larger than 64 MiB each; JSONL rows
use the rollout reader's line-size bound. Malformed and non-object records fail
their artifact, without preventing the other artifacts from being inspected.
This mode cannot be combined with rollout, replay-store, failure-reference,
workspace, call, AX-generation, read-log, or defect-assessment selections.
Exit status is 0 for success, 2 for invalid options, and 1 for read, validation,
or output errors. Inspection writes no artifacts, replay records, locks, or
permissions and scans no other sessions.

## Failure reference lookup

`mekugi inspect-session [--replay-dir PATH] --failures [REF]` reads retained private
failure records without a rollout, router, or state writes. It returns a JSON array ordered
by time; an optional exact reference selects matching records, including records from a
previous router process. Each version-1 record includes `time`, `thread`, `phase`, `code`,
`reference`, the complete `error`, and optional bounded `stream` diagnostics.
Error text may contain request content or credentials. Lookup includes it without
`--field`; treat the output as private. Missing references fail with an expiry-aware error. This mode
cannot be combined with rollout selection, AX, workspace, call, or text-field options.
Failures follow the [router retention policy](router.md); lookup does not revive expired data.
Exit status is 0 for success, 2 for invalid options, and 1 for read, validation, or output errors.
