# Bounded foreground commands

## Purpose

Run one foreground command and select a bounded beginning or ending of its output.

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

`-n` selects up to N rows per stream. `--max-tokens` accepts 1–15500 and bounds
the combined selected command text. When stdout is nonempty, stderr receives
at most half that budget; stdout receives the remainder. Token-window selection
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
publication failures are reported by the frontend host before delivery.

## Acceptance

The descriptor and scripted binder invoke the real command implementation.
Acceptance preserves argv, stdin, status, head/tail and row selection, mixed-stream
budgets, cancellation, and typed delivery omissions. Capture tests cover split
UTF-8 and newline boundaries and bounded pending rows. Authenticated frontend,
PTY/continuation, and retained-output reconstruction checks remain at the host.
