# Bounded foreground commands

## Current capability

Retain bounded foreground-command output through stock execution, including use within
batched or parallel stock calls.

The executable syntax, head/tail allocation, delivery budgets and capture machinery
below are current design.

## Scope

The `mrun` executable runs through the authenticated pinned frontend. It uses the
host's working directory, environment, stdin, process group, permissions, and
continuation. It creates no private continuation handle or recovery store.

## Behavior

`mrun (-n N|--max-tokens N) [--tail] [--] COMMAND [ARG...]` runs a foreground
command under the host's existing process, stdin, cancellation, and continuation
authority. The first non-option operand starts the command; all subsequent
arguments belong to it. `--` explicitly marks the same boundary.
The shared `commandargs.ParseRunOptions` grammar also supplies Activity's
display-only wrapper recognition.

`-n` selects up to N rows per stream. `--max-tokens` takes a positive integer,
capped at 15500, and bounds the combined selected command text. When stdout is
nonempty, stderr receives at most half that budget; stdout receives the remainder. Token-window selection
may cut a row. `--tail` selects the ending instead of the beginning. Output
outside the selected window is discarded, not promised as recoverable.

Delivery is separately capped at 15500 tokens and ends on complete rows. Only
overflow of this delivery page is retained for `mread`; retained stderr consists
of command output, never the frontend's generated limit notice. The notice is
outside the command-output budget. The command's exit status is preserved even
when output is incomplete; invalid frontend arguments exit 2 and a missing
command exits 127.
Newline-terminated retained streams use complete-row continuation when each row
fits a maximum page including framing. Oversized rows and unterminated generic
streams use byte continuation instead, preserving recoverability without adding
or discarding command bytes. This exception does not change the initial delivery
page's complete-row boundary.

## Lifecycle

Each host command owns its capture until completion or cancellation. The module
has no invocation-wide state. Cancellation stops the child and bounds drainage of
inherited output pipes. Codex retains process-group and yielded-session authority.

Row-only selection uses private temporary files and bounded capture heap through delivery
finalization. Head capture retains only selected rows on disk; tail capture uses
temporary disk proportional to all emitted output. Every return attempts closure
and removal. No delivery plan or recovery consumer owns a capture file.

## Codex connection

One required executable frontend, `mrun`, registered through `codexbinding`.
The command contribution carries both its description and compiled implementation.

## Records

None. The command returns typed delivery omissions to the frontend host, which
publishes recovery before exposing references. Window-discarded bytes and generated
limit notices are excluded from those omissions.

## Failure

Invalid frontend arguments fail before command execution. Process and formatting
failures remain errors; cancellation does not fabricate completion. Recovery
publication failures are reported by the frontend host before delivery. The existing
[recovery limits](../outputrecovery/SPEC.md#managed-read-continuation) apply to normalized
omitted text after the admitted page, not all selected bytes. Capacity failure
preserves that page without silently lowering the requested row count or exposing
an unusable handle; the frontend preserves nonzero child status and changes success
to failure. Capture or finalization failure never labels an unverified partial
selection as complete. Cleanup failure is reported separately and does not invalidate
already finalized output or otherwise successful recovery.

## Acceptance

The descriptor and scripted binder invoke the real command implementation.
Acceptance preserves argv, stdin, status, head/tail and row selection, mixed-stream
budgets, cancellation, and typed delivery omissions. Capture tests cover split
UTF-8 and newline boundaries and bounded pending rows. Authenticated frontend,
PTY/continuation, and retained-output reconstruction checks remain at the host.
