# Command segments

## Requirements

Inherited from [product requirements](../../docs/product.md#user-requirements): provide per-command
visibility (output, exit status, and timing) for supported shell lists alongside stock execution,
without altering command output, exit status, or host execution authority.

## Current approach (replaceable)

- Uses `libraries/shellinstrumentation` and `mekugi-exec` over invocation-private FIFOs to report
  segment start/end boundaries, elapsed duration, and relayed output for supported Bash and Linux
  `dash`-backed `sh` command lists (terminal commands keep their TTY and report statuses only).
  Also carries synchronous native pre-tool capture requests for
  [Change evidence](../../docs/contracts/change-evidence.md).
- Correlates live reports against host command items by thread and authenticated script within a
  short window (350 ms), falling back cleanly to combined host output when reports are missing,
  incomplete, lossy (past the 4 MiB output cap), or disagree with host exit status.
- Persists immutable reports asynchronously through [Session history](../sessionhistory/SPEC.md)
  (and reads historical mixed-call records) so restored history and forks retain per-command output
  and timing without reviving live processes. Failure of optional segment tracking never blocks
  execution or disables shell approval guards.
